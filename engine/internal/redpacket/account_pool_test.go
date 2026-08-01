package redpacket

import (
	"errors"
	"fmt"
	"io"
	"testing"
	"time"
)

func TestAccountPoolAssignmentsAreStableAndDistributed(t *testing.T) {
	credentials := make([]AccountCredential, 0, 8)
	for i := 0; i < 8; i++ {
		credentials = append(credentials, AccountCredential{
			AccountID:   fmt.Sprintf("account-%02d", i),
			AccountName: fmt.Sprintf("监测账号 %02d", i),
			Cookie:      fmt.Sprintf("sessionid_ss=%02d", i),
		})
	}
	first, err := newAccountPoolWithConfig(credentials, poolConfig{globalParallel: 1, accountParallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := newAccountPoolWithConfig(credentials, poolConfig{globalParallel: 1, accountParallel: 1})
	if err != nil {
		t.Fatal(err)
	}

	const roomCount = 1272
	firstCounts := map[string]int{}
	for i := 0; i < roomCount; i++ {
		roomID := fmt.Sprintf("room-%04d", i)
		left, err := first.accountFor(roomID)
		if err != nil {
			t.Fatal(err)
		}
		right, err := second.accountFor(roomID)
		if err != nil {
			t.Fatal(err)
		}
		if left.credential.AccountID != right.credential.AccountID {
			t.Fatalf("room %s assignment changed: %s != %s", roomID, left.credential.AccountID, right.credential.AccountID)
		}
		firstCounts[left.credential.AccountID]++
	}
	if len(firstCounts) != len(credentials) {
		t.Fatalf("expected all accounts to receive rooms, got %d/%d", len(firstCounts), len(credentials))
	}
	min, max := roomCount, 0
	for _, count := range firstCounts {
		if count < min {
			min = count
		}
		if count > max {
			max = count
		}
	}
	if max-min > 60 {
		t.Fatalf("distribution is unexpectedly skewed: min=%d max=%d", min, max)
	}
}

func TestAccountPoolDistributesLargeRoomSetAcrossNinetyNineAccounts(t *testing.T) {
	credentials := make([]AccountCredential, 0, 99)
	for i := 0; i < 99; i++ {
		credentials = append(credentials, AccountCredential{
			AccountID:   fmt.Sprintf("account-%03d", i),
			AccountName: fmt.Sprintf("监测账号 %03d", i),
			Cookie:      fmt.Sprintf("sessionid_ss=%03d", i),
		})
	}
	pool, err := newAccountPoolWithConfig(credentials, poolConfig{globalParallel: 1, accountParallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1272; i++ {
		if _, err := pool.accountFor(fmt.Sprintf("room-%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	loads := pool.summary()
	if len(loads) != 99 {
		t.Fatalf("expected 99 account loads, got %d", len(loads))
	}
	assigned := 0
	for _, load := range loads {
		if load.RoomCount == 0 {
			t.Fatalf("monitoring account %s received no room assignment", load.AccountID)
		}
		assigned += load.RoomCount
	}
	if assigned != 1272 {
		t.Fatalf("expected 1272 room assignments, got %d", assigned)
	}
}

func TestAccountPoolRateLimitCooldownFailsOver(t *testing.T) {
	pool, err := newAccountPoolWithConfig([]AccountCredential{
		{AccountID: "a", AccountName: "账号 A", Cookie: "sessionid_ss=a"},
		{AccountID: "b", AccountName: "账号 B", Cookie: "sessionid_ss=b"},
	}, poolConfig{globalParallel: 1, accountParallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err := pool.accountFor("room-1")
	if err != nil {
		t.Fatal(err)
	}
	if !pool.markFailure(first.credential.AccountID, errors.New("请求失败: http 444")) {
		t.Fatal("expected HTTP 444 to put the account into cooldown")
	}
	pool.dropAssignment("room-1", first.credential.AccountID)
	second, err := pool.accountFor("room-1")
	if err != nil {
		t.Fatal(err)
	}
	if second.credential.AccountID == first.credential.AccountID {
		t.Fatalf("expected failover away from %s", first.credential.AccountID)
	}
}

func TestAccountPoolTimeoutNeedsThreeConsecutiveFailures(t *testing.T) {
	pool, err := newAccountPoolWithConfig([]AccountCredential{{
		AccountID: "a", AccountName: "账号 A", Cookie: "sessionid_ss=a",
	}}, poolConfig{globalParallel: 1, accountParallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	requestErr := errors.New("context deadline exceeded")
	if pool.markFailure("a", requestErr) || pool.markFailure("a", requestErr) {
		t.Fatal("one or two timeouts must not immediately cool the account")
	}
	if !pool.markFailure("a", requestErr) {
		t.Fatal("third consecutive timeout should cool the account")
	}
	account := pool.accounts["a"]
	if account == nil || time.Until(account.cooldownUntil) <= 0 {
		t.Fatal("expected a future cooldown deadline")
	}
}

func TestAccountPoolSuccessClearsTransientFailureStreak(t *testing.T) {
	pool, err := newAccountPoolWithConfig([]AccountCredential{{
		AccountID: "a", AccountName: "账号 A", Cookie: "sessionid_ss=a",
	}}, poolConfig{globalParallel: 1, accountParallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	pool.markFailure("a", errors.New("timeout"))
	pool.markFailure("a", errors.New("timeout"))
	pool.markSuccess("a")
	if pool.accounts["a"].failureStreak != 0 {
		t.Fatalf("expected success to reset failure streak, got %d", pool.accounts["a"].failureStreak)
	}
}

func TestAccountPoolTransportErrorNeedsThreeConsecutiveFailures(t *testing.T) {
	pool, err := newAccountPoolWithConfig([]AccountCredential{{
		AccountID: "a", AccountName: "账号 A", Cookie: "sessionid_ss=a",
	}}, poolConfig{globalParallel: 1, accountParallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	if pool.markFailure("a", io.ErrUnexpectedEOF) || pool.markFailure("a", io.ErrUnexpectedEOF) {
		t.Fatal("one or two transport errors must not immediately cool the account")
	}
	if !pool.markFailure("a", io.ErrUnexpectedEOF) {
		t.Fatal("third consecutive transport error should cool the account")
	}
}
