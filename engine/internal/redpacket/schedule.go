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
		s.addActivityLocked("participation_schedule_dispatched", "", participationScheduleLabel(*schedule)+"已触发", now)
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
func (s *Store) RecordParticipationBatchResult(scheduleID, mode string, started, skipped int) error {
	mode = strings.TrimSpace(mode)
	label := "立即执行红包参与"
	if strings.TrimSpace(scheduleID) != "" {
		label = "红包参与计划执行"
		if mode == ParticipationScheduleDaily {
			label = "每天固定时间计划执行"
		} else if mode == ParticipationScheduleInterval {
			label = "间隔计划执行"
		} else if mode == ParticipationScheduleOnce {
			label = "指定日期计划执行"
		}
	}
	message := fmt.Sprintf("%s：成功启动 %d 个实例", label, maxInt(started, 0))
	if skipped > 0 {
		message += fmt.Sprintf("，跳过 %d 个", skipped)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addActivityLocked("participation_batch_executed", "", message, time.Now())
	return s.saveLocked()
}

func (s *Store) addActivityLocked(kind, accountID, label string, now time.Time) {
	sum := sha256.Sum256([]byte(kind + "\x00" + accountID + "\x00" + label + "\x00" + now.Format(time.RFC3339Nano)))
	id := hex.EncodeToString(sum[:12])
	s.activities[id] = &Activity{ID: id, Kind: kind, AccountID: accountID, Label: label, CreatedAt: now.Format(time.RFC3339Nano)}
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
