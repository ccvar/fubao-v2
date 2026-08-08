package syncserver

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"fubao.ccvar.com/engine/internal/syncprotocol"
)

// Run with FUBAO_SYNC_TEST_MYSQL=1 against a disposable database. The normal
// test suite remains self-contained and uses SQLite.
func TestMySQLStoreIntegration(t *testing.T) {
	if os.Getenv("FUBAO_SYNC_TEST_MYSQL") != "1" {
		t.Skip("set FUBAO_SYNC_TEST_MYSQL=1 to run the MySQL integration test")
	}
	store, err := OpenStoreWithOptions(StoreOptions{
		Driver:        StorageMySQL,
		MySQLHost:     envTestValue("FUBAO_SYNC_TEST_MYSQL_HOST", "127.0.0.1"),
		MySQLPort:     envTestValue("FUBAO_SYNC_TEST_MYSQL_PORT", "3306"),
		MySQLDatabase: envTestValue("FUBAO_SYNC_TEST_MYSQL_DATABASE", "fubao_sync_test"),
		MySQLUser:     envTestValue("FUBAO_SYNC_TEST_MYSQL_USER", "fubao_test"),
		MySQLPassword: os.Getenv("FUBAO_SYNC_TEST_MYSQL_PASSWORD"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	registration, err := store.RegisterDevice(ctx, syncprotocol.RegisterRequest{ClientID: "desktop_mysql_integration", ClientName: "MySQL 集成测试"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	room := syncprotocol.RoomState{WebRID: "778899", ActualRoomID: "700000000000778899", Title: "MySQL 测试直播间", LiveStatus: "live", LiveStartedAt: now, UpdatedAt: now}
	packet := syncprotocol.RedPacket{WebRID: room.WebRID, ActualRoomID: room.ActualRoomID, PacketID: "packet-mysql", JoinBoxID: "7669047909329177395", Condition: "需1钻", DetectedAt: now, TotalDiamonds: 10, ShareCount: 5}
	roomPayload, _ := json.Marshal(room)
	packetPayload, _ := json.Marshal(packet)
	request := syncprotocol.BatchRequest{
		Version: syncprotocol.Version, ClientID: registration.ClientID, RequestID: "mysql-request-1", SentAt: now,
		Items: []syncprotocol.BatchItem{
			{Type: syncprotocol.ItemRoomState, IdempotencyKey: "room:mysql", OccurredAt: now, Payload: roomPayload},
			{Type: syncprotocol.ItemRedPacket, IdempotencyKey: "packet:mysql", OccurredAt: now, Payload: packetPayload},
		},
	}
	first, err := store.ApplyBatch(ctx, registration.ClientID, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Accepted != 2 || first.Duplicate {
		t.Fatalf("unexpected batch response: %+v", first)
	}
	duplicate, err := store.ApplyBatch(ctx, registration.ClientID, request)
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("MySQL request was not idempotent: response=%+v error=%v", duplicate, err)
	}
	changes, err := store.GetChanges(ctx, 0, syncprotocol.MaxChanges)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Changes) != 2 {
		t.Fatalf("unexpected MySQL changes: %+v", changes.Changes)
	}
	if err := store.ExcludeCenterRooms(ctx, registration.ClientID, []syncprotocol.CenterRoomExclusion{{WebRID: room.WebRID, ActualRoomID: room.ActualRoomID, ExcludedAt: now}}); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Rooms != 0 || stats.RedPacket != 0 || stats.RoomExclusions != 1 {
		t.Fatalf("unexpected MySQL exclusion stats: %+v", stats)
	}
}

func envTestValue(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
