// Package redpacket contains the red-packet-only live-room monitor.  It uses
// the same signed lottery_info request path as 福宝, but deliberately filters
// out 福袋/lottery payloads before they reach the UI or event history.
package redpacket

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fubao.ccvar.com/engine/internal/live/httpclient"
	"fubao.ccvar.com/engine/internal/rooms"
)

const (
	storeVersion         = 14
	unknownProbeInterval = 10 * time.Second
	offlineProbeInterval = 60 * time.Second
	livePacketInterval   = 15 * time.Second
	activePacketInterval = 5 * time.Second
	defaultProbeSlots    = 64
	defaultBulkWorkers   = 64
	// A high-priority source may fill a small burst of ready slots, after
	// which a ready lower-priority source gets a turn. This preserves the
	// source order without starving imported or center-library rooms while a
	// busy following feed is continuously producing due probes.
	bulkPriorityBurst = 8

	ParticipationPacketTypeAll     = "all"
	ParticipationPacketTypeGift    = "gift"
	ParticipationPacketTypeDiamond = "diamond"

	ParticipationFollowPolicyAll      = "all"
	ParticipationFollowPolicyPriority = "follow_priority"
	ParticipationFollowPolicyOnly     = "follow_only"
)

// Monitor is safe metadata returned to the frontend. Cookie values are never
// present in this type or in the persisted files.
type Monitor struct {
	ID                     string `json:"id"`
	RoomID                 string `json:"room_id"`
	WebRID                 string `json:"web_rid,omitempty"`
	ActualRoomID           string `json:"actual_room_id,omitempty"`
	Name                   string `json:"name,omitempty"`
	StreamerName           string `json:"streamer_name,omitempty"`
	Source                 string `json:"source,omitempty"`
	AccountID              string `json:"account_id,omitempty"`
	AccountName            string `json:"account_name,omitempty"`
	Status                 string `json:"status"`
	ConnectionStatus       string `json:"connection_status"`
	LiveStatus             string `json:"live_status"`
	LiveStatusSource       string `json:"live_status_source,omitempty"`
	LiveRawStatus          string `json:"live_raw_status,omitempty"`
	Enabled                bool   `json:"enabled"`
	LastCheckedAt          string `json:"last_checked_at,omitempty"`
	LastLiveCheckedAt      string `json:"last_live_checked_at,omitempty"`
	LiveStartedAt          string `json:"live_started_at,omitempty"`
	LastRedPacketCheckedAt string `json:"last_red_packet_checked_at,omitempty"`
	LastEventAt            string `json:"last_event_at,omitempty"`
	LastError              string `json:"last_error,omitempty"`
	LastPacketID           string `json:"last_packet_id,omitempty"`
	LastPacketTitle        string `json:"last_packet_title,omitempty"`
	LastParticipantCount   int    `json:"last_participant_count,omitempty"`
	PacketCount            int    `json:"packet_count"`
	CreatedAt              string `json:"created_at"`
	UpdatedAt              string `json:"updated_at"`
}

type Event struct {
	ID               string  `json:"id"`
	MonitorID        string  `json:"monitor_id"`
	AccountID        string  `json:"account_id,omitempty"`
	AccountName      string  `json:"account_name,omitempty"`
	RoomID           string  `json:"room_id"`
	RoomName         string  `json:"room_name,omitempty"`
	StreamerName     string  `json:"streamer_name,omitempty"`
	WebRID           string  `json:"web_rid,omitempty"`
	PacketID         string  `json:"packet_id"`
	Title            string  `json:"title,omitempty"`
	Prize            string  `json:"prize,omitempty"`
	Source           string  `json:"source"`
	DataSource       string  `json:"data_source,omitempty"`
	DetectedAt       string  `json:"detected_at"`
	DrawAt           string  `json:"draw_at,omitempty"`
	ExpiresAt        string  `json:"expires_at,omitempty"`
	ParticipantCount int     `json:"participant_count,omitempty"`
	TotalDiamonds    float64 `json:"total_diamonds,omitempty"`
	ShareCount       int     `json:"share_count,omitempty"`
	ActualRoomID     string  `json:"-"`
	JoinBoxID        string  `json:"-"`
	AnchorID         string  `json:"-"`
	BoxType          string  `json:"-"`
	SendTime         string  `json:"-"`
	DelayTime        string  `json:"-"`
}

type CenterEvent struct {
	WebRID           string
	PacketID         string
	ActualRoomID     string
	JoinBoxID        string
	AnchorID         string
	BoxType          string
	SendTime         string
	DelayTime        string
	RoomName         string
	StreamerName     string
	Title            string
	Prize            string
	Source           string
	DetectedAt       string
	DrawAt           string
	ExpiresAt        string
	ParticipantCount int
	TotalDiamonds    float64
	ShareCount       int
}

// ParticipationRecord is safe audit metadata for one account/event attempt.
// It deliberately excludes Cookie values, signed URLs, headers and raw bodies.
type ParticipationRecord struct {
	ID            string                `json:"id"`
	EventID       string                `json:"event_id"`
	AccountID     string                `json:"account_id"`
	AccountName   string                `json:"account_name"`
	TaskID        string                `json:"task_id,omitempty"`
	Settings      ParticipationSettings `json:"settings,omitempty"`
	RoomID        string                `json:"room_id,omitempty"`
	WebRID        string                `json:"web_rid,omitempty"`
	ActualRoomID  string                `json:"actual_room_id,omitempty"`
	RoomName      string                `json:"room_name,omitempty"`
	StreamerName  string                `json:"streamer_name,omitempty"`
	PacketID      string                `json:"packet_id"`
	Title         string                `json:"title,omitempty"`
	Prize         string                `json:"prize,omitempty"`
	Award         string                `json:"award,omitempty"`
	DrawAt        string                `json:"draw_at,omitempty"`
	ExpiresAt     string                `json:"expires_at,omitempty"`
	Endpoint      string                `json:"endpoint,omitempty"`
	Status        string                `json:"status"`
	Message       string                `json:"message,omitempty"`
	AttemptCount  int                   `json:"attempt_count"`
	Joined        bool                  `json:"joined"`
	Won           bool                  `json:"won,omitempty"`
	JoinedAt      string                `json:"joined_at,omitempty"`
	CooldownUntil string                `json:"cooldown_until,omitempty"`
	CreatedAt     string                `json:"created_at"`
	UpdatedAt     string                `json:"updated_at"`
}

// ParticipationState explains why a browser account can or cannot accept new
// join tasks. It contains only safe counters and timing metadata.
type ParticipationState struct {
	AccountID     string `json:"account_id"`
	TaskID        string `json:"task_id,omitempty"`
	Active        bool   `json:"active"`
	JoinCount     int    `json:"join_count"`
	WinCount      int    `json:"win_count"`
	Stopped       bool   `json:"stopped"`
	StopReason    string `json:"stop_reason,omitempty"`
	WaitingDraw   bool   `json:"waiting_draw"`
	WaitingReason string `json:"waiting_reason,omitempty"`
	CooldownUntil string `json:"cooldown_until,omitempty"`
}

// ParticipationTask is one explicit browser-card start. Stop limits are
// scoped to this task; historical participation records remain statistics and
// never prevent a later start from creating a fresh task.
type ParticipationTask struct {
	ID              string `json:"id"`
	AccountID       string `json:"account_id"`
	AccountName     string `json:"account_name,omitempty"`
	BatchActivityID string `json:"batch_activity_id,omitempty"`
	// Settings is the immutable policy snapshot captured when this explicit
	// task starts. Global settings edits affect only future tasks.
	Settings  ParticipationSettings `json:"settings,omitempty"`
	Active    bool                  `json:"active"`
	StartedAt string                `json:"started_at"`
	EndedAt   string                `json:"ended_at,omitempty"`
	EndReason string                `json:"end_reason,omitempty"`
}

// ParticipationOverview is the safe all-time aggregate shown in the sidebar.
// It is derived from idempotent account/event records, never from frontend
// counters, so relaunching the client cannot duplicate totals.
type ParticipationOverview struct {
	JoinCount   int     `json:"join_count"`
	WinCount    int     `json:"win_count"`
	WinDiamonds float64 `json:"win_diamonds"`
}

// ActivityAccountSummary freezes one exact task's result for activity detail.
// A later task for the same canonical account therefore cannot change history.
type ActivityAccountSummary struct {
	AccountID   string  `json:"account_id"`
	AccountName string  `json:"account_name,omitempty"`
	TaskID      string  `json:"task_id"`
	JoinCount   int     `json:"join_count"`
	WinCount    int     `json:"win_count"`
	WinDiamonds float64 `json:"win_diamonds"`
	EndReason   string  `json:"end_reason,omitempty"`
}

// ParticipationRestartReconciliation reports task state repaired after a
// real engine restart. Native page contexts never survive process exit.
type ParticipationRestartReconciliation struct {
	StoppedAccountIDs []string `json:"stopped_account_ids"`
	PendingAccountIDs []string `json:"pending_account_ids"`
}

// ParticipationTrace is a credential-free native request audit row. Request
// signatures, URLs, headers, Cookies and raw unfiltered responses are never
// stored or exposed through this type.
type ParticipationTrace struct {
	ID               string            `json:"id"`
	TaskID           string            `json:"task_id,omitempty"`
	EventID          string            `json:"event_id"`
	AccountID        string            `json:"account_id"`
	AccountName      string            `json:"account_name,omitempty"`
	Action           string            `json:"action"`
	Endpoint         string            `json:"endpoint,omitempty"`
	HTTPStatus       int               `json:"http_status,omitempty"`
	RequestParams    map[string]string `json:"request_params"`
	ResponseParams   string            `json:"response_params,omitempty"`
	Error            string            `json:"error,omitempty"`
	FollowPolicy     string            `json:"follow_policy,omitempty"`
	Followed         bool              `json:"followed,omitempty"`
	FollowMatchKnown bool              `json:"follow_match_known,omitempty"`
	CreatedAt        string            `json:"created_at"`
}

// ParticipationSettings are safe global limits applied independently to each
// participation account. Zero keeps the corresponding limit disabled.
type ParticipationSettings struct {
	StopAfterJoins           int    `json:"stop_after_joins"`
	CooldownSeconds          int    `json:"cooldown_seconds"`
	StopAfterWins            int    `json:"stop_after_wins"`
	DrawResultTimeoutSeconds int    `json:"draw_result_timeout_seconds"`
	MinimumDiamonds          int    `json:"minimum_diamonds"`
	PacketType               string `json:"packet_type"`
	FollowPolicy             string `json:"follow_policy"`
}

// MonitoringSettings are safe, persisted throughput controls for the native
// room-monitor pipeline. They contain no account credentials or request data.
type MonitoringSettings struct {
	GlobalRequestIntervalMS  int `json:"global_request_interval_ms"`
	AccountRequestIntervalMS int `json:"account_request_interval_ms"`
	GlobalConcurrency        int `json:"global_concurrency"`
	AccountConcurrency       int `json:"account_concurrency"`
	ProbeConcurrency         int `json:"probe_concurrency"`
}

// Activity is safe sidebar history. It never contains credentials, request
// URLs, signatures, headers, or raw interface responses.
type Activity struct {
	ID               string                   `json:"id"`
	Kind             string                   `json:"kind"`
	AccountID        string                   `json:"account_id,omitempty"`
	AccountIDs       []string                 `json:"account_ids,omitempty"`
	TaskIDs          map[string]string        `json:"task_ids,omitempty"`
	AccountSummaries []ActivityAccountSummary `json:"account_summaries,omitempty"`
	Title            string                   `json:"title,omitempty"`
	Label            string                   `json:"label"`
	Active           bool                     `json:"active,omitempty"`
	JoinCount        int                      `json:"join_count,omitempty"`
	WinCount         int                      `json:"win_count,omitempty"`
	WinDiamonds      float64                  `json:"win_diamonds,omitempty"`
	CreatedAt        string                   `json:"created_at"`
	FinishedAt       string                   `json:"finished_at,omitempty"`
	StoppedAt        string                   `json:"stopped_at,omitempty"`
	EndReason        string                   `json:"end_reason,omitempty"`
}

// ParticipationSchedule is a credential-free persisted trigger definition.
// The frontend claims due runs, then prepares each native browser context.
type ParticipationSchedule struct {
	ID                  string `json:"id"`
	Mode                string `json:"mode"`
	Enabled             bool   `json:"enabled"`
	RunAt               string `json:"run_at,omitempty"`
	DailyTime           string `json:"daily_time,omitempty"`
	IntervalSeconds     int    `json:"interval_seconds,omitempty"`
	NextRunAt           string `json:"next_run_at"`
	MonitorPrewarmedFor string `json:"monitor_prewarmed_for,omitempty"`
	LastRunAt           string `json:"last_run_at,omitempty"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

// ParticipationScheduleExecution is the safe claim returned when a persisted
// plan becomes due. It contains no browser credentials or request metadata.
type ParticipationScheduleExecution struct {
	ScheduleID string `json:"schedule_id"`
	Mode       string `json:"mode"`
	Label      string `json:"label"`
	DueAt      string `json:"due_at"`
}

// nativeParticipationMetadata is persisted only in the permission-restricted
// Go store. It is never part of Event JSON returned to the frontend.
type nativeParticipationMetadata struct {
	ActualRoomID string `json:"actual_room_id,omitempty"`
	JoinBoxID    string `json:"join_box_id,omitempty"`
	AnchorID     string `json:"anchor_id,omitempty"`
	BoxType      string `json:"box_type,omitempty"`
	SendTime     string `json:"send_time,omitempty"`
	DelayTime    string `json:"delay_time,omitempty"`
}

type file struct {
	Version                int                                    `json:"version"`
	Monitors               []*Monitor                             `json:"monitors"`
	Events                 []*Event                               `json:"events"`
	NativeParticipation    map[string]nativeParticipationMetadata `json:"native_participation,omitempty"`
	ParticipationRecords   []*ParticipationRecord                 `json:"participation_records,omitempty"`
	ParticipationSettings  ParticipationSettings                  `json:"participation_settings"`
	MonitoringSettings     MonitoringSettings                     `json:"monitoring_settings"`
	ParticipationTasks     []*ParticipationTask                   `json:"participation_tasks,omitempty"`
	ParticipationTraces    []*ParticipationTrace                  `json:"participation_traces,omitempty"`
	ParticipationSchedules []*ParticipationSchedule               `json:"participation_schedules,omitempty"`
	Activities             []*Activity                            `json:"activities,omitempty"`
}

type Store struct {
	mu                     sync.Mutex
	path                   string
	monitors               map[string]*Monitor
	events                 map[string]*Event
	participations         map[string]*ParticipationRecord
	participationTasks     map[string]*ParticipationTask
	participationTraces    map[string]*ParticipationTrace
	participationSchedules map[string]*ParticipationSchedule
	settings               ParticipationSettings
	monitoringSettings     MonitoringSettings
	activities             map[string]*Activity
	runtime                map[string]context.CancelFunc
	pool                   *accountPool
	bulkCancel             context.CancelFunc
	bulkIDs                map[string]struct{}
	requestRecorder        func(accountID string, requestErr error)
	eventHandler           func(Event)
	liveResultHandler      func(roomID, status string, checkedAt time.Time)
	persistDirty           bool
	persistScheduled       bool
	probeSlots             chan struct{}
}

type MonitorSummary struct {
	Total        int `json:"total"`
	Enabled      int `json:"enabled"`
	Running      int `json:"running"`
	FirstChecked int `json:"first_checked"`
	PendingFirst int `json:"pending_first"`
	LiveRunning  int `json:"live_running"`
	Errors       int `json:"errors"`
}

type MonitorPage struct {
	Items   []Monitor      `json:"items"`
	Summary MonitorSummary `json:"summary"`
}

type monitorJob struct {
	id           string
	ctx          context.Context
	cancel       context.CancelFunc
	source       monitorSource
	pool         *accountPool
	initialDelay time.Duration
}

func NewStore(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("红包监测数据目录为空")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建红包监测数据目录失败: %w", err)
	}
	s := &Store{
		path:                   filepath.Join(dataDir, "red_packet_monitors.json"),
		monitors:               map[string]*Monitor{},
		events:                 map[string]*Event{},
		participations:         map[string]*ParticipationRecord{},
		participationTasks:     map[string]*ParticipationTask{},
		participationTraces:    map[string]*ParticipationTrace{},
		participationSchedules: map[string]*ParticipationSchedule{},
		activities:             map[string]*Activity{},
		runtime:                map[string]context.CancelFunc{},
		bulkIDs:                map[string]struct{}{},
		settings:               normalizeParticipationSettings(ParticipationSettings{}),
		monitoringSettings:     normalizeMonitoringSettings(MonitoringSettings{}),
	}
	s.probeSlots = make(chan struct{}, s.monitoringSettings.ProbeConcurrency)
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// SetRequestRecorder attaches the account-store hook used to keep the safe
// monitoring request counters local to this Go engine. The hook receives no
// Cookie or response data.
func (s *Store) SetRequestRecorder(recorder func(accountID string, requestErr error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestRecorder = recorder
}

// SetEventHandler attaches a private engine callback for newly detected red
// packets. It receives safe event metadata only and is invoked outside the
// store lock so API participation cannot delay room polling.
func (s *Store) SetEventHandler(handler func(Event)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventHandler = handler
}

// SetLiveResultHandler attaches a private callback for successful, definitive
// live/offline probes. Error and unknown outcomes are deliberately excluded so
// room-retention policy cannot mistake a transient request failure for an
// offline day. The callback is invoked outside the store lock.
func (s *Store) SetLiveResultHandler(handler func(roomID, status string, checkedAt time.Time)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveResultHandler = handler
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取红包监测数据失败: %w", err)
	}
	var payload file
	if err := json.Unmarshal(b, &payload); err != nil {
		return fmt.Errorf("解析红包监测数据失败: %w", err)
	}
	for _, monitor := range payload.Monitors {
		if monitor != nil && monitor.ID != "" {
			// Runtime goroutines are intentionally not persisted. A restarted
			// engine must never claim that a monitor is still running.
			if monitor.Status == "running" || monitor.Status == "error" {
				monitor.Status = "stopped"
				monitor.ConnectionStatus = "disconnected"
			}
			s.monitors[monitor.ID] = monitor
		}
	}
	for _, event := range payload.Events {
		if event != nil && event.ID != "" {
			if metadata, ok := payload.NativeParticipation[event.ID]; ok {
				event.ActualRoomID = metadata.ActualRoomID
				event.JoinBoxID = metadata.JoinBoxID
				event.AnchorID = metadata.AnchorID
				event.BoxType = metadata.BoxType
				event.SendTime = metadata.SendTime
				event.DelayTime = metadata.DelayTime
			}
			s.events[event.ID] = event
		}
	}
	for _, record := range payload.ParticipationRecords {
		if record != nil && record.ID != "" && record.EventID != "" && record.AccountID != "" {
			s.participations[record.ID] = record
		}
	}
	for _, task := range payload.ParticipationTasks {
		if task != nil && task.ID != "" && task.AccountID != "" {
			s.participationTasks[task.AccountID] = task
		}
	}
	for _, trace := range payload.ParticipationTraces {
		if trace != nil && trace.ID != "" {
			s.participationTraces[trace.ID] = trace
		}
	}
	for _, schedule := range payload.ParticipationSchedules {
		if schedule != nil && schedule.ID != "" && schedule.Enabled && schedule.NextRunAt != "" {
			s.participationSchedules[schedule.ID] = schedule
		}
	}
	s.settings = normalizeParticipationSettings(payload.ParticipationSettings)
	s.monitoringSettings = normalizeMonitoringSettings(payload.MonitoringSettings)
	s.probeSlots = make(chan struct{}, s.monitoringSettings.ProbeConcurrency)
	migrated := payload.Version < storeVersion
	for _, record := range s.participations {
		if !record.Joined || participationDrawTerminal(record.Status) {
			continue
		}
		deadlineText := firstNonEmpty(record.DrawAt, record.ExpiresAt)
		if deadlineText == "" {
			if event := s.events[record.EventID]; event != nil {
				deadlineText = firstNonEmpty(event.DrawAt, event.ExpiresAt)
			}
		}
		settings := s.settings
		if snapshot, ok := taskSettingsSnapshot(record.Settings); ok {
			settings = snapshot
		}
		deadlineGrace := time.Duration(settings.DrawResultTimeoutSeconds) * time.Second
		deadline, parseErr := time.Parse(time.RFC3339Nano, deadlineText)
		if parseErr != nil || time.Now().Before(deadline.Add(deadlineGrace)) {
			continue
		}
		record.Status = "draw_error"
		record.Message = fmt.Sprintf("开奖异常：超过开奖时间 %d 秒仍未获取到结果", settings.DrawResultTimeoutSeconds)
		record.Endpoint = "receive"
		record.UpdatedAt = time.Now().Format(time.RFC3339Nano)
		migrated = true
	}
	for _, activity := range payload.Activities {
		if activity != nil && activity.ID != "" && activity.Label != "" {
			s.activities[activity.ID] = activity
		}
	}
	if s.migrateLegacyBatchActivitiesLocked() {
		migrated = true
	}
	if migrated {
		return s.saveLocked()
	}
	return nil
}

func (s *Store) saveLocked() error {
	monitors := make([]*Monitor, 0, len(s.monitors))
	for _, item := range s.monitors {
		copy := *item
		monitors = append(monitors, &copy)
	}
	events := make([]*Event, 0, len(s.events))
	nativeParticipation := make(map[string]nativeParticipationMetadata)
	for _, item := range s.events {
		copy := *item
		events = append(events, &copy)
		if item.ActualRoomID != "" || item.JoinBoxID != "" || item.AnchorID != "" || item.BoxType != "" || item.SendTime != "" || item.DelayTime != "" {
			nativeParticipation[item.ID] = nativeParticipationMetadata{
				ActualRoomID: item.ActualRoomID, JoinBoxID: item.JoinBoxID, AnchorID: item.AnchorID,
				BoxType: item.BoxType, SendTime: item.SendTime, DelayTime: item.DelayTime,
			}
		}
	}
	participations := make([]*ParticipationRecord, 0, len(s.participations))
	for _, item := range s.participations {
		copy := *item
		participations = append(participations, &copy)
	}
	participationTasks := make([]*ParticipationTask, 0, len(s.participationTasks))
	for _, item := range s.participationTasks {
		copy := *item
		participationTasks = append(participationTasks, &copy)
	}
	participationTraces := make([]*ParticipationTrace, 0, len(s.participationTraces))
	for _, item := range s.participationTraces {
		copy := *item
		copy.RequestParams = cloneStringMap(item.RequestParams)
		participationTraces = append(participationTraces, &copy)
	}
	participationSchedules := make([]*ParticipationSchedule, 0, len(s.participationSchedules))
	for _, item := range s.participationSchedules {
		copy := *item
		participationSchedules = append(participationSchedules, &copy)
	}
	activities := make([]*Activity, 0, len(s.activities))
	for _, item := range s.activities {
		copy := *item
		copy.AccountIDs = append([]string(nil), item.AccountIDs...)
		activities = append(activities, &copy)
	}
	sort.Slice(monitors, func(i, j int) bool { return monitors[i].Name < monitors[j].Name })
	sort.Slice(events, func(i, j int) bool { return events[i].DetectedAt > events[j].DetectedAt })
	sort.Slice(participations, func(i, j int) bool { return participations[i].UpdatedAt > participations[j].UpdatedAt })
	sort.Slice(participationTraces, func(i, j int) bool { return participationTraces[i].CreatedAt > participationTraces[j].CreatedAt })
	sort.Slice(participationSchedules, func(i, j int) bool { return participationSchedules[i].NextRunAt < participationSchedules[j].NextRunAt })
	if len(participationTraces) > 500 {
		participationTraces = participationTraces[:500]
	}
	sort.Slice(activities, func(i, j int) bool { return activities[i].CreatedAt > activities[j].CreatedAt })
	if len(activities) > 100 {
		activities = activities[:100]
	}
	payload, err := json.Marshal(file{
		Version: storeVersion, Monitors: monitors, Events: events, NativeParticipation: nativeParticipation, ParticipationRecords: participations,
		ParticipationSettings: s.settings, MonitoringSettings: s.monitoringSettings,
		ParticipationTasks: participationTasks, ParticipationTraces: participationTraces,
		ParticipationSchedules: participationSchedules, Activities: activities,
	})
	if err != nil {
		return fmt.Errorf("序列化红包监测数据失败: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return fmt.Errorf("写入红包监测临时文件失败: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存红包监测数据失败: %w", err)
	}
	return nil
}

// scheduleSaveLocked coalesces high-frequency probe state changes. Serializing
// a store with tens of thousands of rooms after every probe can monopolize the
// engine RPC loop and Windows message pump for minutes.
func (s *Store) scheduleSaveLocked() {
	s.persistDirty = true
	if s.persistScheduled {
		return
	}
	s.persistScheduled = true
	go func() {
		time.Sleep(3 * time.Second)
		s.mu.Lock()
		if s.persistDirty {
			s.persistDirty = false
			_ = s.saveLocked()
		}
		s.persistScheduled = false
		again := s.persistDirty
		if again {
			s.scheduleSaveLocked()
		}
		s.mu.Unlock()
	}()
}

// ReserveParticipation durably claims one account/event pair before the native
// request is sent. Existing records make the operation idempotent across engine
// restarts as well as repeated event callbacks.
func (s *Store) ReserveParticipation(event Event, accountID, accountName string) (bool, error) {
	accountID = strings.TrimSpace(accountID)
	if strings.TrimSpace(event.ID) == "" || accountID == "" || strings.TrimSpace(event.JoinBoxID) == "" {
		return false, errors.New("红包参与记录参数不完整")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.participationTasks[accountID]
	if task == nil || !task.Active {
		return false, errors.New("红包参与任务尚未启动")
	}
	id := participationRecordID(accountID, event.ID)
	if s.participations[id] != nil {
		return false, nil
	}
	now := time.Now().Format(time.RFC3339Nano)
	s.participations[id] = &ParticipationRecord{
		ID: id, EventID: event.ID,
		AccountID: accountID, AccountName: strings.TrimSpace(accountName), TaskID: task.ID,
		Settings: task.Settings,
		RoomID:   event.RoomID, WebRID: event.WebRID, ActualRoomID: event.ActualRoomID,
		RoomName: event.RoomName, StreamerName: event.StreamerName,
		PacketID: event.JoinBoxID, Title: event.Title, Prize: event.Prize,
		DrawAt: event.DrawAt, ExpiresAt: event.ExpiresAt,
		Status: "pending", Message: "等待发送红包参与请求",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.saveLocked(); err != nil {
		delete(s.participations, id)
		return false, err
	}
	return true, nil
}

// CompleteParticipation updates a reserved record with safe result metadata.
func (s *Store) CompleteParticipation(eventID, accountID, endpoint, status, message string, attempts int, joined, won bool, cooldown time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.participations[participationRecordID(accountID, eventID)]
	if record == nil {
		return errors.New("红包参与记录不存在")
	}
	now := time.Now()
	record.Endpoint = strings.TrimSpace(endpoint)
	record.Status = strings.TrimSpace(status)
	record.Message = strings.TrimSpace(message)
	record.AttemptCount = attempts
	record.Joined = joined
	record.Won = won
	if joined && record.JoinedAt == "" {
		record.JoinedAt = now.Format(time.RFC3339Nano)
	}
	record.CooldownUntil = ""
	if cooldown > 0 {
		record.CooldownUntil = now.Add(cooldown).Format(time.RFC3339Nano)
	}
	record.UpdatedAt = now.Format(time.RFC3339Nano)
	return s.saveLocked()
}

func normalizeParticipationSettings(settings ParticipationSettings) ParticipationSettings {
	switch strings.ToLower(strings.TrimSpace(settings.PacketType)) {
	case ParticipationPacketTypeAll:
		settings.PacketType = ParticipationPacketTypeAll
	case ParticipationPacketTypeGift:
		settings.PacketType = ParticipationPacketTypeGift
	case ParticipationPacketTypeDiamond:
		settings.PacketType = ParticipationPacketTypeDiamond
	default:
		settings.PacketType = ParticipationPacketTypeDiamond
	}
	switch strings.ToLower(strings.TrimSpace(settings.FollowPolicy)) {
	case ParticipationFollowPolicyAll:
		settings.FollowPolicy = ParticipationFollowPolicyAll
	case ParticipationFollowPolicyOnly:
		settings.FollowPolicy = ParticipationFollowPolicyOnly
	case ParticipationFollowPolicyPriority:
		settings.FollowPolicy = ParticipationFollowPolicyPriority
	default:
		settings.FollowPolicy = ParticipationFollowPolicyPriority
	}
	if settings.StopAfterJoins < 0 {
		settings.StopAfterJoins = 0
	}
	if settings.StopAfterJoins > 100000 {
		settings.StopAfterJoins = 100000
	}
	if settings.CooldownSeconds < 0 {
		settings.CooldownSeconds = 0
	}
	if settings.CooldownSeconds > 86400 {
		settings.CooldownSeconds = 86400
	}
	if settings.StopAfterWins < 0 {
		settings.StopAfterWins = 0
	}
	if settings.StopAfterWins > 100000 {
		settings.StopAfterWins = 100000
	}
	if settings.DrawResultTimeoutSeconds <= 0 {
		settings.DrawResultTimeoutSeconds = 10
	}
	if settings.DrawResultTimeoutSeconds > 300 {
		settings.DrawResultTimeoutSeconds = 300
	}
	if settings.MinimumDiamonds <= 0 {
		settings.MinimumDiamonds = 1
	}
	if settings.MinimumDiamonds > 1000000 {
		settings.MinimumDiamonds = 1000000
	}
	return settings
}

func normalizeMonitoringSettings(settings MonitoringSettings) MonitoringSettings {
	if settings.GlobalRequestIntervalMS <= 0 {
		settings.GlobalRequestIntervalMS = int(defaultGlobalRequestInterval / time.Millisecond)
	}
	settings.GlobalRequestIntervalMS = maxInt(40, minInt(settings.GlobalRequestIntervalMS, 2000))
	if settings.AccountRequestIntervalMS <= 0 {
		settings.AccountRequestIntervalMS = int(defaultAccountRequestInterval / time.Millisecond)
	}
	settings.AccountRequestIntervalMS = maxInt(250, minInt(settings.AccountRequestIntervalMS, 5000))
	if settings.GlobalConcurrency <= 0 {
		settings.GlobalConcurrency = defaultGlobalConcurrency
	}
	settings.GlobalConcurrency = minInt(settings.GlobalConcurrency, 128)
	if settings.AccountConcurrency <= 0 {
		settings.AccountConcurrency = defaultAccountConcurrency
	}
	settings.AccountConcurrency = minInt(settings.AccountConcurrency, 8)
	if settings.ProbeConcurrency <= 0 {
		settings.ProbeConcurrency = defaultProbeSlots
	}
	settings.ProbeConcurrency = maxInt(8, minInt(settings.ProbeConcurrency, 256))
	return settings
}

func (settings MonitoringSettings) poolConfig() poolConfig {
	settings = normalizeMonitoringSettings(settings)
	return poolConfig{
		globalInterval:  time.Duration(settings.GlobalRequestIntervalMS) * time.Millisecond,
		accountInterval: time.Duration(settings.AccountRequestIntervalMS) * time.Millisecond,
		globalParallel:  settings.GlobalConcurrency,
		accountParallel: settings.AccountConcurrency,
	}
}

// GetMonitoringSettings returns credential-free monitor throughput controls.
func (s *Store) GetMonitoringSettings() MonitoringSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.monitoringSettings
}

// SetMonitoringSettings persists and hot-applies monitor throughput controls.
// Requests already in flight retain their original gates; subsequent requests
// and probes use the replacement gates without restarting room monitors.
func (s *Store) SetMonitoringSettings(settings MonitoringSettings) (MonitoringSettings, error) {
	settings = normalizeMonitoringSettings(settings)
	s.mu.Lock()
	previousSettings := s.monitoringSettings
	previousSlots := s.probeSlots
	s.monitoringSettings = settings
	s.probeSlots = make(chan struct{}, settings.ProbeConcurrency)
	pool := s.pool
	if err := s.saveLocked(); err != nil {
		s.monitoringSettings = previousSettings
		s.probeSlots = previousSlots
		s.mu.Unlock()
		return MonitoringSettings{}, err
	}
	s.mu.Unlock()
	if pool != nil {
		pool.applyConfig(settings.poolConfig())
	}
	return settings, nil
}

// GetParticipationSettings returns a safe copy for frontend display.
func (s *Store) GetParticipationSettings() ParticipationSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// GetParticipationSettingsForAccount returns the policy snapshot belonging to
// the account's active task. It intentionally falls back to the global policy
// for legacy tasks written before task snapshots were introduced.
func (s *Store) GetParticipationSettingsForAccount(accountID string) ParticipationSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.participationSettingsForAccountLocked(accountID)
}

// ParticipationSettingsForEvent returns the immutable policy snapshot for the
// task that reserved an account/event pair. This keeps delayed draw polling
// tied to the task that accepted the packet, even after a new task starts.
func (s *Store) ParticipationSettingsForEvent(eventID, accountID string) ParticipationSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.participations[participationRecordID(accountID, eventID)]
	if record != nil {
		if snapshot, ok := taskSettingsSnapshot(record.Settings); ok {
			return snapshot
		}
		if task := s.participationTaskByIDLocked(record.AccountID, record.TaskID); task != nil {
			if snapshot, ok := taskSettingsSnapshot(task.Settings); ok {
				return snapshot
			}
		}
	}
	return s.settings
}

func (s *Store) participationSettingsForAccountLocked(accountID string) ParticipationSettings {
	if task := s.participationTasks[strings.TrimSpace(accountID)]; task != nil {
		if snapshot, ok := taskSettingsSnapshot(task.Settings); ok {
			return snapshot
		}
	}
	return s.settings
}

func (s *Store) participationTaskByIDLocked(accountID, taskID string) *ParticipationTask {
	task := s.participationTasks[strings.TrimSpace(accountID)]
	if task == nil || strings.TrimSpace(task.ID) != strings.TrimSpace(taskID) {
		return nil
	}
	return task
}

func taskSettingsSnapshot(settings ParticipationSettings) (ParticipationSettings, bool) {
	// PacketType, FollowPolicy, and the normalized defaults make a real snapshot
	// distinguishable from an older zero-value task persisted by pre-snapshot
	// versions.
	if strings.TrimSpace(settings.PacketType) == "" && strings.TrimSpace(settings.FollowPolicy) == "" &&
		settings.DrawResultTimeoutSeconds == 0 && settings.MinimumDiamonds == 0 {
		return ParticipationSettings{}, false
	}
	return normalizeParticipationSettings(settings), true
}

// SetParticipationSettings persists global per-account participation limits.
func (s *Store) SetParticipationSettings(settings ParticipationSettings) (ParticipationSettings, error) {
	settings = normalizeParticipationSettings(settings)
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.settings
	s.settings = settings
	if err := s.saveLocked(); err != nil {
		s.settings = previous
		return ParticipationSettings{}, err
	}
	return settings, nil
}

// ParticipationPolicy is re-checked immediately before each native request.
// Counts are local durable records and limits come from the active task's
// immutable snapshot, so later global edits cannot change a running task.
func (s *Store) ParticipationPolicy(accountID string, now time.Time) (bool, time.Duration) {
	state, cooldown := s.participationState(accountID, now)
	return !state.Stopped && !state.WaitingDraw && state.CooldownUntil == "", cooldown
}

// GetParticipationState returns the safe limit/cooldown state used by browser
// card controls. A stop limit is distinct from a temporary cooldown.
func (s *Store) GetParticipationState(accountID string, now time.Time) ParticipationState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, _ := s.participationStateLocked(accountID, now)
	return state
}

func (s *Store) participationState(accountID string, now time.Time) (ParticipationState, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.participationStateLocked(accountID, now)
}

func (s *Store) participationStateLocked(accountID string, now time.Time) (ParticipationState, time.Duration) {
	task := s.participationTasks[accountID]
	if task == nil || !task.Active {
		return ParticipationState{AccountID: accountID}, 0
	}
	joins, wins, waitingDraws := 0, 0, 0
	var lastJoined time.Time
	for _, record := range s.participations {
		if record.AccountID != accountID || record.TaskID != task.ID {
			continue
		}
		if record.Joined {
			joins++
			joinedAt := firstNonEmpty(record.JoinedAt, record.UpdatedAt)
			if parsed, err := time.Parse(time.RFC3339Nano, joinedAt); err == nil && parsed.After(lastJoined) {
				lastJoined = parsed
			}
		}
		if record.Won {
			wins++
		}
		if record.Joined && !record.Won && !participationDrawTerminal(record.Status) {
			waitingDraws++
		}
	}
	state := ParticipationState{AccountID: accountID, TaskID: task.ID, Active: true, JoinCount: joins, WinCount: wins}
	settings := s.participationSettingsForAccountLocked(accountID)
	if settings.StopAfterJoins > 0 && joins >= settings.StopAfterJoins {
		state.Stopped = true
		state.StopReason = fmt.Sprintf("已达到参与停止上限（%d 次）", settings.StopAfterJoins)
		return state, 0
	}
	if settings.StopAfterWins > 0 && wins >= settings.StopAfterWins {
		state.Stopped = true
		state.StopReason = fmt.Sprintf("已达到中奖停止上限（%d 次）", settings.StopAfterWins)
		return state, 0
	}
	if waitingDraws > 0 {
		state.WaitingDraw = true
		state.WaitingReason = "上一轮红包尚未开奖"
		return state, 0
	}
	cooldown := time.Duration(settings.CooldownSeconds) * time.Second
	if cooldown > 0 && !lastJoined.IsZero() {
		remaining := lastJoined.Add(cooldown).Sub(now)
		if remaining > 0 {
			state.CooldownUntil = lastJoined.Add(cooldown).Format(time.RFC3339Nano)
			return state, remaining
		}
	}
	return state, cooldown
}

// PendingDraws returns accepted records whose personal draw result is still
// unresolved. The returned Event contains no credentials or signed data.
func (s *Store) PendingDraws(accountID string) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.participationTasks[accountID]
	if task == nil || !task.Active {
		return nil
	}
	items := make([]Event, 0)
	for _, record := range s.participations {
		if record.AccountID != accountID || record.TaskID != task.ID || !record.Joined || record.Won || participationDrawTerminal(record.Status) {
			continue
		}
		item := Event{
			ID: record.EventID, RoomID: record.RoomID, WebRID: record.WebRID,
			ActualRoomID: record.ActualRoomID, JoinBoxID: record.PacketID,
			RoomName: record.RoomName, StreamerName: record.StreamerName,
			Title: record.Title, Prize: record.Prize, DrawAt: record.DrawAt, ExpiresAt: record.ExpiresAt,
		}
		if event := s.events[record.EventID]; event != nil {
			item.MonitorID = event.MonitorID
			item.WebRID = firstNonEmpty(item.WebRID, event.WebRID)
			item.ActualRoomID = firstNonEmpty(item.ActualRoomID, event.ActualRoomID)
			item.DrawAt = firstNonEmpty(item.DrawAt, event.DrawAt)
			item.ExpiresAt = firstNonEmpty(item.ExpiresAt, event.ExpiresAt)
			item.AnchorID = event.AnchorID
		}
		if monitor := s.monitors[item.MonitorID]; monitor != nil {
			item.WebRID = firstNonEmpty(item.WebRID, monitor.WebRID)
			item.ActualRoomID = firstNonEmpty(item.ActualRoomID, monitor.ActualRoomID)
		}
		items = append(items, item)
	}
	return items
}

func participationDrawTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case "won", "not_won", "draw_error":
		return true
	default:
		return false
	}
}

// ResolveParticipationDraw persists one definitive personal result. It returns
// true only when the record newly transitions into a confirmed win.
func (s *Store) ResolveParticipationDraw(eventID, accountID, status, message, award string, attempts int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.participations[participationRecordID(accountID, eventID)]
	if record == nil {
		return false, errors.New("红包参与记录不存在")
	}
	if participationDrawTerminal(record.Status) {
		return false, nil
	}
	newWin := status == "won" && !record.Won
	record.Status = status
	record.Message = strings.TrimSpace(message)
	record.Award = strings.TrimSpace(award)
	record.Won = status == "won"
	record.Endpoint = "receive"
	record.AttemptCount += attempts
	record.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	return newWin, s.saveLocked()
}

// RecordParticipationStarted appends one real explicit start action.
func (s *Store) RecordParticipationStarted(accountID, accountName string) error {
	accountID = strings.TrimSpace(accountID)
	accountName = strings.TrimSpace(accountName)
	if accountName == "" {
		accountName = "参与账号"
	}
	now := time.Now()
	sum := sha256.Sum256([]byte(accountID + "\x00" + now.Format(time.RFC3339Nano)))
	activity := &Activity{
		ID: hex.EncodeToString(sum[:12]), Kind: "participation_started", AccountID: accountID,
		AccountIDs: []string{accountID}, Title: accountName, Active: true,
		Label: fmt.Sprintf("参与账号“%s”启动了红包参与", accountName), CreatedAt: now.Format(time.RFC3339Nano),
	}
	activity.TaskIDs = map[string]string{accountID: activity.ID}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.participationTasks[accountID] = &ParticipationTask{
		ID: activity.ID, AccountID: accountID, AccountName: accountName, Active: true, StartedAt: now.Format(time.RFC3339Nano),
		Settings: s.settings,
	}
	s.activities[activity.ID] = activity
	return s.saveLocked()
}

// FinishParticipationTask closes only the current explicit start. A later
// click creates a fresh task with zero per-task counters.
func (s *Store) FinishParticipationTask(accountID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.participationTasks[strings.TrimSpace(accountID)]
	if task == nil || !task.Active {
		return nil
	}
	now := time.Now()
	task.Active = false
	task.EndedAt = now.Format(time.RFC3339Nano)
	task.EndReason = strings.TrimSpace(reason)
	s.finalizeParticipationTaskActivityLocked(task, now)
	return s.saveLocked()
}

// ParticipationOverview returns persisted all-time totals across every
// account and task. A joined account/event record contributes exactly once.
func (s *Store) ParticipationOverview() ParticipationOverview {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := ParticipationOverview{}
	for _, record := range s.participations {
		if record == nil {
			continue
		}
		if record.Joined {
			result.JoinCount++
		}
		if record.Won {
			result.WinCount++
			result.WinDiamonds += participationAwardDiamonds(record.Award)
		}
	}
	return result
}

func (s *Store) participationTaskSummaryLocked(accountID, taskID string) ActivityAccountSummary {
	accountID, taskID = strings.TrimSpace(accountID), strings.TrimSpace(taskID)
	summary := ActivityAccountSummary{AccountID: accountID, TaskID: taskID}
	if task := s.participationTasks[accountID]; task != nil && task.ID == taskID {
		summary.AccountName = strings.TrimSpace(task.AccountName)
		summary.EndReason = strings.TrimSpace(task.EndReason)
	}
	for _, record := range s.participations {
		if record == nil || record.AccountID != accountID || record.TaskID != taskID {
			continue
		}
		if summary.AccountName == "" {
			summary.AccountName = strings.TrimSpace(record.AccountName)
		}
		if record.Joined {
			summary.JoinCount++
		}
		if record.Won {
			summary.WinCount++
			summary.WinDiamonds += participationAwardDiamonds(record.Award)
		}
	}
	if summary.AccountName == "" {
		summary.AccountName = "参与账号"
	}
	return summary
}

func (s *Store) finalizeParticipationTaskActivityLocked(task *ParticipationTask, now time.Time) {
	if task == nil {
		return
	}
	if batchID := strings.TrimSpace(task.BatchActivityID); batchID != "" {
		s.finalizeParticipationBatchActivityLocked(batchID, false, now)
		return
	}
	summary := s.participationTaskSummaryLocked(task.AccountID, task.ID)
	activity := s.activities[task.ID]
	if activity == nil {
		activity = &Activity{ID: task.ID, CreatedAt: task.StartedAt}
		s.activities[task.ID] = activity
	}
	activity.Kind = "participation_task_completed"
	activity.AccountID = task.AccountID
	activity.AccountIDs = []string{task.AccountID}
	activity.TaskIDs = map[string]string{task.AccountID: task.ID}
	activity.AccountSummaries = []ActivityAccountSummary{summary}
	activity.Title = summary.AccountName
	activity.Label = fmt.Sprintf("%s已完成：参与 %d 次，中奖 %d 次 / %s 钻",
		summary.AccountName, summary.JoinCount, summary.WinCount, formatParticipationDiamonds(summary.WinDiamonds))
	activity.Active = false
	activity.JoinCount = summary.JoinCount
	activity.WinCount = summary.WinCount
	activity.WinDiamonds = summary.WinDiamonds
	activity.FinishedAt = now.Format(time.RFC3339Nano)
	activity.EndReason = strings.TrimSpace(task.EndReason)
}

func (s *Store) finalizeParticipationBatchActivityLocked(activityID string, stopped bool, now time.Time) bool {
	activity := s.activities[strings.TrimSpace(activityID)]
	if activity == nil || activity.Kind != "participation_batch_executed" {
		return false
	}
	if !stopped {
		for accountID, taskID := range activity.TaskIDs {
			if task := s.participationTasks[accountID]; task != nil && task.ID == taskID && task.Active {
				return false
			}
		}
	}
	summaries := make([]ActivityAccountSummary, 0, len(activity.AccountIDs))
	joins, wins := 0, 0
	diamonds := 0.0
	for _, accountID := range activity.AccountIDs {
		taskID := activity.TaskIDs[accountID]
		if taskID == "" {
			continue
		}
		summary := s.participationTaskSummaryLocked(accountID, taskID)
		summaries = append(summaries, summary)
		joins += summary.JoinCount
		wins += summary.WinCount
		diamonds += summary.WinDiamonds
	}
	title := strings.TrimSpace(activity.Title)
	if title == "" {
		title = "红包参与任务"
	}
	state := "已完成"
	if stopped {
		state = "已停止"
		activity.StoppedAt = now.Format(time.RFC3339Nano)
	}
	activity.Label = fmt.Sprintf("“%s”%s：%d 个账号，参与 %d 次，中奖 %d 次 / %s 钻",
		title, state, len(activity.AccountIDs), joins, wins, formatParticipationDiamonds(diamonds))
	activity.Active = false
	activity.AccountSummaries = summaries
	activity.JoinCount = joins
	activity.WinCount = wins
	activity.WinDiamonds = diamonds
	activity.FinishedAt = now.Format(time.RFC3339Nano)
	return true
}

func participationAwardDiamonds(award string) float64 {
	award = strings.TrimSpace(award)
	if !strings.HasSuffix(award, "钻") {
		return 0
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(award, "钻")), 64)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func formatParticipationDiamonds(value float64) string {
	if value <= 0 {
		return "0"
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// ReconcileParticipationTasksAfterRestart removes stale "active" state left
// by a process restart. Tasks with an unresolved accepted packet remain only
// as resumable draw-result work; every other task is closed because its native
// browser page context no longer exists.
func (s *Store) ReconcileParticipationTasksAfterRestart(now time.Time) (ParticipationRestartReconciliation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := ParticipationRestartReconciliation{}
	previous := make(map[string]ParticipationTask)
	for accountID, task := range s.participationTasks {
		if task == nil || !task.Active {
			continue
		}
		pending := false
		for _, record := range s.participations {
			if record == nil || record.AccountID != accountID || record.TaskID != task.ID {
				continue
			}
			if record.Joined && !record.Won && !participationDrawTerminal(record.Status) {
				pending = true
				break
			}
		}
		if pending {
			result.PendingAccountIDs = append(result.PendingAccountIDs, accountID)
			continue
		}
		previous[accountID] = *task
		task.Active = false
		task.EndedAt = now.Format(time.RFC3339Nano)
		task.EndReason = "客户端重启，原生参与上下文已结束"
		s.finalizeParticipationTaskActivityLocked(task, now)
		result.StoppedAccountIDs = append(result.StoppedAccountIDs, accountID)
	}
	sort.Strings(result.StoppedAccountIDs)
	sort.Strings(result.PendingAccountIDs)
	if len(result.StoppedAccountIDs) == 0 {
		return result, nil
	}
	if err := s.saveLocked(); err != nil {
		for accountID, task := range previous {
			copy := task
			s.participationTasks[accountID] = &copy
		}
		return ParticipationRestartReconciliation{}, err
	}
	return result, nil
}

// Activities returns newest-first safe sidebar history.
func (s *Store) Activities() []Activity {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Activity, 0, len(s.activities))
	for _, activity := range s.activities {
		copy := *activity
		copy.AccountIDs = append([]string(nil), activity.AccountIDs...)
		copy.AccountSummaries = append([]ActivityAccountSummary(nil), activity.AccountSummaries...)
		if len(activity.TaskIDs) > 0 {
			copy.TaskIDs = make(map[string]string, len(activity.TaskIDs))
			for accountID, taskID := range activity.TaskIDs {
				copy.TaskIDs[accountID] = taskID
			}
		}
		if copy.Kind == "participation_batch_executed" && copy.Active {
			copy.Active = false
			for _, accountID := range copy.AccountIDs {
				taskID := copy.TaskIDs[accountID]
				if task := s.participationTasks[accountID]; task != nil && task.Active && (taskID == "" || task.ID == taskID) {
					copy.Active = true
					break
				}
			}
		}
		items = append(items, copy)
	}
	activityTime := func(item Activity) string {
		if item.FinishedAt != "" {
			return item.FinishedAt
		}
		if item.StoppedAt != "" {
			return item.StoppedAt
		}
		return item.CreatedAt
	}
	sort.Slice(items, func(i, j int) bool { return activityTime(items[i]) > activityTime(items[j]) })
	if len(items) > 100 {
		items = items[:100]
	}
	return items
}

// CancelParticipation removes a reservation only when no native request was
// issued. It is used when the user stops a browser card context while its task
// is still queued, so the event remains eligible if the context is enabled
// again before the packet expires.
func (s *Store) CancelParticipation(eventID, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := participationRecordID(accountID, eventID)
	record := s.participations[id]
	if record == nil {
		return nil
	}
	if record.Status != "pending" || record.AttemptCount != 0 {
		return errors.New("已发送的红包参与记录不能取消")
	}
	delete(s.participations, id)
	if err := s.saveLocked(); err != nil {
		s.participations[id] = record
		return err
	}
	return nil
}

// ResetParticipation reopens only a failed native attempt after the user
// explicitly prepares the matching browser page context. Successful,
// already-joined, expired and in-flight records remain idempotently sealed.
func (s *Store) ResetParticipation(eventID, accountID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := participationRecordID(accountID, eventID)
	record := s.participations[id]
	if record == nil {
		return true, nil
	}
	retryable := record.Status == "failed" || record.Status == "context_required" || record.Status == "network_error"
	if record.Joined || !retryable {
		return false, nil
	}
	delete(s.participations, id)
	if err := s.saveLocked(); err != nil {
		s.participations[id] = record
		return false, err
	}
	return true, nil
}

// ParticipationRecords returns newest-first safe audit rows for frontend IPC.
func (s *Store) ParticipationRecords() []ParticipationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]ParticipationRecord, 0, len(s.participations))
	for _, record := range s.participations {
		items = append(items, *record)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt > items[j].UpdatedAt })
	return items
}

// RecordParticipationTrace persists only allowlisted request parameters and a
// recursively redacted JSON response. It must be called before the response is
// reduced into user-facing participation status.
func (s *Store) RecordParticipationTrace(task PageParticipationTask, response PageParticipationResponse) error {
	now := time.Now()
	sum := sha256.Sum256([]byte(task.AccountID + "\x00" + task.EventID + "\x00" + task.Action + "\x00" + now.Format(time.RFC3339Nano)))
	params := map[string]string{
		"aid": "6383", "app_name": "douyin_web", "device_platform": "web", "live_id": "1",
		"web_rid": task.WebRID, "room_id": task.ActualRoomID, "box_id": task.BoxID,
	}
	if strings.TrimSpace(task.PacketID) != "" {
		params["packet_id"] = strings.TrimSpace(task.PacketID)
	}
	for key, value := range map[string]string{
		"anchor_id": task.AnchorID, "box_type": task.BoxType, "send_time": task.SendTime, "delay_time": task.DelayTime,
	} {
		if strings.TrimSpace(value) != "" {
			params[key] = strings.TrimSpace(value)
		}
	}
	trace := &ParticipationTrace{
		ID: hex.EncodeToString(sum[:12]), EventID: task.EventID, AccountID: task.AccountID,
		AccountName: strings.TrimSpace(task.AccountName), Action: firstNonEmpty(task.Action, "join"),
		Endpoint: strings.TrimSpace(response.Endpoint), HTTPStatus: response.HTTPStatus,
		RequestParams: params, ResponseParams: safeParticipationResponse(response.Body),
		Error: safeParticipationLogText(response.Error), FollowPolicy: task.FollowPolicy,
		Followed: task.Followed, FollowMatchKnown: task.FollowMatchKnown,
		CreatedAt: now.Format(time.RFC3339Nano),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.participationTasks[task.AccountID]; current != nil {
		trace.TaskID = current.ID
	}
	s.participationTraces[trace.ID] = trace
	return s.saveLocked()
}

func (s *Store) ParticipationTraces() []ParticipationTrace {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]ParticipationTrace, 0, len(s.participationTraces))
	for _, trace := range s.participationTraces {
		copy := *trace
		copy.RequestParams = cloneStringMap(trace.RequestParams)
		items = append(items, copy)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
	return items
}

func (s *Store) ClearParticipationTraces() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.participationTraces
	s.participationTraces = map[string]*ParticipationTrace{}
	if err := s.saveLocked(); err != nil {
		s.participationTraces = previous
		return err
	}
	return nil
}

func cloneStringMap(source map[string]string) map[string]string {
	copy := make(map[string]string, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

func safeParticipationResponse(body string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	var payload any
	if json.Unmarshal([]byte(body), &payload) != nil {
		return `{"unparsed":true}`
	}
	cleaned := sanitizeParticipationValue(payload, 0)
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		return `{"unparsed":true}`
	}
	if len(encoded) > 16000 {
		return `{"truncated":true}`
	}
	return string(encoded)
}

func sanitizeParticipationValue(value any, depth int) any {
	if depth > 6 {
		return "[truncated]"
	}
	switch item := value.(type) {
	case map[string]any:
		cleaned := make(map[string]any, len(item))
		for key, child := range item {
			if participationLogKeyForbidden(key) {
				continue
			}
			cleaned[key] = sanitizeParticipationValue(child, depth+1)
		}
		return cleaned
	case []any:
		limit := len(item)
		if limit > 100 {
			limit = 100
		}
		cleaned := make([]any, 0, limit)
		for _, child := range item[:limit] {
			cleaned = append(cleaned, sanitizeParticipationValue(child, depth+1))
		}
		return cleaned
	case string:
		return safeParticipationLogText(item)
	default:
		return item
	}
}

func participationLogKeyForbidden(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	return containsAny(lower, "cookie", "token", "bogus", "signature", "authorization", "session", "csrf", "passport", "header", "device_id", "fingerprint")
}

func safeParticipationLogText(value string) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if containsAny(lower, "sessionid", "a_bogus", "mstoken", "cookie:", "authorization:") {
		return "[已隐藏敏感原生参数]"
	}
	if len(value) > 1000 {
		return value[:1000] + "…"
	}
	return value
}

func participationRecordID(accountID, eventID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(accountID) + "\x00" + strings.TrimSpace(eventID)))
	return hex.EncodeToString(sum[:16])
}

// SyncRooms creates a monitor card for every canonical room without starting
// network work. Existing assignment and runtime status are preserved.
func (s *Store) SyncRooms(items []rooms.Room) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Format(time.RFC3339Nano)
	changed := false
	roomMonitorIDs := make(map[string]struct{}, len(items))
	for _, room := range items {
		id := "room_" + room.ID
		roomMonitorIDs[id] = struct{}{}
		// Follow attribution is canonical even when the followed room is
		// currently offline. Keep that source visible to the native scheduler
		// so a room learned through the following list remains in the highest
		// priority tier instead of falling back to its original import label.
		monitorSource := room.Source
		if len(room.FollowSources) > 0 {
			monitorSource = "following-live"
		}
		monitor := s.monitors[id]
		if monitor == nil {
			monitor = &Monitor{ID: id, RoomID: room.ID, Status: "stopped", ConnectionStatus: "disconnected", LiveStatus: "unknown", CreatedAt: now}
			s.monitors[id] = monitor
			changed = true
		}
		if monitor.LiveStatus == "" {
			monitor.LiveStatus = "unknown"
			changed = true
		}
		if monitor.RoomID != room.ID || monitor.WebRID != room.WebRID || monitor.ActualRoomID != room.ActualRoomID || monitor.Name != room.Name || monitor.StreamerName != room.StreamerName || monitor.Source != monitorSource || monitor.Enabled != room.Enabled {
			monitor.RoomID = room.ID
			monitor.WebRID = room.WebRID
			monitor.ActualRoomID = room.ActualRoomID
			monitor.Name = room.Name
			monitor.StreamerName = room.StreamerName
			monitor.Source = monitorSource
			monitor.Enabled = room.Enabled
			monitor.UpdatedAt = now
			changed = true
		}
	}
	// The room store is canonical. Remove stale monitor snapshots and their
	// events when an invalid/no-WebRID room has been cleaned from that store.
	for id := range s.monitors {
		if _, exists := roomMonitorIDs[id]; exists {
			continue
		}
		if cancel := s.runtime[id]; cancel != nil {
			cancel()
			delete(s.runtime, id)
		}
		delete(s.bulkIDs, id)
		delete(s.monitors, id)
		for eventID, item := range s.events {
			if item.MonitorID == id {
				delete(s.events, eventID)
			}
		}
		changed = true
	}
	if !changed {
		return nil
	}
	s.scheduleSaveLocked()
	return nil
}

func (s *Store) List() []Monitor {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Monitor, 0, len(s.monitors))
	for _, monitor := range s.monitors {
		items = append(items, *monitor)
	}
	sort.Slice(items, func(i, j int) bool {
		left := strings.ToLower(firstNonEmpty(items[i].Name, items[i].StreamerName, items[i].RoomID))
		right := strings.ToLower(firstNonEmpty(items[j].Name, items[j].StreamerName, items[j].RoomID))
		return left < right
	})
	return items
}

func (s *Store) MonitorCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.monitors)
}

// PageForRooms returns only monitor rows currently visible in the desktop UI,
// while Summary remains global for counters and bulk-control state.
func (s *Store) PageForRooms(roomIDs []string) MonitorPage {
	wanted := make(map[string]struct{}, len(roomIDs))
	for _, roomID := range roomIDs {
		if roomID = strings.TrimSpace(roomID); roomID != "" {
			wanted["room_"+roomID] = struct{}{}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	page := MonitorPage{Items: make([]Monitor, 0, len(wanted))}
	for id, monitor := range s.monitors {
		page.Summary.Total++
		if monitor.Enabled {
			page.Summary.Enabled++
		}
		if monitor.Status == "running" {
			page.Summary.Running++
			if monitor.ConnectionStatus == "connecting" {
				page.Summary.PendingFirst++
			} else {
				page.Summary.FirstChecked++
			}
			if monitor.LiveStatus == "live" {
				page.Summary.LiveRunning++
			}
		}
		if monitor.Status == "error" || monitor.ConnectionStatus == "error" {
			page.Summary.Errors++
		}
		if _, ok := wanted[id]; ok {
			page.Items = append(page.Items, *monitor)
		}
	}
	sort.Slice(page.Items, func(i, j int) bool {
		left := strings.ToLower(firstNonEmpty(page.Items[i].Name, page.Items[i].StreamerName, page.Items[i].RoomID))
		right := strings.ToLower(firstNonEmpty(page.Items[j].Name, page.Items[j].StreamerName, page.Items[j].RoomID))
		return left < right
	})
	return page
}

// Get returns a safe snapshot of one monitor after a state-changing command.
// It lets the IPC layer acknowledge the persisted state without making the
// frontend infer success from a later list refresh.
func (s *Store) Get(id string) (Monitor, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	monitor := s.monitors[id]
	if monitor == nil {
		return Monitor{}, false
	}
	return *monitor, true
}

func (s *Store) Events(monitorID string) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Event, 0)
	for _, event := range s.events {
		if event.MonitorID == monitorID {
			items = append(items, *event)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DetectedAt > items[j].DetectedAt })
	return items
}

// EventsAll returns the latest safe red-packet events across all rooms. Room
// metadata is joined here so the frontend does not need to issue one request
// per room and never receives any monitor credential.
func (s *Store) EventsAll() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Event, 0, len(s.events))
	for _, event := range s.events {
		copy := *event
		copy.Prize = normalizePacketPrize(copy.Prize)
		if monitor := s.monitors[event.MonitorID]; monitor != nil {
			copy.AccountID = firstNonEmpty(copy.AccountID, monitor.AccountID)
			copy.AccountName = firstNonEmpty(copy.AccountName, monitor.AccountName)
			copy.RoomName = firstNonEmpty(monitor.Name, monitor.StreamerName)
			copy.StreamerName = monitor.StreamerName
			copy.WebRID = firstNonEmpty(monitor.WebRID, monitor.RoomID)
		}
		items = append(items, copy)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DetectedAt > items[j].DetectedAt })
	return items
}

// MergeCenter persists red-packet events learned from the shared center. Newer
// native uploaders include only the non-credential participation identifiers
// already observed by their local monitor. Those identifiers remain hidden
// from frontend JSON, but allow another native engine to dispatch the event.
func (s *Store) MergeCenter(items []CenterEvent) (int, error) {
	s.mu.Lock()
	changed := false
	imported := 0
	dispatch := make([]Event, 0)
	for _, item := range items {
		webRID, packetID := strings.TrimSpace(item.WebRID), strings.TrimSpace(item.PacketID)
		if webRID == "" || packetID == "" {
			continue
		}
		existing := s.findEventByIdentityLocked(webRID, packetID)
		if existing == nil {
			sum := sha256.Sum256([]byte(webRID + "\x00" + packetID))
			existing = &Event{
				ID: "center:" + hex.EncodeToString(sum[:16]), RoomID: webRID, WebRID: webRID,
				PacketID: packetID, Source: firstNonEmpty(item.Source, "center_sync"), DataSource: "center",
				DetectedAt: firstNonEmpty(item.DetectedAt, time.Now().Format(time.RFC3339Nano)),
			}
			s.events[existing.ID] = existing
			changed = true
			imported++
		}
		wasParticipationReady := eventParticipationMetadataReady(existing)
		centerOwned := existing.DataSource == "center"
		if centerOwned || existing.RoomName == "" {
			existing.RoomName = firstNonEmpty(item.RoomName, existing.RoomName)
		}
		if centerOwned || existing.StreamerName == "" {
			existing.StreamerName = firstNonEmpty(item.StreamerName, existing.StreamerName)
		}
		if centerOwned || existing.Title == "" {
			existing.Title = firstNonEmpty(item.Title, existing.Title)
		}
		if centerOwned || existing.Prize == "" {
			existing.Prize = firstNonEmpty(normalizePacketPrize(item.Prize), existing.Prize)
		}
		if centerOwned {
			existing.Source = firstNonEmpty(item.Source, existing.Source, "center_sync")
			existing.DetectedAt = firstNonEmpty(item.DetectedAt, existing.DetectedAt)
			existing.DrawAt = firstNonEmpty(item.DrawAt, existing.DrawAt)
			existing.ExpiresAt = firstNonEmpty(item.ExpiresAt, existing.ExpiresAt)
			if item.ParticipantCount > existing.ParticipantCount {
				existing.ParticipantCount = item.ParticipantCount
			}
			if item.TotalDiamonds > existing.TotalDiamonds {
				existing.TotalDiamonds = item.TotalDiamonds
			}
			if item.ShareCount > existing.ShareCount {
				existing.ShareCount = item.ShareCount
			}
		}
		if validLuckyboxID(item.ActualRoomID) {
			existing.ActualRoomID = firstNonEmpty(existing.ActualRoomID, item.ActualRoomID)
		}
		if validLuckyboxID(item.JoinBoxID) {
			existing.JoinBoxID = firstNonEmpty(existing.JoinBoxID, item.JoinBoxID)
		}
		existing.AnchorID = firstNonEmpty(existing.AnchorID, item.AnchorID)
		existing.BoxType = firstNonEmpty(existing.BoxType, item.BoxType)
		existing.SendTime = firstNonEmpty(existing.SendTime, item.SendTime)
		existing.DelayTime = firstNonEmpty(existing.DelayTime, item.DelayTime)
		if !wasParticipationReady && eventParticipationMetadataReady(existing) && eventOpenAt(*existing, time.Now()) {
			dispatch = append(dispatch, *existing)
		}
		changed = true
	}
	if !changed {
		s.mu.Unlock()
		return imported, nil
	}
	err := s.saveLocked()
	handler := s.eventHandler
	s.mu.Unlock()
	if err != nil {
		return imported, err
	}
	if handler != nil {
		for _, event := range dispatch {
			event := event
			go handler(event)
		}
	}
	return imported, nil
}

func (s *Store) findEventByIdentityLocked(webRID, packetID string) *Event {
	for _, event := range s.events {
		if strings.TrimSpace(firstNonEmpty(event.WebRID, event.RoomID)) == webRID && strings.TrimSpace(event.PacketID) == packetID {
			return event
		}
	}
	return nil
}

// StartAll is retained for callers that deliberately pin every room to one
// account. Bulk desktop monitoring should use StartAllPool instead.
func (s *Store) StartAll(accountID, accountName, cookie string) (int, error) {
	result, err := s.StartAllPool([]AccountCredential{{AccountID: accountID, AccountName: accountName, Cookie: cookie}})
	return result.Started, err
}

// StartAllPool distributes enabled rooms over all usable monitoring accounts.
// Assignment is stable for a room until its account enters cooldown, at which
// point the next poll automatically fails over to another available account.
func (s *Store) StartAllPool(credentials []AccountCredential) (PoolStartResult, error) {
	s.mu.Lock()
	poolConfig := s.monitoringSettings.poolConfig()
	s.mu.Unlock()
	pool, err := newAccountPoolWithConfig(credentials, poolConfig)
	if err != nil {
		return PoolStartResult{}, err
	}
	s.mu.Lock()
	if s.bulkCancel != nil {
		s.bulkCancel()
	}
	s.pool = pool
	s.bulkIDs = make(map[string]struct{}, len(s.monitors))
	ctx, cancel := context.WithCancel(context.Background())
	s.bulkCancel = cancel
	now := time.Now().Format(time.RFC3339Nano)
	ids := make([]string, 0, len(s.monitors))
	for _, monitor := range s.monitors {
		if !monitor.Enabled {
			continue
		}
		if _, running := s.runtime[monitor.ID]; running {
			if account, accountErr := pool.accountFor(monitor.ID); accountErr == nil {
				monitor.AccountID, monitor.AccountName = account.credential.AccountID, account.credential.AccountName
			}
			continue
		}
		account, accountErr := pool.accountFor(monitor.ID)
		if accountErr != nil {
			continue
		}
		monitor.AccountID, monitor.AccountName = account.credential.AccountID, account.credential.AccountName
		monitor.Status, monitor.ConnectionStatus, monitor.LastError = "running", "connecting", ""
		monitor.LiveStatus = "unknown"
		monitor.UpdatedAt = now
		ids = append(ids, monitor.ID)
		s.bulkIDs[monitor.ID] = struct{}{}
	}
	prioritizeBulkMonitorIDs(ids, s.monitors)
	s.scheduleSaveLocked()
	s.mu.Unlock()
	go s.runBulkScheduler(ctx, ids, pool)
	return PoolStartResult{Started: len(ids), AccountCount: len(pool.ordered), Assignments: pool.summary()}, nil
}

// RefreshMonitoringPool hot-reloads the account pool used by running bulk and
// per-room monitors. Current native requests are allowed to finish; the next
// account lookup observes the new pool membership and credentials.
func (s *Store) RefreshMonitoringPool(credentials []AccountCredential) PoolRefreshResult {
	s.mu.Lock()
	pool := s.pool
	s.mu.Unlock()
	if pool == nil {
		return PoolRefreshResult{}
	}
	return pool.syncCredentials(credentials)
}

// StopAll stops every room monitor. Persisted event history is retained.
func (s *Store) StopAll() (int, error) {
	s.mu.Lock()
	if s.bulkCancel != nil {
		s.bulkCancel()
		s.bulkCancel = nil
	}
	s.bulkIDs = map[string]struct{}{}
	s.pool = nil
	stopped := 0
	now := time.Now().Format(time.RFC3339Nano)
	for id, cancel := range s.runtime {
		if cancel != nil {
			cancel()
		}
		delete(s.runtime, id)
	}
	for _, monitor := range s.monitors {
		if monitor.Status == "running" || monitor.Status == "error" || monitor.ConnectionStatus != "disconnected" {
			stopped++
		}
		monitor.Status, monitor.ConnectionStatus = "stopped", "disconnected"
		monitor.UpdatedAt = now
	}
	s.scheduleSaveLocked()
	s.mu.Unlock()
	return stopped, nil
}

// StartPooled starts one monitor through the shared account pool. This keeps
// single-row starts consistent with bulk starts instead of silently reverting
// to the first monitoring account.
func (s *Store) StartPooled(id string, credentials []AccountCredential) error {
	s.mu.Lock()
	pool := s.pool
	poolConfig := s.monitoringSettings.poolConfig()
	s.mu.Unlock()
	if pool == nil {
		var err error
		pool, err = newAccountPoolWithConfig(credentials, poolConfig)
		if err != nil {
			return err
		}
		s.mu.Lock()
		if s.pool == nil {
			s.pool = pool
		} else {
			pool = s.pool
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	monitor := s.monitors[id]
	if monitor == nil {
		s.mu.Unlock()
		return errors.New("红包监测不存在")
	}
	if !monitor.Enabled {
		s.mu.Unlock()
		return errors.New("直播间已停用")
	}
	if s.runtime[id] != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	account, err := pool.accountFor(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	monitor = s.monitors[id]
	if monitor == nil {
		s.mu.Unlock()
		return errors.New("红包监测不存在")
	}
	if s.runtime[id] != nil {
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.runtime[id] = cancel
	monitor.AccountID, monitor.AccountName = account.credential.AccountID, account.credential.AccountName
	monitor.Status, monitor.ConnectionStatus, monitor.LastError = "running", "connecting", ""
	monitor.LiveStatus = "unknown"
	monitor.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	s.scheduleSaveLocked()
	s.mu.Unlock()
	go s.runPooled(ctx, id, pool, 0)
	return nil
}

func (s *Store) Start(id, accountID, accountName, cookie string) error {
	s.mu.Lock()
	monitor := s.monitors[id]
	if monitor == nil {
		s.mu.Unlock()
		return errors.New("红包监测不存在")
	}
	if !monitor.Enabled {
		s.mu.Unlock()
		return errors.New("直播间已停用")
	}
	if strings.TrimSpace(cookie) == "" {
		s.mu.Unlock()
		return errors.New("监测账号没有可用 Cookie")
	}
	if cancel := s.runtime[id]; cancel != nil {
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.runtime[id] = cancel
	monitor.AccountID, monitor.AccountName = accountID, accountName
	monitor.Status, monitor.ConnectionStatus, monitor.LastError = "running", "connecting", ""
	monitor.LiveStatus = "unknown"
	monitor.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	s.scheduleSaveLocked()
	roomID := firstNonEmpty(monitor.WebRID, monitor.RoomID)
	actualRoomID := firstNonEmpty(monitor.ActualRoomID, monitor.RoomID)
	s.mu.Unlock()

	client := httpclient.New(httpclient.WithCookie(cookie), httpclient.WithRoomURL("https://live.douyin.com/"+roomID))
	source := newSource(client, roomID, actualRoomID)
	go s.run(ctx, id, source, 0)
	return nil
}

func (s *Store) Stop(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	monitor := s.monitors[id]
	if monitor == nil {
		return errors.New("红包监测不存在")
	}
	if cancel := s.runtime[id]; cancel != nil {
		cancel()
		delete(s.runtime, id)
	}
	delete(s.bulkIDs, id)
	if s.pool != nil {
		s.pool.dropAssignment(id, monitor.AccountID)
	}
	monitor.Status, monitor.ConnectionStatus = "stopped", "disconnected"
	monitor.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	s.scheduleSaveLocked()
	return nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.monitors[id]; !ok {
		return errors.New("红包监测不存在")
	}
	if cancel := s.runtime[id]; cancel != nil {
		cancel()
		delete(s.runtime, id)
	}
	delete(s.bulkIDs, id)
	if s.pool != nil {
		s.pool.dropAssignment(id, s.monitors[id].AccountID)
	}
	delete(s.monitors, id)
	for eventID, event := range s.events {
		if event.MonitorID == id {
			delete(s.events, eventID)
		}
	}
	return s.saveLocked()
}

func (s *Store) runPooled(ctx context.Context, id string, pool *accountPool, initialDelay time.Duration) {
	defer func() {
		s.mu.Lock()
		if _, ok := s.runtime[id]; ok {
			delete(s.runtime, id)
			if monitor := s.monitors[id]; monitor != nil && monitor.Status == "running" {
				monitor.Status, monitor.ConnectionStatus = "stopped", "disconnected"
				monitor.UpdatedAt = time.Now().Format(time.RFC3339Nano)
				s.scheduleSaveLocked()
			}
		}
		s.mu.Unlock()
	}()
	if initialDelay > 0 {
		timer := time.NewTimer(initialDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	var currentAccountID string
	var observed *observedMonitorSource
	for {
		s.mu.Lock()
		monitor := s.monitors[id]
		if monitor == nil {
			s.mu.Unlock()
			return
		}
		webRID := firstNonEmpty(monitor.WebRID, monitor.RoomID)
		actualRoomID := firstNonEmpty(monitor.ActualRoomID, monitor.RoomID)
		s.mu.Unlock()

		account, err := pool.accountFor(id)
		if err != nil {
			next := time.Second
			var unavailable *poolUnavailableError
			if errors.As(err, &unavailable) && unavailable.retryAfter > next {
				next = unavailable.retryAfter
				if next > 30*time.Second {
					next = 30 * time.Second
				}
			}
			s.updatePoolWaitState(id, err)
			if !waitContext(ctx, next) {
				return
			}
			continue
		}

		s.mu.Lock()
		if monitor := s.monitors[id]; monitor != nil {
			monitor.AccountID, monitor.AccountName = account.credential.AccountID, account.credential.AccountName
			monitor.Status = "running"
			monitor.UpdatedAt = time.Now().Format(time.RFC3339Nano)
		}
		s.mu.Unlock()

		if observed == nil || currentAccountID != account.credential.AccountID {
			currentAccountID = account.credential.AccountID
			observed = &observedMonitorSource{inner: pool.sourceFor(account, webRID, actualRoomID)}
		}
		observed.lastErr = nil
		releaseProbe, acquired := s.acquireProbeSlot(ctx)
		if !acquired {
			return
		}
		next := s.pollOnce(ctx, id, observed)
		releaseProbe()
		if observed.lastErr == nil {
			pool.markSuccess(account.credential.AccountID)
		} else if pool.markFailure(account.credential.AccountID, observed.lastErr) {
			pool.dropAssignment(id, account.credential.AccountID)
			next = time.Second
		}
		if next <= 0 {
			next = unknownProbeInterval
		}
		if !waitContext(ctx, next) {
			return
		}
	}
}

// bulkMonitorJob is kept in one of three source queues. A scheduler chooses
// only ready jobs, preferring the lowest source rank, and workers perform the
// actual network probe. This gives "关注列表 > 导入 > 中心库" a real queue
// boundary without creating one goroutine per room.
type bulkMonitorJob struct {
	id       string
	due      time.Time
	sequence uint64
}

type bulkMonitorHeap []bulkMonitorJob

func (h bulkMonitorHeap) Len() int { return len(h) }

func (h bulkMonitorHeap) Less(i, j int) bool {
	if h[i].due.Equal(h[j].due) {
		if h[i].id == h[j].id {
			return h[i].sequence < h[j].sequence
		}
		return h[i].id < h[j].id
	}
	return h[i].due.Before(h[j].due)
}

func (h bulkMonitorHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *bulkMonitorHeap) Push(value any) { *h = append(*h, value.(bulkMonitorJob)) }

func (h *bulkMonitorHeap) Pop() any {
	items := *h
	last := len(items) - 1
	value := items[last]
	*h = items[:last]
	return value
}

type bulkMonitorResult struct {
	job       bulkMonitorJob
	next      time.Duration
	stillBulk bool
}

func (s *Store) runBulkScheduler(ctx context.Context, ids []string, pool *accountPool) {
	workerCount := minInt(defaultBulkWorkers, len(ids))
	if workerCount == 0 {
		return
	}
	var queues [3]bulkMonitorHeap
	startedAt := time.Now()
	sequence := uint64(0)
	s.mu.Lock()
	for _, id := range ids {
		rank := monitorSourcePriority(s.monitors[id])
		// Hash staggering spreads a large first probe window, while the source
		// queues still ensure a ready follow room is chosen before a ready
		// imported or center-library room.
		heap.Push(&queues[rank], bulkMonitorJob{id: id, due: startedAt.Add(monitorStaggerDelay(id)), sequence: sequence})
		sequence++
	}
	s.mu.Unlock()
	for rank := range queues {
		heap.Init(&queues[rank])
	}

	jobs := make(chan bulkMonitorJob, workerCount)
	results := make(chan bulkMonitorResult, workerCount)
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()
	for worker := 0; worker < workerCount; worker++ {
		go s.runBulkWorker(workerCtx, pool, jobs, results)
	}

	inflight := 0
	priorityBurst := 0
	for {
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		for inflight < workerCount {
			rank, ok := nextReadyBulkQueue(queues, now, priorityBurst)
			if !ok {
				break
			}
			job := heap.Pop(&queues[rank]).(bulkMonitorJob)
			select {
			case jobs <- job:
				inflight++
				if rank == 0 {
					priorityBurst++
				} else {
					priorityBurst = 0
				}
			case <-ctx.Done():
				return
			}
		}

		if inflight == 0 {
			due, ok := nextBulkDue(queues)
			if !ok {
				return
			}
			if !waitContext(ctx, time.Until(due)) {
				return
			}
			continue
		}

		var timer *time.Timer
		var wake <-chan time.Time
		if inflight < workerCount {
			if due, ok := nextBulkDue(queues); ok {
				delay := time.Until(due)
				if delay < 50*time.Millisecond {
					delay = 50 * time.Millisecond
				}
				timer = time.NewTimer(delay)
				wake = timer.C
			}
		}
		select {
		case result := <-results:
			inflight--
			if result.stillBulk {
				if result.next <= 0 {
					result.next = unknownProbeInterval
				}
				rank := monitorSourcePriorityForID(s, result.job.id)
				heap.Push(&queues[rank], bulkMonitorJob{
					id: result.job.id, due: time.Now().Add(result.next), sequence: sequence,
				})
				sequence++
			}
		case <-wake:
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func nextReadyBulkQueue(queues [3]bulkMonitorHeap, now time.Time, priorityBurst int) (int, bool) {
	ready := func(rank int) bool {
		return queues[rank].Len() > 0 && !queues[rank][0].due.After(now)
	}
	if ready(0) {
		if priorityBurst >= bulkPriorityBurst {
			if ready(1) {
				return 1, true
			}
			if ready(2) {
				return 2, true
			}
		}
		return 0, true
	}
	if ready(1) {
		return 1, true
	}
	if ready(2) {
		return 2, true
	}
	return 0, false
}

func nextBulkDue(queues [3]bulkMonitorHeap) (time.Time, bool) {
	var earliest time.Time
	found := false
	for rank := range queues {
		if queues[rank].Len() == 0 {
			continue
		}
		candidate := queues[rank][0].due
		if !found || candidate.Before(earliest) {
			earliest, found = candidate, true
		}
	}
	return earliest, found
}

func (s *Store) runBulkWorker(ctx context.Context, pool *accountPool, jobs <-chan bulkMonitorJob, results chan<- bulkMonitorResult) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-jobs:
			next, stillBulk := s.pollBulkMonitor(ctx, job.id, pool)
			select {
			case results <- bulkMonitorResult{job: job, next: next, stillBulk: stillBulk}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (s *Store) pollBulkMonitor(ctx context.Context, id string, pool *accountPool) (time.Duration, bool) {
	s.mu.Lock()
	monitor := s.monitors[id]
	_, bulk := s.bulkIDs[id]
	if monitor == nil || !bulk || !monitor.Enabled || monitor.Status != "running" {
		s.mu.Unlock()
		return 0, false
	}
	webRID := firstNonEmpty(monitor.WebRID, monitor.RoomID)
	actualRoomID := firstNonEmpty(monitor.ActualRoomID, monitor.RoomID)
	s.mu.Unlock()

	account, err := pool.accountFor(id)
	if err != nil {
		s.updatePoolWaitState(id, err)
		return time.Second, true
	}
	s.mu.Lock()
	if monitor := s.monitors[id]; monitor != nil {
		monitor.AccountID, monitor.AccountName = account.credential.AccountID, account.credential.AccountName
		monitor.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	}
	s.mu.Unlock()
	observed := &observedMonitorSource{inner: pool.sourceFor(account, webRID, actualRoomID)}
	releaseProbe, acquired := s.acquireProbeSlot(ctx)
	if !acquired {
		return 0, false
	}
	next := s.pollOnce(ctx, id, observed)
	releaseProbe()
	s.mu.Lock()
	_, stillBulk := s.bulkIDs[id]
	if !stillBulk {
		if monitor := s.monitors[id]; monitor != nil {
			monitor.Status, monitor.ConnectionStatus = "stopped", "disconnected"
			monitor.UpdatedAt = time.Now().Format(time.RFC3339Nano)
			s.scheduleSaveLocked()
		}
	}
	s.mu.Unlock()
	if !stillBulk {
		return 0, false
	}
	if observed.lastErr == nil {
		pool.markSuccess(account.credential.AccountID)
	} else if pool.markFailure(account.credential.AccountID, observed.lastErr) {
		pool.dropAssignment(id, account.credential.AccountID)
		next = time.Second
	}
	if next <= 0 {
		next = unknownProbeInterval
	}
	return next, true
}

func (s *Store) updatePoolWaitState(id string, waitErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if monitor := s.monitors[id]; monitor != nil {
		monitor.Status, monitor.ConnectionStatus = "running", "connecting"
		monitor.LastError = waitErr.Error()
		monitor.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	}
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Store) run(ctx context.Context, id string, source monitorSource, initialDelay time.Duration) {
	defer func() {
		s.mu.Lock()
		if _, ok := s.runtime[id]; ok {
			delete(s.runtime, id)
			if monitor := s.monitors[id]; monitor != nil && monitor.Status == "running" {
				monitor.Status, monitor.ConnectionStatus = "stopped", "disconnected"
				monitor.UpdatedAt = time.Now().Format(time.RFC3339Nano)
				s.scheduleSaveLocked()
			}
		}
		s.mu.Unlock()
	}()
	if initialDelay > 0 {
		timer := time.NewTimer(initialDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	for {
		releaseProbe, acquired := s.acquireProbeSlot(ctx)
		if !acquired {
			return
		}
		next := s.pollOnce(ctx, id, source)
		releaseProbe()
		if next <= 0 {
			next = unknownProbeInterval
		}
		timer := time.NewTimer(next)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// pollOnce implements the two-stage 福宝 cadence:
//  1. room/web/enter determines whether the room is currently live.
//  2. Red-packet APIs are queried only after a positive live result.
//
// The returned duration controls the next round (unknown 10s, offline 60s,
// live 15s, or 5s while an explicit red-packet payload is active).
func (s *Store) pollOnce(ctx context.Context, id string, source monitorSource) time.Duration {
	s.mu.Lock()
	monitor := s.monitors[id]
	accountID := ""
	if monitor != nil {
		accountID = monitor.AccountID
	}
	recorder := s.requestRecorder
	s.mu.Unlock()

	probe, probeErr := source.ProbeLive(ctx)
	if ctx.Err() == nil && recorder != nil && accountID != "" && !errors.Is(probeErr, errMonitoringAccountCooling) {
		recorder(accountID, probeErr)
	}
	if ctx.Err() != nil {
		return 0
	}
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return 0
	}
	monitor = s.monitors[id]
	if monitor == nil {
		s.mu.Unlock()
		return 0
	}
	now := time.Now().Format(time.RFC3339Nano)
	monitor.LastCheckedAt, monitor.LastLiveCheckedAt, monitor.UpdatedAt = now, now, now
	monitor.Status = "running"
	if probeErr != nil {
		monitor.LiveStatus, monitor.LiveStatusSource = "error", "room_web_enter"
		monitor.ConnectionStatus, monitor.LastError = "error", probeErr.Error()
		s.scheduleSaveLocked()
		s.mu.Unlock()
		return unknownProbeInterval
	}
	previousLiveStatus := monitor.LiveStatus
	monitor.LiveStatus = firstNonEmpty(probe.Status, "unknown")
	if monitor.LiveStatus == "live" && (previousLiveStatus != "live" || monitor.LiveStartedAt == "") {
		// This is a state-transition timestamp, not the recurring live-probe time.
		// Keeping it stable allows the UI to sort by when a room most recently
		// started broadcasting without poll cadence changing the order.
		monitor.LiveStartedAt = now
	}
	monitor.LiveStatusSource, monitor.LiveRawStatus = probe.Source, probe.RawStatus
	monitor.ConnectionStatus, monitor.LastError = "connected", ""
	if probe.ActualRoomID != "" {
		monitor.ActualRoomID = probe.ActualRoomID
	}
	if probe.Title != "" {
		monitor.Name = probe.Title
	}
	if probe.StreamerName != "" {
		monitor.StreamerName = probe.StreamerName
	}
	liveResultHandler := s.liveResultHandler
	roomID := monitor.RoomID
	if monitor.LiveStatus != "live" {
		s.scheduleSaveLocked()
		status := monitor.LiveStatus
		s.mu.Unlock()
		if liveResultHandler != nil && status == "offline" {
			liveResultHandler(roomID, status, time.Now())
		}
		if status == "offline" {
			return offlineProbeInterval
		}
		return unknownProbeInterval
	}
	s.mu.Unlock()
	if liveResultHandler != nil {
		liveResultHandler(roomID, "live", time.Now())
	}

	snapshots, err := source.Fetch(ctx)
	if ctx.Err() == nil && recorder != nil && accountID != "" && !errors.Is(err, errMonitoringAccountCooling) {
		recorder(accountID, err)
	}
	if ctx.Err() != nil {
		return 0
	}
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return 0
	}
	monitor = s.monitors[id]
	if monitor == nil {
		s.mu.Unlock()
		return 0
	}
	now = time.Now().Format(time.RFC3339Nano)
	monitor.LastRedPacketCheckedAt, monitor.LastCheckedAt, monitor.UpdatedAt = now, now, now
	if err != nil {
		monitor.ConnectionStatus, monitor.LastError = "error", err.Error()
		s.scheduleSaveLocked()
		s.mu.Unlock()
		return livePacketInterval
	}
	monitor.ConnectionStatus, monitor.LastError = "connected", ""
	newEvents := make([]Event, 0)
	for _, snapshot := range snapshots {
		packet, ok := extractRedPacket(snapshot.Data)
		if !ok {
			continue
		}
		packetID := firstNonEmpty(packet.id, stableID(snapshot.Data))
		eventID := id + ":" + packetID
		existing := s.events[eventID]
		if existing == nil {
			existing = s.findEventByIdentityLocked(firstNonEmpty(monitor.WebRID, monitor.RoomID), packetID)
		}
		if existing != nil {
			wasParticipationReady := eventParticipationMetadataReady(existing)
			wasCenterOwned := existing.DataSource == "center"
			if wasCenterOwned {
				existing.DataSource = ""
				existing.MonitorID = id
				existing.AccountID, existing.AccountName = monitor.AccountID, monitor.AccountName
				existing.RoomID = monitor.RoomID
			}
			existing.WebRID = firstNonEmpty(monitor.WebRID, existing.WebRID, monitor.RoomID)
			existing.RoomName = firstNonEmpty(monitor.Name, monitor.StreamerName, existing.RoomName)
			existing.StreamerName = firstNonEmpty(monitor.StreamerName, existing.StreamerName)
			existing.ActualRoomID = firstNonEmpty(snapshot.ActualRoomID, monitor.ActualRoomID, existing.ActualRoomID)
			// The same activity remains in the luckybox list while it is active.
			// Use later snapshots to enrich legacy/incomplete rows after the
			// grouped 福宝 prize parser has enough box records.
			if prize := normalizePacketPrize(packet.prize); prize != "" {
				existing.Prize = prize
			} else {
				existing.Prize = normalizePacketPrize(existing.Prize)
			}
			existing.Title = firstNonEmpty(packet.title, existing.Title)
			existing.DrawAt = firstNonEmpty(packet.drawAt, existing.DrawAt)
			existing.ExpiresAt = firstNonEmpty(packet.expiresAt, existing.ExpiresAt)
			if packet.participants > 0 {
				existing.ParticipantCount = packet.participants
			}
			if packet.totalDiamonds > 0 && packet.shareCount > 0 {
				existing.TotalDiamonds = packet.totalDiamonds
				existing.ShareCount = packet.shareCount
			}
			// activity_id is a grouping key and may be a shared non-numeric
			// business identifier (for example AC2025...). Participation must
			// use the row's real numeric box_id_str instead.
			existing.JoinBoxID = firstNonEmpty(packet.boxID, existing.JoinBoxID)
			existing.AnchorID = firstNonEmpty(packet.anchorID, existing.AnchorID)
			existing.BoxType = firstNonEmpty(packet.boxType, existing.BoxType)
			existing.SendTime = firstNonEmpty(packet.sendTime, existing.SendTime)
			existing.DelayTime = firstNonEmpty(packet.delayTime, existing.DelayTime)
			// Center-library rows intentionally contain display metadata only. A
			// positive local room probe may later add the private native request
			// identifiers. Dispatch exactly once when that existing row first
			// becomes actionable; otherwise the UI can show a real packet while
			// the participation scheduler never receives it.
			if !wasParticipationReady && eventParticipationMetadataReady(existing) && eventOpenAt(*existing, time.Now()) {
				newEvents = append(newEvents, *existing)
				if wasCenterOwned {
					monitor.PacketCount++
				}
				monitor.LastEventAt, monitor.LastPacketID, monitor.LastPacketTitle = now, existing.PacketID, existing.Title
				monitor.LastParticipantCount = existing.ParticipantCount
			}
			continue
		}
		event := &Event{
			ID: eventID, MonitorID: id,
			AccountID: monitor.AccountID, AccountName: monitor.AccountName,
			RoomID: monitor.RoomID, RoomName: firstNonEmpty(monitor.Name, monitor.StreamerName),
			StreamerName: monitor.StreamerName, WebRID: firstNonEmpty(monitor.WebRID, monitor.RoomID),
			PacketID: packetID, Title: packet.title, Prize: packet.prize,
			Source: snapshot.Source, DetectedAt: now,
			DrawAt: packet.drawAt, ExpiresAt: packet.expiresAt,
			ParticipantCount: packet.participants,
			TotalDiamonds:    packet.totalDiamonds, ShareCount: packet.shareCount,
			ActualRoomID: firstNonEmpty(snapshot.ActualRoomID, monitor.ActualRoomID),
			JoinBoxID: firstNonEmpty(packet.boxID, func() string {
				if validLuckyboxID(packet.id) {
					return packet.id
				}
				return ""
			}()),
			AnchorID: packet.anchorID, BoxType: packet.boxType,
			SendTime: packet.sendTime, DelayTime: packet.delayTime,
		}
		s.events[eventID] = event
		newEvents = append(newEvents, *event)
		monitor.PacketCount++
		monitor.LastEventAt, monitor.LastPacketID, monitor.LastPacketTitle = now, packetID, packet.title
		monitor.LastParticipantCount = packet.participants
	}
	s.scheduleSaveLocked()
	handler := s.eventHandler
	s.mu.Unlock()
	if handler != nil {
		for _, event := range newEvents {
			event := event
			go handler(event)
		}
	}
	if len(snapshots) > 0 {
		return activePacketInterval
	}
	return livePacketInterval
}

func eventParticipationMetadataReady(event *Event) bool {
	return event != nil && strings.TrimSpace(event.ActualRoomID) != "" && validLuckyboxID(event.JoinBoxID)
}

func (s *Store) acquireProbeSlot(ctx context.Context) (func(), bool) {
	s.mu.Lock()
	probeSlots := s.probeSlots
	s.mu.Unlock()
	select {
	case probeSlots <- struct{}{}:
		return func() { <-probeSlots }, true
	case <-ctx.Done():
		return nil, false
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func monitorStaggerDelay(id string) time.Duration {
	sum := sha256.Sum256([]byte(id))
	bucket := int(sum[0])<<8 | int(sum[1])
	return time.Duration(bucket%int(unknownProbeInterval/time.Millisecond)) * time.Millisecond
}

// monitorSourcePriority defines the native monitoring order. Following rooms
// are the most actionable local resources, imported/legacy rooms are next,
// and rows learned only from the center library form the lower-priority tier.
func monitorSourcePriority(monitor *Monitor) int {
	if monitor == nil {
		return 1
	}
	switch strings.ToLower(strings.TrimSpace(monitor.Source)) {
	case "following", "following-live", "follow", "关注列表", "关注":
		return 0
	case "center", "center_sync", "center-library":
		return 2
	default:
		return 1
	}
}

func monitorSourcePriorityForID(s *Store, id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return monitorSourcePriority(s.monitors[id])
}

// prioritizeBulkMonitorIDs applies the same three-tier order used by the
// runtime queues. The return value remains the local-room count for callers
// and tests that use it to verify center rows stay in the lower tier.
func prioritizeBulkMonitorIDs(ids []string, monitors map[string]*Monitor) int {
	sort.Slice(ids, func(i, j int) bool {
		leftRank := monitorSourcePriority(monitors[ids[i]])
		rightRank := monitorSourcePriority(monitors[ids[j]])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return ids[i] < ids[j]
	})
	localCount := 0
	for _, id := range ids {
		if monitorSourcePriority(monitors[id]) == 2 {
			break
		}
		localCount++
	}
	return localCount
}

type packetMeta struct {
	id, boxID, title, prize, drawAt, expiresAt string
	anchorID, boxType, sendTime, delayTime     string
	participants                               int
	totalDiamonds                              float64
	shareCount                                 int
}

var redMarkers = []string{"红包", "red_packet", "redpacket", "luckybox", "lucky_box", "抢红包", "领红包"}

// extractRedPacket intentionally accepts explicit red-packet markers only.
// Generic lottery_info fields without one of those markers are not surfaced,
// preventing 福袋 activities from appearing in this monitor.
func extractRedPacket(data map[string]any) (packetMeta, bool) {
	var pairs []pair
	collectPairs(data, "", &pairs)
	var text strings.Builder
	for _, item := range pairs {
		text.WriteString(strings.ToLower(item.key))
		text.WriteByte(' ')
		text.WriteString(strings.ToLower(item.value))
		text.WriteByte(' ')
	}
	joined := text.String()
	red := false
	for _, marker := range redMarkers {
		if strings.Contains(joined, marker) {
			red = true
			break
		}
	}
	if !red || (strings.Contains(joined, "福袋") && !strings.Contains(joined, "红包")) {
		return packetMeta{}, false
	}
	meta := packetMeta{}
	meta.id = firstPairValue(pairs, "red_packet_id", "redPacketId", "redpacket_id", "activity_id", "activityId", "lottery_id_str", "lotteryIdStr", "box_id_str", "boxIdStr", "box_id", "boxId")
	meta.boxID = firstPairValue(pairs, "box_id_str", "boxIdStr", "box_id", "boxId", "luckybox_id", "luckyboxId", "red_packet_id", "redPacketId")
	if !validLuckyboxID(meta.boxID) {
		meta.boxID = ""
	}
	meta.anchorID = firstPairValue(pairs, "anchor_id", "anchorId")
	meta.boxType = firstPairValue(pairs, "box_type", "boxType")
	meta.sendTime = firstPairValue(pairs, "send_time", "sendTime", "start_time", "startTime")
	meta.delayTime = firstPairValue(pairs, "delay_time", "delayTime", "duration", "duration_s")
	meta.title = firstPairValue(pairs, "title", "display_name", "displayName", "name", "activity_name", "activityName")
	meta.prize = formatPacketPrize(pairs)
	meta.totalDiamonds, meta.shareCount = packetDiamondShares(pairs)
	meta.drawAt = normalizePacketTime(firstPairValue(pairs,
		"draw_time", "drawTime", "lottery_draw_time", "lotteryDrawTime", "open_time", "openTime",
	))
	meta.expiresAt = normalizePacketTime(firstPairValue(pairs,
		"expire_time", "expireTime", "expires_at", "expiresAt", "expiration_time", "expirationTime",
		"end_time", "endTime", "finish_time", "finishTime", "close_time", "closeTime",
	))
	// 福宝's luckybox payload often carries no absolute expiry. In that case
	// the verified legacy rule derives it from send_time + delay_time. Some
	// responses expose only a remaining countdown, which is anchored to the
	// local detection time before the event is persisted.
	if meta.expiresAt == "" {
		meta.expiresAt = packetTimeAfter(
			firstPairValue(pairs, "send_time", "sendTime", "start_time", "startTime"),
			firstPairValue(pairs, "delay_time", "delayTime", "duration", "duration_s", "wait_time", "waitTime", "open_delay", "openDelay"),
		)
	}
	if meta.expiresAt == "" {
		if countdown := firstPairValue(pairs,
			"count_down_time", "countDownTime", "countdown_time", "countdownTime", "count_down", "countDown", "countdown",
			"remaining_time", "remainingTime", "remain_time", "remainTime", "left_time", "leftTime",
		); countdown != "" {
			meta.expiresAt = packetTimeAfter(time.Now().Format(time.RFC3339Nano), countdown)
		}
	}
	if meta.expiresAt == "" {
		meta.expiresAt = meta.drawAt
	}
	meta.participants = firstPairInt(pairs, "participant_count", "participantCount", "candidate_user_num", "candidateUserNum", "user_count", "userCount")
	return meta, true
}

func packetDiamondShares(pairs []pair) (float64, int) {
	diamondText := firstPositivePairNumber(pairs,
		"total_diamond_count", "totalDiamondCount", "total_diamond", "totalDiamond",
		"total_amount", "totalAmount", "total_diamond_num", "totalDiamondNum",
		"diamond_count", "diamondCount", "diamond_num", "diamondNum", "diamond_amount", "diamondAmount",
		"diamond", "diamonds", "amount", "content_amount", "contentAmount",
		"content_diamond_count", "contentDiamondCount", "prize_count", "prizeCount",
	)
	shareText := firstPositivePairNumber(pairs,
		"box_count", "boxCount", "lucky_box_count", "luckyBoxCount", "packet_count", "packetCount",
		"red_packet_count", "redPacketCount", "redpack_count", "redpackCount",
		"share_count", "shareCount", "content_count", "contentCount", "content_num", "contentNum", "quantity",
		"box_num", "boxNum", "lucky_box_num", "luckyBoxNum", "winner_count", "winnerCount", "total_count", "totalCount",
	)
	diamonds, diamondErr := strconv.ParseFloat(diamondText, 64)
	shares64, shareErr := strconv.ParseFloat(shareText, 64)
	if diamondErr != nil || shareErr != nil || diamonds <= 0 || shares64 <= 0 || shares64 != float64(int(shares64)) {
		return 0, 0
	}
	return diamonds, int(shares64)
}

func validLuckyboxID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 3 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func formatPacketPrize(pairs []pair) string {
	if direct := firstPairValue(pairs,
		"prize", "prize_text", "prizeText", "prize_name", "prizeName",
		"reward_name", "rewardName", "award_name", "awardName", "display_text", "displayText",
	); normalizePacketPrize(direct) != "" {
		return normalizePacketPrize(direct)
	}
	diamonds := firstPositivePairNumber(pairs,
		"total_diamond_count", "totalDiamondCount", "total_diamond", "totalDiamond",
		"total_amount", "totalAmount", "total_diamond_num", "totalDiamondNum",
		"diamond_count", "diamondCount", "diamond_num", "diamondNum", "diamond_amount", "diamondAmount",
		"diamond", "diamonds", "amount", "content_amount", "contentAmount",
		"content_diamond_count", "contentDiamondCount", "prize_count", "prizeCount",
	)
	shares := firstPositivePairNumber(pairs,
		"box_count", "boxCount", "lucky_box_count", "luckyBoxCount", "packet_count", "packetCount",
		"red_packet_count", "redPacketCount", "redpack_count", "redpackCount",
		"share_count", "shareCount", "content_count", "contentCount", "content_num", "contentNum", "quantity",
		"box_num", "boxNum", "lucky_box_num", "luckyBoxNum", "winner_count", "winnerCount", "total_count", "totalCount",
	)
	if diamonds != "" && shares != "" {
		return fmt.Sprintf("总%s钻，%s份红包", diamonds, shares)
	}
	if diamonds != "" {
		return "总" + diamonds + "钻"
	}
	if shares != "" {
		return shares + "份红包"
	}
	if giftName := firstPairValue(pairs, "gift_name", "giftName"); giftName != "" {
		if giftCount := firstPositivePairNumber(pairs, "gift_count", "giftCount", "gift_num", "giftNum", "gift_cnt", "giftCnt"); giftCount != "" {
			return giftCount + "个" + giftName
		}
		return giftName
	}
	return normalizePacketPrize(firstPairValue(pairs, "reward_description", "rewardDescription", "award_description", "awardDescription"))
}

func firstPositivePairNumber(pairs []pair, keys ...string) string {
	for _, key := range keys {
		for _, item := range pairs {
			if !strings.EqualFold(item.key, key) {
				continue
			}
			value, err := strconv.ParseFloat(strings.TrimSpace(item.value), 64)
			if err == nil && value > 0 {
				return strconv.FormatFloat(value, 'f', -1, 64)
			}
		}
	}
	return ""
}

func normalizePacketPrize(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	compact := strings.NewReplacer(" ", "", "，", ",", "。", "").Replace(strings.ToLower(value))
	for _, invalid := range []string{"0钻", "总0钻", "0份", "0份红包", "总0钻,0份", "总0钻,0份红包"} {
		if compact == invalid {
			return ""
		}
	}
	return value
}

func normalizePacketTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if numeric, err := strconv.ParseFloat(value, 64); err == nil && numeric > 0 {
		for numeric > 1e11 {
			numeric /= 1000
		}
		seconds := int64(numeric)
		nanos := int64((numeric - float64(seconds)) * float64(time.Second))
		return time.Unix(seconds, nanos).Format(time.RFC3339Nano)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006/01/02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed.Format(time.RFC3339Nano)
		}
	}
	return value
}

func packetTimeAfter(baseValue, secondsValue string) string {
	base := normalizePacketTime(baseValue)
	seconds, err := strconv.ParseFloat(strings.TrimSpace(secondsValue), 64)
	if base == "" || err != nil || seconds < 0 {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, base)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, base)
	}
	if err != nil {
		return ""
	}
	return parsed.Add(time.Duration(seconds * float64(time.Second))).Format(time.RFC3339Nano)
}

type pair struct{ key, value string }

func collectPairs(value any, key string, out *[]pair) {
	switch item := value.(type) {
	case map[string]any:
		for childKey, child := range item {
			collectPairs(child, childKey, out)
		}
	case []any:
		for _, child := range item {
			collectPairs(child, key, out)
		}
	default:
		if key != "" {
			*out = append(*out, pair{key: key, value: scalarString(item)})
		}
	}
}

func firstPairValue(pairs []pair, keys ...string) string {
	for _, key := range keys {
		for _, item := range pairs {
			if strings.EqualFold(item.key, key) && strings.TrimSpace(item.value) != "" {
				return strings.TrimSpace(item.value)
			}
		}
	}
	return ""
}

func firstPairInt(pairs []pair, keys ...string) int {
	value := firstPairValue(pairs, keys...)
	parsed, _ := strconv.Atoi(value)
	return parsed
}

func scalarString(value any) string {
	switch item := value.(type) {
	case string:
		return item
	case float64:
		return strconv.FormatFloat(item, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(item)
	default:
		return fmt.Sprint(item)
	}
}

func stableID(data map[string]any) string {
	b, _ := json.Marshal(data)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:10])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
