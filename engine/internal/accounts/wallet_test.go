package accounts

import (
	"strings"
	"testing"
)

func TestParseRedPacketWalletBalance(t *testing.T) {
	balance, err := parseRedPacketWalletBalance([]byte(`{"data":{"user_id":59557349249,"diamond":4,"diamond_x10":40,"money":0},"status_code":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if balance.UserID != "59557349249" || balance.Diamond != 4 || balance.DiamondX10 != 40 || balance.Money != 0 {
		t.Fatalf("unexpected wallet balance: %+v", balance)
	}
}

func TestRedPacketWalletBalancePersistsWithoutCookieInSafeView(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	view, _, err := store.UpsertAuthenticatedCookie("sessionid_ss=wallet-test", "参与账号", "20001", "sec-20001", RoleParticipation)
	if err != nil {
		t.Fatal(err)
	}
	balance := RedPacketWalletBalance{UserID: "20001", Diamond: 26, DiamondX10: 260, CheckedAt: "2026-08-04T12:00:00Z"}
	if err := store.RecordRedPacketWalletBalance(view.ID, balance); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(store.path[:len(store.path)-len("accounts.json")])
	if err != nil {
		t.Fatal(err)
	}
	persisted := reloaded.List(RoleParticipation)
	if len(persisted) != 1 || persisted[0].Participation == nil || persisted[0].Participation.DiamondBalance != 26 || persisted[0].Participation.DiamondX10 != 260 {
		t.Fatalf("wallet balance did not persist: %+v", persisted)
	}
	if strings.Contains(persisted[0].CookieMessage, "sessionid_ss") {
		t.Fatal("safe account view leaked Cookie material")
	}
}
