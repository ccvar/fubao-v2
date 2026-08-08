package redpacket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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

func TestDefaultPoolKeepsSafePacingWhileAllowingSlowRequestsToOverlap(t *testing.T) {
	config := defaultPoolConfig()
	if config.globalInterval != 80*time.Millisecond || config.accountInterval != 750*time.Millisecond {
		t.Fatalf("request pacing changed unexpectedly: global=%s account=%s", config.globalInterval, config.accountInterval)
	}
	if !config.globalIntervalAuto {
		t.Fatal("the machine-wide pace must default to 自动 so extra accounts raise throughput")
	}
	if config.globalParallel != 32 || config.accountParallel != 3 {
		t.Fatalf("slow-request overlap window not applied: global=%d account=%d", config.globalParallel, config.accountParallel)
	}
}

func TestAutoGlobalIntervalScalesWithTheAccountFleet(t *testing.T) {
	for _, testCase := range []struct {
		accounts int
		want     time.Duration
	}{
		// A small fleet cannot be paced faster than the historical default.
		{1, defaultGlobalRequestInterval},
		{9, defaultGlobalRequestInterval},
		// 750ms/102 accounts is the fleet's own sustainable pace.
		{102, 750 * time.Millisecond / 102},
		// A very large fleet is bounded by the shared egress-IP floor.
		{1000, autoGlobalRequestFloor},
	} {
		if got := autoGlobalInterval(defaultAccountRequestInterval, testCase.accounts); got != testCase.want {
			t.Fatalf("%d accounts derived %s, want %s", testCase.accounts, got, testCase.want)
		}
	}
}

func TestAutoGlobalPaceFollowsPoolMembership(t *testing.T) {
	credentials := make([]AccountCredential, 0, 60)
	for i := 0; i < 60; i++ {
		credentials = append(credentials, AccountCredential{
			AccountID: fmt.Sprintf("account-%03d", i), Cookie: fmt.Sprintf("sessionid_ss=%03d", i),
		})
	}
	pool, err := newAccountPoolWithConfig(credentials[:1], defaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	if pool.globalGate.interval != defaultGlobalRequestInterval {
		t.Fatalf("single-account pool should keep the conservative pace, got %s", pool.globalGate.interval)
	}
	if result := pool.syncCredentials(credentials); result.AccountCount != 60 {
		t.Fatalf("hot refresh did not install the full fleet: %+v", result)
	}
	want := autoGlobalInterval(defaultAccountRequestInterval, 60)
	if pool.globalGate.interval != want {
		t.Fatalf("importing accounts did not raise the machine-wide pace: got %s want %s",
			pool.globalGate.interval, want)
	}
}

func TestExplicitGlobalPaceIsNotOverriddenByFleetSize(t *testing.T) {
	config := defaultPoolConfig()
	config.globalInterval = 200 * time.Millisecond
	config.globalIntervalAuto = false
	credentials := make([]AccountCredential, 0, 50)
	for i := 0; i < 50; i++ {
		credentials = append(credentials, AccountCredential{
			AccountID: fmt.Sprintf("account-%03d", i), Cookie: fmt.Sprintf("sessionid_ss=%03d", i),
		})
	}
	pool, err := newAccountPoolWithConfig(credentials, config)
	if err != nil {
		t.Fatal(err)
	}
	if pool.globalGate.interval != 200*time.Millisecond {
		t.Fatalf("an explicitly configured pace must be honoured, got %s", pool.globalGate.interval)
	}
}

func TestRequestGatePacingDoesNotOccupyConcurrencySlots(t *testing.T) {
	// Two slots and a 50ms pace: three callers must all be paced, but no caller
	// may hold a slot while it is merely waiting for its turn. Holding a slot
	// during the pacing wait is what made "并发" mean something other than the
	// number of requests actually in flight.
	gate := newRequestGate(50*time.Millisecond, 2)
	var peak, current atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			readyAt := gate.reserve()
			if err := sleepUntil(context.Background(), readyAt); err != nil {
				return
			}
			release, err := gate.acquireSlot(context.Background())
			if err != nil {
				return
			}
			if now := current.Add(1); now > peak.Load() {
				peak.Store(now)
			}
			time.Sleep(10 * time.Millisecond)
			current.Add(-1)
			release()
		}()
	}
	wg.Wait()
	// Each caller is paced 50ms apart but only occupies a slot for its 10ms
	// "request", so the slots never fill up.
	if peak.Load() > 2 {
		t.Fatalf("in-flight slots exceeded the configured concurrency: %d", peak.Load())
	}
}

func TestPoolSharesOneConnectionPoolAcrossProbes(t *testing.T) {
	pool, err := newAccountPoolWithConfig([]AccountCredential{
		{AccountID: "a", Cookie: "sessionid_ss=a"},
		{AccountID: "b", Cookie: "sessionid_ss=b"},
	}, defaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	if pool.httpClient == nil {
		t.Fatal("pool did not build a shared HTTP client")
	}
	accountA := pool.accounts["a"]
	accountB := pool.accounts["b"]
	first, _ := pool.sourceFor(accountA, "111111", "1").(*pooledMonitorSource)
	second, _ := pool.sourceFor(accountA, "222222", "2").(*pooledMonitorSource)
	third, _ := pool.sourceFor(accountB, "333333", "3").(*pooledMonitorSource)
	if first == nil || second == nil || third == nil {
		t.Fatal("sourceFor no longer returns a pooled monitor source")
	}
	// Every probe, for every room and every account, must reuse the same
	// executor. A per-probe transport costs a full TLS handshake per room.
	firstInner := first.inner.(*source)
	secondInner := second.inner.(*source)
	thirdInner := third.inner.(*source)
	if firstInner.client.Doer() != pool.httpClient ||
		secondInner.client.Doer() != pool.httpClient ||
		thirdInner.client.Doer() != pool.httpClient {
		t.Fatal("probe clients did not reuse the pool's shared connection pool")
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

func TestAccountPoolHotRefreshRebalancesWithoutInterruptingExistingPointer(t *testing.T) {
	pool, err := newAccountPoolWithConfig([]AccountCredential{
		{AccountID: "a", AccountName: "账号 A", Cookie: "sessionid_ss=a"},
		{AccountID: "b", AccountName: "账号 B", Cookie: "sessionid_ss=b"},
	}, poolConfig{globalParallel: 4, accountParallel: 2})
	if err != nil {
		t.Fatal(err)
	}
	oldAccount, err := pool.accountFor("room-existing")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 500; index++ {
		if _, err := pool.accountFor(fmt.Sprintf("room-%03d", index)); err != nil {
			t.Fatal(err)
		}
	}
	result := pool.syncCredentials([]AccountCredential{
		{AccountID: "a", AccountName: "账号 A", Cookie: "sessionid_ss=a-new"},
		{AccountID: "b", AccountName: "账号 B", Cookie: "sessionid_ss=b"},
		{AccountID: "c", AccountName: "账号 C", Cookie: "sessionid_ss=c"},
	})
	if result.Added != 1 || result.Updated != 1 || result.Removed != 0 || result.AccountCount != 3 || result.Rebalanced == 0 {
		t.Fatalf("unexpected hot refresh result: %+v", result)
	}
	if oldAccount.credential.Cookie == "sessionid_ss=a-new" {
		t.Fatal("in-flight account pointer was mutated")
	}
	counts := map[string]int{}
	for index := 0; index < 500; index++ {
		account, err := pool.accountFor(fmt.Sprintf("room-%03d", index))
		if err != nil {
			t.Fatal(err)
		}
		counts[account.credential.AccountID]++
	}
	if counts["c"] == 0 {
		t.Fatalf("new account did not receive rebalanced rooms: %+v", counts)
	}
	currentA := pool.accounts["a"]
	if currentA == nil || currentA.credential.Cookie != "sessionid_ss=a-new" {
		t.Fatal("updated credential was not installed for subsequent requests")
	}

	result = pool.syncCredentials([]AccountCredential{
		{AccountID: "a", AccountName: "账号 A", Cookie: "sessionid_ss=a-new"},
		{AccountID: "c", AccountName: "账号 C", Cookie: "sessionid_ss=c"},
	})
	if result.Removed != 1 || result.AccountCount != 2 || pool.accounts["b"] != nil {
		t.Fatalf("removed account remained in hot pool: result=%+v", result)
	}
}

func TestAccountPoolHotConfigKeepsInflightPointerAndUpdatesFutureGates(t *testing.T) {
	pool, err := newAccountPoolWithConfig([]AccountCredential{{
		AccountID: "a", AccountName: "账号 A", Cookie: "sessionid_ss=a",
	}}, poolConfig{
		globalInterval: 80 * time.Millisecond, accountInterval: 750 * time.Millisecond,
		globalParallel: 32, accountParallel: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldAccount, err := pool.accountFor("room-a")
	if err != nil {
		t.Fatal(err)
	}
	oldGlobalGate := pool.globalGate
	pool.applyConfig(poolConfig{
		globalInterval: 120 * time.Millisecond, accountInterval: 900 * time.Millisecond,
		globalParallel: 20, accountParallel: 2,
	})
	current, err := pool.accountFor("room-a")
	if err != nil {
		t.Fatal(err)
	}
	if current == oldAccount || current.gate == oldAccount.gate || pool.globalGate == oldGlobalGate {
		t.Fatal("future requests did not receive replacement gates")
	}
	if current.gate.interval != 900*time.Millisecond || cap(current.gate.slots) != 2 ||
		pool.globalGate.interval != 120*time.Millisecond || cap(pool.globalGate.slots) != 20 {
		t.Fatalf("unexpected hot config: account=%s/%d global=%s/%d",
			current.gate.interval, cap(current.gate.slots), pool.globalGate.interval, cap(pool.globalGate.slots))
	}
	if oldAccount.gate.interval != 750*time.Millisecond || cap(oldAccount.gate.slots) != 3 {
		t.Fatal("in-flight account gate was mutated")
	}
}

func TestHotConcurrencyRaiseGrowsTheKeepAlivePool(t *testing.T) {
	pool, err := newAccountPoolWithConfig([]AccountCredential{
		{AccountID: "a", Cookie: "sessionid_ss=a"},
	}, defaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	if pool.idleConnsPerHost != monitorMinIdleConnsPerHost {
		t.Fatalf("unexpected initial keep-alive pool: %d", pool.idleConnsPerHost)
	}
	original := pool.sharedHTTPClient()

	// Raising 全局慢请求并发 past the current pool must install a bigger one,
	// otherwise the extra slots go back to one TLS handshake per request.
	config := defaultPoolConfig()
	config.globalParallel = 128
	pool.applyConfig(config)
	if pool.idleConnsPerHost != 128 {
		t.Fatalf("keep-alive pool did not grow with concurrency: %d", pool.idleConnsPerHost)
	}
	grown := pool.sharedHTTPClient()
	if grown == original {
		t.Fatal("a larger concurrency must install a replacement connection pool")
	}
	transport, ok := grown.Transport.(*http.Transport)
	if !ok || transport.MaxIdleConnsPerHost != 128 {
		t.Fatalf("replacement transport was not sized for the new concurrency: %+v", transport)
	}

	// Lowering it again keeps the warm pool: shrinking buys nothing and would
	// throw away established connections.
	config.globalParallel = 8
	pool.applyConfig(config)
	if pool.idleConnsPerHost != 128 || pool.sharedHTTPClient() != grown {
		t.Fatalf("lowering concurrency must not rebuild the connection pool: %d", pool.idleConnsPerHost)
	}
}

type recordedCooldown struct {
	accountID string
	until     time.Time
	reason    string
	message   string
}

func TestMonitorCooldownIsReportedWithItsRealCause(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		requestErr error
		repeats    int
		wantReason string
	}{
		{"rate limit", errors.New("开播探测接口返回 HTTP 429"), 1, "rate_limited"},
		{"blocked", errors.New("开播探测接口返回 HTTP 444"), 1, "rate_limited"},
		{"auth", errors.New("开播探测接口返回 HTTP 403"), 1, "auth"},
		{"repeated timeout", errors.New("context deadline exceeded"), 3, "network"},
		{"repeated transport", io.ErrUnexpectedEOF, 3, "network"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			pool, err := newAccountPoolWithConfig([]AccountCredential{
				{AccountID: "a", Cookie: "sessionid_ss=a"},
			}, defaultPoolConfig())
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var recorded []recordedCooldown
			pool.setCooldownRecorder(func(accountID string, until time.Time, reason, message string) {
				mu.Lock()
				defer mu.Unlock()
				recorded = append(recorded, recordedCooldown{accountID, until, reason, message})
			})
			for i := 0; i < testCase.repeats; i++ {
				pool.markFailure("a", testCase.requestErr)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(recorded) != 1 {
				t.Fatalf("expected exactly one cooldown report, got %d: %+v", len(recorded), recorded)
			}
			got := recorded[0]
			if got.accountID != "a" || got.reason != testCase.wantReason {
				t.Fatalf("cooldown reported as %+v, want reason %q", got, testCase.wantReason)
			}
			if !got.until.After(time.Now()) {
				t.Fatalf("cooldown expiry is not in the future: %s", got.until)
			}
			if got.message == "" {
				t.Fatal("cooldown must carry safe display copy")
			}
			// The message is rendered in the UI, so it must never leak the raw
			// request or response text.
			if strings.Contains(got.message, "HTTP") || strings.Contains(got.message, "deadline") {
				t.Fatalf("cooldown message leaked raw request detail: %q", got.message)
			}
		})
	}
}

func TestTransientFailureReportsNoCooldown(t *testing.T) {
	pool, err := newAccountPoolWithConfig([]AccountCredential{
		{AccountID: "a", Cookie: "sessionid_ss=a"},
	}, defaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	reports := 0
	pool.setCooldownRecorder(func(string, time.Time, string, string) { reports++ })
	// One timeout is a wobble, not a back-off, and must stay invisible.
	pool.markFailure("a", errors.New("context deadline exceeded"))
	if reports != 0 {
		t.Fatalf("a single timeout must not report a cooldown, got %d", reports)
	}
	// An already-cooling error is the pool's own signal, never a new cooldown.
	pool.markFailure("a", errMonitoringAccountCooling)
	if reports != 0 {
		t.Fatalf("the cooling sentinel must not report a cooldown, got %d", reports)
	}
}

func TestSuccessClearsTheReportedCooldownExactlyOnce(t *testing.T) {
	pool, err := newAccountPoolWithConfig([]AccountCredential{
		{AccountID: "a", Cookie: "sessionid_ss=a"},
	}, defaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var recorded []recordedCooldown
	pool.setCooldownRecorder(func(accountID string, until time.Time, reason, message string) {
		mu.Lock()
		defer mu.Unlock()
		recorded = append(recorded, recordedCooldown{accountID, until, reason, message})
	})
	pool.markFailure("a", errors.New("开播探测接口返回 HTTP 429"))
	pool.markSuccess("a")
	// Every later success is a no-op: monitoring polls constantly and must not
	// write to the account store on each one.
	pool.markSuccess("a")
	pool.markSuccess("a")

	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 2 {
		t.Fatalf("expected one cooldown and one clear, got %+v", recorded)
	}
	if !recorded[1].until.IsZero() || recorded[1].reason != "" {
		t.Fatalf("clear must report a zero expiry, got %+v", recorded[1])
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
