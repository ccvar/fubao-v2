package redpacket

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"fubao.ccvar.com/engine/internal/live/httpclient"
	"fubao.ccvar.com/engine/internal/live/poller"
)

const (
	// All monitoring accounts share the same local network, so a global gate is
	// still required after rooms are distributed across accounts.  This keeps a
	// 1,000+ room start from turning into one large request burst.
	defaultGlobalRequestInterval  = 80 * time.Millisecond
	defaultAccountRequestInterval = 750 * time.Millisecond
	// Concurrency is deliberately larger than the request-rate gates. Requests
	// still begin no faster than one globally every 80ms and one per account
	// every 750ms, but slow Windows/network responses no longer leave those safe
	// pacing windows idle while the previous request is still completing.
	defaultGlobalConcurrency  = 32
	defaultAccountConcurrency = 3
	defaultRateLimitCooldown  = 2 * time.Minute
	defaultTimeoutCooldown    = 30 * time.Second
	maximumAccountCooldown    = 15 * time.Minute
)

var errMonitoringAccountCooling = errors.New("监测账号正在冷却")

// AccountCredential is trusted engine-only data. Cookie values are consumed
// by the Go monitor and are never serialized to frontend IPC responses.
type AccountCredential struct {
	AccountID   string
	AccountName string
	Cookie      string
}

type AccountLoad struct {
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	RoomCount   int    `json:"room_count"`
}

type PoolStartResult struct {
	Started      int           `json:"started"`
	AccountCount int           `json:"account_count"`
	Assignments  []AccountLoad `json:"assignments"`
}

// PoolRefreshResult is safe runtime metadata for a hot account-pool refresh.
// It never contains Cookie values or other native credentials.
type PoolRefreshResult struct {
	Active       bool `json:"active"`
	Added        int  `json:"added"`
	Updated      int  `json:"updated"`
	Removed      int  `json:"removed"`
	AccountCount int  `json:"account_count"`
	Rebalanced   int  `json:"rebalanced"`
}

type poolConfig struct {
	globalInterval  time.Duration
	accountInterval time.Duration
	globalParallel  int
	accountParallel int
}

type accountPool struct {
	mu          sync.Mutex
	accounts    map[string]*poolAccount
	ordered     []*poolAccount
	assignments map[string]string
	globalGate  *requestGate
	config      poolConfig
}

type poolAccount struct {
	credential    AccountCredential
	gate          *requestGate
	cooldownUntil time.Time
	failureStreak int
}

type requestGate struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
	slots    chan struct{}
}

type poolUnavailableError struct {
	retryAfter time.Duration
}

func (e *poolUnavailableError) Error() string {
	if e.retryAfter <= 0 {
		return "当前没有可用的监测账号"
	}
	return fmt.Sprintf("所有监测账号都在冷却，约 %s 后重试", compactDuration(e.retryAfter))
}

func defaultPoolConfig() poolConfig {
	return poolConfig{
		globalInterval:  defaultGlobalRequestInterval,
		accountInterval: defaultAccountRequestInterval,
		globalParallel:  defaultGlobalConcurrency,
		accountParallel: defaultAccountConcurrency,
	}
}

func newAccountPool(credentials []AccountCredential) (*accountPool, error) {
	return newAccountPoolWithConfig(credentials, defaultPoolConfig())
}

func newAccountPoolWithConfig(credentials []AccountCredential, config poolConfig) (*accountPool, error) {
	config = normalizedPoolConfig(config)
	pool := &accountPool{
		accounts:    map[string]*poolAccount{},
		assignments: map[string]string{},
		globalGate:  newRequestGate(config.globalInterval, config.globalParallel),
		config:      config,
	}
	for _, credential := range normalizedAccountCredentials(credentials) {
		account := &poolAccount{
			credential: credential,
			gate:       newRequestGate(config.accountInterval, config.accountParallel),
		}
		pool.accounts[credential.AccountID] = account
		pool.ordered = append(pool.ordered, account)
	}
	if len(pool.ordered) == 0 {
		return nil, errors.New("没有可用的监测账号 Cookie")
	}
	sort.Slice(pool.ordered, func(i, j int) bool {
		return pool.ordered[i].credential.AccountID < pool.ordered[j].credential.AccountID
	})
	return pool, nil
}

func normalizedPoolConfig(config poolConfig) poolConfig {
	if config.globalInterval < 0 {
		config.globalInterval = 0
	}
	if config.accountInterval < 0 {
		config.accountInterval = 0
	}
	if config.globalParallel < 1 {
		config.globalParallel = 1
	}
	if config.accountParallel < 1 {
		config.accountParallel = 1
	}
	return config
}

func normalizedAccountCredentials(credentials []AccountCredential) []AccountCredential {
	byID := make(map[string]AccountCredential, len(credentials))
	for _, credential := range credentials {
		credential.AccountID = strings.TrimSpace(credential.AccountID)
		credential.AccountName = strings.TrimSpace(credential.AccountName)
		credential.Cookie = strings.TrimSpace(credential.Cookie)
		if credential.AccountID == "" || credential.Cookie == "" {
			continue
		}
		if credential.AccountName == "" {
			credential.AccountName = credential.AccountID
		}
		byID[credential.AccountID] = credential
	}
	items := make([]AccountCredential, 0, len(byID))
	for _, credential := range byID {
		items = append(items, credential)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AccountID < items[j].AccountID })
	return items
}

// syncCredentials hot-reloads monitoring credentials without interrupting an
// already-issued request. Existing poolAccount pointers remain valid for
// in-flight calls; changed credentials are installed as replacement entries
// and are observed by the next accountFor lookup.
func (p *accountPool) syncCredentials(credentials []AccountCredential) PoolRefreshResult {
	normalized := normalizedAccountCredentials(credentials)
	nextCredentials := make(map[string]AccountCredential, len(normalized))
	for _, credential := range normalized {
		nextCredentials[credential.AccountID] = credential
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	result := PoolRefreshResult{Active: true}
	nextAccounts := make(map[string]*poolAccount, len(nextCredentials))
	nextOrdered := make([]*poolAccount, 0, len(nextCredentials))
	for _, credential := range normalized {
		existing := p.accounts[credential.AccountID]
		if existing == nil {
			existing = &poolAccount{
				credential: credential,
				gate:       newRequestGate(p.config.accountInterval, p.config.accountParallel),
			}
			result.Added++
		} else if existing.credential != credential {
			existing = &poolAccount{
				credential:    credential,
				gate:          existing.gate,
				cooldownUntil: existing.cooldownUntil,
				failureStreak: existing.failureStreak,
			}
			result.Updated++
		}
		nextAccounts[credential.AccountID] = existing
		nextOrdered = append(nextOrdered, existing)
	}
	for accountID := range p.accounts {
		if _, exists := nextCredentials[accountID]; !exists {
			result.Removed++
		}
	}
	p.accounts = nextAccounts
	p.ordered = nextOrdered
	result.AccountCount = len(nextOrdered)
	if result.Added > 0 || result.Removed > 0 {
		// StartAllPool preassigns every room. Clear those stable assignments only
		// when membership changes so subsequent polls naturally rebalance across
		// the expanded/reduced pool without cancelling in-flight requests.
		result.Rebalanced = len(p.assignments)
		p.assignments = make(map[string]string, len(p.assignments))
	}
	return result
}

// applyConfig replaces the gates used by future requests. Existing account
// pointers and gates remain alive for already-issued calls, so changing a
// monitoring setting never interrupts or mis-releases an in-flight request.
func (p *accountPool) applyConfig(config poolConfig) {
	config = normalizedPoolConfig(config)
	p.mu.Lock()
	nextAccounts := make(map[string]*poolAccount, len(p.accounts))
	nextOrdered := make([]*poolAccount, 0, len(p.ordered))
	for _, account := range p.ordered {
		replacement := &poolAccount{
			credential:    account.credential,
			gate:          newRequestGate(config.accountInterval, config.accountParallel),
			cooldownUntil: account.cooldownUntil,
			failureStreak: account.failureStreak,
		}
		nextAccounts[replacement.credential.AccountID] = replacement
		nextOrdered = append(nextOrdered, replacement)
	}
	p.accounts = nextAccounts
	p.ordered = nextOrdered
	p.globalGate = newRequestGate(config.globalInterval, config.globalParallel)
	p.config = config
	p.mu.Unlock()
}

func newRequestGate(interval time.Duration, parallel int) *requestGate {
	if parallel < 1 {
		parallel = 1
	}
	return &requestGate{interval: interval, slots: make(chan struct{}, parallel)}
}

func (g *requestGate) acquire(ctx context.Context) (func(), error) {
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-g.slots }

	g.mu.Lock()
	now := time.Now()
	readyAt := now
	if g.next.After(readyAt) {
		readyAt = g.next
	}
	g.next = readyAt.Add(g.interval)
	g.mu.Unlock()

	if wait := time.Until(readyAt); wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			release()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return release, nil
}

func (p *accountPool) accountFor(monitorID string) (*poolAccount, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if accountID := p.assignments[monitorID]; accountID != "" {
		if account := p.accounts[accountID]; account != nil && !account.cooldownUntil.After(now) {
			return account, nil
		}
		delete(p.assignments, monitorID)
	}

	var selected *poolAccount
	var selectedScore uint64
	var earliestCooldown time.Time
	for _, account := range p.ordered {
		if account.cooldownUntil.After(now) {
			if earliestCooldown.IsZero() || account.cooldownUntil.Before(earliestCooldown) {
				earliestCooldown = account.cooldownUntil
			}
			continue
		}
		score := rendezvousScore(monitorID, account.credential.AccountID)
		if selected == nil || score > selectedScore {
			selected, selectedScore = account, score
		}
	}
	if selected == nil {
		retryAfter := time.Duration(0)
		if !earliestCooldown.IsZero() {
			retryAfter = time.Until(earliestCooldown)
		}
		return nil, &poolUnavailableError{retryAfter: retryAfter}
	}
	p.assignments[monitorID] = selected.credential.AccountID
	return selected, nil
}

func (p *accountPool) dropAssignment(monitorID, accountID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.assignments[monitorID] == accountID {
		delete(p.assignments, monitorID)
	}
}

func (p *accountPool) markSuccess(accountID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if account := p.accounts[accountID]; account != nil {
		account.failureStreak = 0
		account.cooldownUntil = time.Time{}
	}
}

func (p *accountPool) markFailure(accountID string, requestErr error) bool {
	if requestErr == nil || errors.Is(requestErr, errMonitoringAccountCooling) {
		return false
	}
	message := strings.ToLower(requestErr.Error())
	p.mu.Lock()
	defer p.mu.Unlock()
	account := p.accounts[accountID]
	if account == nil {
		return false
	}
	account.failureStreak++
	var cooldown time.Duration
	switch {
	case strings.Contains(message, "http 444"), strings.Contains(message, "http 429"):
		cooldown = exponentialCooldown(defaultRateLimitCooldown, account.failureStreak)
	case strings.Contains(message, "http 401"), strings.Contains(message, "http 403"):
		cooldown = exponentialCooldown(5*time.Minute, account.failureStreak)
	case strings.Contains(message, "deadline exceeded"), strings.Contains(message, "timeout"):
		if account.failureStreak >= 3 {
			cooldown = exponentialCooldown(defaultTimeoutCooldown, account.failureStreak-2)
		}
	case isMonitoringTransportError(requestErr):
		// A single connection reset/EOF is common on a long-running webcast
		// connection. Only cool and fail over after repeated transport failures
		// so a brief network wobble does not reshuffle room assignments.
		if account.failureStreak >= 3 {
			cooldown = exponentialCooldown(defaultTimeoutCooldown, account.failureStreak-2)
		}
	}
	if cooldown <= 0 {
		return false
	}
	account.cooldownUntil = time.Now().Add(cooldown)
	return true
}

func isMonitoringTransportError(requestErr error) bool {
	if requestErr == nil {
		return false
	}
	var networkError net.Error
	if errors.As(requestErr, &networkError) {
		return true
	}
	if errors.Is(requestErr, io.EOF) || errors.Is(requestErr, io.ErrUnexpectedEOF) {
		return true
	}
	message := strings.ToLower(requestErr.Error())
	for _, marker := range []string{
		"connection reset", "connection refused", "broken pipe", "unexpected eof",
		"no such host", "server misbehaving", "temporary failure", "tls handshake",
		"server sent goaway",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (p *accountPool) withPermit(ctx context.Context, accountID string, request func() error) error {
	p.mu.Lock()
	account := p.accounts[accountID]
	if account == nil {
		p.mu.Unlock()
		return errors.New("监测账号已从账号池移除")
	}
	if account.cooldownUntil.After(time.Now()) {
		p.mu.Unlock()
		return errMonitoringAccountCooling
	}
	globalGate := p.globalGate
	p.mu.Unlock()

	releaseAccount, err := account.gate.acquire(ctx)
	if err != nil {
		return err
	}
	defer releaseAccount()
	releaseGlobal, err := globalGate.acquire(ctx)
	if err != nil {
		return err
	}
	defer releaseGlobal()

	p.mu.Lock()
	cooling := account.cooldownUntil.After(time.Now())
	p.mu.Unlock()
	if cooling {
		return errMonitoringAccountCooling
	}
	return request()
}

func (p *accountPool) summary() []AccountLoad {
	p.mu.Lock()
	defer p.mu.Unlock()
	counts := make(map[string]int, len(p.accounts))
	for _, accountID := range p.assignments {
		counts[accountID]++
	}
	items := make([]AccountLoad, 0, len(p.ordered))
	for _, account := range p.ordered {
		items = append(items, AccountLoad{
			AccountID:   account.credential.AccountID,
			AccountName: account.credential.AccountName,
			RoomCount:   counts[account.credential.AccountID],
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].RoomCount == items[j].RoomCount {
			return items[i].AccountName < items[j].AccountName
		}
		return items[i].RoomCount > items[j].RoomCount
	})
	return items
}

func rendezvousScore(monitorID, accountID string) uint64 {
	sum := sha256.Sum256([]byte(monitorID + "\x00" + accountID))
	return binary.BigEndian.Uint64(sum[:8])
}

func exponentialCooldown(base time.Duration, streak int) time.Duration {
	if streak < 1 {
		streak = 1
	}
	result := base
	for i := 1; i < streak && result < maximumAccountCooldown; i++ {
		result *= 2
	}
	if result > maximumAccountCooldown {
		return maximumAccountCooldown
	}
	return result
}

func compactDuration(value time.Duration) string {
	if value < time.Second {
		return "1 秒"
	}
	if value < time.Minute {
		return fmt.Sprintf("%d 秒", int(value.Round(time.Second)/time.Second))
	}
	return fmt.Sprintf("%d 分钟", int(value.Round(time.Minute)/time.Minute))
}

type pooledMonitorSource struct {
	inner     monitorSource
	pool      *accountPool
	accountID string
}

func (s *pooledMonitorSource) ProbeLive(ctx context.Context) (probe LiveProbe, err error) {
	err = s.pool.withPermit(ctx, s.accountID, func() error {
		var requestErr error
		probe, requestErr = s.inner.ProbeLive(ctx)
		return requestErr
	})
	return probe, err
}

func (s *pooledMonitorSource) Fetch(ctx context.Context) (snapshots []poller.Snapshot, err error) {
	err = s.pool.withPermit(ctx, s.accountID, func() error {
		var requestErr error
		snapshots, requestErr = s.inner.Fetch(ctx)
		return requestErr
	})
	return snapshots, err
}

func (p *accountPool) sourceFor(account *poolAccount, webRID, actualRoomID string) monitorSource {
	client := httpclient.New(
		httpclient.WithCookie(account.credential.Cookie),
		httpclient.WithRoomURL("https://live.douyin.com/"+webRID),
	)
	return &pooledMonitorSource{
		inner:     newSource(client, webRID, actualRoomID),
		pool:      p,
		accountID: account.credential.AccountID,
	}
}

type observedMonitorSource struct {
	inner   monitorSource
	lastErr error
}

func (s *observedMonitorSource) ProbeLive(ctx context.Context) (LiveProbe, error) {
	probe, err := s.inner.ProbeLive(ctx)
	s.lastErr = err
	return probe, err
}

func (s *observedMonitorSource) Fetch(ctx context.Context) ([]poller.Snapshot, error) {
	snapshots, err := s.inner.Fetch(ctx)
	s.lastErr = err
	return snapshots, err
}
