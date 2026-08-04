package rooms

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRoomSettingsDefaultAndDisabledValuePersist(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Settings().AutoRecycleOfflineDays; got != 7 {
		t.Fatalf("new stores must default to 7 days, got %d", got)
	}
	if got := store.Settings().ParticipationPrewarmMinutes; got != 10 {
		t.Fatalf("new stores must prewarm participation monitoring 10 minutes early, got %d", got)
	}
	if _, err := store.SetSettings(Settings{AutoRecycleOfflineDays: 0, ParticipationPrewarmMinutes: 35}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Settings().AutoRecycleOfflineDays; got != 0 {
		t.Fatalf("zero must persist as disabled, got %d", got)
	}
	if got := reloaded.Settings().ParticipationPrewarmMinutes; got != 35 {
		t.Fatalf("participation prewarm setting must persist, got %d", got)
	}
}

func TestVersionFourRoomSettingsGainPrewarmDefault(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "rooms.json")
	if err := os.WriteFile(path, []byte(`{"version":4,"settings":{"auto_recycle_offline_days":0},"rooms":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	settings := store.Settings()
	if settings.AutoRecycleOfflineDays != 0 || settings.ParticipationPrewarmMinutes != 10 {
		t.Fatalf("version 4 migration lost settings: %+v", settings)
	}
}

func TestOfflineDaysRecycleRestoreAndPermanentDelete(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIDs("123456789012"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSettings(Settings{AutoRecycleOfflineDays: 2}); err != nil {
		t.Fatal(err)
	}
	location := time.FixedZone("CST", 8*60*60)
	first := time.Date(2026, 8, 1, 9, 0, 0, 0, location)
	recycled, err := store.RecordLiveResult("123456789012", "offline", first)
	if err != nil || recycled {
		t.Fatalf("first offline day must remain active, recycled=%v err=%v", recycled, err)
	}
	// Repeated successful probes on one calendar day count only once.
	if recycled, err = store.RecordLiveResult("123456789012", "offline", first.Add(8*time.Hour)); err != nil || recycled {
		t.Fatalf("same day must not advance the streak, recycled=%v err=%v", recycled, err)
	}
	if got := store.List()[0].OfflineDays; got != 1 {
		t.Fatalf("expected one offline day, got %d", got)
	}
	// Unknown and errors are not definitive evidence and must be ignored.
	if recycled, err = store.RecordLiveResult("123456789012", "error", first.Add(24*time.Hour)); err != nil || recycled {
		t.Fatalf("error must be ignored, recycled=%v err=%v", recycled, err)
	}
	// A positive live probe resets the room-specific streak.
	if recycled, err = store.RecordLiveResult("123456789012", "live", first.Add(24*time.Hour)); err != nil || recycled {
		t.Fatalf("live result must reset without recycling, recycled=%v err=%v", recycled, err)
	}
	if got := store.List()[0].OfflineDays; got != 0 {
		t.Fatalf("live result must reset the streak, got %d", got)
	}
	if _, err := store.RecordLiveResult("123456789012", "offline", first.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	recycled, err = store.RecordLiveResult("123456789012", "offline", first.Add(72*time.Hour))
	if err != nil || !recycled {
		t.Fatalf("second distinct offline day must recycle, recycled=%v err=%v", recycled, err)
	}
	if len(store.List()) != 0 || len(store.All()) != 1 || len(store.RecycleBin()) != 1 {
		t.Fatalf("recycled room must leave active list but remain canonical")
	}
	archived := store.RecycleBin()[0]
	if archived.Enabled || archived.RecycleReason == "" || archived.RecycledAt == "" {
		t.Fatalf("recycle metadata is incomplete: %+v", archived)
	}
	restored, err := store.Restore(archived.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Recycled || !restored.Enabled || restored.OfflineDays != 0 || restored.MonitorStatus != "stopped" {
		t.Fatalf("restore must clear archive state without starting monitoring: %+v", restored)
	}
	if len(store.List()) != 1 || len(store.RecycleBin()) != 0 {
		t.Fatal("restored room must return to the active list")
	}
	if _, err := store.SetSettings(Settings{AutoRecycleOfflineDays: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordLiveResult(restored.ID, "offline", first.Add(96*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRecycled(restored.ID); err != nil {
		t.Fatal(err)
	}
	if len(store.All()) != 0 {
		t.Fatal("permanent delete must remove the canonical room")
	}
}

func TestAutoRecycleDisabledNeverArchives(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIDs("987654321012"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSettings(Settings{AutoRecycleOfflineDays: 0}); err != nil {
		t.Fatal(err)
	}
	for day := 1; day <= 30; day++ {
		recycled, err := store.RecordLiveResult("987654321012", "offline", time.Date(2026, 8, day, 12, 0, 0, 0, time.Local))
		if err != nil || recycled {
			t.Fatalf("disabled policy must never recycle on day %d, recycled=%v err=%v", day, recycled, err)
		}
	}
	if len(store.List()) != 1 || len(store.RecycleBin()) != 0 {
		t.Fatal("disabled policy removed an active room")
	}
}

func TestLowLiveCleanupSkipsUnknownAndRecyclesLocalRoom(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIDs("777777777777"); err != nil {
		t.Fatal(err)
	}
	settings := store.Settings()
	settings.AutoRecycleOfflineDays = 0
	settings.AutoRecycleLowLiveEnabled = true
	settings.AutoRecycleMaxLiveSessions = 0
	if _, err := store.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 8, 4, 10, 0, 0, 0, time.Local)
	if cleaned, err := store.RecordLiveResult("777777777777", "unknown", when); err != nil || cleaned {
		t.Fatalf("unknown room must be skipped, cleaned=%v err=%v", cleaned, err)
	}
	if len(store.List()) != 1 {
		t.Fatal("a room without a definitive probe was removed")
	}
	if cleaned, err := store.RecordLiveResult("777777777777", "offline", when); err != nil || !cleaned {
		t.Fatalf("definitively offline room with zero live sessions must recycle, cleaned=%v err=%v", cleaned, err)
	}
	if len(store.List()) != 0 || len(store.RecycleBin()) != 1 {
		t.Fatal("local room must enter the recoverable recycle bin")
	}
}

func TestCenterOnlyCleanupPersistsExclusionUntilRestored(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	item := CenterRoom{WebRID: "888888888888", Title: "中心库直播间", MetricsVersion: 1, LiveSessionCount: 0, CenterUpdatedAt: "2026-08-04T09:00:00+08:00"}
	if _, err := store.MergeCenter([]CenterRoom{item}); err != nil {
		t.Fatal(err)
	}
	settings := store.Settings()
	settings.AutoRecycleOfflineDays = 0
	settings.AutoRecycleLowLiveEnabled = true
	settings.AutoRecycleMaxLiveSessions = 0
	if _, err := store.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	if cleaned, err := store.RecordLiveResult(item.WebRID, "offline", time.Now()); err != nil || !cleaned {
		t.Fatalf("center-only room was not cleaned, cleaned=%v err=%v", cleaned, err)
	}
	if len(store.All()) != 0 || len(store.RecycleBin()) != 0 {
		t.Fatal("center-only room must be deleted locally instead of archived")
	}
	if exclusions := store.CenterExclusions(); len(exclusions) != 1 || exclusions[0].WebRID != item.WebRID {
		t.Fatalf("center cleanup must create one exclusion: %+v", exclusions)
	}
	if pending := store.PendingCenterExclusions(); len(pending) != 1 || !pending[0].SyncPending {
		t.Fatalf("new local exclusion must wait for center acknowledgement: %+v", pending)
	}
	if result, err := store.MergeCenter([]CenterRoom{item}); err != nil || result.Excluded != 1 || result.Imported != 0 {
		t.Fatalf("excluded room must be skipped by later center sync: %+v err=%v", result, err)
	}
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.CenterExclusions()) != 1 {
		t.Fatal("center exclusion must persist across restarts")
	}
	restored, err := reloaded.RestoreCenterExclusion(item.WebRID)
	if err != nil || restored.WebRID != item.WebRID {
		t.Fatalf("center exclusion was not restored: %+v err=%v", restored, err)
	}
	if len(reloaded.CenterExclusions()) != 0 || len(reloaded.List()) != 1 {
		t.Fatal("restoring exclusion must recreate the stopped local room")
	}
}

func TestCenterLowLiveCleanupRequiresAuthoritativeMetrics(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item := CenterRoom{WebRID: "888888888889", Title: "旧协议中心直播间", CenterUpdatedAt: "2026-08-04T09:00:00+08:00"}
	if _, err := store.MergeCenter([]CenterRoom{item}); err != nil {
		t.Fatal(err)
	}
	settings := store.Settings()
	settings.AutoRecycleOfflineDays = 0
	settings.AutoRecycleLowLiveEnabled = true
	settings.AutoRecycleMaxLiveSessions = 0
	if _, err := store.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	if cleaned, err := store.RecordLiveResult(item.WebRID, "offline", time.Now()); err != nil || cleaned {
		t.Fatalf("center room without authoritative metrics must not be cleaned: cleaned=%v err=%v", cleaned, err)
	}
	if len(store.All()) != 1 || len(store.CenterExclusions()) != 0 {
		t.Fatal("unknown center metrics created a destructive exclusion")
	}
	item.MetricsVersion = 1
	item.LiveSessionCount = 4
	if _, err := store.MergeCenter([]CenterRoom{item}); err != nil {
		t.Fatal(err)
	}
	if cleaned, err := store.RecordLiveResult(item.WebRID, "offline", time.Now().Add(time.Hour)); err != nil || cleaned {
		t.Fatalf("center authoritative live count above threshold must be retained: cleaned=%v err=%v", cleaned, err)
	}
}

func TestExecuteCleanupUsesDefinitiveStateAndSupportsPaging(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIDs("710000000001"); err != nil {
		t.Fatal(err)
	}
	checkedAt := time.Date(2026, 8, 4, 10, 0, 0, 0, time.Local)
	if cleaned, err := store.RecordLiveResult("710000000001", "offline", checkedAt); err != nil || cleaned {
		t.Fatalf("room should only collect definitive evidence before the rule is enabled: cleaned=%v err=%v", cleaned, err)
	}
	center := CenterRoom{
		WebRID:           "710000000002",
		Title:            "中心库待清理直播间",
		LiveStatus:       "offline",
		MetricsVersion:   1,
		LiveSessionCount: 0,
		CenterUpdatedAt:  checkedAt.Format(time.RFC3339Nano),
	}
	if _, err := store.MergeCenter([]CenterRoom{center}); err != nil {
		t.Fatal(err)
	}
	unknown := CenterRoom{WebRID: "710000000003", Title: "中心库未知直播间", MetricsVersion: 1, CenterUpdatedAt: checkedAt.Format(time.RFC3339Nano)}
	if _, err := store.MergeCenter([]CenterRoom{unknown}); err != nil {
		t.Fatal(err)
	}
	settings := store.Settings()
	settings.AutoRecycleOfflineDays = 0
	settings.AutoRecycleLowLiveEnabled = true
	settings.AutoRecycleMaxLiveSessions = 0
	if _, err := store.SetSettings(settings); err != nil {
		t.Fatal(err)
	}

	first, err := store.ExecuteCleanup("", 1, checkedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if first.Scanned != 1 || !first.HasMore || first.Cleaned != 1 {
		t.Fatalf("unexpected first cleanup page: %+v", first)
	}
	second, err := store.ExecuteCleanup(first.NextCursor, 10, checkedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second.Scanned != 2 || second.HasMore || first.Cleaned+second.Cleaned != 2 || first.Excluded+second.Excluded != 1 || first.Skipped+second.Skipped != 1 {
		t.Fatalf("unexpected second cleanup page: %+v", second)
	}
	if len(store.RecycleBin()) != 1 || len(store.CenterExclusions()) != 1 || len(store.List()) != 1 {
		t.Fatalf("unexpected cleanup result: active=%+v recycled=%+v exclusions=%+v", store.List(), store.RecycleBin(), store.CenterExclusions())
	}
}

func TestGlobalCenterExclusionsRemoveCenterRoomsWithoutResurrection(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item := CenterRoom{WebRID: "887777777777", ActualRoomID: "7000000000000000007", Title: "全局垃圾直播间"}
	if _, err := store.MergeCenter([]CenterRoom{item}); err != nil {
		t.Fatal(err)
	}
	exclusion := CenterExclusion{ID: item.WebRID, WebRID: item.WebRID, ActualRoomID: item.ActualRoomID, Reason: "中心全局排除", ExcludedAt: time.Now().Format(time.RFC3339Nano)}
	if err := store.MergeGlobalCenterExclusions([]CenterExclusion{exclusion}); err != nil {
		t.Fatal(err)
	}
	if len(store.All()) != 0 || len(store.CenterExclusions()) != 1 || len(store.PendingCenterExclusions()) != 0 {
		t.Fatalf("global exclusion was not applied: rooms=%+v exclusions=%+v", store.All(), store.CenterExclusions())
	}
	if result, err := store.MergeCenter([]CenterRoom{item}); err != nil || result.Excluded != 1 {
		t.Fatalf("global exclusion allowed center resurrection: result=%+v err=%v", result, err)
	}
	if err := store.MergeGlobalCenterExclusions(nil); err != nil {
		t.Fatal(err)
	}
	if len(store.CenterExclusions()) != 0 {
		t.Fatalf("server-authoritative restore was not reconciled: %+v", store.CenterExclusions())
	}
}

func TestPermanentDeleteCenterLinkedRoomCreatesExclusion(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item := CenterRoom{WebRID: "889999999999", ActualRoomID: "778888888888", Title: "待删除中心房间"}
	if _, err := store.MergeCenter([]CenterRoom{item}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIDs(item.WebRID); err != nil {
		t.Fatal(err)
	}
	settings := store.Settings()
	settings.AutoRecycleOfflineDays = 0
	settings.AutoRecycleLowLiveEnabled = true
	settings.AutoRecycleMaxLiveSessions = 0
	if _, err := store.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	if cleaned, err := store.RecordLiveResult(item.WebRID, "offline", time.Now()); err != nil || !cleaned {
		t.Fatalf("center-linked local room must enter recycle bin: cleaned=%v err=%v", cleaned, err)
	}
	if err := store.DeleteRecycled(item.WebRID); err != nil {
		t.Fatal(err)
	}
	if exclusions := store.CenterExclusions(); len(exclusions) != 1 || exclusions[0].ActualRoomID != item.ActualRoomID {
		t.Fatalf("permanent delete must retain center exclusion metadata: %+v", exclusions)
	}
}

func TestNoPacketCleanupUsesFirstProbeAndLatestPacket(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIDs("999999999999"); err != nil {
		t.Fatal(err)
	}
	settings := store.Settings()
	settings.AutoRecycleOfflineDays = 0
	settings.AutoRecycleNoPacketEnabled = true
	settings.AutoRecycleNoPacketDays = 3
	if _, err := store.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 8, 1, 10, 0, 0, 0, time.Local)
	if cleaned, err := store.RecordLiveResult("999999999999", "offline", first); err != nil || cleaned {
		t.Fatalf("first definitive probe starts the observation window: cleaned=%v err=%v", cleaned, err)
	}
	packetAt := first.Add(48 * time.Hour)
	if err := store.RecordRedPacketEvent("999999999999", packetAt); err != nil {
		t.Fatal(err)
	}
	if cleaned, err := store.RecordLiveResult("999999999999", "offline", first.Add(96*time.Hour)); err != nil || cleaned {
		t.Fatalf("recent packet must reset the no-packet window: cleaned=%v err=%v", cleaned, err)
	}
	if cleaned, err := store.RecordLiveResult("999999999999", "offline", packetAt.Add(72*time.Hour)); err != nil || !cleaned {
		t.Fatalf("three full days without a packet must recycle: cleaned=%v err=%v", cleaned, err)
	}
}

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

func TestImportIDsHandlesLargeBatchesWithoutQuadraticLookup(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	values := make([]string, 1200)
	for index := range values {
		values[index] = strconv.FormatInt(473000000000+int64(index), 10)
	}
	result, err := store.ImportIDs(strings.Join(values, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != len(values) || result.Total != len(values) {
		t.Fatalf("unexpected large import result: %+v", result)
	}
	result, err = store.ImportIDs(strings.Join(values[:100], "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Merged != 100 || result.Total != len(values) {
		t.Fatalf("expected indexed duplicate merge: %+v", result)
	}
}

func TestImportIDsBatchPersistsOnlyFinalChunkAndPagesResults(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIDsBatch("473000000001\n473000000002", false); err != nil {
		t.Fatal(err)
	}
	beforeFinal, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if beforeFinal.CountAll() != 0 {
		t.Fatal("intermediate import chunks must not rewrite the room store")
	}
	if _, err := store.ImportIDsBatch("473000000003", true); err != nil {
		t.Fatal(err)
	}
	page := store.Page(0, 2, "")
	if page.Total != 3 || len(page.Items) != 2 {
		t.Fatalf("unexpected first page: %+v", page)
	}
	filtered := store.Page(0, 10, "000003")
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].WebRID != "473000000003" {
		t.Fatalf("unexpected filtered page: %+v", filtered)
	}
}

func TestPageKeepsHundredThousandRoomResponseBounded(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	values := make([]string, 100000)
	for index := range values {
		values[index] = strconv.FormatInt(600000000000+int64(index), 10)
	}
	if _, err := store.ImportIDsBatch(strings.Join(values, "\n"), false); err != nil {
		t.Fatal(err)
	}
	page := store.Page(0, 300, "")
	if page.Total != len(values) || len(page.Items) != 300 {
		t.Fatalf("high-volume page must return 300/%d rows, got %d/%d", len(values), len(page.Items), page.Total)
	}
}

func TestPageBySourceFiltersBeforePagination(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Format(time.RFC3339Nano)
	store.mu.Lock()
	store.rooms = map[string]*Room{
		"100001": {ID: "100001", WebRID: "100001", Source: "manual", CreatedAt: now, UpdatedAt: now},
		"100002": {ID: "100002", WebRID: "100002", Source: "dy-kiro", CreatedAt: now, UpdatedAt: now},
		"100003": {ID: "100003", WebRID: "100003", Source: "center", CreatedAt: now, UpdatedAt: now},
		"100004": {
			ID: "100004", WebRID: "100004", Source: "manual", CreatedAt: now, UpdatedAt: now,
			FollowSources: []FollowSource{{AccountID: "account-1", AccountName: "关注账号"}},
		},
	}
	store.mu.Unlock()

	tests := []struct {
		source string
		want   []string
	}{
		{source: "imported", want: []string{"100001", "100002"}},
		{source: "center", want: []string{"100003"}},
		{source: "following", want: []string{"100004"}},
	}
	for _, test := range tests {
		page := store.PageBySource(0, 10, "", test.source)
		if page.Total != len(test.want) || len(page.Items) != len(test.want) {
			t.Fatalf("source %q returned %d/%d rooms, want %d", test.source, len(page.Items), page.Total, len(test.want))
		}
		for index, id := range test.want {
			if page.Items[index].ID != id {
				t.Fatalf("source %q item %d=%q, want %q", test.source, index, page.Items[index].ID, id)
			}
		}
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
