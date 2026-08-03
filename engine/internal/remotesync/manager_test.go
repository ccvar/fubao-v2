package remotesync

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fubao.ccvar.com/engine/internal/redpacket"
	"fubao.ccvar.com/engine/internal/rooms"
	"fubao.ccvar.com/engine/internal/syncprotocol"
	"fubao.ccvar.com/engine/internal/syncserver"
)

func TestSnapshotOutboxContainsOnlySafeRoomAndPacketData(t *testing.T) {
	dataDir := t.TempDir()
	config := Config{Version: configVersion, Enabled: true, Endpoint: syncprotocol.DefaultEndpoint, DeviceToken: "device-test-token"}
	content, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(dataDir, "remote_sync.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err = manager.SyncSnapshot(
		[]rooms.Room{{ID: "room-local", WebRID: "123456", ActualRoomID: "actual-1", Name: "直播间", StreamerName: "主播", UpdatedAt: now}},
		[]redpacket.Monitor{{ID: "room_room-local", RoomID: "room-local", WebRID: "123456", LiveStatus: "live", AccountID: "secret-account", AccountName: "不应上传", UpdatedAt: now}},
		[]redpacket.Event{{ID: "room_room-local:packet-1", WebRID: "123456", PacketID: "packet-1", AccountID: "secret-account", AccountName: "不应上传", Title: "红包", DetectedAt: now, ActualRoomID: "private-actual", JoinBoxID: "private-box", AnchorID: "private-anchor"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := os.ReadFile(filepath.Join(dataDir, "remote_sync_outbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(outbox)
	for _, forbidden := range []string{"secret-account", "不应上传", "private-box", "private-anchor"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("outbox exposed forbidden value %q: %s", forbidden, text)
		}
	}
	var stored outboxFile
	if err := json.Unmarshal(outbox, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Items) != 2 {
		t.Fatalf("outbox items = %d, want 2", len(stored.Items))
	}
}

func TestUnconfiguredManagerStillQueuesSafeUploads(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := manager.EnqueueEvent(redpacket.Event{WebRID: "123456", PacketID: "packet", DetectedAt: now}); err != nil {
		t.Fatal(err)
	}
	if manager.Status().Pending != 1 {
		t.Fatal("unconfigured remote sync did not queue its safe upload")
	}
}

func TestExistingConfigReceivesDefaultFallbackEndpoint(t *testing.T) {
	dataDir := t.TempDir()
	content := []byte(`{"version":1,"enabled":false,"endpoint":"https://fbv2.ccvar.com/api/v1","device_token":"device-test-token"}`)
	if err := os.WriteFile(filepath.Join(dataDir, "remote_sync.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	status := manager.Status()
	if got := status.FallbackEndpoint; got != syncprotocol.DefaultFallbackEndpoint {
		t.Fatalf("fallback endpoint = %q, want %q", got, syncprotocol.DefaultFallbackEndpoint)
	}
	if !status.Enabled {
		t.Fatal("configured full-access center connection was not automatically enabled")
	}
	if status.TokenMasked != "devi…oken" {
		t.Fatalf("masked token = %q, want %q", status.TokenMasked, "devi…oken")
	}
	statusContent, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(statusContent), "device-test-token") {
		t.Fatalf("remote sync status exposed the raw token: %s", statusContent)
	}
}

func TestMaskTokenNeverReturnsShortTokenVerbatim(t *testing.T) {
	if got := maskToken("secret"); got != "••••••" {
		t.Fatalf("short token mask = %q, want a fixed mask", got)
	}
}

func TestUnchangedSnapshotKeepsStableOutboxPayload(t *testing.T) {
	dataDir := t.TempDir()
	config := Config{Version: configVersion, Enabled: true, Endpoint: syncprotocol.DefaultEndpoint, DeviceToken: "device-test-token"}
	content, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(dataDir, "remote_sync.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	updatedAt := "2026-08-02T09:30:00Z"
	roomItems := []rooms.Room{{ID: "room-local", WebRID: "123456", Name: "直播间", UpdatedAt: updatedAt}}
	monitorItems := []redpacket.Monitor{{ID: "room_room-local", RoomID: "room-local", WebRID: "123456", LiveStatus: "live", UpdatedAt: updatedAt}}
	if err := manager.SyncSnapshot(roomItems, monitorItems, nil); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(dataDir, "remote_sync_outbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := manager.SyncSnapshot(roomItems, monitorItems, nil); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(dataDir, "remote_sync_outbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("unchanged snapshot rewrote its queued payload\nfirst: %s\nsecond: %s", first, second)
	}
}

func TestManagerRegistersAndFlushesToServer(t *testing.T) {
	store, err := syncserver.OpenStore(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enrollmentToken := "0123456789abcdef0123456789abcdef"
	service, err := syncserver.New(store, enrollmentToken, "test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(service.Handler())
	defer server.Close()

	dataDir := t.TempDir()
	config := Config{Version: configVersion, Enabled: true, Endpoint: server.URL + "/api/v1", EnrollmentToken: enrollmentToken}
	content, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(dataDir, "remote_sync.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	manager.httpClient = server.Client()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := manager.EnqueueEvent(redpacket.Event{WebRID: "123456", PacketID: "packet-1", Title: "钻石红包", DetectedAt: now}); err != nil {
		t.Fatal(err)
	}
	manager.attempt(context.Background())
	status := manager.Status()
	if status.Pending != 0 || status.LastError != "" || status.LastSuccessAt == "" {
		t.Fatalf("unexpected manager status after flush: %+v", status)
	}
	stats, err := store.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Devices != 1 || stats.RedPacket != 1 {
		t.Fatalf("unexpected server stats: %+v", stats)
	}
	configContent, err := os.ReadFile(filepath.Join(dataDir, "remote_sync.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configContent), enrollmentToken) || !strings.Contains(string(configContent), "device_token") {
		t.Fatalf("enrollment token was not replaced with a device token: %s", configContent)
	}
	if status.TokenMasked != "0123…cdef" {
		t.Fatalf("masked enrollment token = %q, want %q", status.TokenMasked, "0123…cdef")
	}
}

func TestManagerAutoRegistersUploadOnlyWithoutSyncKey(t *testing.T) {
	store, err := syncserver.OpenStore(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := syncserver.New(store, "0123456789abcdef0123456789abcdef", "test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(service.Handler())
	defer server.Close()

	dataDir := t.TempDir()
	content, _ := json.Marshal(Config{Version: configVersion, Endpoint: server.URL + "/api/v1", FallbackEndpoint: server.URL + "/api/v1"})
	if err := os.WriteFile(filepath.Join(dataDir, "remote_sync.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	manager.httpClient = server.Client()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := manager.EnqueueEvent(redpacket.Event{WebRID: "987654", PacketID: "upload-only", Title: "钻石红包", DetectedAt: now}); err != nil {
		t.Fatal(err)
	}
	manager.attempt(context.Background())
	status := manager.Status()
	if status.Pending != 0 || !status.UploadOnly || status.Configured || status.Enabled || status.ActiveEndpoint == "" {
		t.Fatalf("unexpected upload-only status: %+v", status)
	}
	stats, err := store.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Devices != 1 || stats.RedPacket != 1 {
		t.Fatalf("automatic upload did not reach center: %+v", stats)
	}
	roomStore, err := rooms.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	packetStore, err := redpacket.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.PullOnceScoped(context.Background(), roomStore, packetStore, PullAll); err != nil {
		t.Fatal(err)
	}
	if len(roomStore.List()) != 0 || len(packetStore.EventsAll()) != 0 {
		t.Fatal("upload-only device received center data")
	}
}

func TestManagerFallsBackWhenPrimaryHealthIsUnavailable(t *testing.T) {
	store, err := syncserver.OpenStore(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enrollmentToken := "0123456789abcdef0123456789abcdef"
	service, err := syncserver.New(store, enrollmentToken, "test")
	if err != nil {
		t.Fatal(err)
	}
	fallbackServer := httptest.NewTLSServer(service.Handler())
	defer fallbackServer.Close()

	dataDir := t.TempDir()
	config := Config{
		Version: configVersion, Enabled: true,
		Endpoint: "https://127.0.0.1:1/api/v1", FallbackEndpoint: fallbackServer.URL + "/api/v1",
		EnrollmentToken: enrollmentToken,
	}
	content, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(dataDir, "remote_sync.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	manager.httpClient = fallbackServer.Client()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := manager.EnqueueEvent(redpacket.Event{WebRID: "654321", PacketID: "fallback-packet", DetectedAt: now}); err != nil {
		t.Fatal(err)
	}
	manager.attempt(context.Background())
	status := manager.Status()
	wantEndpoint := fallbackServer.URL + "/api/v1"
	if status.ActiveEndpoint != wantEndpoint || status.Pending != 0 || status.LastError != "" {
		t.Fatalf("manager did not switch to fallback endpoint: %+v", status)
	}
	stats, err := store.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Devices != 1 || stats.RedPacket != 1 {
		t.Fatalf("fallback server did not receive the batch: %+v", stats)
	}
}

func TestConfigureRegistersAndCanDisableRemoteSync(t *testing.T) {
	store, err := syncserver.OpenStore(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enrollmentToken := "0123456789abcdef0123456789abcdef"
	service, err := syncserver.New(store, enrollmentToken, "test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(service.Handler())
	defer server.Close()

	dataDir := t.TempDir()
	config := Config{
		Version: configVersion, Endpoint: server.URL + "/api/v1",
		FallbackEndpoint: server.URL + "/api/v1",
	}
	content, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(dataDir, "remote_sync.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	manager.httpClient = server.Client()
	status, err := manager.Configure(context.Background(), true, enrollmentToken)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || !status.Configured || status.ActiveEndpoint != server.URL+"/api/v1" {
		t.Fatalf("unexpected configured status: %+v", status)
	}
	configContent, err := os.ReadFile(filepath.Join(dataDir, "remote_sync.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configContent), enrollmentToken) || !strings.Contains(string(configContent), "device_token") {
		t.Fatalf("configured enrollment token was not exchanged safely: %s", configContent)
	}
	status, err = manager.Configure(context.Background(), false, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || !status.Configured {
		t.Fatalf("disabling should retain the device registration: %+v", status)
	}
}

func TestConfigureRejectsNewTokenWithoutReplacingWorkingRegistration(t *testing.T) {
	dataDir := t.TempDir()
	config := Config{
		Version: configVersion, Enabled: true,
		Endpoint: "https://127.0.0.1:1/api/v1", FallbackEndpoint: "https://127.0.0.1:2/api/v1",
		DeviceToken: "working-device-token",
	}
	content, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(dataDir, "remote_sync.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Configure(context.Background(), true, "0123456789abcdef0123456789abcdef")
	if err == nil {
		t.Fatal("expected the unavailable server to reject the replacement token")
	}
	configContent, err := os.ReadFile(filepath.Join(dataDir, "remote_sync.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configContent), "0123456789abcdef0123456789abcdef") || !strings.Contains(string(configContent), "working-device-token") {
		t.Fatalf("failed replacement changed the working registration: %s", configContent)
	}
}

func TestPullOnceImportsOtherClientDataWithoutEchoingIt(t *testing.T) {
	serverStore, err := syncserver.OpenStore(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer serverStore.Close()
	enrollmentToken := "0123456789abcdef0123456789abcdef"
	service, err := syncserver.New(serverStore, enrollmentToken, "test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(service.Handler())
	defer server.Close()

	newManager := func() *Manager {
		dataDir := t.TempDir()
		content, _ := json.Marshal(Config{Version: configVersion, Endpoint: server.URL + "/api/v1", FallbackEndpoint: server.URL + "/api/v1"})
		if err := os.WriteFile(filepath.Join(dataDir, "remote_sync.json"), content, 0o600); err != nil {
			t.Fatal(err)
		}
		manager, err := New(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		manager.httpClient = server.Client()
		if _, err := manager.Configure(context.Background(), true, enrollmentToken); err != nil {
			t.Fatal(err)
		}
		return manager
	}
	managerA, managerB := newManager(), newManager()
	roomStoreA, err := rooms.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roomStoreA.ImportIDs("123456789012"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := managerA.SyncSnapshot(roomStoreA.List(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := managerA.EnqueueEvent(redpacket.Event{
		WebRID: "123456789012", RoomID: "123456789012", PacketID: "packet-center",
		ActualRoomID: "7000000000000000001", JoinBoxID: "7669047909329177395",
		AnchorID: "1234567890", BoxType: "1", SendTime: "100", DelayTime: "30",
		Title: "钻石红包", Prize: "总99钻，24份红包", Source: "luckybox_api", DetectedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	managerA.attempt(context.Background())

	roomStoreB, err := rooms.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	redPacketStoreB, err := redpacket.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := managerB.PullOnceScoped(context.Background(), roomStoreB, redPacketStoreB, PullRedPackets); err != nil {
		t.Fatal(err)
	}
	if roomsB := roomStoreB.List(); len(roomsB) != 0 {
		t.Fatalf("finite-license pull imported center rooms: %+v", roomsB)
	}
	eventsB := redPacketStoreB.EventsAll()
	if len(eventsB) != 1 || eventsB[0].DataSource != "center" || eventsB[0].PacketID != "packet-center" ||
		eventsB[0].ActualRoomID != "7000000000000000001" || eventsB[0].JoinBoxID != "7669047909329177395" {
		t.Fatalf("finite-license pull did not import center packet: %+v", eventsB)
	}
	if err := managerB.PullOnceScoped(context.Background(), roomStoreB, redPacketStoreB, PullAll); err != nil {
		t.Fatal(err)
	}
	roomsB := roomStoreB.List()
	if len(roomsB) != 1 || roomsB[0].Source != "center" {
		t.Fatalf("other-client room was not marked as center data: %+v", roomsB)
	}
	eventsB = redPacketStoreB.EventsAll()
	if len(eventsB) != 1 || eventsB[0].DataSource != "center" || eventsB[0].PacketID != "packet-center" {
		t.Fatalf("other-client packet was not marked as center data: %+v", eventsB)
	}
	if err := managerB.SyncSnapshot(roomsB, redPacketStoreB.List(), eventsB); err != nil {
		t.Fatal(err)
	}
	if managerB.Status().Pending != 0 {
		t.Fatalf("center data was echoed back into the upload queue: %+v", managerB.Status())
	}
}
