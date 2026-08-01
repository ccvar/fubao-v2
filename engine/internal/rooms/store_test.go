package rooms

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRemovesRoomsWithoutValidWebRID(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "rooms.json")
	source := `{
		"version": 1,
		"rooms": [
			{"id":"valid","web_rid":"123456","name":"有效直播间","monitor_status":"stopped","connection_status":"disconnected","enabled":true},
			{"id":"record-only","actual_room_id":"7000000000000000001","name":"仅记录号","monitor_status":"stopped","connection_status":"disconnected","enabled":true}
		]
	}`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	items := store.List()
	if len(items) != 1 || items[0].ID != "valid" {
		t.Fatalf("expected only the valid room, got %+v", items)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved roomFile
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Rooms) != 1 || saved.Rooms[0].ID != "valid" {
		t.Fatalf("invalid room must also be removed from disk: %+v", saved.Rooms)
	}
}

func TestMigrateLegacyCopiesAndMergesRooms(t *testing.T) {
	dataDir := t.TempDir()
	legacyDir := t.TempDir()
	source := `{
		"123456": {
			"room_id": "123456",
			"web_rid": "123456",
			"actual_room_id": "7000000000000000001",
			"room_name": "福利直播间",
			"streamer_name": "测试主播",
			"monitor_status": "running",
			"connection_status": "connected",
			"enabled": true
		}
	}`
	if err := os.WriteFile(filepath.Join(legacyDir, "rooms_config.json"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.MigrateLegacy(legacyDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 1 || result.Total != 1 {
		t.Fatalf("unexpected migration result: %+v", result)
	}
	items := store.List()
	if len(items) != 1 {
		t.Fatalf("expected one room, got %d", len(items))
	}
	if items[0].Name != "福利直播间" || items[0].StreamerName != "测试主播" {
		t.Fatalf("unexpected migrated room: %+v", items[0])
	}
	if items[0].MonitorStatus != "stopped" || items[0].ConnectionStatus != "disconnected" {
		t.Fatalf("runtime status must be reset during migration: %+v", items[0])
	}

	result, err = store.MigrateLegacy(legacyDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Merged != 1 || result.Total != 1 {
		t.Fatalf("expected merge on second migration: %+v", result)
	}
}

func TestMigrateLegacySkipsRecordOnlyRooms(t *testing.T) {
	dataDir := t.TempDir()
	legacyDir := t.TempDir()
	source := `{
		"record-only": {
			"room_id": "record-only",
			"actual_room_id": "7000000000000000001",
			"room_name": "缺少房间号"
		}
	}`
	if err := os.WriteFile(filepath.Join(legacyDir, "rooms_config.json"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.MigrateLegacy(legacyDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Invalid != 1 || result.Imported != 0 || result.Total != 0 {
		t.Fatalf("unexpected migration result: %+v", result)
	}
}

func TestImportIDsNormalizesDeduplicatesAndMerges(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ImportIDs("123456789012\nhttps://live.douyin.com/987654321098, 123456789012\nnot-an-id")
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 2 || result.Invalid != 1 || result.Total != 2 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	result, err = store.ImportIDs("123456789012")
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 0 || result.Merged != 1 || result.Total != 2 {
		t.Fatalf("expected duplicate merge: %+v", result)
	}
	items := store.List()
	if len(items) != 2 || items[0].Source != "manual" {
		t.Fatalf("unexpected imported rooms: %+v", items)
	}
}
