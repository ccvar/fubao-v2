package followinglive

import (
	"context"
	"testing"
)

func TestParseItemsNormalizesAndDeduplicates(t *testing.T) {
	body := []byte(`{
      "status_code": 0,
      "data": {"data": [
        {"web_rid":"778899","room":{"id_str":"123","title":"晚间直播","user_count_str":"1.2万","owner":{"id_str":"u1","sec_uid":"sec1","nickname":"主播甲","avatar_thumb":{"url_list":["https://example.com/a.jpg"]}}}},
        {"web_rid":"778899","room":{"id_str":"123","title":"重复房间","owner":{"nickname":"主播甲"}}},
        {"web_rid":"990011","room":{"id_str":"456","title":"新品直播","stats":{"user_count_str":"320"},"owner":{"nickname":"主播乙"}}}
      ]}
    }`)
	items, err := parseItems(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].RoomID != "123" || items[0].WebRID != "778899" || items[0].Nickname != "主播甲" || items[0].ViewerCount != "1.2万" {
		t.Fatalf("unexpected first item: %#v", items[0])
	}
	if items[1].ViewerCount != "320" {
		t.Fatalf("expected stats viewer fallback, got %#v", items[1])
	}
}

func TestParseItemsRejectsNonzeroStatus(t *testing.T) {
	if _, err := parseItems([]byte(`{"status_code":20003,"status_msg":"not login"}`)); err == nil {
		t.Fatal("expected status error")
	}
}

func TestStoreNativeNormalizesAndCachesSafeSnapshot(t *testing.T) {
	service := NewService()
	result := service.StoreNative(" account-1 ", []Item{
		{RoomID: " 123 ", WebRID: "778899", Nickname: " 主播甲 ", AvatarURL: "javascript:alert(1)"},
		{RoomID: "123", WebRID: "778899", Nickname: "重复"},
		{WebRID: "990011", Nickname: "主播乙", AvatarURL: "https://example.com/avatar.jpg"},
		{},
	})
	if result.AccountID != "account-1" || result.Total != 2 {
		t.Fatalf("unexpected native result: %#v", result)
	}
	if result.Items[0].Nickname != "主播甲" || result.Items[0].AvatarURL != "" {
		t.Fatalf("expected trimmed safe first item, got %#v", result.Items[0])
	}

	cached, err := service.Fetch(context.Background(), "account-1", "sessionid_ss=unused", false)
	if err != nil {
		t.Fatal(err)
	}
	if cached.Total != 2 || cached.RefreshedAt != result.RefreshedAt {
		t.Fatalf("expected native snapshot from cache, got %#v", cached)
	}
	if matched, known := service.MatchRoom("account-1", "778899", "", ""); !known || !matched {
		t.Fatalf("expected web room to match the account snapshot: matched=%v known=%v", matched, known)
	}
	if matched, known := service.MatchRoom("account-1", "", "123", ""); !known || !matched {
		t.Fatalf("expected actual room to match the account snapshot: matched=%v known=%v", matched, known)
	}
	if matched, known := service.MatchRoom("account-1", "not-followed", "", ""); !known || matched {
		t.Fatalf("expected authoritative non-match: matched=%v known=%v", matched, known)
	}
	if matched, known := service.MatchRoom("missing-account", "778899", "", ""); known || matched {
		t.Fatalf("missing snapshots must remain unknown: matched=%v known=%v", matched, known)
	}
}
