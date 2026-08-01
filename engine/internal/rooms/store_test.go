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

func TestSyncFollowingLiveMergesCanonicalRoomAndTracksEveryAccount(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIDs("688220427462"); err != nil {
		t.Fatal(err)
	}

	first, err := store.SyncFollowingLive("account-a", "川、", []FollowingLiveRoom{{
		RoomID: "7000000000000000001", WebRID: "688220427462", Title: "关注直播标题", StreamerName: "韩可乐",
	}}, "2026-08-01T16:00:00+08:00")
	if err != nil {
		t.Fatal(err)
	}
	if first.Imported != 0 || first.Merged != 1 || first.Total != 1 {
		t.Fatalf("expected existing room merge, got %+v", first)
	}

	second, err := store.SyncFollowingLive("account-b", "jojo", []FollowingLiveRoom{{
		RoomID: "7000000000000000001", WebRID: "688220427462", Title: "更新后的直播标题", StreamerName: "韩可乐",
	}, {
		RoomID: "7000000000000000002", WebRID: "699330528573", Title: "另一场直播", StreamerName: "新主播",
	}}, "2026-08-01T16:01:00+08:00")
	if err != nil {
		t.Fatal(err)
	}
	if second.Imported != 1 || second.Merged != 1 || second.Total != 2 {
		t.Fatalf("unexpected second sync: %+v", second)
	}

	items := store.List()
	if len(items) != 2 {
		t.Fatalf("expected two canonical rooms, got %+v", items)
	}
	var merged Room
	for _, item := range items {
		if item.WebRID == "688220427462" {
			merged = item
		}
	}
	if merged.Name != "更新后的直播标题" || merged.ActualRoomID != "7000000000000000001" {
		t.Fatalf("live metadata was not refreshed: %+v", merged)
	}
	if !merged.FollowingLive || !merged.FollowSources[0].IsLive || !merged.FollowSources[1].IsLive {
		t.Fatalf("expected the merged room and both sources to be live: %+v", merged)
	}
	if len(merged.FollowSources) != 2 || merged.FollowSources[0].AccountName != "jojo" || merged.FollowSources[1].AccountName != "川、" {
		t.Fatalf("expected both account attributions, got %+v", merged.FollowSources)
	}
	if merged.Source != "manual" {
		t.Fatalf("sync must preserve the original room source, got %q", merged.Source)
	}

	if _, err := store.SyncFollowingLive("account-a", "川、", nil, "2026-08-01T16:02:00+08:00"); err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 2 {
		t.Fatal("an empty follow snapshot must never delete canonical rooms")
	}
	items = store.List()
	for _, item := range items {
		if item.WebRID == "688220427462" {
			if !item.FollowingLive || item.FollowSources[1].IsLive {
				t.Fatalf("account-a must turn offline while account-b keeps the room live: %+v", item)
			}
		}
	}
	if _, err := store.SyncFollowingLive("account-b", "jojo", nil, "2026-08-01T16:03:00+08:00"); err != nil {
		t.Fatal(err)
	}
	for _, item := range store.List() {
		if item.WebRID == "688220427462" && item.FollowingLive {
			t.Fatalf("room should become offline after every source goes offline: %+v", item)
		}
	}
}
