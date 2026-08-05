package browsers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSurfaceForAccountSourceIsAlwaysEmbedded(t *testing.T) {
	for _, source := range []string{"manual-import", "qr-login", "native-rebind", ""} {
		if got := SurfaceForAccountSource(source); got != SurfaceEmbedded {
			t.Fatalf("source %q surface = %q, want embedded", source, got)
		}
	}
}

func TestCreateStoresEmbeddedSurface(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Even if a caller requests external chrome, create still records the
	// requested surface parameter; product routing forces embedded via
	// SurfaceForAccountSource when creating from account provenance.
	instance, err := store.CreateWithLimit("import-1", "导入账号", "", 0, SurfaceEmbedded)
	if err != nil {
		t.Fatal(err)
	}
	if instance.Surface != SurfaceEmbedded {
		t.Fatalf("surface = %q", instance.Surface)
	}
}

func TestCreateKeepsOneIsolatedInstancePerAccount(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := store.Create("account-1", "测试账号", "")
	if err != nil {
		t.Fatal(err)
	}
	if instance.AccountID != "account-1" || instance.AccountName != "测试账号" {
		t.Fatalf("unexpected instance: %#v", instance)
	}
	if instance.Status != StatusStopped {
		t.Fatalf("new instance status = %q", instance.Status)
	}
	if instance.Surface != SurfaceEmbedded {
		t.Fatalf("default surface = %q", instance.Surface)
	}
	reused, err := store.Create("account-1", "测试账号", "重复实例")
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID != instance.ID {
		t.Fatalf("same account created a second instance: first=%q reused=%q", instance.ID, reused.ID)
	}
	items := store.List()
	if len(items) != 1 {
		t.Fatalf("instance count = %d", len(items))
	}
}

func TestCreateWithLimitReusesExistingAccountAndRejectsAnother(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateWithLimit("account-1", "账号一", "", 1, SurfaceEmbedded)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := store.CreateWithLimit("account-1", "账号一", "重复实例", 1, SurfaceEmbedded)
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID != first.ID {
		t.Fatalf("limited create did not reuse the existing account instance: first=%q reused=%q", first.ID, reused.ID)
	}
	if _, err := store.CreateWithLimit("account-2", "账号二", "", 1, SurfaceEmbedded); err == nil || !strings.Contains(err.Error(), "免费版最多只能创建 1 个浏览器实例") {
		t.Fatalf("second limited instance error = %v", err)
	}
	if items := store.List(); len(items) != 1 || items[0].ID != first.ID {
		t.Fatalf("limited create changed existing instances: %#v", items)
	}
}

func TestRuntimeAdmissionQueuesAndPromotesByDeviceCapacity(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.resourceProbe = func() ResourceSnapshot {
		return ResourceSnapshot{
			CPUCount:        1,
			MemoryTotal:     8 * 1024 * 1024 * 1024,
			MemoryAvailable: 6 * 1024 * 1024 * 1024,
			Pressure:        PressureNormal,
		}
	}
	first, err := store.Create("account-1", "账号一", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create("account-2", "账号二", "")
	if err != nil {
		t.Fatal(err)
	}

	admission, err := store.AcquireEmbedded(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !admission.Granted || admission.Capacity.EffectiveLimit != 1 {
		t.Fatalf("first admission = %#v", admission)
	}
	waiting, err := store.AcquireEmbedded(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Granted || waiting.State != RuntimeWaiting || waiting.QueuePosition != 1 {
		t.Fatalf("second admission = %#v", waiting)
	}
	if _, err := store.ReleaseEmbedded(first.ID); err != nil {
		t.Fatal(err)
	}
	promoted, err := store.AcquireEmbedded(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !promoted.Granted || promoted.State != RuntimeRunning {
		t.Fatalf("promoted admission = %#v", promoted)
	}

	items := store.List()
	if len(items) != 2 || items[1].RuntimeState != RuntimeRunning {
		t.Fatalf("decorated runtime states = %#v", items)
	}
}

func TestRecommendedLimitUsesCPUHeadroomWithinMemoryAndAutoCaps(t *testing.T) {
	resources := ResourceSnapshot{
		CPUCount:    14,
		MemoryTotal: 64 * 1024 * 1024 * 1024,
	}
	recommendation := calculateCapacityRecommendation(resources)
	if recommendation.cpuLimit != 21 || recommendation.memoryLimit != 70 || recommendation.limit != 21 {
		t.Fatalf("unexpected 14-core recommendation: %+v", recommendation)
	}
	if recommendation.memoryReserve != 16*1024*1024*1024 {
		t.Fatalf("memory reserve=%d, want 16 GiB", recommendation.memoryReserve)
	}

	resources.CPUCount = 32
	recommendation = calculateCapacityRecommendation(resources)
	if recommendation.limit != maximumAutoInstances {
		t.Fatalf("automatic cap was not enforced: %+v", recommendation)
	}
}

func TestCriticalPressurePausesNewAdmissions(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.resourceProbe = func() ResourceSnapshot {
		return ResourceSnapshot{
			CPUCount:        8,
			MemoryTotal:     8 * 1024 * 1024 * 1024,
			MemoryAvailable: 512 * 1024 * 1024,
			Pressure:        PressureCritical,
		}
	}
	instance, err := store.Create("account-1", "测试账号", "")
	if err != nil {
		t.Fatal(err)
	}
	admission, err := store.AcquireEmbedded(instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if admission.Granted || admission.Capacity.EffectiveLimit != 0 {
		t.Fatalf("critical admission = %#v", admission)
	}
}

func TestParticipationContextRetainsRuntimeUntilExplicitRelease(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	store.now = func() time.Time { return now }
	instance, err := store.Create("account-participation", "参与账号", "")
	if err != nil {
		t.Fatal(err)
	}
	if admission, err := store.AcquireEmbedded(instance.ID); err != nil || !admission.Granted {
		t.Fatalf("embedded admission = %+v, err=%v", admission, err)
	}
	if admission, err := store.RetainParticipationContext(instance.ID); err != nil || !admission.Granted {
		t.Fatalf("participation admission = %+v, err=%v", admission, err)
	}
	if capacity, err := store.ReleaseEmbedded(instance.ID); err != nil || capacity.Running != 1 {
		t.Fatalf("participation lease was released with card: capacity=%+v err=%v", capacity, err)
	}
	now = now.Add(time.Minute)
	if capacity := store.Capacity(); capacity.Running != 1 {
		t.Fatalf("participation lease was pruned: %+v", capacity)
	}
	if capacity, err := store.ReleaseParticipationContext(instance.ID); err != nil || capacity.Running != 0 {
		t.Fatalf("participation release = %+v, err=%v", capacity, err)
	}
}

func TestParseCookies(t *testing.T) {
	items := parseCookies("sessionid_ss=secret; sid_guard=guard=value; malformed")
	if len(items) != 2 {
		t.Fatalf("cookie count = %d", len(items))
	}
	if items[1].Name != "sid_guard" || items[1].Value != "guard=value" {
		t.Fatalf("unexpected cookie: %#v", items[1])
	}
}

func TestSerializeBrowserCookies(t *testing.T) {
	items := []browserCookie{
		{Name: "sid_guard", Value: "guard=value"},
		{Name: "sessionid_ss", Value: "secret"},
		{Name: "sid_guard", Value: "new-guard"},
	}
	if !hasBrowserLoginCookie(items) {
		t.Fatal("expected browser login cookie")
	}
	got := serializeBrowserCookies(items)
	if !strings.Contains(got, "sessionid_ss=secret") || !strings.Contains(got, "sid_guard=new-guard") {
		t.Fatalf("serialized cookie = %q", got)
	}
	if strings.Count(got, "sid_guard=") != 1 {
		t.Fatalf("duplicate cookies were not collapsed: %q", got)
	}
}

func TestRecognizesCurrentDouyinLoginCookieVariants(t *testing.T) {
	for _, name := range []string{
		"sessionid", "sessionid_ss", "sid_guard", "sid_tt", "sid_ucp_v1",
		"ssid_ucp_v1", "uid_tt", "uid_tt_ss", "passport_assist_user",
	} {
		if !hasBrowserLoginCookie([]browserCookie{{Name: name, Value: "present"}}) {
			t.Fatalf("login cookie %q was not recognized", name)
		}
	}
	if hasBrowserLoginCookie([]browserCookie{{Name: "ttwid", Value: "present"}}) {
		t.Fatal("non-login cookie was accepted as authenticated")
	}
}

func TestRealDouyinChromeCookieRestore(t *testing.T) {
	dataDir := strings.TrimSpace(os.Getenv("FUBAO_REAL_BROWSER_DATA_DIR"))
	instanceID := strings.TrimSpace(os.Getenv("FUBAO_REAL_BROWSER_INSTANCE_ID"))
	rawCookie := strings.TrimSpace(os.Getenv("FUBAO_REAL_BROWSER_COOKIE"))
	if dataDir == "" || instanceID == "" || rawCookie == "" {
		t.Skip("set FUBAO_REAL_BROWSER_DATA_DIR, FUBAO_REAL_BROWSER_INSTANCE_ID, and FUBAO_REAL_BROWSER_COOKIE to run the native Chrome restore check")
	}
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	store.SetCookieUpdater(func(_ string, _ string) error { return nil })
	accountID, err := store.AccountID(instanceID)
	if err != nil {
		t.Fatal(err)
	}
	profileDir := store.profileDir(accountID)
	t.Cleanup(func() {
		_ = stopProfileBrowserProcesses(profileDir)
	})
	opened, err := store.Open(instanceID, rawCookie)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Status != StatusOnline || opened.PID <= 0 {
		t.Fatalf("real browser did not reach online state: status=%q pid=%d", opened.Status, opened.PID)
	}
	if err := stopProfileBrowserProcesses(profileDir); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, item := range store.List() {
			if item.ID == instanceID && item.Status == StatusStopped {
				deadline = time.Time{}
				break
			}
		}
		if deadline.IsZero() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	reopened, err := store.Open(instanceID, rawCookie)
	if err != nil {
		t.Fatalf("reopening the same account profile failed: %v", err)
	}
	if reopened.Status != StatusOnline || reopened.PID <= 0 || reopened.AccountID != accountID {
		t.Fatalf("real browser did not reuse the account profile: status=%q pid=%d account=%q", reopened.Status, reopened.PID, reopened.AccountID)
	}
}

func TestProcessUsesOnlyMatchingAccountProfile(t *testing.T) {
	profile := "/Users/test/Library/Application Support/福宝控制台/browser-profiles/account-a"
	matching := "/Applications/Google Chrome --user-data-dir=" + profile + " --app=http://127.0.0.1:1234/bootstrap"
	quoted := `/Applications/Google Chrome --user-data-dir="` + profile + `" --app=https://www.douyin.com/`
	other := "/Applications/Google Chrome --user-data-dir=/Users/test/Library/Application Support/福宝控制台/browser-profiles/account-b"
	prefixCollision := matching[:strings.Index(matching, " --app=")] + "-secondary --app=http://127.0.0.1:1234/bootstrap"
	if !processUsesProfile(matching, profile) || !processUsesProfile(quoted, profile) {
		t.Fatal("expected matching profile process")
	}
	if processUsesProfile(other, profile) || processUsesProfile(prefixCollision, profile) || processUsesProfile(matching, "") {
		t.Fatal("must not match another or empty profile")
	}
}

func TestCookieSyncEndpointUpdatesCanonicalAccount(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := store.Create("account-1", "测试账号", "")
	if err != nil {
		t.Fatal(err)
	}
	type update struct {
		accountID string
		cookie    string
	}
	updates := make(chan update, 1)
	store.SetCookieUpdater(func(accountID, rawCookie string) error {
		updates <- update{accountID: accountID, cookie: rawCookie}
		return nil
	})
	store.mu.Lock()
	endpoint, err := store.ensureCookieSyncEndpointLocked(store.instances[instance.ID])
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = endpoint.server.Close() })
	payload, err := json.Marshal(map[string]any{
		"cookies": []browserCookie{
			{Name: "sessionid_ss", Value: "new-session"},
			{Name: "sid_guard", Value: "new-guard"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, endpoint.URL, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Fubao-Token", endpoint.Token)
	client := http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("sync status = %d", response.StatusCode)
	}
	select {
	case got := <-updates:
		if got.accountID != "account-1" || !strings.Contains(got.cookie, "sessionid_ss=new-session") {
			t.Fatalf("unexpected update: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("cookie update was not received")
	}
	items := store.List()
	if len(items) != 1 || items[0].CredentialUpdatedAt == "" {
		t.Fatalf("credential update timestamp missing: %#v", items)
	}
}
