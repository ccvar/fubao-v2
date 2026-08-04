package rooms

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const storeVersion = 7

const (
	defaultAutoRecycleOfflineDays      = 7
	defaultParticipationPrewarmMinutes = 10
	maximumParticipationPrewarmMinutes = 24 * 60
)

type Settings struct {
	AutoRecycleOfflineDays      int  `json:"auto_recycle_offline_days"`
	ParticipationPrewarmMinutes int  `json:"participation_prewarm_minutes"`
	AutoRecycleLowLiveEnabled   bool `json:"auto_recycle_low_live_enabled"`
	AutoRecycleMaxLiveSessions  int  `json:"auto_recycle_max_live_sessions"`
	AutoRecycleNoPacketEnabled  bool `json:"auto_recycle_no_packet_enabled"`
	AutoRecycleNoPacketDays     int  `json:"auto_recycle_no_packet_days"`
	// AutoRecycleImportedNoPacketEnabled is an explicit manual-cleanup rule.
	// It applies only to locally imported rooms that have never produced a
	// red-packet event, including records that have not completed a probe yet.
	AutoRecycleImportedNoPacketEnabled bool `json:"auto_recycle_imported_no_packet_enabled"`
}

type ListPage struct {
	Items []Room `json:"items"`
	Total int    `json:"total"`
}

type CleanupProgress struct {
	Total      int    `json:"total"`
	Scanned    int    `json:"scanned"`
	Cleaned    int    `json:"cleaned"`
	Recycled   int    `json:"recycled"`
	Excluded   int    `json:"excluded"`
	Skipped    int    `json:"skipped"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// FollowSource records which participation account reported a broadcaster as
// currently live. It contains safe attribution metadata only; credentials
// remain inside the account store.
type FollowSource struct {
	AccountID      string `json:"account_id"`
	AccountName    string `json:"account_name"`
	LastSeenLiveAt string `json:"last_seen_live_at"`
	IsLive         bool   `json:"is_live"`
}

// FollowingLiveRoom is the safe account-scoped live-room snapshot accepted by
// SyncFollowingLive. Keeping this type local avoids coupling the canonical
// room store to a particular Douyin transport implementation.
type FollowingLiveRoom struct {
	RoomID       string
	WebRID       string
	Title        string
	StreamerName string
}

type CenterRoom struct {
	WebRID           string
	ActualRoomID     string
	Title            string
	StreamerName     string
	LiveStatus       string
	LiveStartedAt    string
	LastSeenLiveAt   string
	LastEventAt      string
	MetricsVersion   int
	LiveSessionCount int
	RedPacketCount   int
	CenterUpdatedAt  string
}

// CenterExclusion is a safe persistent tombstone for a room removed from the
// shared center library. It prevents a later center snapshot from silently
// recreating known junk while keeping an explicit recovery path for mistakes.
type CenterExclusion struct {
	ID           string `json:"id"`
	WebRID       string `json:"web_rid"`
	ActualRoomID string `json:"actual_room_id,omitempty"`
	Name         string `json:"name,omitempty"`
	StreamerName string `json:"streamer_name,omitempty"`
	Reason       string `json:"reason,omitempty"`
	ExcludedAt   string `json:"excluded_at"`
	SyncPending  bool   `json:"sync_pending,omitempty"`
}

type Room struct {
	ID                      string         `json:"id"`
	WebRID                  string         `json:"web_rid,omitempty"`
	ActualRoomID            string         `json:"actual_room_id,omitempty"`
	Name                    string         `json:"name,omitempty"`
	StreamerName            string         `json:"streamer_name,omitempty"`
	MonitorStatus           string         `json:"monitor_status"`
	ConnectionStatus        string         `json:"connection_status"`
	Enabled                 bool           `json:"enabled"`
	Source                  string         `json:"source,omitempty"`
	FollowSources           []FollowSource `json:"follow_sources,omitempty"`
	FollowingLive           bool           `json:"following_live,omitempty"`
	LastSeenLiveAt          string         `json:"last_seen_live_at,omitempty"`
	CenterLiveStatus        string         `json:"center_live_status,omitempty"`
	CenterLiveAt            string         `json:"center_live_at,omitempty"`
	CenterLastEventAt       string         `json:"center_last_event_at,omitempty"`
	CenterMetricsVersion    int            `json:"center_metrics_version,omitempty"`
	CenterLiveSessionCount  int            `json:"center_live_session_count,omitempty"`
	CenterRedPacketCount    int            `json:"center_red_packet_count,omitempty"`
	CenterLinked            bool           `json:"center_linked,omitempty"`
	Recycled                bool           `json:"recycled,omitempty"`
	RecycledAt              string         `json:"recycled_at,omitempty"`
	RecycleReason           string         `json:"recycle_reason,omitempty"`
	OfflineDays             int            `json:"offline_days,omitempty"`
	LastOfflineDay          string         `json:"last_offline_day,omitempty"`
	HasDefinitiveProbe      bool           `json:"has_definitive_probe,omitempty"`
	FirstDefinitiveProbeAt  string         `json:"first_definitive_probe_at,omitempty"`
	LastDefinitiveProbeAt   string         `json:"last_definitive_probe_at,omitempty"`
	LastDefinitiveLiveState string         `json:"last_definitive_live_state,omitempty"`
	LiveSessionCount        int            `json:"live_session_count,omitempty"`
	LastRedPacketAt         string         `json:"last_red_packet_at,omitempty"`
	CreatedAt               string         `json:"created_at"`
	UpdatedAt               string         `json:"updated_at"`
}

// MergeCenter adds safe room metadata learned from the shared center. A room
// that already exists locally keeps its original source; only rows first
// learned from the server are labeled as center data.
func (s *Store) MergeCenter(items []CenterRoom) (MigrationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := MigrationResult{}
	changed := false
	for _, item := range items {
		webRID := strings.TrimSpace(item.WebRID)
		if !validRoomID(webRID) {
			result.Invalid++
			continue
		}
		if s.centerRoomExcludedLocked(webRID, strings.TrimSpace(item.ActualRoomID)) {
			result.Excluded++
			continue
		}
		var room *Room
		for _, candidate := range s.rooms {
			if candidate.ID == webRID || candidate.WebRID == webRID || (item.ActualRoomID != "" && candidate.ActualRoomID == item.ActualRoomID) {
				room = candidate
				break
			}
		}
		if room == nil {
			createdAt := firstNonEmpty(item.CenterUpdatedAt, time.Now().Format(time.RFC3339Nano))
			room = &Room{
				ID: webRID, WebRID: webRID, ActualRoomID: strings.TrimSpace(item.ActualRoomID),
				Name: strings.TrimSpace(item.Title), StreamerName: strings.TrimSpace(item.StreamerName),
				MonitorStatus: "stopped", ConnectionStatus: "disconnected", Enabled: true,
				Source: "center", CreatedAt: createdAt, UpdatedAt: createdAt,
				CenterLinked: true,
			}
			s.rooms[room.ID] = room
			result.Imported++
			changed = true
		} else {
			result.Merged++
			if !room.CenterLinked {
				room.CenterLinked = true
				changed = true
			}
		}
		if value := strings.TrimSpace(item.ActualRoomID); value != "" && room.ActualRoomID == "" {
			room.ActualRoomID = value
			changed = true
		}
		if value := strings.TrimSpace(item.Title); value != "" && (room.Name == "" || room.Source == "center") && room.Name != value {
			room.Name = value
			changed = true
		}
		if value := strings.TrimSpace(item.StreamerName); value != "" && (room.StreamerName == "" || room.Source == "center") && room.StreamerName != value {
			room.StreamerName = value
			changed = true
		}
		if value := strings.TrimSpace(item.LastEventAt); value != "" && value > room.LastRedPacketAt {
			room.LastRedPacketAt = value
			changed = true
		}
		if item.MetricsVersion > 0 && (room.CenterMetricsVersion != item.MetricsVersion || room.CenterLiveSessionCount != item.LiveSessionCount || room.CenterRedPacketCount != item.RedPacketCount) {
			room.CenterMetricsVersion = item.MetricsVersion
			room.CenterLiveSessionCount = max(0, item.LiveSessionCount)
			room.CenterRedPacketCount = max(0, item.RedPacketCount)
			changed = true
		}
		centerLiveAt := firstNonEmpty(item.LastSeenLiveAt, item.LiveStartedAt, item.CenterUpdatedAt)
		if room.CenterLiveStatus != item.LiveStatus || room.CenterLiveAt != centerLiveAt || room.CenterLastEventAt != item.LastEventAt {
			room.CenterLiveStatus = strings.TrimSpace(item.LiveStatus)
			room.CenterLiveAt = strings.TrimSpace(centerLiveAt)
			room.CenterLastEventAt = strings.TrimSpace(item.LastEventAt)
			changed = true
		}
		if room.Source == "center" && item.CenterUpdatedAt > room.UpdatedAt {
			room.UpdatedAt = item.CenterUpdatedAt
			changed = true
		}
	}
	result.Total = len(s.rooms)
	if !changed {
		return result, nil
	}
	if err := s.saveLocked(); err != nil {
		return MigrationResult{}, err
	}
	return result, nil
}

type MigrationResult struct {
	Imported int `json:"imported"`
	Merged   int `json:"merged"`
	Total    int `json:"total"`
	Invalid  int `json:"invalid,omitempty"`
	Excluded int `json:"excluded,omitempty"`
}

type roomFile struct {
	Version          int                `json:"version"`
	Settings         Settings           `json:"settings"`
	Rooms            []*Room            `json:"rooms"`
	CenterExclusions []*CenterExclusion `json:"center_exclusions,omitempty"`
}

type legacyRoom struct {
	RoomID           string `json:"room_id"`
	WebRID           string `json:"web_rid"`
	ActualRoomID     string `json:"actual_room_id"`
	RoomName         string `json:"room_name"`
	StreamerName     string `json:"streamer_name"`
	MonitorStatus    string `json:"monitor_status"`
	ConnectionStatus string `json:"connection_status"`
	Enabled          *bool  `json:"enabled"`
}

type Store struct {
	mu               sync.Mutex
	path             string
	rooms            map[string]*Room
	centerExclusions map[string]*CenterExclusion
	settings         Settings
	persistDirty     bool
	persistScheduled bool
}

func NewStore(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("直播间数据目录为空")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建直播间数据目录失败: %w", err)
	}
	store := &Store{
		path:             filepath.Join(dataDir, "rooms.json"),
		rooms:            map[string]*Room{},
		centerExclusions: map[string]*CenterExclusion{},
		settings:         defaultSettings(),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取直播间数据失败: %w", err)
	}
	var file roomFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("解析直播间数据失败: %w", err)
	}
	if file.Version >= 6 {
		s.settings = normalizeSettings(file.Settings)
	} else if file.Version >= 5 {
		// Existing installations opt in to the new cleanup rules explicitly;
		// retaining the values without enabling them avoids a surprise bulk
		// cleanup immediately after upgrading.
		s.settings = normalizeSettings(Settings{
			AutoRecycleOfflineDays:      file.Settings.AutoRecycleOfflineDays,
			ParticipationPrewarmMinutes: file.Settings.ParticipationPrewarmMinutes,
			AutoRecycleMaxLiveSessions:  0,
			AutoRecycleNoPacketDays:     3,
		})
	} else if file.Version >= 4 {
		// Version 4 already persisted the automatic-recycle value. Preserve it
		// while introducing the participation-monitor prewarm default.
		s.settings = normalizeSettings(Settings{
			AutoRecycleOfflineDays:      file.Settings.AutoRecycleOfflineDays,
			ParticipationPrewarmMinutes: defaultParticipationPrewarmMinutes,
		})
	} else {
		s.settings = defaultSettings()
	}
	removedInvalid := false
	for _, room := range file.Rooms {
		if room == nil || strings.TrimSpace(room.ID) == "" {
			removedInvalid = true
			continue
		}
		// A Douyin actual-room ID alone is not enough to revisit the public
		// live room. Canonical room rows must have a valid public WebRID.
		if !validRoomID(strings.TrimSpace(room.WebRID)) {
			removedInvalid = true
			continue
		}
		if strings.EqualFold(strings.TrimSpace(room.Source), "center") {
			room.CenterLinked = true
		}
		s.rooms[room.ID] = room
	}
	for _, exclusion := range file.CenterExclusions {
		if exclusion == nil || !validRoomID(strings.TrimSpace(exclusion.WebRID)) {
			removedInvalid = true
			continue
		}
		copy := *exclusion
		copy.ID = strings.TrimSpace(firstNonEmpty(copy.ID, copy.WebRID))
		copy.WebRID = strings.TrimSpace(copy.WebRID)
		s.centerExclusions[copy.ID] = &copy
	}
	if removedInvalid {
		return s.saveLocked()
	}
	return nil
}

func (s *Store) saveLocked() error {
	items := make([]*Room, 0, len(s.rooms))
	for _, room := range s.rooms {
		items = append(items, room)
	}
	sortRooms(items)
	exclusions := make([]*CenterExclusion, 0, len(s.centerExclusions))
	for _, exclusion := range s.centerExclusions {
		exclusions = append(exclusions, exclusion)
	}
	sort.Slice(exclusions, func(i, j int) bool {
		if exclusions[i].ExcludedAt != exclusions[j].ExcludedAt {
			return exclusions[i].ExcludedAt > exclusions[j].ExcludedAt
		}
		return exclusions[i].ID < exclusions[j].ID
	})
	payload, err := json.Marshal(roomFile{Version: storeVersion, Settings: s.settings, Rooms: items, CenterExclusions: exclusions})
	if err != nil {
		return fmt.Errorf("序列化直播间数据失败: %w", err)
	}
	tempPath := s.path + ".tmp"
	if err := os.WriteFile(tempPath, payload, 0o600); err != nil {
		return fmt.Errorf("写入直播间临时文件失败: %w", err)
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("设置直播间文件权限失败: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("保存直播间数据失败: %w", err)
	}
	return nil
}

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
		if s.persistDirty {
			s.scheduleSaveLocked()
		}
		s.mu.Unlock()
	}()
}

func (s *Store) List() []Room {
	return s.list(false)
}

func (s *Store) CountAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rooms)
}

func (s *Store) Page(offset, limit int, query string) ListPage {
	return s.PageBySource(offset, limit, query, "")
}

// PageBySource returns a bounded page of active rooms filtered by their
// canonical acquisition source. The filtering happens before pagination so
// high-volume room lists never produce partial frontend-only results.
func (s *Store) PageBySource(offset, limit int, query, source string) ListPage {
	if offset < 0 {
		offset = 0
	}
	if limit < 1 {
		limit = 300
	}
	if limit > 3000 {
		limit = 3000
	}
	query = strings.ToLower(strings.TrimSpace(query))
	source = normalizePageSource(source)
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Room, 0, minInt(len(s.rooms), limit*2))
	for _, room := range s.rooms {
		if room.Recycled {
			continue
		}
		if source != "" && roomPageSource(room) != source {
			continue
		}
		if query != "" {
			haystack := strings.ToLower(strings.Join([]string{room.Name, room.StreamerName, room.WebRID, room.ActualRoomID, room.ID}, " "))
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		copy := *room
		copy.FollowSources = append([]FollowSource(nil), room.FollowSources...)
		items = append(items, copy)
	}
	sort.Slice(items, func(i, j int) bool { return roomSortKey(&items[i]) < roomSortKey(&items[j]) })
	total := len(items)
	if offset >= total {
		return ListPage{Items: []Room{}, Total: total}
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return ListPage{Items: items[offset:end], Total: total}
}

func normalizePageSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "following", "imported", "center":
		return strings.ToLower(strings.TrimSpace(source))
	default:
		return ""
	}
}

func roomPageSource(room *Room) string {
	if room == nil {
		return "imported"
	}
	if len(room.FollowSources) > 0 || strings.EqualFold(strings.TrimSpace(room.Source), "following-live") {
		return "following"
	}
	if strings.EqualFold(strings.TrimSpace(room.Source), "center") {
		return "center"
	}
	return "imported"
}

// All returns active and recycled rooms for internal monitor synchronization.
// Recycled rooms remain canonical records so their monitor and event history
// is not destroyed merely because the room is archived.
func (s *Store) All() []Room {
	return s.list(true)
}

func (s *Store) list(includeRecycled bool) []Room {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Room, 0, len(s.rooms))
	for _, room := range s.rooms {
		if room.Recycled && !includeRecycled {
			continue
		}
		copy := *room
		copy.FollowSources = append([]FollowSource(nil), room.FollowSources...)
		items = append(items, copy)
	}
	sort.Slice(items, func(i, j int) bool {
		return roomSortKey(&items[i]) < roomSortKey(&items[j])
	})
	return items
}

func (s *Store) RecycleBin() []Room {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Room, 0)
	for _, room := range s.rooms {
		if !room.Recycled {
			continue
		}
		copy := *room
		copy.FollowSources = append([]FollowSource(nil), room.FollowSources...)
		items = append(items, copy)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].RecycledAt != items[j].RecycledAt {
			return items[i].RecycledAt > items[j].RecycledAt
		}
		return roomSortKey(&items[i]) < roomSortKey(&items[j])
	})
	return items
}

// CenterExclusions returns safe center-library tombstones for management UI.
func (s *Store) CenterExclusions() []CenterExclusion {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]CenterExclusion, 0, len(s.centerExclusions))
	for _, exclusion := range s.centerExclusions {
		items = append(items, *exclusion)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ExcludedAt != items[j].ExcludedAt {
			return items[i].ExcludedAt > items[j].ExcludedAt
		}
		return items[i].ID < items[j].ID
	})
	return items
}

func (s *Store) CenterExclusion(roomID string) (CenterExclusion, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roomID = strings.TrimSpace(roomID)
	for _, exclusion := range s.centerExclusions {
		if exclusion.ID == roomID || exclusion.WebRID == roomID || exclusion.ActualRoomID == roomID {
			return *exclusion, true
		}
	}
	return CenterExclusion{}, false
}

func (s *Store) centerRoomExcludedLocked(webRID, actualRoomID string) bool {
	webRID = strings.TrimSpace(webRID)
	actualRoomID = strings.TrimSpace(actualRoomID)
	for _, exclusion := range s.centerExclusions {
		if exclusion.WebRID == webRID || (actualRoomID != "" && exclusion.ActualRoomID == actualRoomID) {
			return true
		}
	}
	return false
}

func (s *Store) excludeCenterRoomLocked(room *Room, reason string, excludedAt time.Time) {
	if room == nil || !room.CenterLinked || !validRoomID(strings.TrimSpace(room.WebRID)) {
		return
	}
	if excludedAt.IsZero() {
		excludedAt = time.Now()
	}
	id := strings.TrimSpace(room.WebRID)
	s.centerExclusions[id] = &CenterExclusion{
		ID: id, WebRID: id, ActualRoomID: strings.TrimSpace(room.ActualRoomID),
		Name: strings.TrimSpace(room.Name), StreamerName: strings.TrimSpace(room.StreamerName),
		Reason: strings.TrimSpace(reason), ExcludedAt: excludedAt.Format(time.RFC3339Nano), SyncPending: true,
	}
}

// PendingCenterExclusions returns only locally-created exclusions that have
// not yet been acknowledged by the center server. Server-originated rows are
// never re-uploaded, which prevents a stale client from resurrecting an
// exclusion that another authorized client has restored globally.
func (s *Store) PendingCenterExclusions() []CenterExclusion {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]CenterExclusion, 0)
	for _, exclusion := range s.centerExclusions {
		if exclusion.SyncPending {
			items = append(items, *exclusion)
		}
	}
	return items
}

func (s *Store) MarkCenterExclusionsSynced(webRIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, webRID := range webRIDs {
		if exclusion := s.centerExclusions[strings.TrimSpace(webRID)]; exclusion != nil && exclusion.SyncPending {
			exclusion.SyncPending = false
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.saveLocked()
}

// MergeGlobalCenterExclusions reconciles the local cache with the
// server-authoritative exclusion library. Locally pending rows survive until
// acknowledged; center-only room rows are removed immediately.
func (s *Store) MergeGlobalCenterExclusions(items []CenterExclusion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	global := make(map[string]CenterExclusion, len(items))
	for _, item := range items {
		item.WebRID = strings.TrimSpace(item.WebRID)
		if !validRoomID(item.WebRID) {
			continue
		}
		item.ID = item.WebRID
		item.SyncPending = false
		global[item.WebRID] = item
	}
	changed := false
	for key, current := range s.centerExclusions {
		if current.SyncPending {
			continue
		}
		if _, exists := global[key]; !exists {
			delete(s.centerExclusions, key)
			changed = true
		}
	}
	for key, item := range global {
		current := s.centerExclusions[key]
		if current == nil || *current != item {
			copy := item
			s.centerExclusions[key] = &copy
			changed = true
		}
	}
	for id, room := range s.rooms {
		if room.Source == "center" && s.centerRoomExcludedLocked(room.WebRID, room.ActualRoomID) {
			delete(s.rooms, id)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.saveLocked()
}

// RestoreCenterExclusion removes a center-library tombstone and restores the
// room locally in a stopped state. Future center snapshots may enrich it again.
func (s *Store) RestoreCenterExclusion(roomID string) (Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roomID = strings.TrimSpace(roomID)
	var key string
	var exclusion *CenterExclusion
	for candidateKey, candidate := range s.centerExclusions {
		if candidate.ID == roomID || candidate.WebRID == roomID || candidate.ActualRoomID == roomID {
			key, exclusion = candidateKey, candidate
			break
		}
	}
	if exclusion == nil {
		return Room{}, errors.New("中心库排除记录不存在")
	}
	delete(s.centerExclusions, key)
	now := time.Now().Format(time.RFC3339Nano)
	room := s.rooms[exclusion.WebRID]
	if room == nil {
		room = &Room{
			ID: exclusion.WebRID, WebRID: exclusion.WebRID, ActualRoomID: exclusion.ActualRoomID,
			Name: exclusion.Name, StreamerName: exclusion.StreamerName,
			MonitorStatus: "stopped", ConnectionStatus: "disconnected", Enabled: true,
			Source: "center", CenterLinked: true, CreatedAt: now, UpdatedAt: now,
		}
		s.rooms[room.ID] = room
	}
	if err := s.saveLocked(); err != nil {
		s.centerExclusions[key] = exclusion
		return Room{}, err
	}
	copy := *room
	copy.FollowSources = append([]FollowSource(nil), room.FollowSources...)
	return copy, nil
}

func normalizeSettings(settings Settings) Settings {
	if settings.AutoRecycleOfflineDays < 0 {
		settings.AutoRecycleOfflineDays = 0
	}
	if settings.AutoRecycleOfflineDays > 3650 {
		settings.AutoRecycleOfflineDays = 3650
	}
	if settings.ParticipationPrewarmMinutes < 0 {
		settings.ParticipationPrewarmMinutes = 0
	}
	if settings.ParticipationPrewarmMinutes > maximumParticipationPrewarmMinutes {
		settings.ParticipationPrewarmMinutes = maximumParticipationPrewarmMinutes
	}
	if settings.AutoRecycleMaxLiveSessions < 0 {
		settings.AutoRecycleMaxLiveSessions = 0
	}
	if settings.AutoRecycleMaxLiveSessions > 100000 {
		settings.AutoRecycleMaxLiveSessions = 100000
	}
	if settings.AutoRecycleNoPacketDays < 0 {
		settings.AutoRecycleNoPacketDays = 0
	}
	if settings.AutoRecycleNoPacketDays > 3650 {
		settings.AutoRecycleNoPacketDays = 3650
	}
	return settings
}

func defaultSettings() Settings {
	return Settings{
		AutoRecycleOfflineDays:      defaultAutoRecycleOfflineDays,
		ParticipationPrewarmMinutes: defaultParticipationPrewarmMinutes,
		AutoRecycleMaxLiveSessions:  0,
		AutoRecycleNoPacketDays:     3,
	}
}

func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

func (s *Store) SetSettings(settings Settings) (Settings, error) {
	settings = normalizeSettings(settings)
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.settings
	s.settings = settings
	if err := s.saveLocked(); err != nil {
		s.settings = previous
		return Settings{}, err
	}
	return s.settings, nil
}

// ExecuteCleanup scans a bounded stable slice of the current room store and
// applies the persisted cleanup rules. Network-derived rules require an
// already definitive offline result; the explicit local-import/no-packet rule
// may also clean unprobed imports. No rule performs a network probe here.
func (s *Store) ExecuteCleanup(cursor string, limit int, checkedAt time.Time) (CleanupProgress, error) {
	return s.ExecuteCleanupScoped(cursor, limit, checkedAt, true)
}

// ExecuteCleanupScoped applies the cleanup rules while keeping center-library
// deletion authority outside the store. Non-permanent clients pass false so
// center-only rows are never removed or converted into global exclusions;
// locally imported rows can still be recycled normally.
func (s *Store) ExecuteCleanupScoped(cursor string, limit int, checkedAt time.Time, allowCenterExclusions bool) (CleanupProgress, error) {
	if limit <= 0 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}
	cursor = strings.TrimSpace(cursor)

	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.rooms))
	for id, room := range s.rooms {
		if room != nil && !room.Recycled && id > cursor {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	total := 0
	for _, room := range s.rooms {
		if room != nil && !room.Recycled {
			total++
		}
	}
	result := CleanupProgress{Total: total}
	if len(ids) == 0 {
		return result, nil
	}
	if len(ids) > limit {
		result.HasMore = true
		ids = ids[:limit]
	}
	result.NextCursor = ids[len(ids)-1]
	changedRooms := make(map[string]Room)
	changedExclusions := make(map[string]*CenterExclusion)
	for _, id := range ids {
		room := s.rooms[id]
		result.Scanned++
		centerOnly := strings.EqualFold(strings.TrimSpace(room.Source), "center") && len(room.FollowSources) == 0
		if centerOnly && !allowCenterExclusions {
			result.Skipped++
			continue
		}
		reason, ok := s.cleanupReasonLocked(room, checkedAt)
		if !ok {
			result.Skipped++
			continue
		}
		changedRooms[id] = *room
		if centerOnly {
			if previous, exists := s.centerExclusions[room.WebRID]; exists && previous != nil {
				copy := *previous
				changedExclusions[room.WebRID] = &copy
			} else {
				changedExclusions[room.WebRID] = nil
			}
			s.excludeCenterRoomLocked(room, reason, checkedAt)
			delete(s.rooms, id)
			result.Excluded++
		} else {
			room.Recycled = true
			room.RecycledAt = checkedAt.Format(time.RFC3339Nano)
			room.RecycleReason = reason
			room.Enabled = false
			room.MonitorStatus = "stopped"
			room.ConnectionStatus = "disconnected"
			room.UpdatedAt = checkedAt.Format(time.RFC3339Nano)
			result.Recycled++
		}
		result.Cleaned++
	}
	if result.Cleaned == 0 {
		return result, nil
	}
	if err := s.saveLocked(); err != nil {
		for id, room := range changedRooms {
			copy := room
			s.rooms[id] = &copy
		}
		for webRID, exclusion := range changedExclusions {
			if exclusion == nil {
				delete(s.centerExclusions, webRID)
			} else {
				copy := *exclusion
				s.centerExclusions[webRID] = &copy
			}
		}
		return CleanupProgress{}, err
	}
	return result, nil
}

func (s *Store) cleanupReasonLocked(room *Room, checkedAt time.Time) (string, bool) {
	if room == nil || room.Recycled {
		return "", false
	}
	centerOnly := strings.EqualFold(strings.TrimSpace(room.Source), "center") && len(room.FollowSources) == 0
	liveSessionCount := room.LiveSessionCount
	liveMetricsKnown := room.HasDefinitiveProbe
	definitivelyOffline := room.HasDefinitiveProbe && room.LastDefinitiveLiveState == "offline"
	if centerOnly {
		liveSessionCount = room.CenterLiveSessionCount
		liveMetricsKnown = room.CenterMetricsVersion > 0
		definitivelyOffline = strings.EqualFold(strings.TrimSpace(room.CenterLiveStatus), "offline")
	}
	if s.settings.AutoRecycleImportedNoPacketEnabled && roomPageSource(room) == "imported" {
		localPacketMissing := strings.TrimSpace(room.LastRedPacketAt) == ""
		centerPacketMissing := strings.TrimSpace(room.CenterLastEventAt) == "" && (room.CenterMetricsVersion == 0 || room.CenterRedPacketCount == 0)
		if localPacketMissing && centerPacketMissing {
			return "本地导入后从未发现红包，手动执行自动清理", true
		}
	}
	if !definitivelyOffline {
		return "", false
	}
	if s.settings.AutoRecycleLowLiveEnabled && liveMetricsKnown && liveSessionCount <= s.settings.AutoRecycleMaxLiveSessions {
		return fmt.Sprintf("累计确认开播 %d 次，不超过设置的 %d 次，手动执行自动清理", liveSessionCount, s.settings.AutoRecycleMaxLiveSessions), true
	}
	if s.settings.AutoRecycleNoPacketEnabled && s.settings.AutoRecycleNoPacketDays > 0 {
		baseline := firstNonEmpty(room.LastRedPacketAt, room.FirstDefinitiveProbeAt)
		if centerOnly {
			baseline = firstNonEmpty(room.CenterLastEventAt, room.FirstDefinitiveProbeAt)
		}
		if since, ok := parseStoredTime(baseline); ok && checkedAt.Sub(since) >= time.Duration(s.settings.AutoRecycleNoPacketDays)*24*time.Hour {
			return fmt.Sprintf("近 %d 天未发现红包，手动执行自动清理", s.settings.AutoRecycleNoPacketDays), true
		}
	}
	return "", false
}

// RecordLiveResult applies only a definitive successful live probe. Unknown,
// error, and network outcomes are ignored by design. An offline calendar day
// is counted at most once; a transition into live counts one independent live
// session. Cleanup is evaluated only while definitively offline, so a room can
// never be removed while a successful probe says it is live.
func (s *Store) RecordLiveResult(roomID, status string, checkedAt time.Time) (bool, error) {
	roomID = strings.TrimSpace(roomID)
	status = strings.ToLower(strings.TrimSpace(status))
	if roomID == "" || (status != "live" && status != "offline") {
		return false, nil
	}
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	room := s.rooms[roomID]
	if room == nil {
		for _, candidate := range s.rooms {
			if candidate.WebRID == roomID || candidate.ActualRoomID == roomID {
				room = candidate
				break
			}
		}
	}
	if room == nil || room.Recycled {
		return false, nil
	}
	changed := false
	checkedAtText := checkedAt.Format(time.RFC3339Nano)
	if !room.HasDefinitiveProbe {
		room.HasDefinitiveProbe = true
		room.FirstDefinitiveProbeAt = checkedAtText
		changed = true
	}
	if room.LastDefinitiveProbeAt != checkedAtText {
		room.LastDefinitiveProbeAt = checkedAtText
		changed = true
	}
	if status == "live" {
		if room.LastDefinitiveLiveState != "live" {
			room.LiveSessionCount++
			changed = true
		}
		if room.LastDefinitiveLiveState != "live" {
			room.LastDefinitiveLiveState = "live"
			changed = true
		}
		if room.OfflineDays != 0 || room.LastOfflineDay != "" {
			room.OfflineDays = 0
			room.LastOfflineDay = ""
			changed = true
		}
	} else {
		if room.LastDefinitiveLiveState != "offline" {
			room.LastDefinitiveLiveState = "offline"
			changed = true
		}
		day := checkedAt.In(time.Local).Format("2006-01-02")
		if room.LastOfflineDay != day {
			room.LastOfflineDay = day
			room.OfflineDays++
			changed = true
		}
		reason := ""
		liveSessionCount := room.LiveSessionCount
		liveMetricsKnown := true
		if strings.EqualFold(strings.TrimSpace(room.Source), "center") && len(room.FollowSources) == 0 {
			liveSessionCount = room.CenterLiveSessionCount
			liveMetricsKnown = room.CenterMetricsVersion > 0
		}
		if s.settings.AutoRecycleLowLiveEnabled && liveMetricsKnown && liveSessionCount <= s.settings.AutoRecycleMaxLiveSessions {
			reason = fmt.Sprintf("累计确认开播 %d 次，不超过设置的 %d 次，系统自动清理", liveSessionCount, s.settings.AutoRecycleMaxLiveSessions)
		}
		if reason == "" && s.settings.AutoRecycleNoPacketEnabled && s.settings.AutoRecycleNoPacketDays > 0 {
			baseline := firstNonEmpty(room.LastRedPacketAt, room.FirstDefinitiveProbeAt)
			if since, ok := parseStoredTime(baseline); ok && checkedAt.Sub(since) >= time.Duration(s.settings.AutoRecycleNoPacketDays)*24*time.Hour {
				reason = fmt.Sprintf("近 %d 天未发现红包，系统自动清理", s.settings.AutoRecycleNoPacketDays)
			}
		}
		limit := s.settings.AutoRecycleOfflineDays
		if reason == "" && limit > 0 && room.OfflineDays >= limit {
			reason = fmt.Sprintf("连续 %d 天监测未发现直播，系统自动回收", room.OfflineDays)
		}
		if reason != "" {
			centerOnly := strings.EqualFold(strings.TrimSpace(room.Source), "center") && len(room.FollowSources) == 0
			if centerOnly {
				s.excludeCenterRoomLocked(room, reason, checkedAt)
				delete(s.rooms, room.ID)
				if err := s.saveLocked(); err != nil {
					delete(s.centerExclusions, room.WebRID)
					s.rooms[room.ID] = room
					return false, err
				}
				return true, nil
			}
			room.Recycled = true
			room.RecycledAt = checkedAtText
			room.RecycleReason = reason
			room.Enabled = false
			room.MonitorStatus = "stopped"
			room.ConnectionStatus = "disconnected"
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	room.UpdatedAt = checkedAt.Format(time.RFC3339Nano)
	s.scheduleSaveLocked()
	return room.Recycled, nil
}

// RecordRedPacketEvent records only safe discovery evidence. It is called by
// the native red-packet store and never exposes packet data to the frontend.
func (s *Store) RecordRedPacketEvent(roomID string, detectedAt time.Time) error {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return nil
	}
	if detectedAt.IsZero() {
		detectedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	room := s.rooms[roomID]
	if room == nil {
		for _, candidate := range s.rooms {
			if candidate.WebRID == roomID || candidate.ActualRoomID == roomID {
				room = candidate
				break
			}
		}
	}
	if room == nil || room.Recycled {
		return nil
	}
	value := detectedAt.Format(time.RFC3339Nano)
	if value <= room.LastRedPacketAt {
		return nil
	}
	room.LastRedPacketAt = value
	room.UpdatedAt = value
	s.scheduleSaveLocked()
	return nil
}

func parseStoredTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, value)
	}
	return parsed, err == nil
}

func (s *Store) Restore(roomID string) (Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	room := s.rooms[strings.TrimSpace(roomID)]
	if room == nil || !room.Recycled {
		return Room{}, errors.New("回收站中的直播间不存在")
	}
	now := time.Now().Format(time.RFC3339Nano)
	room.Recycled = false
	room.RecycledAt = ""
	room.RecycleReason = ""
	room.OfflineDays = 0
	room.LastOfflineDay = ""
	room.HasDefinitiveProbe = false
	room.FirstDefinitiveProbeAt = ""
	room.LastDefinitiveProbeAt = ""
	room.LastDefinitiveLiveState = ""
	room.LiveSessionCount = 0
	room.LastRedPacketAt = ""
	room.Enabled = true
	room.MonitorStatus = "stopped"
	room.ConnectionStatus = "disconnected"
	room.UpdatedAt = now
	if err := s.saveLocked(); err != nil {
		return Room{}, err
	}
	copy := *room
	copy.FollowSources = append([]FollowSource(nil), room.FollowSources...)
	return copy, nil
}

func (s *Store) DeleteRecycled(roomID string) error {
	return s.DeleteRecycledScoped(roomID, true)
}

// DeleteRecycledScoped permanently removes a local recycled record. Only a
// permanent client may turn that deletion into a shared center tombstone.
func (s *Store) DeleteRecycledScoped(roomID string, allowCenterExclusion bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	roomID = strings.TrimSpace(roomID)
	room := s.rooms[roomID]
	if room == nil || !room.Recycled {
		return errors.New("回收站中的直播间不存在")
	}
	if allowCenterExclusion {
		s.excludeCenterRoomLocked(room, firstNonEmpty(room.RecycleReason, "从回收站永久删除"), time.Now())
	}
	delete(s.rooms, roomID)
	return s.saveLocked()
}

// SyncFollowingLive merges one participation account's currently-live
// follow feed into the canonical room list. Existing rooms are matched by
// public WebRID first and actual room ID second, so the same broadcaster is
// never duplicated when several accounts follow it. An empty snapshot never
// deletes rooms or historical attribution.
func (s *Store) SyncFollowingLive(accountID, accountName string, items []FollowingLiveRoom, refreshedAt string) (MigrationResult, error) {
	accountID = strings.TrimSpace(accountID)
	accountName = strings.TrimSpace(accountName)
	if accountID == "" {
		return MigrationResult{}, errors.New("关注来源账号为空")
	}
	if accountName == "" {
		accountName = accountID
	}
	if strings.TrimSpace(refreshedAt) == "" {
		refreshedAt = time.Now().Format(time.RFC3339Nano)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	result := MigrationResult{}
	changed := false
	changedRooms := map[*Room]struct{}{}
	// A successful account snapshot is authoritative for that account's
	// current live set. Preserve the attribution and room when a followed
	// broadcaster goes offline, but clear only this account's live flag before
	// applying the rooms that are present in the new snapshot.
	for _, room := range s.rooms {
		for index := range room.FollowSources {
			source := &room.FollowSources[index]
			if source.AccountID == accountID && source.IsLive {
				source.IsLive = false
				changed = true
				changedRooms[room] = struct{}{}
			}
		}
	}
	seen := map[string]struct{}{}
	for _, item := range items {
		webRID := strings.TrimSpace(item.WebRID)
		actualRoomID := strings.TrimSpace(item.RoomID)
		if !validRoomID(webRID) {
			result.Invalid++
			continue
		}
		if _, exists := seen[webRID]; exists {
			continue
		}
		seen[webRID] = struct{}{}
		roomChanged := false

		var room *Room
		for _, candidate := range s.rooms {
			if candidate.WebRID == webRID || candidate.ID == webRID ||
				(actualRoomID != "" && (candidate.ActualRoomID == actualRoomID || candidate.ID == actualRoomID)) {
				room = candidate
				break
			}
		}
		if room == nil {
			room = &Room{
				ID:               webRID,
				WebRID:           webRID,
				ActualRoomID:     actualRoomID,
				Name:             strings.TrimSpace(item.Title),
				StreamerName:     strings.TrimSpace(item.StreamerName),
				MonitorStatus:    "stopped",
				ConnectionStatus: "disconnected",
				Enabled:          true,
				Source:           "following-live",
				FollowingLive:    true,
				CreatedAt:        refreshedAt,
				UpdatedAt:        refreshedAt,
			}
			s.rooms[room.ID] = room
			result.Imported++
			changed = true
			roomChanged = true
		} else {
			result.Merged++
			if room.Source == "center" {
				room.Source = "following-live"
				changed = true
				roomChanged = true
			}
		}

		if room.WebRID != webRID {
			room.WebRID = webRID
			changed = true
			roomChanged = true
		}
		if actualRoomID != "" && room.ActualRoomID != actualRoomID {
			room.ActualRoomID = actualRoomID
			changed = true
			roomChanged = true
		}
		if title := strings.TrimSpace(item.Title); title != "" && room.Name != title {
			room.Name = title
			changed = true
			roomChanged = true
		}
		if streamer := strings.TrimSpace(item.StreamerName); streamer != "" && room.StreamerName != streamer {
			room.StreamerName = streamer
			changed = true
			roomChanged = true
		}
		if room.LastSeenLiveAt != refreshedAt {
			room.LastSeenLiveAt = refreshedAt
			changed = true
			roomChanged = true
		}
		if upsertFollowSource(room, FollowSource{AccountID: accountID, AccountName: accountName, LastSeenLiveAt: refreshedAt, IsLive: true}) {
			changed = true
			roomChanged = true
		}
		if roomChanged {
			room.UpdatedAt = refreshedAt
			changedRooms[room] = struct{}{}
		}
	}
	for _, room := range s.rooms {
		followingLive := false
		for _, source := range room.FollowSources {
			if source.IsLive {
				followingLive = true
				break
			}
		}
		if room.FollowingLive != followingLive {
			room.FollowingLive = followingLive
			changed = true
			changedRooms[room] = struct{}{}
		}
	}
	for room := range changedRooms {
		room.UpdatedAt = refreshedAt
	}
	result.Total = len(s.rooms)
	if !changed {
		return result, nil
	}
	if err := s.saveLocked(); err != nil {
		return MigrationResult{}, err
	}
	return result, nil
}

func upsertFollowSource(room *Room, source FollowSource) bool {
	for index := range room.FollowSources {
		current := &room.FollowSources[index]
		if current.AccountID != source.AccountID {
			continue
		}
		if current.AccountName == source.AccountName && current.LastSeenLiveAt == source.LastSeenLiveAt && current.IsLive == source.IsLive {
			return false
		}
		*current = source
		sort.Slice(room.FollowSources, func(i, j int) bool { return room.FollowSources[i].AccountName < room.FollowSources[j].AccountName })
		return true
	}
	room.FollowSources = append(room.FollowSources, source)
	sort.Slice(room.FollowSources, func(i, j int) bool { return room.FollowSources[i].AccountName < room.FollowSources[j].AccountName })
	return true
}

func (s *Store) MigrateLegacy(legacyDir string) (MigrationResult, error) {
	if strings.TrimSpace(legacyDir) == "" {
		legacyDir = defaultLegacyDataDir()
	}
	sourcePath := filepath.Join(legacyDir, "rooms_config.json")
	file, err := os.Open(sourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return MigrationResult{}, errors.New("未找到旧福宝直播间数据")
		}
		return MigrationResult{}, fmt.Errorf("读取旧福宝直播间失败: %w", err)
	}
	defer file.Close()

	legacyRooms := map[string]legacyRoom{}
	if err := json.NewDecoder(file).Decode(&legacyRooms); err != nil {
		return MigrationResult{}, fmt.Errorf("解析旧福宝直播间失败: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	result := MigrationResult{}
	now := time.Now().Format(time.RFC3339Nano)
	for key, legacy := range legacyRooms {
		webRID := strings.TrimSpace(legacy.WebRID)
		if !validRoomID(webRID) {
			result.Invalid++
			continue
		}
		id := firstNonEmpty(legacy.RoomID, webRID, key)
		enabled := true
		if legacy.Enabled != nil {
			enabled = *legacy.Enabled
		}
		if existing := s.rooms[id]; existing != nil {
			existing.WebRID = webRID
			existing.ActualRoomID = firstNonEmpty(legacy.ActualRoomID, existing.ActualRoomID)
			existing.Name = firstNonEmpty(legacy.RoomName, existing.Name)
			existing.StreamerName = firstNonEmpty(legacy.StreamerName, existing.StreamerName)
			existing.Enabled = enabled
			existing.Source = "dy-kiro"
			existing.UpdatedAt = now
			result.Merged++
			continue
		}
		s.rooms[id] = &Room{
			ID:               id,
			WebRID:           webRID,
			ActualRoomID:     legacy.ActualRoomID,
			Name:             legacy.RoomName,
			StreamerName:     legacy.StreamerName,
			MonitorStatus:    normalizeStatus(legacy.MonitorStatus, "stopped"),
			ConnectionStatus: normalizeStatus(legacy.ConnectionStatus, "disconnected"),
			Enabled:          enabled,
			Source:           "dy-kiro",
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		result.Imported++
	}
	if err := s.saveLocked(); err != nil {
		return MigrationResult{}, err
	}
	result.Total = len(s.rooms)
	return result, nil
}

// ImportIDs adds live-room IDs from pasted text or a .txt/.csv file. Inputs
// are normalized and deduplicated locally; no legacy file is modified.
func (s *Store) ImportIDs(raw string) (MigrationResult, error) {
	return s.importIDs(raw, true)
}

// ImportIDsBatch applies one bounded import chunk. Intermediate chunks stay
// in the in-memory canonical store and only the final chunk persists the
// complete file, avoiding hundreds of full-file rewrites for very large
// imports while the serial native RPC still observes every completed batch.
func (s *Store) ImportIDsBatch(raw string, persist bool) (MigrationResult, error) {
	return s.importIDs(raw, persist)
}

func (s *Store) importIDs(raw string, persist bool) (MigrationResult, error) {
	values := splitRoomInputs(raw)
	if len(values) == 0 {
		return MigrationResult{}, errors.New("没有识别到有效的直播间 ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := MigrationResult{}
	now := time.Now().Format(time.RFC3339Nano)
	seen := map[string]struct{}{}
	existingByID := make(map[string]*Room, len(s.rooms)*3)
	for _, room := range s.rooms {
		for _, key := range []string{room.ID, room.WebRID, room.ActualRoomID} {
			if key = strings.TrimSpace(key); key != "" {
				existingByID[key] = room
			}
		}
	}
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		if !validRoomID(value) {
			result.Invalid++
			continue
		}
		existing := existingByID[value]
		if existing != nil {
			if existing.Source == "center" {
				existing.Source = "manual"
				existing.UpdatedAt = now
			}
			result.Merged++
			continue
		}
		s.rooms[value] = &Room{
			ID:               value,
			WebRID:           value,
			Name:             "直播间 " + value[maxInt(0, len(value)-4):],
			MonitorStatus:    "stopped",
			ConnectionStatus: "disconnected",
			Enabled:          true,
			Source:           "manual",
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		existingByID[value] = s.rooms[value]
		result.Imported++
	}
	result.Total = len(s.rooms)
	if result.Imported == 0 && result.Merged == 0 {
		return result, errors.New("没有识别到有效的直播间 ID")
	}
	if persist {
		if err := s.saveLocked(); err != nil {
			return MigrationResult{}, err
		}
	}
	return result, nil
}

func sortRooms(items []*Room) {
	sort.Slice(items, func(i, j int) bool {
		return roomSortKey(items[i]) < roomSortKey(items[j])
	})
}

func roomSortKey(room *Room) string {
	return strings.ToLower(firstNonEmpty(room.StreamerName, room.Name, room.WebRID, room.ID))
}

func normalizeStatus(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "running" || value == "connected" || value == "connecting" || value == "reconnecting" {
		return fallback
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func defaultLegacyDataDir() string {
	if override := strings.TrimSpace(os.Getenv("FUBAO_LEGACY_DATA_DIR")); override != "" {
		return override
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, "Library", "Application Support", "DouyinFubaoMonitor", "data"),
		filepath.Join(os.Getenv("APPDATA"), "DouyinFubaoMonitor", "data"),
		filepath.Join(home, ".local", "share", "DouyinFubaoMonitor", "data"),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return candidates[0]
}

func splitRoomInputs(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		if slash := strings.LastIndex(value, "/"); slash >= 0 {
			value = value[slash+1:]
		}
		if cut := strings.IndexAny(value, "?#"); cut >= 0 {
			value = value[:cut]
		}
		values = append(values, strings.TrimSpace(value))
	}
	return values
}

func validRoomID(value string) bool {
	if len(value) < 6 || len(value) > 20 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
