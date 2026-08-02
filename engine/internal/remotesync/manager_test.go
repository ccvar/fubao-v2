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

func TestDisabledManagerDoesNotQueue(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := manager.EnqueueEvent(redpacket.Event{WebRID: "123456", PacketID: "packet", DetectedAt: now}); err != nil {
		t.Fatal(err)
	}
	if manager.Status().Pending != 0 {
		t.Fatal("disabled remote sync queued an event")
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
}
