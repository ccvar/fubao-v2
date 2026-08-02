package syncserver

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

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
