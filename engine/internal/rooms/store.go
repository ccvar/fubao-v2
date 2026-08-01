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

const storeVersion = 1

type Room struct {
	ID               string `json:"id"`
	WebRID           string `json:"web_rid,omitempty"`
	ActualRoomID     string `json:"actual_room_id,omitempty"`
	Name             string `json:"name,omitempty"`
	StreamerName     string `json:"streamer_name,omitempty"`
	MonitorStatus    string `json:"monitor_status"`
	ConnectionStatus string `json:"connection_status"`
	Enabled          bool   `json:"enabled"`
	Source           string `json:"source,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

type MigrationResult struct {
	Imported int `json:"imported"`
	Merged   int `json:"merged"`
	Total    int `json:"total"`
	Invalid  int `json:"invalid,omitempty"`
}

type roomFile struct {
	Version int     `json:"version"`
	Rooms   []*Room `json:"rooms"`
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
	mu    sync.Mutex
	path  string
	rooms map[string]*Room
}

func NewStore(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("直播间数据目录为空")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建直播间数据目录失败: %w", err)
	}
	store := &Store{
		path:  filepath.Join(dataDir, "rooms.json"),
		rooms: map[string]*Room{},
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
		s.rooms[room.ID] = room
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
	payload, err := json.MarshalIndent(roomFile{Version: storeVersion, Rooms: items}, "", "  ")
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

func (s *Store) List() []Room {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Room, 0, len(s.rooms))
	for _, room := range s.rooms {
		items = append(items, *room)
	}
	sort.Slice(items, func(i, j int) bool {
		return roomSortKey(&items[i]) < roomSortKey(&items[j])
	})
	return items
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
	values := splitRoomInputs(raw)
	if len(values) == 0 {
		return MigrationResult{}, errors.New("没有识别到有效的直播间 ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := MigrationResult{}
	now := time.Now().Format(time.RFC3339Nano)
	seen := map[string]struct{}{}
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		if !validRoomID(value) {
			result.Invalid++
			continue
		}
		var existing *Room
		for _, room := range s.rooms {
			if room.ID == value || room.WebRID == value {
				existing = room
				break
			}
		}
		if existing != nil {
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
		result.Imported++
	}
	result.Total = len(s.rooms)
	if result.Imported == 0 && result.Merged == 0 {
		return result, errors.New("没有识别到有效的直播间 ID")
	}
	if err := s.saveLocked(); err != nil {
		return MigrationResult{}, err
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
