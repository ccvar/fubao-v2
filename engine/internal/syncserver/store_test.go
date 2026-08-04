package syncserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"fubao.ccvar.com/engine/internal/syncprotocol"
)

func TestOpenStoreMigratesLegacyDevicesToFullAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-sync.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE devices (
			client_id TEXT PRIMARY KEY,
			client_name TEXT NOT NULL DEFAULT '',
			platform TEXT NOT NULL DEFAULT '',
			app_version TEXT NOT NULL DEFAULT '',
			token_hash TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL
		);
		INSERT INTO devices(client_id, token_hash, created_at, last_seen_at)
		VALUES('desktop_legacy', ?, '2026-08-01T00:00:00Z', '2026-08-01T00:00:00Z')`, tokenHash("legacy-device-token"))
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authorization, err := store.AuthorizeDevice(context.Background(), "legacy-device-token")
	if err != nil {
		t.Fatal(err)
	}
	if authorization.ClientID != "desktop_legacy" || authorization.AccessMode != syncprotocol.DeviceAccessFull {
		t.Fatalf("unexpected migrated authorization: %+v", authorization)
	}
}

func TestCenterMetricsCountUniqueLiveSessionsAndRedPackets(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	clientID := "desktop_metrics"
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	requestIndex := 0
	apply := func(itemType syncprotocol.ItemType, payload any, occurredAt time.Time) {
		t.Helper()
		requestIndex++
		content, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		_, applyErr := store.ApplyBatch(ctx, clientID, syncprotocol.BatchRequest{
			Version:   syncprotocol.Version,
			RequestID: clientID + "-request-" + time.Duration(requestIndex).String(),
			ClientID:  clientID,
			SentAt:    base.Add(time.Duration(requestIndex) * time.Second).Format(time.RFC3339Nano),
			Items: []syncprotocol.BatchItem{{
				Type:           itemType,
				IdempotencyKey: clientID + "-item-" + time.Duration(requestIndex).String(),
				OccurredAt:     occurredAt.Format(time.RFC3339Nano),
				Payload:        content,
			}},
		})
		if applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	room := func(status, startedAt string, updatedAt time.Time) syncprotocol.RoomState {
		return syncprotocol.RoomState{
			WebRID:        "721652357894",
			LiveStatus:    status,
			LiveStartedAt: startedAt,
			UpdatedAt:     updatedAt.Format(time.RFC3339Nano),
		}
	}

	apply(syncprotocol.ItemRoomState, room("offline", "", base), base)
	apply(syncprotocol.ItemRoomState, room("live", "2026-08-04T08:01:00Z", base.Add(time.Minute)), base.Add(time.Minute))
	apply(syncprotocol.ItemRoomState, room("live", "2026-08-04T08:01:00Z", base.Add(2*time.Minute)), base.Add(2*time.Minute))
	apply(syncprotocol.ItemRoomState, room("offline", "2026-08-04T08:01:00Z", base.Add(3*time.Minute)), base.Add(3*time.Minute))
	apply(syncprotocol.ItemRoomState, room("live", "2026-08-04T09:00:00Z", base.Add(4*time.Minute)), base.Add(4*time.Minute))

	packet := func(id string, detectedAt time.Time) syncprotocol.RedPacket {
		return syncprotocol.RedPacket{
			WebRID:     "721652357894",
			PacketID:   id,
			DetectedAt: detectedAt.Format(time.RFC3339Nano),
		}
	}
	apply(syncprotocol.ItemRedPacket, packet("packet-a", base.Add(5*time.Minute)), base.Add(5*time.Minute))
	apply(syncprotocol.ItemRedPacket, packet("packet-a", base.Add(5*time.Minute)), base.Add(6*time.Minute))
	apply(syncprotocol.ItemRedPacket, packet("packet-b", base.Add(7*time.Minute)), base.Add(7*time.Minute))

	var metricsVersion, liveSessions, redPackets int
	if err := store.db.QueryRowContext(ctx, `SELECT metrics_version, live_session_count, red_packet_count FROM rooms WHERE web_rid = ?`, "721652357894").
		Scan(&metricsVersion, &liveSessions, &redPackets); err != nil {
		t.Fatal(err)
	}
	if metricsVersion != 1 || liveSessions != 2 || redPackets != 2 {
		t.Fatalf("unexpected center metrics: version=%d live=%d packets=%d", metricsVersion, liveSessions, redPackets)
	}

	changes, err := store.GetChangesByType(ctx, 0, syncprotocol.MaxChanges, syncprotocol.ItemRoomState)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Changes) == 0 {
		t.Fatal("expected canonical room change")
	}
	latest := changes.Changes[len(changes.Changes)-1]
	if latest.OriginClientID != centerServerOriginClientID {
		t.Fatalf("unexpected canonical origin: %q", latest.OriginClientID)
	}
	var canonical syncprotocol.RoomState
	if err := json.Unmarshal(latest.Payload, &canonical); err != nil {
		t.Fatal(err)
	}
	if canonical.MetricsVersion != 1 || canonical.LiveSessionCount != 2 || canonical.RedPacketCount != 2 {
		t.Fatalf("unexpected canonical room metrics: %+v", canonical)
	}
}
