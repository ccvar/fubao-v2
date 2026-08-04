package redpacket

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	ParticipationScheduleOnce     = "once"
	ParticipationScheduleDaily    = "daily"
	ParticipationScheduleInterval = "interval"
)

// CreateParticipationSchedule validates and persists one native-side plan.
func (s *Store) CreateParticipationSchedule(schedule ParticipationSchedule, now time.Time) (ParticipationSchedule, error) {
	if now.IsZero() {
		now = time.Now()
	}
	schedule.Mode = strings.TrimSpace(schedule.Mode)
	schedule.RunAt = strings.TrimSpace(schedule.RunAt)
	schedule.DailyTime = strings.TrimSpace(schedule.DailyTime)
	schedule.Enabled = true
	var next time.Time
	var err error
	switch schedule.Mode {
	case ParticipationScheduleOnce:
		next, err = time.Parse(time.RFC3339Nano, schedule.RunAt)
		if err != nil || !next.After(now) {
			return ParticipationSchedule{}, errors.New("指定执行时间必须晚于当前时间")
		}
	case ParticipationScheduleDaily:
		next, err = nextDailySchedule(now, schedule.DailyTime)
		if err != nil {
			return ParticipationSchedule{}, err
		}
	case ParticipationScheduleInterval:
		if schedule.IntervalSeconds < 60 || schedule.IntervalSeconds > 30*24*60*60 {
			return ParticipationSchedule{}, errors.New("间隔执行时间必须在 1 分钟到 30 天之间")
		}
		next = now
	default:
		return ParticipationSchedule{}, errors.New("红包参与计划类型无效")
	}
	createdAt := now.Format(time.RFC3339Nano)
	sum := sha256.Sum256([]byte(schedule.Mode + "\x00" + createdAt + "\x00" + schedule.RunAt + "\x00" + schedule.DailyTime))
	schedule.ID = hex.EncodeToString(sum[:12])
	schedule.NextRunAt = next.Format(time.RFC3339Nano)
	schedule.CreatedAt = createdAt
	schedule.UpdatedAt = createdAt

	s.mu.Lock()
	defer s.mu.Unlock()
	s.participationSchedules[schedule.ID] = &schedule
	s.addActivityLocked("participation_schedule_created", "", "已创建"+participationScheduleLabel(schedule), now)
	if err := s.saveLocked(); err != nil {
		delete(s.participationSchedules, schedule.ID)
		return ParticipationSchedule{}, err
	}
	return schedule, nil
}

func nextDailySchedule(now time.Time, dailyTime string) (time.Time, error) {
	parsed, err := time.Parse("15:04", dailyTime)
	if err != nil {
		return time.Time{}, errors.New("每天固定时间格式无效")
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next, nil
}

// ParticipationSchedules returns active plans in next-run order.
func (s *Store) ParticipationSchedules() []ParticipationSchedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]ParticipationSchedule, 0, len(s.participationSchedules))
	for _, schedule := range s.participationSchedules {
		items = append(items, *schedule)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].NextRunAt < items[j].NextRunAt })
	return items
}

// DeleteParticipationSchedule disables and removes a future plan.
func (s *Store) DeleteParticipationSchedule(scheduleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	schedule := s.participationSchedules[strings.TrimSpace(scheduleID)]
	if schedule == nil {
		return errors.New("红包参与计划不存在")
	}
	delete(s.participationSchedules, schedule.ID)
	s.addActivityLocked("participation_schedule_deleted", "", "已删除"+participationScheduleLabel(*schedule), time.Now())
	return s.saveLocked()
}

// PendingParticipationMonitorPrewarms returns schedule occurrences whose
// configurable monitor-start window has opened. It does not mutate state: the
// native worker marks an occurrence only after every eligible room monitor has
// been checked successfully, allowing transient account/store failures to be
// retried until the actual execution time.
func (s *Store) PendingParticipationMonitorPrewarms(now time.Time, advance time.Duration) []ParticipationScheduleExecution {
	if now.IsZero() {
		now = time.Now()
	}
	if advance <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	executions := make([]ParticipationScheduleExecution, 0)
	for _, schedule := range s.participationSchedules {
		if !schedule.Enabled || schedule.MonitorPrewarmedFor == schedule.NextRunAt {
			continue
		}
		due, err := time.Parse(time.RFC3339Nano, schedule.NextRunAt)
		if err != nil || !due.After(now) || now.Before(due.Add(-advance)) {
			continue
		}
		executions = append(executions, ParticipationScheduleExecution{
			ScheduleID: schedule.ID,
			Mode:       schedule.Mode,
			Label:      participationScheduleLabel(*schedule),
			DueAt:      schedule.NextRunAt,
		})
	}
	sort.Slice(executions, func(i, j int) bool { return executions[i].DueAt < executions[j].DueAt })
	return executions
}

// RecordParticipationMonitorPrewarm atomically marks one occurrence and adds
// a compact safe activity summary. No account Cookie, request parameters, or
// signed interface data enters the persisted activity feed.
func (s *Store) RecordParticipationMonitorPrewarm(execution ParticipationScheduleExecution, advance time.Duration, monitoredRooms, accountCount int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	schedule := s.participationSchedules[strings.TrimSpace(execution.ScheduleID)]
	if schedule == nil || schedule.NextRunAt != strings.TrimSpace(execution.DueAt) {
		return nil
	}
	if schedule.MonitorPrewarmedFor == schedule.NextRunAt {
		return nil
	}
	schedule.MonitorPrewarmedFor = schedule.NextRunAt
	schedule.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	message := fmt.Sprintf(
		"已提前 %s检查“%s”：%d 个直播间监测已启用",
		formatScheduleInterval(int(advance.Seconds())),
		participationScheduleModeName(schedule.Mode),
		maxInt(monitoredRooms, 0),
	)
	if accountCount > 0 {
		message += fmt.Sprintf("，使用 %d 个监测账号", accountCount)
	}
	s.addActivityLocked("participation_monitor_prewarmed", schedule.ID, message, time.Now())
	return s.saveLocked()
}

// ClaimDueParticipationSchedules atomically advances each due plan before it
// is handed to the UI, so repeated polling cannot execute the same occurrence.
func (s *Store) ClaimDueParticipationSchedules(now time.Time) ([]ParticipationScheduleExecution, error) {
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	executions := make([]ParticipationScheduleExecution, 0)
	for id, schedule := range s.participationSchedules {
		if !schedule.Enabled {
			continue
		}
		due, err := time.Parse(time.RFC3339Nano, schedule.NextRunAt)
		if err != nil || due.After(now) {
			continue
		}
		executions = append(executions, ParticipationScheduleExecution{
			ScheduleID: schedule.ID, Mode: schedule.Mode, Label: participationScheduleLabel(*schedule), DueAt: schedule.NextRunAt,
		})
		schedule.LastRunAt = now.Format(time.RFC3339Nano)
		schedule.UpdatedAt = schedule.LastRunAt
		switch schedule.Mode {
		case ParticipationScheduleOnce:
			delete(s.participationSchedules, id)
		case ParticipationScheduleDaily:
			next, nextErr := nextDailySchedule(now, schedule.DailyTime)
			if nextErr != nil {
				delete(s.participationSchedules, id)
			} else {
				schedule.NextRunAt = next.Format(time.RFC3339Nano)
			}
		case ParticipationScheduleInterval:
			schedule.NextRunAt = now.Add(time.Duration(schedule.IntervalSeconds) * time.Second).Format(time.RFC3339Nano)
		}
		s.addActivityLocked("participation_schedule_dispatched", schedule.ID, participationScheduleLabel(*schedule)+"已触发", now)
	}
	if len(executions) == 0 {
		return executions, nil
	}
	sort.Slice(executions, func(i, j int) bool { return executions[i].DueAt < executions[j].DueAt })
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return executions, nil
}

// RecordParticipationBatchResult adds one compact, safe dispatch summary to
// recent activity after native browser preparation has completed.
func (s *Store) RecordParticipationBatchResult(scheduleID, mode string, started, skipped int, accountIDs []string) error {
	mode = strings.TrimSpace(mode)
	label := "立即执行"
	if strings.TrimSpace(scheduleID) != "" {
		label = "红包参与计划"
		if mode == ParticipationScheduleDaily {
			label = "每天固定时间"
		} else if mode == ParticipationScheduleInterval {
			label = "间隔执行"
		} else if mode == ParticipationScheduleOnce {
			label = "指定日期"
		}
	}
	verb := "已启动"
	if strings.TrimSpace(scheduleID) != "" {
		verb = "已执行"
	}
	message := fmt.Sprintf("%s“%s”：%d 个实例参与", verb, label, maxInt(started, 0))
	if skipped > 0 {
		message += fmt.Sprintf("，%d 个实例跳过", skipped)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The batch summary replaces only the per-account start activities created
	// by this exact batch. Participation tasks and detailed request records stay
	// intact; only the noisy sidebar entries are consolidated.
	for _, accountID := range accountIDs {
		task := s.participationTasks[strings.TrimSpace(accountID)]
		if task == nil || strings.TrimSpace(task.ID) == "" {
			continue
		}
		if activity := s.activities[task.ID]; activity != nil && (activity.Kind == "participation_started" || activity.Kind == "participation_task_completed") {
			delete(s.activities, task.ID)
		}
	}
	if scheduleID = strings.TrimSpace(scheduleID); scheduleID != "" {
		for id, activity := range s.activities {
			if activity.Kind == "participation_schedule_dispatched" && activity.AccountID == scheduleID {
				delete(s.activities, id)
			}
		}
	}
	activity := s.addActivityLocked("participation_batch_executed", "", message, time.Now())
	activity.Title = label
	activity.TaskIDs = map[string]string{}
	seen := make(map[string]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		accountID = strings.TrimSpace(accountID)
		if accountID == "" {
			continue
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		activity.AccountIDs = append(activity.AccountIDs, accountID)
		if task := s.participationTasks[accountID]; task != nil && task.ID != "" {
			activity.TaskIDs[accountID] = task.ID
			task.BatchActivityID = activity.ID
			if task.Active {
				activity.Active = true
			}
		}
	}
	if len(activity.AccountIDs) > 0 && !activity.Active {
		s.finalizeParticipationBatchActivityLocked(activity.ID, false, time.Now())
	}
	return s.saveLocked()
}

// StopParticipationBatch prevents future assignments for every account in the
// batch. Already-issued native requests are intentionally not interrupted.
func (s *Store) StopParticipationBatch(activityID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	activity := s.activities[strings.TrimSpace(activityID)]
	if activity == nil || activity.Kind != "participation_batch_executed" {
		return nil, errors.New("红包参与批次不存在")
	}
	stoppedAt := time.Now()
	now := stoppedAt.Format(time.RFC3339Nano)
	for _, accountID := range activity.AccountIDs {
		taskID := activity.TaskIDs[accountID]
		if task := s.participationTasks[accountID]; task != nil && task.Active && (taskID == "" || task.ID == taskID) {
			task.Active = false
			task.EndedAt = now
			task.EndReason = "批次手动停止"
		}
	}
	s.finalizeParticipationBatchActivityLocked(activity.ID, true, stoppedAt)
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return append([]string(nil), activity.AccountIDs...), nil
}

// migrateLegacyBatchActivitiesLocked upgrades batches written before account
// membership was persisted. Only the exact currently-active task activity for
// each account can be folded in, bounded to the batch's preparation window.
func (s *Store) migrateLegacyBatchActivitiesLocked() bool {
	migrated := false
	for _, batch := range s.activities {
		if batch.Kind != "participation_batch_executed" {
			continue
		}
		batchAt, err := time.Parse(time.RFC3339Nano, batch.CreatedAt)
		if err != nil {
			continue
		}
		if len(batch.AccountIDs) == 0 {
			for accountID, task := range s.participationTasks {
				if task == nil || !task.Active {
					continue
				}
				started := s.activities[task.ID]
				if started == nil || started.Kind != "participation_started" {
					continue
				}
				startedAt, parseErr := time.Parse(time.RFC3339Nano, started.CreatedAt)
				if parseErr != nil || startedAt.After(batchAt) || batchAt.Sub(startedAt) > 5*time.Minute {
					continue
				}
				batch.AccountIDs = append(batch.AccountIDs, accountID)
				delete(s.activities, task.ID)
				batch.Active = true
				migrated = true
			}
		}
		if len(batch.AccountIDs) > 0 && len(batch.TaskIDs) == 0 && batch.Active {
			sort.Strings(batch.AccountIDs)
			batch.TaskIDs = map[string]string{}
			for _, accountID := range batch.AccountIDs {
				if task := s.participationTasks[accountID]; task != nil && task.Active {
					batch.TaskIDs[accountID] = task.ID
					task.BatchActivityID = batch.ID
				}
			}
			batch.Title = participationBatchTitleFromLabel(batch.Label)
			migrated = true
		}
	}
	return migrated
}

func participationBatchTitleFromLabel(label string) string {
	for _, title := range []string{"立即执行", "指定日期", "每天固定时间", "间隔执行"} {
		if strings.Contains(label, title) {
			return title
		}
	}
	return "红包参与任务"
}

func (s *Store) addActivityLocked(kind, accountID, label string, now time.Time) *Activity {
	sum := sha256.Sum256([]byte(kind + "\x00" + accountID + "\x00" + label + "\x00" + now.Format(time.RFC3339Nano)))
	id := hex.EncodeToString(sum[:12])
	activity := &Activity{ID: id, Kind: kind, AccountID: accountID, Label: label, CreatedAt: now.Format(time.RFC3339Nano)}
	s.activities[id] = activity
	return activity
}

func participationScheduleLabel(schedule ParticipationSchedule) string {
	switch schedule.Mode {
	case ParticipationScheduleOnce:
		if runAt, err := time.Parse(time.RFC3339Nano, schedule.RunAt); err == nil {
			return fmt.Sprintf("指定日期 %s 的红包参与计划", runAt.Local().Format("2006-01-02 15:04"))
		}
		return "指定日期红包参与计划"
	case ParticipationScheduleDaily:
		return fmt.Sprintf("每天 %s 执行的红包参与计划", schedule.DailyTime)
	case ParticipationScheduleInterval:
		return fmt.Sprintf("每 %s执行的红包参与计划", formatScheduleInterval(schedule.IntervalSeconds))
	default:
		return "红包参与计划"
	}
}

func participationScheduleModeName(mode string) string {
	switch mode {
	case ParticipationScheduleOnce:
		return "指定日期"
	case ParticipationScheduleDaily:
		return "每天固定时间"
	case ParticipationScheduleInterval:
		return "间隔执行"
	default:
		return "红包参与计划"
	}
}

func formatScheduleInterval(seconds int) string {
	if seconds%(24*60*60) == 0 {
		return fmt.Sprintf("%d 天", seconds/(24*60*60))
	}
	if seconds%(60*60) == 0 {
		return fmt.Sprintf("%d 小时", seconds/(60*60))
	}
	return fmt.Sprintf("%d 分钟", seconds/60)
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
