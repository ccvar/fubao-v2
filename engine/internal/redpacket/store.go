// Package redpacket contains the red-packet-only live-room monitor.  It uses
// the same signed lottery_info request path as 福宝, but deliberately filters
// out 福袋/lottery payloads before they reach the UI or event history.
package redpacket

import (
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
	storeVersion         = 1
	unknownProbeInterval = 10 * time.Second
	offlineProbeInterval = 60 * time.Second
	livePacketInterval   = 15 * time.Second
	activePacketInterval = 5 * time.Second
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
	ID               string `json:"id"`
	MonitorID        string `json:"monitor_id"`
	AccountID        string `json:"account_id,omitempty"`
	AccountName      string `json:"account_name,omitempty"`
	RoomID           string `json:"room_id"`
	RoomName         string `json:"room_name,omitempty"`
	StreamerName     string `json:"streamer_name,omitempty"`
	WebRID           string `json:"web_rid,omitempty"`
	PacketID         string `json:"packet_id"`
	Title            string `json:"title,omitempty"`
	Prize            string `json:"prize,omitempty"`
	Source           string `json:"source"`
	DetectedAt       string `json:"detected_at"`
	DrawAt           string `json:"draw_at,omitempty"`
	ExpiresAt        string `json:"expires_at,omitempty"`
	ParticipantCount int    `json:"participant_count,omitempty"`
}

type file struct {
	Version  int        `json:"version"`
	Monitors []*Monitor `json:"monitors"`
	Events   []*Event   `json:"events"`
}

type Store struct {
	mu              sync.Mutex
	path            string
	monitors        map[string]*Monitor
	events          map[string]*Event
	runtime         map[string]context.CancelFunc
	requestRecorder func(accountID string, requestErr error)
}

type monitorJob struct {
	id           string
	ctx          context.Context
	cancel       context.CancelFunc
	source       monitorSource
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
		path:     filepath.Join(dataDir, "red_packet_monitors.json"),
		monitors: map[string]*Monitor{},
		events:   map[string]*Event{},
		runtime:  map[string]context.CancelFunc{},
	}
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
			s.events[event.ID] = event
		}
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
	for _, item := range s.events {
		copy := *item
		events = append(events, &copy)
	}
	sort.Slice(monitors, func(i, j int) bool { return monitors[i].Name < monitors[j].Name })
	sort.Slice(events, func(i, j int) bool { return events[i].DetectedAt > events[j].DetectedAt })
	payload, err := json.MarshalIndent(file{Version: storeVersion, Monitors: monitors, Events: events}, "", "  ")
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
		if monitor.RoomID != room.ID || monitor.WebRID != room.WebRID || monitor.ActualRoomID != room.ActualRoomID || monitor.Name != room.Name || monitor.StreamerName != room.StreamerName || monitor.Enabled != room.Enabled {
			monitor.RoomID = room.ID
			monitor.WebRID = room.WebRID
			monitor.ActualRoomID = room.ActualRoomID
			monitor.Name = room.Name
			monitor.StreamerName = room.StreamerName
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
	return s.saveLocked()
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

// StartAll starts every enabled room monitor with one selected monitoring
// account. The account credential is kept inside the Go engine.
func (s *Store) StartAll(accountID, accountName, cookie string) (int, error) {
	if strings.TrimSpace(cookie) == "" {
		return 0, errors.New("监测账号没有可用 Cookie")
	}
	s.mu.Lock()
	now := time.Now().Format(time.RFC3339Nano)
	jobs := make([]monitorJob, 0, len(s.monitors))
	for _, monitor := range s.monitors {
		if !monitor.Enabled {
			continue
		}
		if _, running := s.runtime[monitor.ID]; running {
			jobs = append(jobs, monitorJob{id: monitor.ID})
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.runtime[monitor.ID] = cancel
		monitor.AccountID, monitor.AccountName = accountID, accountName
		monitor.Status, monitor.ConnectionStatus, monitor.LastError = "running", "connecting", ""
		monitor.UpdatedAt = now
		roomID := firstNonEmpty(monitor.WebRID, monitor.RoomID)
		actualRoomID := firstNonEmpty(monitor.ActualRoomID, monitor.RoomID)
		jobs = append(jobs, monitorJob{id: monitor.ID, ctx: ctx, cancel: cancel, source: newSource(httpclient.New(httpclient.WithCookie(cookie), httpclient.WithRoomURL("https://live.douyin.com/"+roomID)), roomID, actualRoomID), initialDelay: monitorStaggerDelay(monitor.ID)})
	}
	if err := s.saveLocked(); err != nil {
		for _, job := range jobs {
			if job.cancel != nil {
				job.cancel()
				delete(s.runtime, job.id)
			}
		}
		s.mu.Unlock()
		return 0, err
	}
	s.mu.Unlock()
	for _, job := range jobs {
		if job.ctx != nil && job.source != nil {
			go s.run(job.ctx, job.id, job.source, job.initialDelay)
		}
	}
	return len(jobs), nil
}

// StopAll stops every room monitor. Persisted event history is retained.
func (s *Store) StopAll() (int, error) {
	s.mu.Lock()
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
	err := s.saveLocked()
	s.mu.Unlock()
	return stopped, err
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
	monitor.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	if err := s.saveLocked(); err != nil {
		delete(s.runtime, id)
		cancel()
		s.mu.Unlock()
		return err
	}
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
	monitor.Status, monitor.ConnectionStatus = "stopped", "disconnected"
	monitor.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	return s.saveLocked()
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
	delete(s.monitors, id)
	for eventID, event := range s.events {
		if event.MonitorID == id {
			delete(s.events, eventID)
		}
	}
	return s.saveLocked()
}

func (s *Store) run(ctx context.Context, id string, source monitorSource, initialDelay time.Duration) {
	defer func() {
		s.mu.Lock()
		if _, ok := s.runtime[id]; ok {
			delete(s.runtime, id)
			if monitor := s.monitors[id]; monitor != nil && monitor.Status == "running" {
				monitor.Status, monitor.ConnectionStatus = "stopped", "disconnected"
				monitor.UpdatedAt = time.Now().Format(time.RFC3339Nano)
				_ = s.saveLocked()
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
		next := s.pollOnce(ctx, id, source)
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
	if ctx.Err() == nil && recorder != nil && accountID != "" {
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
		_ = s.saveLocked()
		s.mu.Unlock()
		return unknownProbeInterval
	}
	monitor.LiveStatus = firstNonEmpty(probe.Status, "unknown")
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
	if monitor.LiveStatus != "live" {
		_ = s.saveLocked()
		status := monitor.LiveStatus
		s.mu.Unlock()
		if status == "offline" {
			return offlineProbeInterval
		}
		return unknownProbeInterval
	}
	s.mu.Unlock()

	snapshots, err := source.Fetch(ctx)
	if ctx.Err() == nil && recorder != nil && accountID != "" {
		recorder(accountID, err)
	}
	if ctx.Err() != nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return 0
	}
	monitor = s.monitors[id]
	if monitor == nil {
		return 0
	}
	now = time.Now().Format(time.RFC3339Nano)
	monitor.LastRedPacketCheckedAt, monitor.LastCheckedAt, monitor.UpdatedAt = now, now, now
	if err != nil {
		monitor.ConnectionStatus, monitor.LastError = "error", err.Error()
		_ = s.saveLocked()
		return livePacketInterval
	}
	monitor.ConnectionStatus, monitor.LastError = "connected", ""
	for _, snapshot := range snapshots {
		packet, ok := extractRedPacket(snapshot.Data)
		if !ok {
			continue
		}
		packetID := firstNonEmpty(packet.id, stableID(snapshot.Data))
		eventID := id + ":" + packetID
		if existing := s.events[eventID]; existing != nil {
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
		}
		s.events[eventID] = event
		monitor.PacketCount++
		monitor.LastEventAt, monitor.LastPacketID, monitor.LastPacketTitle = now, packetID, packet.title
		monitor.LastParticipantCount = packet.participants
	}
	_ = s.saveLocked()
	if len(snapshots) > 0 {
		return activePacketInterval
	}
	return livePacketInterval
}

func monitorStaggerDelay(id string) time.Duration {
	sum := sha256.Sum256([]byte(id))
	bucket := int(sum[0])<<8 | int(sum[1])
	return time.Duration(bucket%int(unknownProbeInterval/time.Millisecond)) * time.Millisecond
}

type packetMeta struct {
	id, title, prize, drawAt, expiresAt string
	participants                        int
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
	meta.id = firstPairValue(pairs, "red_packet_id", "redPacketId", "redpacket_id", "activity_id", "activityId", "lottery_id_str", "lotteryIdStr", "box_id_str", "boxIdStr", "id")
	meta.title = firstPairValue(pairs, "title", "display_name", "displayName", "name", "activity_name", "activityName")
	meta.prize = formatPacketPrize(pairs)
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
