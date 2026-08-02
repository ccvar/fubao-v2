package accounts

import (
	"strings"
	"testing"
)

func TestParseImportSourcesSupportsLegacyAndBrowserFormats(t *testing.T) {
	sources := []ImportSource{
		{Name: "legacy.json", Content: `{"accounts":[{"name":"旧福宝账号","cookie":"sessionid_ss=legacy; ttwid=one","user_id":"10001"}]}`},
		{Name: "browser.json", Content: `[{"domain":".douyin.com","name":"sessionid_ss","value":"browser"},{"domain":".douyin.com","name":"ttwid","value":"two"}]`},
		{Name: "curl.txt", Content: `curl 'https://www.douyin.com/' -H 'Cookie: sessionid_ss=curl; ttwid=three'`},
		{Name: "cookies.txt", Content: ".douyin.com\tTRUE\t/\tTRUE\t0\tsessionid_ss\tnetscape\n.douyin.com\tTRUE\t/\tTRUE\t0\tttwid\tfour"},
	}
	records, invalid := ParseImportSources(sources)
	if invalid != 0 || len(records) != 4 {
		t.Fatalf("expected four imported formats, invalid=%d records=%+v", invalid, records)
	}
	if records[0].Nickname != "旧福宝账号" || records[0].UserID != "10001" {
		t.Fatalf("legacy identity metadata was not preserved: %+v", records[0])
	}
	for _, record := range records {
		if !strings.Contains(record.Cookie, "sessionid_ss=") {
			t.Fatalf("record was not normalized: %+v", record)
		}
	}
}

func TestParseImportSourcesDeduplicatesByLoginSession(t *testing.T) {
	records, invalid := ParseImportSources([]ImportSource{
		{Name: "one.txt", Content: "sessionid_ss=same; ttwid=old"},
		{Name: "two.txt", Content: "sessionid_ss=same; ttwid=new"},
	})
	if invalid != 0 || len(records) != 1 {
		t.Fatalf("expected duplicate sessions to collapse: invalid=%d records=%+v", invalid, records)
	}
}

func TestImportedCookieUsesSelectedRoleAndRemainsPrivate(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	view, created, err := store.UpsertImportedCookie("sessionid_ss=imported; ttwid=x", "导入账号", "", "", RoleMonitoring)
	if err != nil {
		t.Fatal(err)
	}
	if !created || !hasRoleInView(view, RoleMonitoring) || hasRoleInView(view, RoleParticipation) {
		t.Fatalf("import did not honor monitoring role: %+v", view)
	}
	if view.CookieStatus != cookieStatusUnknown || view.Monitoring == nil || view.Monitoring.CookieStatus != cookieStatusUnknown {
		t.Fatalf("import must wait for real validation: %+v", view)
	}
	if encoded := strings.TrimSpace(view.CookieMessage); strings.Contains(encoded, "sessionid") {
		t.Fatalf("safe view leaked Cookie material: %+v", view)
	}
}
