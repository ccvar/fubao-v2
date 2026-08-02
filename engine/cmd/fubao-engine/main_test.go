package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fubao.ccvar.com/engine/internal/license"
	"fubao.ccvar.com/engine/internal/remotesync"
)

func TestRemoteSyncPullScopeFollowsLicenseLifetime(t *testing.T) {
	if got := remoteSyncPullScope(nil); got != remotesync.PullNone {
		t.Fatalf("nil license manager scope=%q", got)
	}
	if got := remoteSyncPullScope(newTestLicenseManager(t, "")); got != remotesync.PullAll {
		t.Fatalf("permanent license scope=%q", got)
	}
	finiteExpiry := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	if got := remoteSyncPullScope(newTestLicenseManager(t, finiteExpiry)); got != remotesync.PullRedPackets {
		t.Fatalf("finite license scope=%q", got)
	}
	expired := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if got := remoteSyncPullScope(newTestLicenseManager(t, expired)); got != remotesync.PullNone {
		t.Fatalf("expired license scope=%q", got)
	}
}

func newTestLicenseManager(t *testing.T, expiresAt string) *license.Manager {
	t.Helper()
	dataDir := t.TempDir()
	state := map[string]any{
		"license_key":       "TEST-LICENSE-KEY",
		"expires_at":        expiresAt,
		"offline_until":     time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
		"last_validated_at": time.Now().UTC().Format(time.RFC3339),
	}
	content, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "license_state.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := license.New(dataDir, "test", license.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}
