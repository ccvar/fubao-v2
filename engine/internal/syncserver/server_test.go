package syncserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"fubao.ccvar.com/engine/internal/syncprotocol"
)

func TestRegisterAndSyncBatch(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := New(store, "0123456789abcdef0123456789abcdef", "test")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()

	register := syncprotocol.RegisterRequest{Version: syncprotocol.Version, ClientID: "desktop-test", ClientName: "测试客户端", Platform: "darwin/arm64"}
	var registration syncprotocol.RegisterResponse
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/devices/register", "0123456789abcdef0123456789abcdef", register, http.StatusCreated, &registration)
	if registration.DeviceToken == "" || registration.AccessMode != syncprotocol.DeviceAccessFull {
		t.Fatal("device token was empty")
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	roomPayload, _ := json.Marshal(syncprotocol.RoomState{WebRID: "123456", Title: "测试直播间", LiveStatus: "live", UpdatedAt: now})
	packetPayload, _ := json.Marshal(syncprotocol.RedPacket{
		WebRID: "123456", PacketID: "packet-1", ActualRoomID: "7000000000000000001",
		JoinBoxID: "7669047909329177395", AnchorID: "1234567890", BoxType: "1", SendTime: "100", DelayTime: "30",
		Title: "钻石红包", Prize: "总99钻，24份红包", DetectedAt: now, TotalDiamonds: 99, ShareCount: 24,
	})
	batch := syncprotocol.BatchRequest{
		Version: syncprotocol.Version, RequestID: "request-1", ClientID: registration.ClientID, SentAt: now,
		Items: []syncprotocol.BatchItem{
			{Type: syncprotocol.ItemRoomState, IdempotencyKey: "room:123456", OccurredAt: now, Payload: roomPayload},
			{Type: syncprotocol.ItemRedPacket, IdempotencyKey: "red_packet:123456:packet-1", OccurredAt: now, Payload: packetPayload},
		},
	}
	var first syncprotocol.BatchResponse
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/sync/batch", registration.DeviceToken, batch, http.StatusOK, &first)
	if first.Accepted != 2 || first.Duplicate {
		t.Fatalf("unexpected first response: %+v", first)
	}
	var duplicate syncprotocol.BatchResponse
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/sync/batch", registration.DeviceToken, batch, http.StatusOK, &duplicate)
	if !duplicate.Duplicate {
		t.Fatal("repeated request was not idempotent")
	}
	stats, err := store.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Devices != 1 || stats.Rooms != 1 || stats.RedPacket != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	var changes syncprotocol.ChangesResponse
	requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/v1/sync/changes?cursor=0&limit=200", registration.DeviceToken, nil, http.StatusOK, &changes)
	if changes.Version != syncprotocol.Version || changes.NextCursor == 0 || len(changes.Changes) != 2 {
		t.Fatalf("unexpected center changes: %+v", changes)
	}
	if changes.Changes[0].OriginClientID != registration.ClientID || changes.Changes[1].OriginClientID != registration.ClientID {
		t.Fatalf("change origin was not retained: %+v", changes.Changes)
	}
	var syncedPacket syncprotocol.RedPacket
	for _, change := range changes.Changes {
		if change.Type == syncprotocol.ItemRedPacket {
			if err := json.Unmarshal(change.Payload, &syncedPacket); err != nil {
				t.Fatal(err)
			}
		}
	}
	if syncedPacket.ActualRoomID != "7000000000000000001" || syncedPacket.JoinBoxID != "7669047909329177395" || syncedPacket.DelayTime != "30" {
		t.Fatalf("native participation metadata was not retained by center: %+v", syncedPacket)
	}
	batch.RequestID = "request-2"
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/sync/batch", registration.DeviceToken, batch, http.StatusOK, &first)
	var unchanged syncprotocol.ChangesResponse
	requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/v1/sync/changes?cursor=0&limit=200", registration.DeviceToken, nil, http.StatusOK, &unchanged)
	if len(unchanged.Changes) != 2 {
		t.Fatalf("unchanged snapshot created duplicate center changes: %+v", unchanged.Changes)
	}
}

func TestUploadOnlyRegistrationCanWriteButCannotReadChanges(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := New(store, "0123456789abcdef0123456789abcdef", "test")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()

	register := syncprotocol.RegisterRequest{Version: syncprotocol.Version, ClientID: "desktop_upload_test", ClientName: "仅上传客户端"}
	var registration syncprotocol.RegisterResponse
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/devices/register-upload", "", register, http.StatusCreated, &registration)
	if registration.AccessMode != syncprotocol.DeviceAccessUploadOnly || registration.DeviceToken == "" {
		t.Fatalf("unexpected upload registration: %+v", registration)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	packetPayload, _ := json.Marshal(syncprotocol.RedPacket{WebRID: "654321", PacketID: "packet-upload", DetectedAt: now})
	batch := syncprotocol.BatchRequest{
		Version: syncprotocol.Version, RequestID: "upload-request", ClientID: registration.ClientID, SentAt: now,
		Items: []syncprotocol.BatchItem{{Type: syncprotocol.ItemRedPacket, IdempotencyKey: "red_packet:654321:packet-upload", OccurredAt: now, Payload: packetPayload}},
	}
	var batchResponse syncprotocol.BatchResponse
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/sync/batch", registration.DeviceToken, batch, http.StatusOK, &batchResponse)
	var denied map[string]any
	requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/v1/sync/changes?cursor=0&limit=200", registration.DeviceToken, nil, http.StatusForbidden, &denied)
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/rooms/exclusions", registration.DeviceToken,
		syncprotocol.CenterRoomExclusionsRequest{Items: []syncprotocol.CenterRoomExclusion{{WebRID: "654321", ExcludedAt: now}}},
		http.StatusForbidden, &denied)
	stats, err := store.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Devices != 1 || stats.RedPacket != 1 {
		t.Fatalf("upload-only batch was not persisted: %+v", stats)
	}
}

func TestCenterExclusionDeletesDataAndRejectsLaterUploads(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enrollmentToken := "0123456789abcdef0123456789abcdef"
	service, err := New(store, enrollmentToken, "test")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()

	register := syncprotocol.RegisterRequest{Version: syncprotocol.Version, ClientID: "desktop-exclusion-test"}
	var registration syncprotocol.RegisterResponse
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/devices/register", enrollmentToken, register, http.StatusCreated, &registration)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	roomPayload, _ := json.Marshal(syncprotocol.RoomState{WebRID: "123456", ActualRoomID: "7000000000000000001", Title: "垃圾直播间", UpdatedAt: now})
	packetPayload, _ := json.Marshal(syncprotocol.RedPacket{WebRID: "123456", ActualRoomID: "7000000000000000001", PacketID: "packet-junk", DetectedAt: now})
	batch := syncprotocol.BatchRequest{
		Version: syncprotocol.Version, RequestID: "before-exclusion", ClientID: registration.ClientID, SentAt: now,
		Items: []syncprotocol.BatchItem{
			{Type: syncprotocol.ItemRoomState, IdempotencyKey: "room:123456", OccurredAt: now, Payload: roomPayload},
			{Type: syncprotocol.ItemRedPacket, IdempotencyKey: "packet:123456", OccurredAt: now, Payload: packetPayload},
		},
	}
	var batchResponse syncprotocol.BatchResponse
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/sync/batch", registration.DeviceToken, batch, http.StatusOK, &batchResponse)

	exclusions := syncprotocol.CenterRoomExclusionsRequest{Items: []syncprotocol.CenterRoomExclusion{{
		WebRID: "123456", ActualRoomID: "7000000000000000001", Name: "垃圾直播间", Reason: "自动清理", ExcludedAt: now,
	}}}
	var excluded map[string]any
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/rooms/exclusions", registration.DeviceToken, exclusions, http.StatusOK, &excluded)
	stats, err := store.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Rooms != 0 || stats.RedPacket != 0 || stats.RoomExclusions != 1 {
		t.Fatalf("center data was not removed: %+v", stats)
	}
	var changes syncprotocol.ChangesResponse
	requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/v1/sync/changes?cursor=0&limit=200", registration.DeviceToken, nil, http.StatusOK, &changes)
	if len(changes.Changes) != 0 {
		t.Fatalf("removed center data remained in changes: %+v", changes.Changes)
	}

	// A different client/key combination using the same actual room ID must not
	// reintroduce the excluded center room or its packets.
	aliasRoom, _ := json.Marshal(syncprotocol.RoomState{WebRID: "999999", ActualRoomID: "7000000000000000001", UpdatedAt: now})
	aliasPacket, _ := json.Marshal(syncprotocol.RedPacket{WebRID: "999999", ActualRoomID: "7000000000000000001", PacketID: "packet-alias", DetectedAt: now})
	batch.RequestID = "after-exclusion"
	batch.Items[0].Payload = aliasRoom
	batch.Items[1].Payload = aliasPacket
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/sync/batch", registration.DeviceToken, batch, http.StatusOK, &batchResponse)
	stats, _ = store.Stats(context.Background())
	if stats.Rooms != 0 || stats.RedPacket != 0 {
		t.Fatalf("excluded room was reintroduced: %+v", stats)
	}

	var listed syncprotocol.CenterRoomExclusionsResponse
	requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/v1/rooms/exclusions", registration.DeviceToken, nil, http.StatusOK, &listed)
	if len(listed.Items) != 1 || listed.Items[0].WebRID != "123456" {
		t.Fatalf("unexpected exclusion list: %+v", listed.Items)
	}
	var restored map[string]bool
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/rooms/exclusions/restore", registration.DeviceToken,
		syncprotocol.CenterRoomExclusionRestoreRequest{WebRID: "123456"}, http.StatusOK, &restored)
	if !restored["restored"] {
		t.Fatal("center exclusion was not restored")
	}
}

func TestFullRegistrationUpgradesUploadOnlyDevice(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enrollmentToken := "0123456789abcdef0123456789abcdef"
	service, err := New(store, enrollmentToken, "test")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()

	register := syncprotocol.RegisterRequest{Version: syncprotocol.Version, ClientID: "desktop_upgrade_test", ClientName: "升级测试客户端"}
	var uploadOnly syncprotocol.RegisterResponse
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/devices/register-upload", "", register, http.StatusCreated, &uploadOnly)

	var full syncprotocol.RegisterResponse
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/devices/register", enrollmentToken, register, http.StatusCreated, &full)
	if full.AccessMode != syncprotocol.DeviceAccessFull || full.DeviceToken == "" || full.DeviceToken == uploadOnly.DeviceToken {
		t.Fatalf("unexpected upgraded registration: upload=%+v full=%+v", uploadOnly, full)
	}

	var rejected map[string]any
	requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/v1/sync/changes?cursor=0&limit=200", uploadOnly.DeviceToken, nil, http.StatusUnauthorized, &rejected)
	var changes syncprotocol.ChangesResponse
	requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/v1/sync/changes?cursor=0&limit=200", full.DeviceToken, nil, http.StatusOK, &changes)
	if changes.Version != syncprotocol.Version {
		t.Fatalf("unexpected changes response after upgrade: %+v", changes)
	}
}

func TestSyncRejectsWrongDeviceToken(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, _ := New(store, "0123456789abcdef0123456789abcdef", "test")
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sync/batch", bytes.NewReader([]byte(`{}`)))
	request.Header.Set("Authorization", "Bearer invalid")
	service.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestSafeTextPreservesUTF8(t *testing.T) {
	value := safeText("  福宝直播间数据  ", 5)
	if value != "福宝直播间" {
		t.Fatalf("safeText() = %q, want %q", value, "福宝直播间")
	}
	if !json.Valid([]byte(`{"value":"` + value + `"}`)) {
		t.Fatalf("safeText returned invalid UTF-8: %q", value)
	}
}

func requestJSON(t *testing.T, client *http.Client, method, target, token string, input any, expectedStatus int, output any) {
	t.Helper()
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		t.Fatalf("status = %d, want %d", response.StatusCode, expectedStatus)
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		t.Fatal(err)
	}
}
