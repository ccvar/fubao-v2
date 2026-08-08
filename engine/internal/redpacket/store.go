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
	"sync/atomic"
	"time"

	"fubao.ccvar.com/engine/internal/live/httpclient"
	"fubao.ccvar.com/engine/internal/rooms"
)

const (
	storeVersion         = 20
	unknownProbeInterval = 10 * time.Second
	offlineProbeInterval = 60 * time.Second
	livePacketInterval   = 15 * time.Second
	activePacketInterval = 5 * time.Second
	// A room that never resolves to a definitive live/offline result must not
	// keep asking at the short unknown cadence. Dead, deleted and mistyped room
	// ids are the bulk of a large legacy import and they all answer "unknown",
	// so at the flat 10s cadence they demand six times the budget of a healthy
	// offline room and starve the rooms that are actually broadcasting. Each
	// consecutive inconclusive probe doubles the wait; any definitive result
	// resets it immediately.
	maxUnknownProbeInterval = 10 * time.Minute
	maxErrorProbeInterval   = 5 * time.Minute
	maxProbeBackoffStreak   = 12
	// Legacy stores persisted the fixed machine-wide pace. Version 19 migrates
	// exactly this value to 自动 (0).
	legacyFixedGlobalRequestIntervalMS = 80
	// minGlobalRequestIntervalMS bounds an explicit machine-wide pace. The old
	// 40ms floor capped even a deliberately tuned install at 25 请求/秒.
	minGlobalRequestIntervalMS = 5
	defaultProbeSlots          = 64
	// Soft upper bounds for user-facing monitoring throughput controls.
	// Defaults stay conservative; these only stop unsafe extreme values.
	maxGlobalConcurrency  = 512
	maxAccountConcurrency = 8
	maxProbeConcurrency   = 1024
	// A high-priority source may fill a small burst of ready slots, after
	// which a ready lower-priority source gets a turn. This preserves the
	// source order without starving imported or center-library rooms while a
	// busy following feed is continuously producing due probes.
	bulkPriorityBurst = 8
	// Live rooms are queued ahead of everything else, because due-time ordering
	// alone collapses once the room count exceeds what the request budget can
	// service: thousands of overdue offline probes carry older due stamps than a
	// live room's next 15s slot and would win every comparison, flattening the
	// whole 5s/15s/60s cadence design into one slow rotation. bulkLiveBurst
	// bounds that preference so discovery of newly started rooms never stops.
	bulkLiveBurst   = 32
	bulkTierLive    = 0
	bulkTierIdle    = 1
	bulkTierCount   = 2
	bulkSourceCount = 3

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
	ID           string `json:"id"`
	RoomID       string `json:"room_id"`
	WebRID       string `json:"web_rid,omitempty"`
	ActualRoomID string `json:"actual_room_id,omitempty"`
	Name         string `json:"name,omitempty"`
	StreamerName string `json:"streamer_name,omitempty"`
	// StreamerID is native-only (owner uid from live enter probe) and used to
	// fill missing red-packet join anchor_id. Never returned to the frontend.
	StreamerID             string `json:"-"`
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
	// ProbeBackoffStreak counts consecutive inconclusive probes (unknown result
	// or request error). It only widens this room's own probe cadence and is
	// never evidence of being offline, so it can not influence auto-recycling.
	ProbeBackoffStreak int    `json:"probe_backoff_streak,omitempty"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
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
	Condition        string  `json:"condition,omitempty"`
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
	Condition        string
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
	ID                     string                `json:"id"`
	EventID                string                `json:"event_id"`
	AccountID              string                `json:"account_id"`
	AccountName            string                `json:"account_name"`
	TaskID                 string                `json:"task_id,omitempty"`
	Settings               ParticipationSettings `json:"settings,omitempty"`
	RoomID                 string                `json:"room_id,omitempty"`
	WebRID                 string                `json:"web_rid,omitempty"`
	ActualRoomID           string                `json:"actual_room_id,omitempty"`
	RoomName               string                `json:"room_name,omitempty"`
	StreamerName           string                `json:"streamer_name,omitempty"`
	PacketID               string                `json:"packet_id"`
	Title                  string                `json:"title,omitempty"`
	Prize                  string                `json:"prize,omitempty"`
	Award                  string                `json:"award,omitempty"`
	DrawAt                 string                `json:"draw_at,omitempty"`
	ExpiresAt              string                `json:"expires_at,omitempty"`
	Endpoint               string                `json:"endpoint,omitempty"`
	Status                 string                `json:"status"`
	Message                string                `json:"message,omitempty"`
	AttemptCount           int                   `json:"attempt_count"`
	Joined                 bool                  `json:"joined"`
	Won                    bool                  `json:"won,omitempty"`
	WalletBeforeDiamond    int64                 `json:"wallet_before_diamond,omitempty"`
	WalletBaselineRecorded bool                  `json:"wallet_baseline_recorded,omitempty"`
	WalletAfterDiamond     int64                 `json:"wallet_after_diamond,omitempty"`
	WalletDiamondDelta     int64                 `json:"wallet_diamond_delta,omitempty"`
	ResultSource           string                `json:"result_source,omitempty"`
	JoinedAt               string                `json:"joined_at,omitempty"`
	CooldownUntil          string                `json:"cooldown_until,omitempty"`
	CreatedAt              string                `json:"created_at"`
	UpdatedAt              string                `json:"updated_at"`
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
// JoinCount is successful joins only (Joined=true). Today* fields use the
// local calendar day of join/win confirmation.
type ParticipationOverview struct {
	JoinCount        int     `json:"join_count"`
	WinCount         int     `json:"win_count"`
	WinDiamonds      float64 `json:"win_diamonds"`
	TodayJoinCount   int     `json:"today_join_count"`
	TodayWinCount    int     `json:"today_win_count"`
	TodayWinDiamonds float64 `json:"today_win_diamonds"`
}

// ActivityAccountSummary freezes one exact task's result for activity detail.
// A later task for the same canonical account therefore cannot change history.
type ActivityAccountSummary struct {
	AccountID    string  `json:"account_id"`
	AccountName  string  `json:"account_name,omitempty"`
	TaskID       string  `json:"task_id"`
	JoinCount    int     `json:"join_count"`
	FailureCount int     `json:"failure_count"`
	WinCount     int     `json:"win_count"`
	WinDiamonds  float64 `json:"win_diamonds"`
	EndReason    string  `json:"end_reason,omitempty"`
}

// ParticipationTaskRun is the safe persisted-task view shown above the
// account/event participation detail table. One explicit account start is one
// run; one immediate or scheduled batch is also one run rather than one row
// per account. TaskIDs exist only to filter safe detail rows in the frontend.
type ParticipationTaskRun struct {
	ID               string                   `json:"id"`
	Mode             string                   `json:"mode"`
	Title            string                   `json:"title,omitempty"`
	ModeLabel        string                   `json:"mode_label"`
	Status           string                   `json:"status"`
	AccountCount     int                      `json:"account_count"`
	SkippedCount     int                      `json:"skipped_count,omitempty"`
	StartedAt        string                   `json:"started_at"`
	EndedAt          string                   `json:"ended_at,omitempty"`
	SuccessCount     int                      `json:"success_count"`
	FailureCount     int                      `json:"failure_count"`
	WinCount         int                      `json:"win_count"`
	WinDiamonds      float64                  `json:"win_diamonds"`
	EndReason        string                   `json:"end_reason,omitempty"`
	TaskIDs          []string                 `json:"task_ids"`
	AccountSummaries []ActivityAccountSummary `json:"account_summaries,omitempty"`
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
// participation account. Zero keeps the user-facing stop/cooldown limits
// disabled; result-query fields are normalized to bounded defaults.
type ParticipationSettings struct {
	StopAfterJoins  int `json:"stop_after_joins"`
	CooldownSeconds int `json:"cooldown_seconds"`
	StopAfterWins   int `json:"stop_after_wins"`
	// DrawResultDelaySeconds is the native delay after the accepted join before
	// querying luckybox/receive. Event timestamps are metadata only and must
	// not make a joined record wait for a later validity-window deadline.
	DrawResultDelaySeconds int `json:"draw_result_delay_seconds"`
	// DrawResultMaxAttempts bounds result queries. Once the attempts are
	// exhausted the wallet-delta fallback runs before an abnormal result is
	// recorded.
	DrawResultMaxAttempts int `json:"draw_result_max_attempts"`
	// DrawResultTimeoutSeconds is retained only for migration compatibility with
	// older stores/frontends. New scheduling uses delay + max attempts.
	DrawResultTimeoutSeconds int `json:"draw_result_timeout_seconds,omitempty"`
	// ParticipationCountdownSeconds is the final validity window in which a
	// new join can be assigned. For example, 2 admits only when two seconds or
	// less remain. Zero disables this extra gate; an already expired packet is
	// still never eligible.
	ParticipationCountdownSeconds int `json:"participation_countdown_seconds"`
	// RiskControlCooldownMinutes is applied when Douyin returns the soft-deny
	// shape succeed=false/hit_bonus=false (including real browser captures that
	// still fail). The account is marked 冷却中 and skipped until it expires.
	// Default is 60 minutes.
	RiskControlCooldownMinutes int    `json:"risk_control_cooldown_minutes"`
	MinimumDiamonds            int    `json:"minimum_diamonds"`
	PacketType                 string `json:"packet_type"`
	FollowPolicy               string `json:"follow_policy"`
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
	Mode             string                   `json:"mode,omitempty"`
	Label            string                   `json:"label"`
	Active           bool                     `json:"active,omitempty"`
	StartedCount     int                      `json:"started_count,omitempty"`
	SkippedCount     int                      `json:"skipped_count,omitempty"`
	JoinCount        int                      `json:"join_count,omitempty"`
	FailureCount     int                      `json:"failure_count,omitempty"`
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
	ParticipationRuns      []*Activity                            `json:"participation_runs,omitempty"`
	Activities             []*Activity                            `json:"activities,omitempty"`
}

type Store struct {
	mu                     sync.Mutex
	saveMu                 sync.Mutex // serializes marshal+disk write outside s.mu
	path                   string
	monitors               map[string]*Monitor
	events                 map[string]*Event
	participations         map[string]*ParticipationRecord
	participationTasks     map[string]*ParticipationTask
	participationTraces    map[string]*ParticipationTrace
	participationSchedules map[string]*ParticipationSchedule
	participationRuns      map[string]*Activity
	settings               ParticipationSettings
	monitoringSettings     MonitoringSettings
	activities             map[string]*Activity
	runtime                map[string]context.CancelFunc
	pool                   *accountPool
	bulkCancel             context.CancelFunc
	bulkIDs                map[string]struct{}
	requestRecorder        func(accountID string, requestErr error)
	cooldownRecorder       monitorCooldownRecorder
	eventHandler           func(Event)
	liveResultHandler      func(roomID, status string, checkedAt time.Time)
	persistDirty           bool
	persistScheduled       bool
	// probeSlots is read on every probe, so it gets its own lock instead of
	// contending with the full-store save and list_page work under s.mu.
	probeMu    sync.RWMutex
	probeSlots chan struct{}
	// bulkWorkerTarget mirrors the probe window for the bulk scheduler, which
	// reads it lock-free every loop so a settings change resizes the running
	// worker pool without restarting monitoring.
	bulkWorkerTarget atomic.Int64
	// monitorSummary is rebuilt on list_page under a short lock (no JSON/IO).
	// Combined with lock-free save, UI stays responsive at 10万 monitors.
	monitorSummary MonitorSummary
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
		participationRuns:      map[string]*Activity{},
		activities:             map[string]*Activity{},
		runtime:                map[string]context.CancelFunc{},
		bulkIDs:                map[string]struct{}{},
		settings: normalizeParticipationSettings(ParticipationSettings{
			DrawResultDelaySeconds: 1, DrawResultMaxAttempts: 3,
			ParticipationCountdownSeconds: 10,
		}),
		monitoringSettings: normalizeMonitoringSettings(MonitoringSettings{}),
	}
	s.setProbeWindow(s.monitoringSettings.ProbeConcurrency)
	if err := s.load(); err != nil {
		return nil, err
	}
	s.resetMonitorSummaryLocked()
	return s, nil
}

func (s *Store) resetMonitorSummaryLocked() {
	s.monitorSummary = MonitorSummary{
		Total:        len(s.monitors),
		Enabled:      0,
		Running:      0,
		PendingFirst: 0,
		FirstChecked: 0,
		LiveRunning:  0,
		Errors:       0,
	}
	for _, monitor := range s.monitors {
		if monitor.Enabled {
			s.monitorSummary.Enabled++
		}
		if monitor.Status == "running" {
			s.monitorSummary.Running++
			if monitor.ConnectionStatus == "connecting" {
				s.monitorSummary.PendingFirst++
			} else {
				s.monitorSummary.FirstChecked++
			}
			if monitor.LiveStatus == "live" {
				s.monitorSummary.LiveRunning++
			}
		} else if monitor.Status == "error" || monitor.ConnectionStatus == "error" {
			s.monitorSummary.Errors++
		}
	}
}

// SetRequestRecorder attaches the account-store hook used to keep the safe
// monitoring request counters local to this Go engine. The hook receives no
// Cookie or response data.
func (s *Store) SetRequestRecorder(recorder func(accountID string, requestErr error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestRecorder = recorder
}

// SetCooldownRecorder attaches the account-store hook that surfaces an account
// pool back-off on the monitoring account row. The hook receives only the
// account id, expiry and a safe reason; no Cookie or request data.
func (s *Store) SetCooldownRecorder(recorder monitorCooldownRecorder) {
	s.mu.Lock()
	s.cooldownRecorder = recorder
	pool := s.pool
	s.mu.Unlock()
	if pool != nil {
		pool.setCooldownRecorder(recorder)
	}
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
	payload, present, err := loadPersistedStoreFile(s.path)
	if err != nil {
		return err
	}
	if !present {
		return nil
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
			repairImmediateWinRecord(record)
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
	migrated := payload.Version < storeVersion
	// Version 15 introduced the participation countdown. Older files could not
	// express an explicit zero, so a missing/zero value on migration means the
	// new default of ten seconds. New writes preserve an explicit zero.
	if payload.Version < 15 && payload.ParticipationSettings.ParticipationCountdownSeconds == 0 {
		payload.ParticipationSettings.ParticipationCountdownSeconds = 10
		migrated = true
	}
	// Version 16 replaced the unbounded wall-clock draw timeout with an
	// explicit query delay and attempt count. These fields were not present in
	// older files, so only migration zero values receive the new defaults.
	if payload.Version < 16 {
		if payload.ParticipationSettings.DrawResultDelaySeconds == 0 {
			payload.ParticipationSettings.DrawResultDelaySeconds = 1
		}
		if payload.ParticipationSettings.DrawResultMaxAttempts == 0 {
			payload.ParticipationSettings.DrawResultMaxAttempts = 3
		}
		migrated = true
	}
	// Version 17 demotes historical soft-deny false successes (see
	// demoteFalseSuccessfulJoins). Must run after records are loaded.
	if payload.Version < 17 {
		if demoteFalseSuccessfulJoins(s.participations) > 0 {
			migrated = true
		} else {
			// Still bump version so we do not re-scan every launch.
			migrated = true
		}
	}
	// Version 18 drops noise participation rows (failed / network_error /
	// pending) from the durable audit list so the UI only keeps meaningful
	// outcomes. Idempotent account/event keys are freed for a later retry.
	if payload.Version < 18 {
		if purgeNoiseParticipationRecords(s.participations) > 0 {
			migrated = true
		} else {
			migrated = true
		}
	}
	// Version 19 moves the machine-wide request pace to 自动 for stores still
	// pinned to the historical fixed default. That value predates monitoring
	// account pooling and caps the whole machine at ~12.5 请求/秒 regardless of
	// how many accounts are imported. An explicitly tuned pace is left alone.
	if payload.Version < 19 && payload.MonitoringSettings.GlobalRequestIntervalMS == legacyFixedGlobalRequestIntervalMS {
		payload.MonitoringSettings.GlobalRequestIntervalMS = 0
		migrated = true
	}
	s.settings = normalizeParticipationSettings(payload.ParticipationSettings)
	s.monitoringSettings = normalizeMonitoringSettings(payload.MonitoringSettings)
	s.setProbeWindow(s.monitoringSettings.ProbeConcurrency)
	for _, record := range s.participations {
		if !record.Joined || participationDrawTerminal(record.Status) {
			continue
		}
		settings := s.settings
		if snapshot, ok := taskSettingsSnapshot(record.Settings); ok {
			settings = snapshot
		}
		// A pending result is timed from the accepted join, not from the
		// packet's expiry timestamp. The latter can be a one-minute validity
		// window and must never leave a record waiting for a minute before its
		// first receive query. The active participant performs the wallet
		// fallback; reload reconciliation only cleans up records that are
		// already beyond the bounded query window.
		joinedAtText := firstNonEmpty(record.JoinedAt, record.CreatedAt)
		joinedAt, parseErr := time.Parse(time.RFC3339Nano, joinedAtText)
		if parseErr != nil {
			continue
		}
		queryWindow := time.Duration(settings.DrawResultDelaySeconds)*time.Second +
			time.Duration(settings.DrawResultMaxAttempts)*defaultDrawResultRequestTimeout
		if time.Now().Before(joinedAt.Add(queryWindow)) {
			continue
		}
		record.Status = "draw_error"
		record.Message = fmt.Sprintf("开奖查询失败：已尝试 %d 次，钻石增量未变化", settings.DrawResultMaxAttempts)
		record.Endpoint = "receive"
		record.UpdatedAt = time.Now().Format(time.RFC3339Nano)
		migrated = true
	}
	for _, activity := range payload.ParticipationRuns {
		if activity != nil && activity.ID != "" {
			s.participationRuns[activity.ID] = activity
			s.activities[activity.ID] = activity
		}
	}
	for _, activity := range payload.Activities {
		if activity != nil && activity.ID != "" && activity.Label != "" {
			if run := s.participationRuns[activity.ID]; run != nil {
				// The dedicated archive is canonical for task rows. Reuse the same
				// pointer so later completion updates both views atomically.
				s.activities[activity.ID] = run
			} else {
				s.activities[activity.ID] = activity
				if participationActivityIsTaskRun(activity) {
					s.participationRuns[activity.ID] = activity
				}
			}
		}
	}
	// Version 20 separates task history from the 100-row recent-activity feed.
	// Existing task activities become the initial archive during migration.
	if payload.Version < 20 {
		migrated = true
	}
	if s.migrateLegacyBatchActivitiesLocked() {
		migrated = true
	}
	if migrated {
		// load runs before the store is shared; still take s.mu so saveLocked's
		// unlock/relock contract (caller holds the lock) is satisfied.
		s.mu.Lock()
		err := s.saveLocked()
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *Store) saveLocked() error {
	// Snapshot under lock for consistency, then marshal/write outside lock to
	// avoid blocking probe state updates, list_page, and UI for minutes.
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
		activities = append(activities, cloneActivity(item))
	}
	participationRuns := make([]*Activity, 0, len(s.participationRuns))
	for _, item := range s.participationRuns {
		participationRuns = append(participationRuns, cloneActivity(item))
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
	sort.Slice(participationRuns, func(i, j int) bool { return participationRuns[i].CreatedAt > participationRuns[j].CreatedAt })
	if len(participationRuns) > 1000 {
		participationRuns = participationRuns[:1000]
	}
	// Capture settings/path under lock; marshal and disk I/O must not hold s.mu
	// or Windows UI/RPC blocks for the whole multi-MB write.
	settings := s.settings
	monitoringSettings := s.monitoringSettings
	path := s.path
	s.mu.Unlock()

	s.saveMu.Lock()
	payload, err := json.Marshal(file{
		Version: storeVersion, Monitors: monitors, Events: events, NativeParticipation: nativeParticipation, ParticipationRecords: participations,
		ParticipationSettings: settings, MonitoringSettings: monitoringSettings,
		ParticipationTasks: participationTasks, ParticipationTraces: participationTraces,
		ParticipationSchedules: participationSchedules, ParticipationRuns: participationRuns, Activities: activities,
	})
	if err != nil {
		s.saveMu.Unlock()
		s.mu.Lock()
		return fmt.Errorf("序列化红包监测数据失败: %w", err)
	}
	if err := writePersistedStoreFile(path, payload); err != nil {
		s.saveMu.Unlock()
		s.mu.Lock()
		return fmt.Errorf("保存红包监测数据失败: %w", err)
	}
	s.saveMu.Unlock()
	s.mu.Lock()
	return nil
}

func cloneActivity(item *Activity) *Activity {
	if item == nil {
		return nil
	}
	copy := *item
	copy.AccountIDs = append([]string(nil), item.AccountIDs...)
	copy.AccountSummaries = append([]ActivityAccountSummary(nil), item.AccountSummaries...)
	if len(item.TaskIDs) > 0 {
		copy.TaskIDs = make(map[string]string, len(item.TaskIDs))
		for accountID, taskID := range item.TaskIDs {
			copy.TaskIDs[accountID] = taskID
		}
	}
	return &copy
}

func participationActivityIsTaskRun(activity *Activity) bool {
	if activity == nil {
		return false
	}
	switch activity.Kind {
	case "participation_started", "participation_task_completed", "participation_batch_executed":
		return true
	default:
		return false
	}
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
	delay := s.persistFlushDelayLocked()
	go func() {
		time.Sleep(delay)
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

// persistFlushDelayLocked widens the coalescing window as the store grows. Room
// probe state is derived data — it is re-established by the next probe — so
// re-serializing thousands of monitors every three seconds for the entire life
// of a monitoring session buys nothing. Participation reservations and results
// bypass this path entirely and still save synchronously.
func (s *Store) persistFlushDelayLocked() time.Duration {
	delay := 3 * time.Second
	if steps := len(s.monitors) / 2000; steps > 0 {
		delay += time.Duration(steps) * 3 * time.Second
	}
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
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
func (s *Store) CompleteParticipation(eventID, accountID, endpoint, status, message string, attempts int, joined, won bool, award string, cooldown time.Duration) error {
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
	record.Won = won || status == "won"
	if award = strings.TrimSpace(award); award != "" {
		record.Award = award
	}
	// Immediate join/rush prizes must not remain status=joined with only a Won
	// flag — the participation UI and history read status/message/award.
	if record.Won && (record.Status == "" || record.Status == "joined" || record.Status == "already_joined") {
		record.Status = "won"
		if record.Message == "" || record.Message == "红包参与请求已受理" || record.Message == "红包已受理" {
			if record.Award != "" {
				record.Message = "已中" + record.Award
			} else {
				record.Message = "已中奖"
			}
		}
	}
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

// RecordParticipationWalletBaseline stores the pre-join wallet balance for a
// reserved account/event pair. It is safe aggregate data only and is used by
// the native result fallback when luckybox/receive omits personal outcome.
func (s *Store) RecordParticipationWalletBaseline(eventID, accountID string, diamond int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.participations[participationRecordID(accountID, eventID)]
	if record == nil {
		return errors.New("红包参与记录不存在")
	}
	if !record.WalletBaselineRecorded {
		record.WalletBeforeDiamond = maxInt64(0, diamond)
		record.WalletBaselineRecorded = true
	}
	record.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	return s.saveLocked()
}

func (s *Store) ParticipationWalletBaseline(eventID, accountID string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.participations[participationRecordID(accountID, eventID)]
	if record == nil || !record.WalletBaselineRecorded {
		return 0, false
	}
	return record.WalletBeforeDiamond, true
}

// RecordParticipationWalletResult stores the post-draw wallet snapshot and
// the computed positive delta. A wallet delta is a fallback result source, not
// a replacement for an explicit luckybox/receive outcome.
func (s *Store) RecordParticipationWalletResult(eventID, accountID string, after, delta int64, source string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.participations[participationRecordID(accountID, eventID)]
	if record == nil {
		return errors.New("红包参与记录不存在")
	}
	record.WalletAfterDiamond = maxInt64(0, after)
	record.WalletDiamondDelta = maxInt64(0, delta)
	if strings.TrimSpace(source) != "" {
		record.ResultSource = strings.TrimSpace(source)
	}
	record.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	return s.saveLocked()
}

// AnnotateFailedWithWalletWin keeps status=failed but attaches a confirmed
// wallet diamond win when soft-deny join still credited diamonds. UI shows
// "参与失败 · 已中N钻". Returns true when a new win is recorded.
func (s *Store) AnnotateFailedWithWalletWin(eventID, accountID, award string, delta int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.participations[participationRecordID(accountID, eventID)]
	if record == nil {
		return false, errors.New("红包参与记录不存在")
	}
	if strings.TrimSpace(record.Status) != "failed" {
		return false, nil
	}
	if record.Won && strings.TrimSpace(record.Award) != "" {
		return false, nil
	}
	award = strings.TrimSpace(award)
	if award == "" && delta > 0 {
		award = fmt.Sprintf("%d钻", delta)
	}
	if award == "" && delta <= 0 {
		return false, nil
	}
	newWin := !record.Won
	record.Won = true
	record.Award = award
	if delta > 0 {
		record.WalletDiamondDelta = maxInt64(record.WalletDiamondDelta, delta)
	}
	record.ResultSource = "wallet_delta"
	record.Message = fmt.Sprintf("参与失败 · 已中%s（钱包增量确认）", award)
	record.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	return newWin, s.saveLocked()
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
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
	if settings.RiskControlCooldownMinutes <= 0 {
		settings.RiskControlCooldownMinutes = 60
	}
	if settings.RiskControlCooldownMinutes > 24*60 {
		settings.RiskControlCooldownMinutes = 24 * 60
	}
	if settings.StopAfterWins < 0 {
		settings.StopAfterWins = 0
	}
	if settings.StopAfterWins > 100000 {
		settings.StopAfterWins = 100000
	}
	if settings.DrawResultDelaySeconds < 0 {
		settings.DrawResultDelaySeconds = 0
	}
	if settings.DrawResultDelaySeconds > 60 {
		settings.DrawResultDelaySeconds = 60
	}
	if settings.DrawResultMaxAttempts <= 0 {
		settings.DrawResultMaxAttempts = 3
	}
	if settings.DrawResultMaxAttempts > 20 {
		settings.DrawResultMaxAttempts = 20
	}
	if settings.DrawResultTimeoutSeconds <= 0 {
		settings.DrawResultTimeoutSeconds = 10
	}
	if settings.ParticipationCountdownSeconds < 0 {
		settings.ParticipationCountdownSeconds = 0
	}
	if settings.ParticipationCountdownSeconds > 300 {
		settings.ParticipationCountdownSeconds = 300
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
	// Zero means 自动: the machine-wide pace is derived from the usable
	// monitoring account fleet when the pool is built. Only an explicitly
	// configured pace is clamped.
	if settings.GlobalRequestIntervalMS < 0 {
		settings.GlobalRequestIntervalMS = 0
	}
	if settings.GlobalRequestIntervalMS > 0 {
		settings.GlobalRequestIntervalMS = maxInt(minGlobalRequestIntervalMS, minInt(settings.GlobalRequestIntervalMS, 2000))
	}
	if settings.AccountRequestIntervalMS <= 0 {
		settings.AccountRequestIntervalMS = int(defaultAccountRequestInterval / time.Millisecond)
	}
	settings.AccountRequestIntervalMS = maxInt(250, minInt(settings.AccountRequestIntervalMS, 5000))
	if settings.GlobalConcurrency <= 0 {
		settings.GlobalConcurrency = defaultGlobalConcurrency
	}
	// Soft safety caps for high-volume room monitoring. Higher values increase
	// in-flight HTTP/probe pressure without shortening request intervals.
	settings.GlobalConcurrency = minInt(settings.GlobalConcurrency, maxGlobalConcurrency)
	if settings.AccountConcurrency <= 0 {
		settings.AccountConcurrency = defaultAccountConcurrency
	}
	settings.AccountConcurrency = minInt(settings.AccountConcurrency, maxAccountConcurrency)
	if settings.ProbeConcurrency <= 0 {
		settings.ProbeConcurrency = defaultProbeSlots
	}
	settings.ProbeConcurrency = maxInt(8, minInt(settings.ProbeConcurrency, maxProbeConcurrency))
	return settings
}

func (settings MonitoringSettings) poolConfig() poolConfig {
	settings = normalizeMonitoringSettings(settings)
	return poolConfig{
		globalInterval:     time.Duration(settings.GlobalRequestIntervalMS) * time.Millisecond,
		globalIntervalAuto: settings.GlobalRequestIntervalMS == 0,
		accountInterval:    time.Duration(settings.AccountRequestIntervalMS) * time.Millisecond,
		globalParallel:     settings.GlobalConcurrency,
		accountParallel:    settings.AccountConcurrency,
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
	s.monitoringSettings = settings
	s.setProbeWindow(settings.ProbeConcurrency)
	pool := s.pool
	if err := s.saveLocked(); err != nil {
		s.monitoringSettings = previousSettings
		s.setProbeWindow(previousSettings.ProbeConcurrency)
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
		settings.DrawResultDelaySeconds == 0 && settings.DrawResultMaxAttempts == 0 &&
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
	case "won", "not_won", "draw_error", "challenge_blocked", "risk_control":
		return true
	default:
		return false
	}
}

// repairImmediateWinRecord fixes rows where join/rush already set Won=true but
// left status=joined (UI never showed a win). Award text is preserved when
// present; otherwise a generic 已中奖 label is used.
func repairImmediateWinRecord(record *ParticipationRecord) {
	if record == nil || !record.Won {
		return
	}
	status := strings.TrimSpace(record.Status)
	if status != "" && status != "joined" && status != "already_joined" {
		return
	}
	record.Status = "won"
	message := strings.TrimSpace(record.Message)
	if message == "" || message == "红包参与请求已受理" || message == "红包已受理" {
		if award := strings.TrimSpace(record.Award); award != "" {
			record.Message = "已中" + award
		} else {
			record.Message = "已中奖"
		}
	}
}

// RecordEventCondition persists a participation threshold learned from a join
// response. luckybox/box/list often omits the requirement, so the rejected join
// is the only place it appears; keeping it on the event makes every later
// account skip the packet instead of rediscovering the same rejection.
func (s *Store) RecordEventCondition(eventID, condition string) error {
	eventID, condition = strings.TrimSpace(eventID), strings.TrimSpace(condition)
	if eventID == "" || condition == "" {
		return nil
	}
	s.mu.Lock()
	event := s.events[eventID]
	if event == nil || event.Condition == condition {
		s.mu.Unlock()
		return nil
	}
	event.Condition = condition
	err := s.saveLocked()
	s.mu.Unlock()
	return err
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
		AccountIDs: []string{accountID}, Title: accountName, Mode: "manual", Active: true, StartedCount: 1,
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
	s.participationRuns[activity.ID] = activity
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

// ParticipationAccountStats is a credential-free per-account rollup from the
// durable participation record store. Used to repair account-row counters when
// profile join/win totals drift or today_* fields were never backfilled.
type ParticipationAccountStats struct {
	AccountID        string  `json:"account_id"`
	JoinCount        int     `json:"join_count"`
	WinCount         int     `json:"win_count"`
	TodayJoinCount   int     `json:"today_join_count"`
	TodayWinCount    int     `json:"today_win_count"`
	TodayWinDiamonds float64 `json:"today_win_diamonds"`
	TodayStatDate    string  `json:"today_stat_date"`
}

// ParticipationAccountStats returns successful-join / win totals and local-day
// counters for every account that has durable participation records.
// Risk-control, failed, network-error and other non-accepted rows never count.
func (s *Store) ParticipationAccountStats(now time.Time) []ParticipationAccountStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.IsZero() {
		now = time.Now()
	}
	today := now.Format("2006-01-02")
	byID := map[string]*ParticipationAccountStats{}
	for _, record := range s.participations {
		if record == nil || strings.TrimSpace(record.AccountID) == "" {
			continue
		}
		stats := byID[record.AccountID]
		if stats == nil {
			stats = &ParticipationAccountStats{
				AccountID:     record.AccountID,
				TodayStatDate: today,
			}
			byID[record.AccountID] = stats
		}
		if participationIsSuccessfulJoin(record) {
			stats.JoinCount++
			if participationStampOnLocalDay(firstNonEmpty(record.JoinedAt, record.CreatedAt, record.UpdatedAt), now) {
				stats.TodayJoinCount++
			}
		}
		if participationIsConfirmedWin(record) {
			stats.WinCount++
			diamonds := participationWinDiamonds(record)
			if participationStampOnLocalDay(firstNonEmpty(record.UpdatedAt, record.JoinedAt, record.CreatedAt), now) {
				stats.TodayWinCount++
				if diamonds > 0 {
					stats.TodayWinDiamonds += diamonds
				}
			}
		}
	}
	items := make([]ParticipationAccountStats, 0, len(byID))
	for _, stats := range byID {
		items = append(items, *stats)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AccountID < items[j].AccountID })
	return items
}

// ParticipationOverview returns persisted all-time totals across every
// account and task. Only successful joins and confirmed wins count — never
// risk_control / failed / network_error / login_expired / challenge rows.
func (s *Store) ParticipationOverview() ParticipationOverview {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	result := ParticipationOverview{}
	for _, record := range s.participations {
		if record == nil {
			continue
		}
		if participationIsSuccessfulJoin(record) {
			result.JoinCount++
			if participationStampOnLocalDay(firstNonEmpty(record.JoinedAt, record.CreatedAt, record.UpdatedAt), now) {
				result.TodayJoinCount++
			}
		}
		if participationIsConfirmedWin(record) {
			result.WinCount++
			diamonds := participationWinDiamonds(record)
			result.WinDiamonds += diamonds
			if participationStampOnLocalDay(firstNonEmpty(record.UpdatedAt, record.JoinedAt, record.CreatedAt), now) {
				result.TodayWinCount++
				if diamonds > 0 {
					result.TodayWinDiamonds += diamonds
				}
			}
		}
	}
	return result
}

// falseSuccessfulJoinCutover is when 0.1.42 stopped promoting soft-deny
// (status_code=0 + succeed=false) to joined. Rows marked joined before this
// without a confirmed win were produced by that classifier bug.
// The migration timestamp is a product-local timestamp, not the runner's
// process timezone. Keep it pinned to the Beijing offset used by the stored
// participation records so a UTC GitHub runner does not demote valid rows
// created later that same local day.
var falseSuccessfulJoinCutover = time.Date(2026, 8, 6, 12, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))

// demoteFalseSuccessfulJoins clears Joined on pre-cutover soft-deny false
// accepts so overview / account 参与成功 no longer include them. Confirmed wins
// and wallet-delta proof are kept. Returns the number of demoted rows.
func demoteFalseSuccessfulJoins(records map[string]*ParticipationRecord) int {
	if len(records) == 0 {
		return 0
	}
	demoted := 0
	for _, record := range records {
		if record == nil || !record.Joined {
			continue
		}
		// Confirmed personal wins prove the account was in the draw pool.
		if participationIsConfirmedWin(record) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(record.ResultSource), "wallet_delta") {
			continue
		}
		if participationWinDiamonds(record) > 0 {
			continue
		}
		stamp := firstNonEmpty(record.JoinedAt, record.CreatedAt, record.UpdatedAt)
		if ts, ok := parseParticipationStamp(stamp); ok && !ts.Before(falseSuccessfulJoinCutover) {
			// After the soft-deny fix, trust the joined flag.
			continue
		}
		record.Joined = false
		switch strings.ToLower(strings.TrimSpace(record.Status)) {
		case "not_won", "draw_error", "joined", "already_joined", "pending":
			record.Status = "failed"
			msg := strings.TrimSpace(record.Message)
			if msg == "" || msg == "未中奖" || strings.Contains(msg, "开奖") ||
				msg == "红包参与请求已受理" || msg == "红包参与请求已受理，等待开奖" || msg == "红包已受理" {
				record.Message = "历史误记：接口未真正受理（succeed=false 曾被记为成功）"
			}
		}
		demoted++
	}
	return demoted
}

func parseParticipationStamp(stamp string) (time.Time, bool) {
	stamp = strings.TrimSpace(stamp)
	if stamp == "" {
		return time.Time{}, false
	}
	if ts, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
		return ts, true
	}
	if ts, err := time.Parse(time.RFC3339, stamp); err == nil {
		return ts, true
	}
	return time.Time{}, false
}

// participationIsSuccessfulJoin is true only when Douyin accepted the join.
// Requires Joined=true (or a confirmed win). Status not_won/draw_error alone
// never counts — historical soft-deny rows were rewritten to those statuses
// after an empty receive while Joined was incorrectly true.
func participationIsSuccessfulJoin(record *ParticipationRecord) bool {
	if record == nil {
		return false
	}
	// A confirmed win always implies a real accepted join.
	if participationIsConfirmedWin(record) {
		return true
	}
	if !record.Joined {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(record.Status)) {
	case "risk_control", "failed", "network_error", "login_expired", "challenge_blocked",
		"context_required", "expired", "pending":
		return false
	case "joined", "already_joined", "not_won", "won", "draw_error":
		// draw_error means the join was accepted; only the personal result query failed.
		return true
	default:
		return true
	}
}

// participationIsFailedAttempt counts a real native participation request
// that was not accepted. Records rejected before any request (expired packet,
// missing page context, cancelled reservation) are deliberately excluded.
func participationIsFailedAttempt(record *ParticipationRecord) bool {
	if record == nil || record.AttemptCount <= 0 || participationIsSuccessfulJoin(record) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(record.Status)) {
	case "failed", "risk_control", "network_error", "login_expired", "challenge_blocked":
		return true
	default:
		return false
	}
}

func participationIsConfirmedWin(record *ParticipationRecord) bool {
	if record == nil {
		return false
	}
	if record.Won {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(record.Status), "won")
}

func participationWinDiamonds(record *ParticipationRecord) float64 {
	if record == nil {
		return 0
	}
	if diamonds := participationAwardDiamonds(record.Award); diamonds > 0 {
		return diamonds
	}
	return participationAwardDiamonds(record.Message)
}

// participationStampOnLocalDay reports whether stamp falls on the local
// calendar day of now (RFC3339 / RFC3339Nano).
func participationStampOnLocalDay(stamp string, now time.Time) bool {
	stamp = strings.TrimSpace(stamp)
	if stamp == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, stamp)
	}
	if err != nil {
		return false
	}
	local := parsed.In(now.Location())
	y1, m1, d1 := local.Date()
	y2, m2, d2 := now.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

// formatParticipationWinSegment is the prize half of a completion line.
// Zero wins render as 未中奖; otherwise e.g. 2钻/1次 (no “中奖” prefix).
func formatParticipationWinSegment(wins int, diamonds float64) string {
	if wins <= 0 {
		return "未中奖"
	}
	return fmt.Sprintf("%s钻/%d次", formatParticipationDiamonds(diamonds), wins)
}

func formatParticipationCompletionLabel(name string, joins, wins int, diamonds float64) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "参与账号"
	}
	// Compact copy: no spaces around numbers. Examples:
	// jojo已完成:2钻/1次, 参与10次
	// jojo已完成:未中奖, 参与10次
	return fmt.Sprintf("%s已完成:%s, 参与%d次", name, formatParticipationWinSegment(wins, diamonds), joins)
}

func formatParticipationBatchCompletionLabel(title, state string, joins, wins int, diamonds float64) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "红包参与任务"
	}
	state = strings.TrimSpace(state)
	if state == "" {
		state = "已完成"
	}
	return fmt.Sprintf("“%s”%s:%s, 参与%d次",
		title, state, formatParticipationWinSegment(wins, diamonds), joins)
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
		if participationIsSuccessfulJoin(record) {
			summary.JoinCount++
		}
		if participationIsFailedAttempt(record) {
			summary.FailureCount++
		}
		if participationIsConfirmedWin(record) {
			summary.WinCount++
			summary.WinDiamonds += participationWinDiamonds(record)
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
	s.participationRuns[task.ID] = activity
	activity.Kind = "participation_task_completed"
	activity.AccountID = task.AccountID
	activity.AccountIDs = []string{task.AccountID}
	activity.TaskIDs = map[string]string{task.AccountID: task.ID}
	activity.AccountSummaries = []ActivityAccountSummary{summary}
	activity.Title = summary.AccountName
	activity.Label = formatParticipationCompletionLabel(summary.AccountName, summary.JoinCount, summary.WinCount, summary.WinDiamonds)
	activity.Active = false
	activity.JoinCount = summary.JoinCount
	activity.FailureCount = summary.FailureCount
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
	s.participationRuns[activity.ID] = activity
	if !stopped {
		for accountID, taskID := range activity.TaskIDs {
			if task := s.participationTasks[accountID]; task != nil && task.ID == taskID && task.Active {
				return false
			}
		}
	}
	summaries := make([]ActivityAccountSummary, 0, len(activity.AccountIDs))
	joins, failures, wins := 0, 0, 0
	diamonds := 0.0
	for _, accountID := range activity.AccountIDs {
		taskID := activity.TaskIDs[accountID]
		if taskID == "" {
			continue
		}
		summary := s.participationTaskSummaryLocked(accountID, taskID)
		summaries = append(summaries, summary)
		joins += summary.JoinCount
		failures += summary.FailureCount
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
	activity.Label = formatParticipationBatchCompletionLabel(title, state, joins, wins, diamonds)
	activity.Active = false
	activity.AccountSummaries = summaries
	activity.JoinCount = joins
	activity.FailureCount = failures
	activity.WinCount = wins
	activity.WinDiamonds = diamonds
	activity.FinishedAt = now.Format(time.RFC3339Nano)
	return true
}

func mergeParticipationTaskSummary(base, live ActivityAccountSummary) ActivityAccountSummary {
	if base.AccountID == "" {
		base.AccountID = live.AccountID
	}
	if base.AccountName == "" {
		base.AccountName = live.AccountName
	}
	if base.TaskID == "" {
		base.TaskID = live.TaskID
	}
	if live.JoinCount > base.JoinCount {
		base.JoinCount = live.JoinCount
	}
	if live.FailureCount > base.FailureCount {
		base.FailureCount = live.FailureCount
	}
	if live.WinCount > base.WinCount {
		base.WinCount = live.WinCount
	}
	if live.WinDiamonds > base.WinDiamonds {
		base.WinDiamonds = live.WinDiamonds
	}
	if live.EndReason != "" {
		base.EndReason = live.EndReason
	}
	return base
}

func participationRunModeLabel(mode, title string) string {
	switch strings.TrimSpace(mode) {
	case "manual":
		return "单账号启动"
	case "immediate":
		return "立即执行"
	case ParticipationScheduleOnce:
		return "指定日期"
	case ParticipationScheduleDaily:
		return "每天固定时间"
	case ParticipationScheduleInterval:
		return "间隔执行"
	}
	if title = strings.TrimSpace(title); title != "" {
		return title
	}
	return "红包参与任务"
}

func participationRunStatus(active bool, stoppedAt, endReason string) string {
	if active {
		return "running"
	}
	reason := strings.TrimSpace(endReason)
	if strings.TrimSpace(stoppedAt) != "" || strings.Contains(reason, "停止") {
		return "stopped"
	}
	if strings.Contains(reason, "失败") || strings.Contains(reason, "异常") || strings.Contains(reason, "重启") ||
		strings.Contains(reason, "拦截") || strings.Contains(reason, "失效") || strings.Contains(reason, "风控") {
		return "abnormal"
	}
	return "completed"
}

// ParticipationTaskRuns returns newest-first safe task summaries. Activities
// are the durable one-row-per-run identity; account/event records provide live
// counters and allow late draw results to update a finished task accurately.
func (s *Store) ParticipationTaskRuns() []ParticipationTaskRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := make([]ParticipationTaskRun, 0, len(s.participationRuns))
	for _, activity := range s.participationRuns {
		if activity == nil || (activity.Kind != "participation_started" && activity.Kind != "participation_task_completed" && activity.Kind != "participation_batch_executed") {
			continue
		}
		active := activity.Active
		if activity.Kind == "participation_batch_executed" {
			active = false
			for _, accountID := range activity.AccountIDs {
				taskID := activity.TaskIDs[accountID]
				if task := s.participationTasks[accountID]; task != nil && task.Active && (taskID == "" || task.ID == taskID) {
					active = true
					break
				}
			}
		}

		persisted := make(map[string]ActivityAccountSummary, len(activity.AccountSummaries))
		for _, summary := range activity.AccountSummaries {
			persisted[summary.TaskID] = summary
		}
		accountIDs := append([]string(nil), activity.AccountIDs...)
		if len(accountIDs) == 0 && strings.TrimSpace(activity.AccountID) != "" {
			accountIDs = []string{activity.AccountID}
		}
		summaries := make([]ActivityAccountSummary, 0, len(accountIDs))
		taskIDs := make([]string, 0, len(accountIDs))
		seenTasks := make(map[string]struct{}, len(accountIDs))
		for _, accountID := range accountIDs {
			taskID := strings.TrimSpace(activity.TaskIDs[accountID])
			if taskID == "" && activity.Kind != "participation_batch_executed" {
				taskID = activity.ID
			}
			if taskID == "" {
				continue
			}
			if _, exists := seenTasks[taskID]; exists {
				continue
			}
			seenTasks[taskID] = struct{}{}
			taskIDs = append(taskIDs, taskID)
			live := s.participationTaskSummaryLocked(accountID, taskID)
			summaries = append(summaries, mergeParticipationTaskSummary(persisted[taskID], live))
		}

		successes, failures, wins := 0, 0, 0
		diamonds := 0.0
		for _, summary := range summaries {
			successes += summary.JoinCount
			failures += summary.FailureCount
			wins += summary.WinCount
			diamonds += summary.WinDiamonds
		}
		if activity.JoinCount > successes {
			successes = activity.JoinCount
		}
		if activity.FailureCount > failures {
			failures = activity.FailureCount
		}
		if activity.WinCount > wins {
			wins = activity.WinCount
		}
		if activity.WinDiamonds > diamonds {
			diamonds = activity.WinDiamonds
		}

		accountCount := activity.StartedCount
		if accountCount <= 0 {
			accountCount = len(accountIDs)
		}
		endedAt := firstNonEmpty(activity.StoppedAt, activity.FinishedAt)
		if !active && endedAt == "" {
			endedAt = activity.CreatedAt
		}
		runs = append(runs, ParticipationTaskRun{
			ID: activity.ID, Mode: activity.Mode, Title: activity.Title, ModeLabel: participationRunModeLabel(activity.Mode, activity.Title),
			Status:       participationRunStatus(active, activity.StoppedAt, activity.EndReason),
			AccountCount: accountCount, SkippedCount: activity.SkippedCount,
			StartedAt: activity.CreatedAt, EndedAt: endedAt,
			SuccessCount: successes, FailureCount: failures, WinCount: wins, WinDiamonds: diamonds,
			EndReason: activity.EndReason, TaskIDs: taskIDs, AccountSummaries: summaries,
		})
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt > runs[j].StartedAt })
	if len(runs) > 1000 {
		runs = runs[:1000]
	}
	return runs
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

// purgeNoiseParticipationRecords removes failed / network_error / pending rows
// that clutter the participation log without representing a real accept or
// risk-control outcome. Returns how many rows were deleted.
func purgeNoiseParticipationRecords(records map[string]*ParticipationRecord) int {
	if len(records) == 0 {
		return 0
	}
	removed := 0
	for id, record := range records {
		if record == nil {
			delete(records, id)
			removed++
			continue
		}
		switch strings.ToLower(strings.TrimSpace(record.Status)) {
		case "failed", "network_error", "pending":
			delete(records, id)
			removed++
		}
	}
	return removed
}

// PurgeNoiseParticipationRecords is the locked runtime entry for clearing
// failed / network_error / pending participation audit rows and persisting.
func (s *Store) PurgeNoiseParticipationRecords() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := purgeNoiseParticipationRecords(s.participations)
	if removed == 0 {
		return 0, nil
	}
	if err := s.saveLocked(); err != nil {
		return 0, err
	}
	return removed, nil
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
	if strings.TrimSpace(task.UserID) != "" {
		params["user_id"] = strings.TrimSpace(task.UserID)
	}
	if strings.TrimSpace(task.SecUID) != "" {
		params["sec_user_id"] = strings.TrimSpace(task.SecUID)
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

	// Rebuild summary under the short list lock. This is O(N) integer checks only
	// (no JSON/IO). Combined with lock-free save, list_page stays responsive even
	// at 10万 monitors; the previous pain was save holding the same lock for seconds.
	s.resetMonitorSummaryLocked()
	page := MonitorPage{
		Items:   make([]Monitor, 0, len(wanted)),
		Summary: s.monitorSummary,
	}

	for id, monitor := range s.monitors {
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
		// A locally probed threshold is authoritative: a center row that omits it
		// only means the uploading client could not read it.
		existing.Condition = firstNonEmpty(existing.Condition, item.Condition)
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
		existing.AnchorID = firstNonEmpty(existing.AnchorID, item.AnchorID, s.streamerIDForRoomLocked(webRID, item.ActualRoomID))
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
	cooldownRecorder := s.cooldownRecorder
	s.mu.Unlock()
	pool, err := newAccountPoolWithConfig(credentials, poolConfig)
	if err != nil {
		return PoolStartResult{}, err
	}
	pool.setCooldownRecorder(cooldownRecorder)
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
	s.resetMonitorSummaryLocked()
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
	cooldownRecorder := s.cooldownRecorder
	s.mu.Unlock()
	if pool == nil {
		var err error
		pool, err = newAccountPoolWithConfig(credentials, poolConfig)
		if err != nil {
			return err
		}
		pool.setCooldownRecorder(cooldownRecorder)
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
			s.updatePoolWaitState(id, err)
			if !waitContext(ctx, poolRetryDelay(err)) {
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

// bulkQueueSet holds one due-ordered heap per (activity tier, source tier)
// pair. Source order is a product rule; the activity tier exists because
// due-time ordering alone cannot protect a live room once the room count
// exceeds the request budget.
type bulkQueueSet [bulkTierCount][bulkSourceCount]bulkMonitorHeap

func (s *Store) runBulkScheduler(ctx context.Context, ids []string, pool *accountPool) {
	if len(ids) == 0 {
		return
	}
	var queues bulkQueueSet
	startedAt := time.Now()
	sequence := uint64(0)
	s.mu.Lock()
	for _, id := range ids {
		// Hash staggering spreads a large first probe window, while the queues
		// still ensure a ready live room is chosen before an idle one, and a
		// ready follow room before an imported or center-library room.
		tier, rank := monitorQueueRanks(s.monitors[id])
		heap.Push(&queues[tier][rank], bulkMonitorJob{id: id, due: startedAt.Add(monitorStaggerDelay(id)), sequence: sequence})
		sequence++
	}
	s.mu.Unlock()
	for tier := range queues {
		for rank := range queues[tier] {
			heap.Init(&queues[tier][rank])
		}
	}

	jobs := make(chan bulkMonitorJob, maxProbeConcurrency)
	results := make(chan bulkMonitorResult, maxProbeConcurrency)
	retire := make(chan struct{})
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	// The worker pool follows the persisted probe window instead of a fixed
	// internal constant, so raising 原生探测窗口 takes effect on the running
	// pool rather than silently doing nothing until monitoring is restarted.
	workers := 0
	syncWorkers := func() int {
		target := int(s.bulkWorkerTarget.Load())
		if target < 1 {
			target = 1
		}
		if target > len(ids) {
			target = len(ids)
		}
		for workers < target {
			go s.runBulkWorker(workerCtx, pool, jobs, retire, results)
			workers++
		}
		for workers > target {
			select {
			case retire <- struct{}{}:
				workers--
			default:
				// Every worker is busy; shrink on a later pass rather than
				// interrupting an in-flight probe.
				return workers
			}
		}
		return workers
	}

	inflight := 0
	var sourceBurst [bulkTierCount]int
	liveBurst := 0
	for {
		if ctx.Err() != nil {
			return
		}
		workerCount := syncWorkers()
		now := time.Now()
		for inflight < workerCount {
			tier, rank, ok := nextReadyBulkQueue(&queues, now, &sourceBurst, liveBurst)
			if !ok {
				break
			}
			job := heap.Pop(&queues[tier][rank]).(bulkMonitorJob)
			select {
			case jobs <- job:
				inflight++
				if rank == 0 {
					sourceBurst[tier]++
				} else {
					sourceBurst[tier] = 0
				}
				if tier == bulkTierLive {
					liveBurst++
				} else {
					liveBurst = 0
				}
			case <-ctx.Done():
				return
			}
		}

		if inflight == 0 {
			due, ok := nextBulkDue(&queues)
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
			if due, ok := nextBulkDue(&queues); ok {
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
				tier, rank := s.monitorQueueRanksForID(result.job.id)
				heap.Push(&queues[tier][rank], bulkMonitorJob{
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

// nextReadySourceQueue applies the 关注列表 > 导入 > 中心库 order within one
// activity tier.
func nextReadySourceQueue(tier *[bulkSourceCount]bulkMonitorHeap, now time.Time, burst int) (int, bool) {
	ready := func(rank int) bool {
		return tier[rank].Len() > 0 && !tier[rank][0].due.After(now)
	}
	if ready(0) {
		if burst >= bulkPriorityBurst {
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

// nextReadyBulkQueue prefers a ready live room over any idle room, then applies
// the source order inside the chosen tier. bulkLiveBurst bounds the preference
// so idle rooms keep getting probed and newly started rooms are still found.
func nextReadyBulkQueue(queues *bulkQueueSet, now time.Time, sourceBurst *[bulkTierCount]int, liveBurst int) (int, int, bool) {
	liveRank, liveReady := nextReadySourceQueue(&queues[bulkTierLive], now, sourceBurst[bulkTierLive])
	idleRank, idleReady := nextReadySourceQueue(&queues[bulkTierIdle], now, sourceBurst[bulkTierIdle])
	if liveReady && !(liveBurst >= bulkLiveBurst && idleReady) {
		return bulkTierLive, liveRank, true
	}
	if idleReady {
		return bulkTierIdle, idleRank, true
	}
	if liveReady {
		return bulkTierLive, liveRank, true
	}
	return 0, 0, false
}

func nextBulkDue(queues *bulkQueueSet) (time.Time, bool) {
	var earliest time.Time
	found := false
	for tier := range queues {
		for rank := range queues[tier] {
			if queues[tier][rank].Len() == 0 {
				continue
			}
			candidate := queues[tier][rank][0].due
			if !found || candidate.Before(earliest) {
				earliest, found = candidate, true
			}
		}
	}
	return earliest, found
}

func (s *Store) runBulkWorker(ctx context.Context, pool *accountPool, jobs <-chan bulkMonitorJob, retire <-chan struct{}, results chan<- bulkMonitorResult) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-retire:
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
		return poolRetryDelay(err), true
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
		// A cooling pool is not this room's fault, so it must not widen this
		// room's own ladder; the pool's retry hint already paces that case.
		if errors.Is(probeErr, errMonitoringAccountCooling) {
			s.scheduleSaveLocked()
			s.mu.Unlock()
			return unknownProbeInterval
		}
		monitor.ProbeBackoffStreak++
		next := probeBackoffInterval(unknownProbeInterval, maxErrorProbeInterval, monitor.ProbeBackoffStreak)
		s.scheduleSaveLocked()
		s.mu.Unlock()
		return next
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
	if probe.StreamerID != "" {
		monitor.StreamerID = probe.StreamerID
	}
	liveResultHandler := s.liveResultHandler
	roomID := monitor.RoomID
	if monitor.LiveStatus != "live" {
		status := monitor.LiveStatus
		next := offlineProbeInterval
		if status == "offline" {
			monitor.ProbeBackoffStreak = 0
		} else {
			monitor.ProbeBackoffStreak++
			next = probeBackoffInterval(unknownProbeInterval, maxUnknownProbeInterval, monitor.ProbeBackoffStreak)
		}
		s.scheduleSaveLocked()
		s.mu.Unlock()
		if liveResultHandler != nil && status == "offline" {
			liveResultHandler(roomID, status, time.Now())
		}
		return next
	}
	monitor.ProbeBackoffStreak = 0
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
			existing.Condition = firstNonEmpty(packet.condition, existing.Condition)
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
			existing.AnchorID = firstNonEmpty(packet.anchorID, existing.AnchorID, monitor.StreamerID)
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
			Condition: packet.condition,
			Source:    snapshot.Source, DetectedAt: now,
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
			AnchorID: firstNonEmpty(packet.anchorID, monitor.StreamerID),
			BoxType:  packet.boxType,
			SendTime: packet.sendTime, DelayTime: packet.delayTime,
		}
		s.events[eventID] = event
		newEvents = append(newEvents, *event)
		monitor.PacketCount++
		monitor.LastEventAt, monitor.LastPacketID, monitor.LastPacketTitle = now, packetID, packet.title
		monitor.LastParticipantCount = packet.participants
	}
	// Ensure anchor is filled before dispatch for participation join.
	for i := range newEvents {
		if newEvents[i].AnchorID == "" {
			newEvents[i].AnchorID = firstNonEmpty(
				newEvents[i].AnchorID,
				s.streamerIDForRoomLocked(newEvents[i].WebRID, newEvents[i].ActualRoomID),
			)
			if event := s.events[newEvents[i].ID]; event != nil && event.AnchorID == "" {
				event.AnchorID = newEvents[i].AnchorID
			}
		}
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

// streamerIDForRoomLocked returns a native owner uid previously learned from a
// live enter probe for the same web_rid / actual room. Used when center or
// luckybox rows omit anchor_id.
func (s *Store) streamerIDForRoomLocked(webRID, actualRoomID string) string {
	webRID = strings.TrimSpace(webRID)
	actualRoomID = strings.TrimSpace(actualRoomID)
	for _, monitor := range s.monitors {
		if monitor == nil || strings.TrimSpace(monitor.StreamerID) == "" {
			continue
		}
		if webRID != "" && (monitor.WebRID == webRID || monitor.RoomID == webRID) {
			return monitor.StreamerID
		}
		if actualRoomID != "" && monitor.ActualRoomID == actualRoomID {
			return monitor.StreamerID
		}
	}
	return ""
}

// EnrichParticipationEvent fills native join identifiers that may arrive after
// the event was first discovered (center rows without anchor, later enter
// probe, etc.). Safe for concurrent use; never returns credentials.
func (s *Store) EnrichParticipationEvent(event Event) Event {
	if s == nil {
		return event
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored := s.events[event.ID]; stored != nil {
		event.ActualRoomID = firstNonEmpty(event.ActualRoomID, stored.ActualRoomID)
		event.JoinBoxID = firstNonEmpty(event.JoinBoxID, stored.JoinBoxID)
		event.AnchorID = firstNonEmpty(event.AnchorID, stored.AnchorID)
		event.BoxType = firstNonEmpty(event.BoxType, stored.BoxType)
		event.SendTime = firstNonEmpty(event.SendTime, stored.SendTime)
		event.DelayTime = firstNonEmpty(event.DelayTime, stored.DelayTime)
		event.WebRID = firstNonEmpty(event.WebRID, stored.WebRID)
	}
	// Prefer the running local monitor's current enter-probe ActualRoomID over a
	// stale event/center value. Joining with a previous live-session room_id is a
	// common succeed=false soft-deny while the web_rid page is still live.
	webRID := strings.TrimSpace(firstNonEmpty(event.WebRID, event.RoomID))
	if webRID != "" {
		for _, monitor := range s.monitors {
			if monitor == nil {
				continue
			}
			if monitor.WebRID != webRID && monitor.RoomID != webRID {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(monitor.LiveStatus), "live") &&
				validLuckyboxID(monitor.ActualRoomID) {
				event.ActualRoomID = monitor.ActualRoomID
				if stored := s.events[event.ID]; stored != nil && stored.ActualRoomID != monitor.ActualRoomID {
					stored.ActualRoomID = monitor.ActualRoomID
					s.scheduleSaveLocked()
				}
			}
			if strings.TrimSpace(event.AnchorID) == "" && strings.TrimSpace(monitor.StreamerID) != "" {
				event.AnchorID = strings.TrimSpace(monitor.StreamerID)
			}
			break
		}
	}
	if strings.TrimSpace(event.AnchorID) == "" {
		event.AnchorID = s.streamerIDForRoomLocked(event.WebRID, event.ActualRoomID)
		if stored := s.events[event.ID]; stored != nil && event.AnchorID != "" && stored.AnchorID == "" {
			stored.AnchorID = event.AnchorID
			s.scheduleSaveLocked()
		}
	}
	return event
}

func (s *Store) acquireProbeSlot(ctx context.Context) (func(), bool) {
	s.probeMu.RLock()
	probeSlots := s.probeSlots
	s.probeMu.RUnlock()
	select {
	case probeSlots <- struct{}{}:
		return func() { <-probeSlots }, true
	case <-ctx.Done():
		return nil, false
	}
}

// setProbeWindow resizes the probe semaphore and the bulk worker target
// together. A previously captured channel stays valid so an already-acquired
// slot is always released back into the channel it came from.
func (s *Store) setProbeWindow(size int) {
	if size < 1 {
		size = 1
	}
	s.probeMu.Lock()
	s.probeSlots = make(chan struct{}, size)
	s.probeMu.Unlock()
	s.bulkWorkerTarget.Store(int64(size))
}

// probeBackoffInterval doubles base for each consecutive inconclusive probe and
// never exceeds ceiling.
func probeBackoffInterval(base, ceiling time.Duration, streak int) time.Duration {
	if streak < 2 {
		return base
	}
	if streak > maxProbeBackoffStreak {
		streak = maxProbeBackoffStreak
	}
	interval := base
	for i := 1; i < streak && interval < ceiling; i++ {
		interval *= 2
	}
	if interval > ceiling {
		return ceiling
	}
	return interval
}

// poolRetryDelay honours the pool's own retry hint instead of retrying every
// room once per second. With every account cooling, a flat one-second retry
// turns the whole room set into a mutex-bound spin that performs no network
// work at all.
func poolRetryDelay(err error) time.Duration {
	next := time.Second
	var unavailable *poolUnavailableError
	if errors.As(err, &unavailable) && unavailable.retryAfter > next {
		next = unavailable.retryAfter
		if next > 30*time.Second {
			next = 30 * time.Second
		}
	}
	return next
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

// monitorLiveTier separates rooms that are confirmed to be broadcasting right
// now from everything else. Only a positive live probe earns the fast tier;
// unknown and error results stay with the idle rooms so a large import of dead
// rooms can never dilute it.
func monitorLiveTier(monitor *Monitor) int {
	if monitor != nil && monitor.LiveStatus == "live" {
		return bulkTierLive
	}
	return bulkTierIdle
}

func monitorQueueRanks(monitor *Monitor) (int, int) {
	return monitorLiveTier(monitor), monitorSourcePriority(monitor)
}

func (s *Store) monitorQueueRanksForID(id string) (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return monitorQueueRanks(s.monitors[id])
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
	condition                                  string
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
	// Prefer explicit anchor/owner identity fields (DY-KIRO's meta extractor).
	// Real join success traces always include numeric anchor_id in both query
	// and JSON body. Never fall back to bare id_str which collides with box/room.
	meta.anchorID = extractAnchorID(data, pairs, meta.boxID)
	meta.boxType = firstPairValue(pairs, "box_type", "boxType")
	meta.sendTime = firstPairValue(pairs, "send_time", "sendTime", "start_time", "startTime")
	meta.delayTime = firstPairValue(pairs, "delay_time", "delayTime", "duration", "duration_s")
	meta.title = firstPairValue(pairs, "title", "display_name", "displayName", "name", "activity_name", "activityName")
	meta.prize = formatPacketPrize(pairs)
	meta.condition = extractRedPacketCondition(data)
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

// extractAnchorID prefers explicit anchor/owner user ids from the luckybox
// payload. Nested owner maps are walked before bare id_str pairs so box/room
// identifiers cannot steal the anchor slot.
func extractAnchorID(data map[string]any, pairs []pair, boxID string) string {
	// Explicit anchor_* keys win even when non-numeric (tests / legacy rows).
	for _, key := range []string{
		"anchor_id", "anchorId", "anchor_user_id", "anchorUserId",
		"owner_user_id", "ownerUserId", "owner_id", "ownerId",
	} {
		if value := strings.TrimSpace(firstPairValue(pairs, key)); value != "" && value != boxID {
			return value
		}
	}
	return walkMapAnchorID(data, 0, boxID)
}

func walkMapAnchorID(value any, depth int, boxID string) string {
	if depth > 6 || value == nil {
		return ""
	}
	switch item := value.(type) {
	case map[string]any:
		for _, key := range []string{"anchor_id", "anchorId", "anchor_user_id", "anchorUserId", "owner_user_id", "ownerUserId"} {
			if candidate := validAnchorOrEmpty(item[key], boxID); candidate != "" {
				return candidate
			}
		}
		for _, nestKey := range []string{"owner", "anchor", "author", "user", "room_owner", "roomOwner"} {
			if nest, ok := item[nestKey].(map[string]any); ok {
				for _, key := range []string{"id_str", "idStr", "uid", "user_id", "userId", "id"} {
					if candidate := validAnchorOrEmpty(nest[key], boxID); candidate != "" {
						return candidate
					}
				}
			}
		}
		for _, child := range item {
			if found := walkMapAnchorID(child, depth+1, boxID); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range item {
			if found := walkMapAnchorID(child, depth+1, boxID); found != "" {
				return found
			}
		}
	}
	return ""
}

func validAnchorOrEmpty(value any, boxID string) string {
	text := strings.TrimSpace(scalarString(value))
	if !validAnchorUserID(text) || text == boxID {
		return ""
	}
	return text
}

// validAnchorUserID accepts Douyin numeric user ids (typically 6+ digits).
// Shorter values are almost always placeholder noise.
func validAnchorUserID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 6 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
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
