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

func TestRedPacketAPIDefaultsOffAndPersists(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	view, _, err := store.UpsertAuthenticatedCookie("sessionid_ss=red-packet-opt-in", "参与账号", "20001", "sec-20001", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	if view.Participation == nil || view.Participation.RedPacketAPIEnabled {
		t.Fatalf("new participation accounts must default off: %+v", view.Participation)
	}
	view, err = store.SetRedPacketAPIEnabled(view.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if view.Participation == nil || !view.Participation.RedPacketAPIEnabled {
		t.Fatalf("expected opt-in to be enabled: %+v", view.Participation)
	}
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	persisted := reloaded.List(RoleParticipation)
	if len(persisted) != 1 || persisted[0].Participation == nil || !persisted[0].Participation.RedPacketAPIEnabled {
		t.Fatalf("red-packet opt-in did not persist: %+v", persisted)
	}
}

func TestRedPacketParticipationEligibility(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eligible, _, err := store.UpsertAuthenticatedCookie("sessionid_ss=eligible", "可参与", "30001", "sec-30001", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	expired, _, err := store.UpsertAuthenticatedCookie("sessionid_ss=expired-rp", "失效账号", "30002", "sec-30002", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	cooling, _, err := store.UpsertAuthenticatedCookie("sessionid_ss=cooling", "冷却账号", "30003", "sec-30003", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	disabled, _, err := store.UpsertAuthenticatedCookie("sessionid_ss=disabled", "关闭账号", "30004", "sec-30004", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	for _, accountID := range []string{eligible.ID, expired.ID, cooling.ID} {
		if _, err := store.SetRedPacketAPIEnabled(accountID, true); err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	store.accounts[expired.ID].CookieStatus = cookieStatusExpired
	store.accounts[cooling.ID].Participation.RedPacketCooldownUntil = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	store.mu.Unlock()

	credentials := store.RedPacketParticipationCredentials(time.Now())
	if len(credentials) != 1 || credentials[0].AccountID != eligible.ID {
		t.Fatalf("expected only valid, opted-in, non-cooling participation account; got %+v (disabled=%s)", credentials, disabled.ID)
	}
	encoded, err := json.Marshal(store.List(RoleParticipation))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sessionid_ss=") {
		t.Fatalf("safe account list leaked raw CK: %s", encoded)
	}
}

func TestRedPacketChallengeBlocksWithoutExpiringCookieAndPersists(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	view, _, err := store.UpsertAuthenticatedCookie("sessionid_ss=challenge", "验证账号", "30005", "sec-30005", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetRedPacketAPIEnabled(view.ID, true); err != nil {
		t.Fatal(err)
	}
	store.RecordRedPacketParticipation(view.ID, "challenge_blocked", "验证码/安全验证拦截", false, false, 0, false)

	blocked := store.List(RoleParticipation)[0]
	if blocked.CookieStatus != cookieStatusValid {
		t.Fatalf("captcha interception must not expire CK: %+v", blocked)
	}
	if blocked.Participation == nil || blocked.Participation.RedPacketAPIEnabled || blocked.Participation.LastRedPacketStatus != "challenge_blocked" {
		t.Fatalf("challenge must stop future allocation and remain visible: %+v", blocked.Participation)
	}
	if credentials := store.RedPacketParticipationCredentials(time.Now()); len(credentials) != 0 {
		t.Fatalf("challenge-blocked account must be excluded: %+v", credentials)
	}

	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	persisted := reloaded.List(RoleParticipation)[0]
	if persisted.Participation == nil || persisted.Participation.LastRedPacketStatus != "challenge_blocked" {
		t.Fatalf("challenge state did not persist: %+v", persisted.Participation)
	}
	if _, err := reloaded.SetRedPacketAPIEnabled(view.ID, true); err != nil {
		t.Fatal(err)
	}
	if credentials := reloaded.RedPacketParticipationCredentials(time.Now()); len(credentials) != 1 {
		t.Fatalf("explicit restart after handling challenge should restore eligibility: %+v", credentials)
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
	view, err := store.RemoveRole("account-1", RoleMonitoring)
	if err != nil {
		t.Fatal(err)
	}
	if hasRoleInView(view, RoleMonitoring) || !hasRoleInView(view, RoleParticipation) || view.Monitoring != nil || view.Participation == nil {
		t.Fatalf("removing monitoring role changed the wrong assignment: %+v", view)
	}
	if _, err := store.AddRole("account-1", RoleMonitoring); err != nil {
		t.Fatal(err)
	}
	view, err = store.RemoveRole("account-1", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	if hasRoleInView(view, RoleParticipation) || !hasRoleInView(view, RoleMonitoring) || view.Participation != nil || view.Monitoring == nil {
		t.Fatalf("removing participation role changed the wrong assignment: %+v", view)
	}
	if _, err := store.RemoveRole("account-1", RoleMonitoring); err == nil {
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

func TestParticipationGroupsPersistAndRemainRoleScoped(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateParticipationGroup("主力账号")
	if err != nil {
		t.Fatal(err)
	}
	view, created, err := store.UpsertImportedCookieWithGroup(
		"sessionid_ss=grouped-account",
		"分组账号",
		"20001",
		"sec-20001",
		RoleParticipation,
		group.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !created || view.Participation == nil || view.Participation.GroupID != group.ID {
		t.Fatalf("participation group was not assigned: created=%v view=%+v", created, view)
	}
	view, _, err = store.UpsertImportedCookie("sessionid_ss=grouped-account", "分组账号", "20001", "sec-20001", RoleMonitoring)
	if err != nil {
		t.Fatal(err)
	}
	if view.Monitoring == nil || view.Participation == nil || view.Participation.GroupID != group.ID {
		t.Fatalf("adding monitoring role changed participation grouping: %+v", view)
	}

	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	groups := reloaded.ListParticipationGroups()
	if len(groups) != 1 || groups[0].ID != group.ID || groups[0].Name != group.Name {
		t.Fatalf("participation groups did not persist: %+v", groups)
	}
	accounts := reloaded.List(RoleParticipation)
	if len(accounts) != 1 || accounts[0].Participation == nil || accounts[0].Participation.GroupID != group.ID {
		t.Fatalf("account group did not persist: %+v", accounts)
	}
	ungrouped, err := reloaded.SetParticipationGroup(accounts[0].ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if ungrouped.Participation == nil || ungrouped.Participation.GroupID != "" {
		t.Fatalf("account was not moved to ungrouped: %+v", ungrouped)
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
