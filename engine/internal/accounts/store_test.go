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

func TestReconcileParticipationStatsFromRecordsBackfillsToday(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	view, _, err := store.UpsertAuthenticatedCookie("sessionid_ss=reconcile", "回填账号", "30008", "sec-30008", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	// Inflated all-time counters + empty today fields (legacy profile shape).
	store.mu.Lock()
	store.accounts[view.ID].Participation.JoinCount = 100
	store.accounts[view.ID].Participation.WinCount = 5
	store.accounts[view.ID].Participation.TodayStatDate = ""
	store.accounts[view.ID].Participation.TodayJoinCount = 0
	store.accounts[view.ID].Participation.TodayWinCount = 0
	store.accounts[view.ID].Participation.TodayWinDiamonds = 0
	store.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	changed, err := store.ReconcileParticipationStatsFromRecords([]ParticipationStatsPatch{{
		AccountID: view.ID, JoinCount: 12, WinCount: 2,
		TodayJoinCount: 3, TodayWinCount: 1, TodayWinDiamonds: 8,
		TodayStatDate: today,
	}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if changed == 0 {
		t.Fatal("expected reconciliation to rewrite profile")
	}
	got := store.List(RoleParticipation)[0].Participation
	// Successful-join rollup replaces inflated legacy totals.
	if got.JoinCount != 12 {
		t.Fatalf("join_count=%d, want 12 from successful records", got.JoinCount)
	}
	if got.WinCount != 2 {
		t.Fatalf("win_count=%d, want 2 from successful records", got.WinCount)
	}
	if got.TodayJoinCount != 3 || got.TodayWinCount != 1 || got.TodayWinDiamonds != 8 || got.TodayStatDate != today {
		t.Fatalf("today stats not backfilled: %+v", got)
	}
}

func TestReconcileParticipationStatsZerosAccountsWithoutRecords(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	view, _, err := store.UpsertAuthenticatedCookie("sessionid_ss=orphan-stats", "无记录账号", "30009", "sec-30009", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.accounts[view.ID].Participation.JoinCount = 1675
	store.accounts[view.ID].Participation.WinCount = 29
	store.accounts[view.ID].Participation.TodayJoinCount = 40
	store.accounts[view.ID].Participation.TodayWinCount = 2
	store.accounts[view.ID].Participation.TodayWinDiamonds = 3
	store.accounts[view.ID].Participation.TodayStatDate = time.Now().Format("2006-01-02")
	store.mu.Unlock()

	// Empty patch set means the durable record store has no rows for anyone.
	changed, err := store.ReconcileParticipationStatsFromRecords(nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if changed == 0 {
		t.Fatal("expected inflated legacy totals to be zeroed")
	}
	got := store.List(RoleParticipation)[0].Participation
	if got.JoinCount != 0 || got.WinCount != 0 || got.TodayJoinCount != 0 || got.TodayWinCount != 0 || got.TodayWinDiamonds != 0 {
		t.Fatalf("orphan profile not zeroed: %+v", got)
	}
}

func TestParticipationTodayCountersAndParseWinDiamonds(t *testing.T) {
	if got := parseWinDiamonds("已中8钻"); got != 8 {
		t.Fatalf("parse 已中8钻=%v, want 8", got)
	}
	if got := parseWinDiamonds("已中1.5钻（钱包增量确认）"); got != 1.5 {
		t.Fatalf("parse wallet delta=%v, want 1.5", got)
	}
	if got := parseWinDiamonds("未中奖"); got != 0 {
		t.Fatalf("parse not-won should be 0, got %v", got)
	}

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	view, _, err := store.UpsertAuthenticatedCookie("sessionid_ss=today-stat", "今日统计", "30007", "sec-30007", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	store.RecordRedPacketParticipation(view.ID, "joined", "红包参与请求已受理", true, false, 0, false)
	store.RecordRedPacketParticipation(view.ID, "joined", "红包参与请求已受理", true, false, 0, false)
	store.RecordRedPacketDrawResult(view.ID, "已中8钻", true)
	store.RecordRedPacketDrawResult(view.ID, "未中奖", false)

	listed := store.List(RoleParticipation)[0]
	if listed.Participation == nil {
		t.Fatal("missing participation profile")
	}
	p := listed.Participation
	if p.JoinCount != 2 || p.TodayJoinCount != 2 {
		t.Fatalf("join totals: all=%d today=%d", p.JoinCount, p.TodayJoinCount)
	}
	if p.WinCount != 1 || p.TodayWinCount != 1 || p.TodayWinDiamonds != 8 {
		t.Fatalf("win totals: all=%d today=%d diamonds=%v", p.WinCount, p.TodayWinCount, p.TodayWinDiamonds)
	}

	// Simulate day rollover: yesterday stats must not leak into today's list view.
	store.mu.Lock()
	store.accounts[view.ID].Participation.TodayStatDate = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	store.accounts[view.ID].Participation.TodayJoinCount = 9
	store.accounts[view.ID].Participation.TodayWinCount = 9
	store.accounts[view.ID].Participation.TodayWinDiamonds = 99
	store.mu.Unlock()
	rolled := store.List(RoleParticipation)[0].Participation
	if rolled.TodayJoinCount != 0 || rolled.TodayWinCount != 0 || rolled.TodayWinDiamonds != 0 {
		t.Fatalf("list must roll today counters after midnight: %+v", rolled)
	}
	if rolled.JoinCount != 2 || rolled.WinCount != 1 {
		t.Fatalf("all-time counters must survive day roll: join=%d win=%d", rolled.JoinCount, rolled.WinCount)
	}
}

func TestRiskControlCooldownSurvivesDisableAndZeroCooldownRecords(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	view, _, err := store.UpsertAuthenticatedCookie("sessionid_ss=risk-cool", "风控账号", "30006", "sec-30006", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetRedPacketAPIEnabled(view.ID, true); err != nil {
		t.Fatal(err)
	}
	store.RecordRedPacketParticipation(view.ID, "risk_control", "触发风控，账号已进入冷却（30 分钟）", false, false, 30*time.Minute, false)

	listed := store.List(RoleParticipation)[0]
	if listed.Participation == nil || listed.Participation.RedPacketAPIEnabled {
		t.Fatalf("risk control should disable the API switch: %+v", listed.Participation)
	}
	if !activeRedPacketCooldown(listed.Participation.RedPacketCooldownUntil, time.Now()) {
		t.Fatalf("risk control must persist a future cooldown: %+v", listed.Participation)
	}
	until := listed.Participation.RedPacketCooldownUntil
	status := listed.Participation.LastRedPacketStatus
	message := listed.Participation.LastRedPacketMessage

	// Task stop / closing the gift switch previously wiped the cooldown badge.
	updated, err := store.SetRedPacketAPIEnabled(view.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Participation.RedPacketCooldownUntil != until ||
		updated.Participation.LastRedPacketStatus != status ||
		updated.Participation.LastRedPacketMessage != message {
		t.Fatalf("disabling API must keep risk cooldown badge fields: %+v", updated.Participation)
	}

	// A later zero-cooldown record (for example concurrent context cleanup)
	// must not clear the active risk window or overwrite its status copy.
	store.RecordRedPacketParticipation(view.ID, "failed", "红包接口未确认参与结果", false, false, 0, false)
	after := store.List(RoleParticipation)[0]
	if after.Participation.RedPacketCooldownUntil != until ||
		after.Participation.LastRedPacketStatus != "risk_control" ||
		after.Participation.LastRedPacketMessage != message {
		t.Fatalf("zero-cooldown record wiped risk cooldown: %+v", after.Participation)
	}
	if len(store.RedPacketParticipationCredentials(time.Now())) != 0 {
		t.Fatal("risk-cooling account must stay out of the participation pool")
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

func TestSetBrowserLoginStatePromotesImportedSourceOnlyWhenRequested(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	view, _, err := store.UpsertImportedCookie("sessionid_ss=import-login", "导入用户", "20010", "sec-20010", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	if view.Source != "manual-import" {
		t.Fatalf("import source = %q", view.Source)
	}
	// Card polling / soft login signals must not flip import accounts onto the
	// embedded surface — that path freezes when card + instance share a store.
	view, err = store.SetBrowserLoginState(view.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if view.Source != "manual-import" {
		t.Fatalf("card login state must keep manual-import, got %q", view.Source)
	}
	view, err = store.SetBrowserLoginStateWithPromotion(view.ID, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if view.Source != "native-rebind" {
		t.Fatalf("expected native-rebind after explicit rebind promotion, got %q", view.Source)
	}
	if view.CookieStatus != cookieStatusValid {
		t.Fatalf("expected valid cookie after native login, got %+v", view)
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
