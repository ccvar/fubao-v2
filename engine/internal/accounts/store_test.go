package accounts

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMigrateAndShareRoles(t *testing.T) {
	legacyDir := t.TempDir()
	dataDir := t.TempDir()
	cookie := "sessionid_ss=same-login; other=value"

	writeJSON(t, filepath.Join(legacyDir, "douyin_accounts.json"), map[string]any{
		"accounts": []map[string]any{{
			"account_id":          "monitor-id",
			"name":                "监测昵称",
			"cookie":              cookie,
			"user_id":             "10001",
			"enabled":             true,
			"total_request_count": 300,
			"today_request_count": 25,
		}},
	})
	writeJSON(t, filepath.Join(legacyDir, "lottery_accounts.json"), map[string]any{
		"accounts": []map[string]any{{
			"account_id":             "participation-id",
			"name":                   "参与昵称",
			"cookie":                 cookie,
			"user_id":                "10001",
			"enabled":                true,
			"join_count":             18,
			"win_count":              2,
			"proxy_id":               7,
			"fingerprint_profile_id": 9,
		}},
	})

	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.MigrateLegacy(legacyDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Fatalf("expected one merged account, got %+v", result)
	}
	items := store.List("")
	if len(items) != 1 || len(items[0].Roles) != 2 {
		t.Fatalf("expected both roles, got %+v", items)
	}
	if items[0].Participation == nil || items[0].Participation.ProxyID != 7 {
		t.Fatalf("participation profile was not preserved: %+v", items[0])
	}
	if items[0].Monitoring == nil || items[0].Monitoring.TotalRequestCount != 0 || items[0].Monitoring.TodayRequestCount != 0 {
		t.Fatalf("legacy monitoring request counters must not be imported: %+v", items[0].Monitoring)
	}
}

func TestRecordMonitoringRequestTracksLocalCounters(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.UpsertAuthenticatedCookie("sessionid_ss=local-counter", "本地统计", "10086", "sec-10086", RoleMonitoring); err != nil {
		t.Fatal(err)
	}
	items := store.List(RoleMonitoring)
	if len(items) != 1 {
		t.Fatalf("expected one monitoring account, got %+v", items)
	}
	store.RecordMonitoringRequest(items[0].ID, nil)
	store.RecordMonitoringRequest(items[0].ID, errors.New("temporary upstream failure"))
	current := store.List(RoleMonitoring)[0].Monitoring
	if current == nil || current.TotalRequestCount != 2 || current.TodayRequestCount != 2 || current.LastUsedAt == "" || current.LastUseStatus != "error" {
		t.Fatalf("unexpected local monitoring counters: %+v", current)
	}
	// The write is deliberately batched; give the coalescer a small margin and
	// verify a fresh store sees the same safe counters.
	time.Sleep(1200 * time.Millisecond)
	reloaded, err := NewStore(filepath.Dir(store.path))
	if err != nil {
		t.Fatal(err)
	}
	persisted := reloaded.List(RoleMonitoring)[0].Monitoring
	if persisted == nil || persisted.TotalRequestCount != 2 || persisted.TodayRequestCount != 2 {
		t.Fatalf("batched monitoring counters were not persisted: %+v", persisted)
	}
}

func TestMonitoringCookieHealthIsIndependentFromParticipation(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	view, _, err := store.UpsertAuthenticatedCookie("sessionid_ss=shared-login", "双角色账号", "10010", "sec-10010", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddRole(view.ID, RoleMonitoring); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.accounts[view.ID].CookieStatus = cookieStatusExpired
	store.accounts[view.ID].CookieMessage = "参与账号登录态已失效"
	store.mu.Unlock()

	store.RecordMonitoringRequest(view.ID, nil)
	participation := store.List(RoleParticipation)[0]
	monitoring := store.List(RoleMonitoring)[0]
	if participation.CookieStatus != cookieStatusExpired {
		t.Fatalf("participation status must stay expired, got %+v", participation)
	}
	if monitoring.CookieStatus != cookieStatusValid || monitoring.Monitoring == nil || monitoring.Monitoring.CookieStatus != cookieStatusValid {
		t.Fatalf("successful monitoring request must mark only monitoring CK valid, got %+v", monitoring)
	}

	store.RecordMonitoringRequest(view.ID, errors.New("temporary network failure"))
	monitoring = store.List(RoleMonitoring)[0]
	if monitoring.CookieStatus != cookieStatusValid {
		t.Fatalf("temporary monitoring failure must not turn a valid CK into expired, got %+v", monitoring)
	}
}

func TestAddRoleKeepsExistingRole(t *testing.T) {
	legacyDir := t.TempDir()
	dataDir := t.TempDir()
	writeJSON(t, filepath.Join(legacyDir, "douyin_accounts.json"), map[string]any{
		"accounts": []map[string]any{{
			"account_id": "account-1",
			"name":       "账号一",
			"cookie":     "sessionid_ss=login-one",
			"enabled":    true,
		}},
	})

	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MigrateLegacy(legacyDir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddRole("account-1", RoleParticipation); err != nil {
		t.Fatal(err)
	}
	items := store.List("")
	if len(items) != 1 || !hasRoleInView(items[0], RoleMonitoring) || !hasRoleInView(items[0], RoleParticipation) {
		t.Fatalf("adding a role removed the original role: %+v", items)
	}
	if _, err := store.RemoveRole("account-1", RoleMonitoring); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RemoveRole("account-1", RoleParticipation); err == nil {
		t.Fatal("expected the final role removal to be rejected")
	}
}

func TestDeleteRemovesAccountFromStoreAndDisk(t *testing.T) {
	legacyDir := t.TempDir()
	dataDir := t.TempDir()
	writeJSON(t, filepath.Join(legacyDir, "douyin_accounts.json"), map[string]any{
		"accounts": []map[string]any{{
			"account_id": "account-delete",
			"name":       "待删除账号",
			"cookie":     "sessionid_ss=delete-me",
			"enabled":    true,
		}},
	})

	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MigrateLegacy(legacyDir); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("account-delete"); err != nil {
		t.Fatal(err)
	}
	if items := store.List(""); len(items) != 0 {
		t.Fatalf("expected empty in-memory store, got %+v", items)
	}
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if items := reloaded.List(""); len(items) != 0 {
		t.Fatalf("expected deleted account to stay deleted after reload, got %+v", items)
	}
	if err := store.Delete("account-delete"); err == nil {
		t.Fatal("expected deleting a missing account to fail")
	}
}

func TestReplaceCookieResetsHealthAndNeverLeaksFromView(t *testing.T) {
	legacyDir := t.TempDir()
	dataDir := t.TempDir()
	writeJSON(t, filepath.Join(legacyDir, "douyin_accounts.json"), map[string]any{
		"accounts": []map[string]any{{
			"account_id": "account-rebind",
			"name":       "重新绑定账号",
			"cookie":     "sessionid_ss=expired",
			"enabled":    true,
		}},
	})
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MigrateLegacy(legacyDir); err != nil {
		t.Fatal(err)
	}
	view, err := store.ReplaceCookie("account-rebind", "sessionid_ss=fresh; sid_guard=fresh")
	if err != nil {
		t.Fatal(err)
	}
	if view.CookieStatus != cookieStatusUnknown || view.CookieChecked != "" {
		t.Fatalf("expected new CK to await validation, got %+v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || strings.Contains(string(encoded), "sessionid_ss") {
		t.Fatalf("safe view leaked raw Cookie: %s", encoded)
	}
	credential, err := store.Credential("account-rebind")
	if err != nil {
		t.Fatal(err)
	}
	if credential.Cookie != "sessionid_ss=fresh; sid_guard=fresh" {
		t.Fatalf("new Cookie was not persisted: %+v", credential)
	}
}

func TestSetBrowserLoginStatePersistsWithoutLeakingCookie(t *testing.T) {
	legacyDir := t.TempDir()
	dataDir := t.TempDir()
	writeJSON(t, filepath.Join(legacyDir, "douyin_accounts.json"), map[string]any{
		"accounts": []map[string]any{{
			"account_id": "browser-state-account",
			"name":       "浏览器状态账号",
			"cookie":     "sessionid_ss=secret",
			"enabled":    true,
		}},
	})
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MigrateLegacy(legacyDir); err != nil {
		t.Fatal(err)
	}
	view, err := store.SetBrowserLoginState("browser-state-account", false)
	if err != nil {
		t.Fatal(err)
	}
	if view.CookieStatus != cookieStatusExpired || !strings.Contains(view.CookieMessage, "CK 已失效") {
		t.Fatalf("expected explicit logged-out state, got %+v", view)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "sessionid_ss") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("safe view leaked raw Cookie: %s", encoded)
	}
	view, err = store.SetBrowserLoginState("browser-state-account", true)
	if err != nil {
		t.Fatal(err)
	}
	if view.CookieStatus != cookieStatusValid {
		t.Fatalf("expected native logged-in state, got %+v", view)
	}
}

func TestUpsertAuthenticatedCookieCreatesAndDeduplicates(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	view, created, err := store.UpsertAuthenticatedCookie("sessionid_ss=new-login", "扫码用户", "10086", "sec-10086", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	if !created || view.Nickname != "扫码用户" || !hasRoleInView(view, RoleParticipation) {
		t.Fatalf("unexpected created account: created=%v view=%+v", created, view)
	}
	view, created, err = store.UpsertAuthenticatedCookie("sessionid_ss=refreshed", "扫码用户", "10086", "sec-10086", RoleMonitoring)
	if err != nil {
		t.Fatal(err)
	}
	if created || !hasRoleInView(view, RoleParticipation) || !hasRoleInView(view, RoleMonitoring) {
		t.Fatalf("expected canonical account with both roles: created=%v view=%+v", created, view)
	}
	if len(store.List("")) != 1 {
		t.Fatalf("duplicate login created a second account: %+v", store.List(""))
	}
}

func hasRoleInView(account AccountView, role Role) bool {
	for _, item := range account.Roles {
		if item == role {
			return true
		}
	}
	return false
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
