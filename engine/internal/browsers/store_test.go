package browsers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

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
