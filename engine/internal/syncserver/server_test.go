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
	if registration.DeviceToken == "" {
		t.Fatal("device token was empty")
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	roomPayload, _ := json.Marshal(syncprotocol.RoomState{WebRID: "123456", Title: "测试直播间", LiveStatus: "live", UpdatedAt: now})
	packetPayload, _ := json.Marshal(syncprotocol.RedPacket{WebRID: "123456", PacketID: "packet-1", Title: "钻石红包", Prize: "总99钻，24份红包", DetectedAt: now, TotalDiamonds: 99, ShareCount: 24})
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
	batch.RequestID = "request-2"
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/v1/sync/batch", registration.DeviceToken, batch, http.StatusOK, &first)
	var unchanged syncprotocol.ChangesResponse
	requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/v1/sync/changes?cursor=0&limit=200", registration.DeviceToken, nil, http.StatusOK, &unchanged)
	if len(unchanged.Changes) != 2 {
		t.Fatalf("unchanged snapshot created duplicate center changes: %+v", unchanged.Changes)
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
