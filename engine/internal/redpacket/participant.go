package redpacket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fubao.ccvar.com/engine/internal/accounts"
	"fubao.ccvar.com/engine/internal/live/httpclient"
)

var (
	packetTotalSharePattern = regexp.MustCompile(`总\s*([0-9]+(?:\.[0-9]+)?)\s*钻\s*[,，]\s*([0-9]+)\s*份`)
	packetPerSharePattern   = regexp.MustCompile(`每份\s*([0-9]+(?:\.[0-9]+)?)\s*钻`)
)

const (
	redPacketJoinURL          = "https://live.douyin.com/webcast/luckybox/join/"
	redPacketRushURL          = "https://live.douyin.com/webcast/luckybox/rush/"
	defaultJoinConcurrency    = 4
	defaultRiskCooldown       = 5 * time.Minute
	defaultNetworkCooldown    = 30 * time.Second
	defaultParticipantTimeout = 12 * time.Second
)

// ParticipationStore is the private account-store surface used by the
// scheduler. The credential method is intentionally absent from frontend RPC.
type ParticipationStore interface {
	RedPacketParticipationCredentials(time.Time) []accounts.RedPacketParticipationCredential
	RecordRedPacketParticipation(accountID, status, message string, joined, won bool, cooldown time.Duration, cookieExpired bool)
}

type ParticipationRecordStore interface {
	ReserveParticipation(event Event, accountID, accountName string) (bool, error)
	CompleteParticipation(eventID, accountID, endpoint, status, message string, attempts int, joined, won bool, cooldown time.Duration) error
}

type participationPolicyStore interface {
	ParticipationPolicy(accountID string, now time.Time) (bool, time.Duration)
}

type participationDrawRecordStore interface {
	PendingDraws(accountID string) []Event
	ResolveParticipationDraw(eventID, accountID, status, message, award string, attempts int) (bool, error)
}

type participationEventBacklogStore interface {
	EventsAll() []Event
}

type participationTaskLifecycleStore interface {
	GetParticipationState(accountID string, now time.Time) ParticipationState
	FinishParticipationTask(accountID, reason string) error
}

type participationSettingsStore interface {
	GetParticipationSettings() ParticipationSettings
}

type participationTraceStore interface {
	RecordParticipationTrace(task PageParticipationTask, response PageParticipationResponse) error
}

type participationAccountStopper interface {
	StopAccount(accountID string)
}

type participationDrawAccountStore interface {
	RecordRedPacketDrawResult(accountID, message string, won bool)
}

type participationRetryStore interface {
	ResetParticipation(eventID, accountID string) (bool, error)
}

type participationCancellationStore interface {
	CancelParticipation(eventID, accountID string) error
}

// PageParticipationTask contains only the minimum native metadata needed to
// execute a luckybox action inside an account's live-room WebView. Cookies,
// signed URLs and response bodies never cross the frontend JavaScript bridge.
type PageParticipationTask struct {
	Action       string
	EventID      string
	AccountID    string
	AccountName  string
	WebRID       string
	ActualRoomID string
	BoxID        string
	AnchorID     string
	BoxType      string
	SendTime     string
	DelayTime    string
}

// PageParticipationResponse is returned over the authenticated native
// Rust-to-Go channel after the live page has issued the request. Body remains
// native-only and is reduced to safe status metadata before persistence.
type PageParticipationResponse struct {
	Endpoint       string
	HTTPStatus     int
	Body           string
	Error          string
	Attempts       int
	ContextMissing bool
	LoginExpired   bool
}

// PageParticipationExecutor keeps luckybox participation in the same browser
// page context that owns the account session and Douyin's bdms.js signer.
type PageParticipationExecutor interface {
	Ready(accountID string) bool
	Execute(context.Context, PageParticipationTask) PageParticipationResponse
}

type signedPoster interface {
	PostSigned(context.Context, string, map[string]string) (*http.Response, error)
}

type participantClientFactory func(Event, accounts.RedPacketParticipationCredential) signedPoster

// Participant dispatches each explicit red-packet event to every currently
// eligible opted-in participation account. The record store reserves each
// account/event pair before sending so attempts remain idempotent across engine
// restarts as well as repeated callbacks in the current process.
type Participant struct {
	store         ParticipationStore
	recordStore   ParticipationRecordStore
	clientFactory participantClientFactory
	pageExecutor  PageParticipationExecutor
	sem           chan struct{}
	retryDelay    time.Duration

	mu        sync.Mutex
	attempted map[string]struct{}
	accounts  map[string]*sync.Mutex
	resolving map[string]struct{}
}

type participationResult struct {
	status        string
	message       string
	joined        bool
	won           bool
	cooldown      time.Duration
	cookieExpired bool
	terminal      bool
	endpoint      string
	attempts      int
}

// NewParticipant creates the native-only luckybox join scheduler.
func NewParticipant(store ParticipationStore, recordStores ...ParticipationRecordStore) *Participant {
	return newParticipant(store, defaultJoinConcurrency, func(event Event, credential accounts.RedPacketParticipationCredential) signedPoster {
		roomPageID := firstNonEmpty(event.WebRID, event.RoomID)
		return httpclient.New(
			httpclient.WithCookie(credential.Cookie),
			httpclient.WithRoomURL("https://live.douyin.com/"+roomPageID),
			httpclient.WithTimeout(defaultParticipantTimeout),
		)
	}, recordStores...)
}

// NewPageParticipant creates the production scheduler that delegates every
// luckybox request to the matching account's prepared live-room WebView.
func NewPageParticipant(store ParticipationStore, executor PageParticipationExecutor, recordStores ...ParticipationRecordStore) *Participant {
	participant := newParticipant(store, defaultJoinConcurrency, nil, recordStores...)
	participant.pageExecutor = executor
	return participant
}

func newParticipant(store ParticipationStore, concurrency int, factory participantClientFactory, recordStores ...ParticipationRecordStore) *Participant {
	if concurrency < 1 {
		concurrency = 1
	}
	participant := &Participant{
		store:         store,
		clientFactory: factory,
		sem:           make(chan struct{}, concurrency),
		retryDelay:    250 * time.Millisecond,
		attempted:     map[string]struct{}{},
		accounts:      map[string]*sync.Mutex{},
		resolving:     map[string]struct{}{},
	}
	if len(recordStores) > 0 {
		participant.recordStore = recordStores[0]
	}
	return participant
}

// HandleEvent is suitable for Store.SetEventHandler. The red-packet store calls
// it only for events that passed the explicit 红包 filter and only after its
// lock has been released.
func (p *Participant) HandleEvent(event Event) {
	if p == nil || p.store == nil || strings.TrimSpace(event.ID) == "" ||
		strings.TrimSpace(event.JoinBoxID) == "" || strings.TrimSpace(event.ActualRoomID) == "" {
		return
	}
	if !p.meetsMinimumDiamonds(event) {
		return
	}
	for _, credential := range p.store.RedPacketParticipationCredentials(time.Now()) {
		p.dispatch(event, credential)
	}
}

func (p *Participant) meetsMinimumDiamonds(event Event) bool {
	settingsStore, ok := p.recordStore.(participationSettingsStore)
	if !ok {
		return true
	}
	minimum := settingsStore.GetParticipationSettings().MinimumDiamonds
	if minimum <= 0 {
		minimum = 1
	}
	perShare, known := eventDiamondsPerShare(event)
	return !known || perShare >= float64(minimum)
}

func eventDiamondsPerShare(event Event) (float64, bool) {
	if event.TotalDiamonds > 0 && event.ShareCount > 0 {
		return event.TotalDiamonds / float64(event.ShareCount), true
	}
	prize := strings.TrimSpace(event.Prize)
	if matches := packetPerSharePattern.FindStringSubmatch(prize); len(matches) == 2 {
		value, err := strconv.ParseFloat(matches[1], 64)
		return value, err == nil && value > 0
	}
	if matches := packetTotalSharePattern.FindStringSubmatch(prize); len(matches) == 3 {
		total, totalErr := strconv.ParseFloat(matches[1], 64)
		shares, shareErr := strconv.Atoi(matches[2])
		if totalErr == nil && shareErr == nil && total > 0 && shares > 0 {
			return total / float64(shares), true
		}
	}
	return 0, false
}

func (p *Participant) dispatch(event Event, credential accounts.RedPacketParticipationCredential) {
	if p.pageExecutor != nil && !p.pageExecutor.Ready(credential.AccountID) {
		return
	}
	if !p.policyAllows(credential.AccountID) {
		return
	}
	key := credential.AccountID + "\x00" + event.ID
	if !p.reserve(key) {
		return
	}
	go p.run(event, credential, key)
}

// RetryEventForAccount is used after the user explicitly prepares an account's
// live-room context. It retries only a prior non-joined failure and never
// duplicates an accepted or already-joined participation.
func (p *Participant) RetryEventForAccount(event Event, accountID string) {
	if p == nil || p.store == nil || p.pageExecutor == nil || !p.pageExecutor.Ready(accountID) ||
		strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.JoinBoxID) == "" || strings.TrimSpace(event.ActualRoomID) == "" {
		return
	}
	var selected *accounts.RedPacketParticipationCredential
	for _, credential := range p.store.RedPacketParticipationCredentials(time.Now()) {
		if credential.AccountID == accountID {
			copy := credential
			selected = &copy
			break
		}
	}
	if selected == nil {
		return
	}
	if retryStore, ok := p.recordStore.(participationRetryStore); ok {
		reset, err := retryStore.ResetParticipation(event.ID, accountID)
		if err != nil || !reset {
			return
		}
	}
	key := accountID + "\x00" + event.ID
	p.forget(key)
	p.dispatch(event, *selected)
}

func (p *Participant) reserve(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.attempted[key]; exists {
		return false
	}
	p.attempted[key] = struct{}{}
	return true
}

func (p *Participant) forget(key string) {
	p.mu.Lock()
	delete(p.attempted, key)
	p.mu.Unlock()
}

func (p *Participant) accountLock(accountID string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	lock := p.accounts[accountID]
	if lock == nil {
		lock = &sync.Mutex{}
		p.accounts[accountID] = lock
	}
	return lock
}

func (p *Participant) run(event Event, credential accounts.RedPacketParticipationCredential, key string) {
	// One account must never enter several luckybox actions concurrently. The
	// platform treats that burst as rush_spam; after the previous task records a
	// cooldown, the next waiter re-checks eligibility and is discarded safely.
	accountLock := p.accountLock(credential.AccountID)
	accountLock.Lock()
	defer accountLock.Unlock()

	p.sem <- struct{}{}
	defer func() { <-p.sem }()

	// A toggle may have been switched off while this task waited for the bounded
	// worker slot. Re-read eligibility immediately before sending native traffic.
	if !p.stillEligible(credential.AccountID) || !p.policyAllows(credential.AccountID) {
		p.forget(key)
		return
	}
	if p.pageExecutor != nil && !p.pageExecutor.Ready(credential.AccountID) {
		p.forget(key)
		return
	}
	if p.recordStore != nil {
		reserved, err := p.recordStore.ReserveParticipation(event, credential.AccountID, credential.AccountName)
		if err != nil {
			p.forget(key)
			return
		}
		if !reserved {
			return
		}
	}

	result := p.attempt(event, credential)
	if result.joined && result.cooldown <= 0 {
		if policy, ok := p.recordStore.(participationPolicyStore); ok {
			_, configured := policy.ParticipationPolicy(credential.AccountID, time.Now())
			result.cooldown = configured
		}
	}
	if result.status == "context_required" && result.attempts == 0 {
		// No native request left the account page. An explicitly stopped or
		// vanished context must therefore release both the durable reservation
		// and the in-memory key instead of becoming a misleading failure row.
		if cancellationStore, ok := p.recordStore.(participationCancellationStore); ok {
			_ = cancellationStore.CancelParticipation(event.ID, credential.AccountID)
		}
		p.forget(key)
		return
	}
	if p.recordStore != nil {
		_ = p.recordStore.CompleteParticipation(
			event.ID, credential.AccountID, result.endpoint, result.status, result.message,
			result.attempts, result.joined, result.won, result.cooldown,
		)
	}
	p.store.RecordRedPacketParticipation(
		credential.AccountID,
		result.status,
		result.message,
		result.joined,
		result.won,
		result.cooldown,
		result.cookieExpired,
	)
	if result.joined && !result.won && p.pageExecutor != nil {
		p.scheduleDraw(event, credential.AccountID, credential.AccountName)
	}
	p.finishTaskIfComplete(credential.AccountID)
}

// ResolvePendingDraws resumes accepted records whenever an account page
// context becomes available again, including after an application restart.
func (p *Participant) ResolvePendingDraws(accountID string) {
	drawStore, ok := p.recordStore.(participationDrawRecordStore)
	if !ok || p.pageExecutor == nil || !p.pageExecutor.Ready(accountID) {
		return
	}
	for _, event := range drawStore.PendingDraws(accountID) {
		p.scheduleDraw(event, accountID, "")
	}
}

func (p *Participant) scheduleDraw(event Event, accountID, accountName string) {
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.JoinBoxID) == "" {
		return
	}
	key := accountID + "\x00" + event.ID
	p.mu.Lock()
	if _, exists := p.resolving[key]; exists {
		p.mu.Unlock()
		return
	}
	p.resolving[key] = struct{}{}
	p.mu.Unlock()
	go func() {
		defer func() {
			p.mu.Lock()
			delete(p.resolving, key)
			p.mu.Unlock()
		}()
		p.resolveDraw(event, accountID, accountName)
	}()
}

func (p *Participant) resolveDraw(event Event, accountID, accountName string) {
	drawStore, ok := p.recordStore.(participationDrawRecordStore)
	if !ok || p.pageExecutor == nil {
		return
	}
	target := drawResultTime(event)
	timeout := 10 * time.Second
	if settingsStore, ok := p.recordStore.(participationSettingsStore); ok {
		seconds := settingsStore.GetParticipationSettings().DrawResultTimeoutSeconds
		if seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	deadline := target.Add(timeout)
	if delay := time.Until(target); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
	}
	attempts := 0
	for time.Now().Before(deadline) {
		if !p.pageExecutor.Ready(accountID) {
			if wait := minDuration(time.Second, time.Until(deadline)); wait > 0 {
				time.Sleep(wait)
			}
			continue
		}
		requestTimeout := time.Until(deadline)
		if requestTimeout > 25*time.Second {
			requestTimeout = 25 * time.Second
		}
		if requestTimeout <= 0 {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		task := PageParticipationTask{
			Action: "receive", EventID: event.ID, AccountID: accountID, AccountName: accountName,
			WebRID: event.WebRID, ActualRoomID: event.ActualRoomID, BoxID: event.JoinBoxID, AnchorID: event.AnchorID,
		}
		response := p.pageExecutor.Execute(ctx, task)
		cancel()
		attempts++
		if traceStore, ok := p.recordStore.(participationTraceStore); ok {
			_ = traceStore.RecordParticipationTrace(task, response)
		}
		if response.LoginExpired || response.HTTPStatus == http.StatusUnauthorized || response.HTTPStatus == http.StatusForbidden || containsLoginFailure(strings.ToLower(response.Body)) {
			p.store.RecordRedPacketParticipation(accountID, "login_expired", "CK 已失效：开奖结果接口返回未登录", false, false, 0, true)
			if wait := time.Until(deadline); wait > 0 {
				time.Sleep(wait)
			}
			break
		}
		bodyLower := strings.ToLower(response.Body)
		if response.HTTPStatus == http.StatusTooManyRequests || response.HTTPStatus == 444 || containsAny(bodyLower, "风控", "操作频繁", "rush_spam", "rush_too_often", "验证码") {
			p.store.RecordRedPacketParticipation(accountID, "risk_control", "开奖结果查询触发风控，账号已进入冷却", false, false, defaultRiskCooldown, false)
		}
		if response.ContextMissing {
			if wait := minDuration(time.Second, time.Until(deadline)); wait > 0 {
				time.Sleep(wait)
			}
			continue
		}
		if strings.TrimSpace(response.Error) != "" || response.HTTPStatus < 200 || response.HTTPStatus >= 300 {
			if wait := minDuration(2*time.Second, time.Until(deadline)); wait > 0 {
				time.Sleep(wait)
			}
			continue
		}
		outcome := classifyReceiveResponse(response.Body, event.JoinBoxID)
		if outcome.status == "" {
			if wait := minDuration(2*time.Second, time.Until(deadline)); wait > 0 {
				time.Sleep(wait)
			}
			continue
		}
		newWin, err := drawStore.ResolveParticipationDraw(event.ID, accountID, outcome.status, outcome.message, outcome.award, attempts)
		if err == nil {
			if accountStore, ok := p.store.(participationDrawAccountStore); ok && (newWin || outcome.status == "not_won") {
				accountStore.RecordRedPacketDrawResult(accountID, outcome.message, newWin)
			}
			p.retryCurrentEventsForAccount(accountID)
			p.finishTaskIfComplete(accountID)
		}
		return
	}
	message := fmt.Sprintf("开奖异常：超过开奖时间 %d 秒仍未获取到结果", int(timeout/time.Second))
	if traceStore, ok := p.recordStore.(participationTraceStore); ok {
		_ = traceStore.RecordParticipationTrace(PageParticipationTask{
			Action: "receive_timeout", EventID: event.ID, AccountID: accountID, AccountName: accountName,
			WebRID: event.WebRID, ActualRoomID: event.ActualRoomID, BoxID: event.JoinBoxID, AnchorID: event.AnchorID,
		}, PageParticipationResponse{Endpoint: "receive", Error: message, Attempts: attempts})
	}
	if _, err := drawStore.ResolveParticipationDraw(event.ID, accountID, "draw_error", message, "", attempts); err == nil {
		p.retryCurrentEventsForAccount(accountID)
		p.finishTaskIfComplete(accountID)
	}
}

func minDuration(first, second time.Duration) time.Duration {
	if first < second {
		return first
	}
	return second
}

func (p *Participant) finishTaskIfComplete(accountID string) {
	lifecycle, ok := p.recordStore.(participationTaskLifecycleStore)
	if !ok {
		return
	}
	state := lifecycle.GetParticipationState(accountID, time.Now())
	if !state.Stopped {
		return
	}
	if draws, ok := p.recordStore.(participationDrawRecordStore); ok && len(draws.PendingDraws(accountID)) > 0 {
		return
	}
	reason := state.StopReason
	if err := lifecycle.FinishParticipationTask(accountID, reason); err != nil {
		return
	}
	if stopper, ok := p.pageExecutor.(participationAccountStopper); ok {
		stopper.StopAccount(accountID)
	}
}

func (p *Participant) retryCurrentEventsForAccount(accountID string) {
	backlog, ok := p.recordStore.(participationEventBacklogStore)
	if !ok || p.pageExecutor == nil || !p.pageExecutor.Ready(accountID) {
		return
	}
	if policy, ok := p.recordStore.(participationPolicyStore); ok {
		allowed, wait := policy.ParticipationPolicy(accountID, time.Now())
		if !allowed {
			if wait > 0 {
				time.AfterFunc(wait, func() { p.retryCurrentEventsForAccount(accountID) })
			}
			return
		}
	}
	now := time.Now()
	for _, event := range backlog.EventsAll() {
		if expiresAt, err := time.Parse(time.RFC3339Nano, event.ExpiresAt); err == nil && !now.Before(expiresAt) {
			continue
		}
		p.RetryEventForAccount(event, accountID)
	}
}

func drawResultTime(event Event) time.Time {
	for _, value := range []string{event.DrawAt, event.ExpiresAt} {
		if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return time.Now().Add(5 * time.Second)
}

type drawOutcome struct {
	status  string
	message string
	award   string
}

func classifyReceiveResponse(body, boxID string) drawOutcome {
	var payload map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(body)), &payload) != nil || statusCode(payload) != 0 {
		return drawOutcome{}
	}
	data, _ := payload["data"].(map[string]any)
	value, present := data["receive_info"]
	if !present {
		return drawOutcome{}
	}
	if value == nil {
		return drawOutcome{status: "not_won", message: "未中奖"}
	}
	infos, ok := value.([]any)
	if !ok {
		return drawOutcome{}
	}
	if len(infos) == 0 {
		return drawOutcome{status: "not_won", message: "未中奖"}
	}
	var selected map[string]any
	var idless map[string]any
	for _, item := range infos {
		info, _ := item.(map[string]any)
		if info == nil {
			continue
		}
		candidate := firstMapString(info, "box_id_str", "boxIdStr", "box_id", "boxId", "activity_id", "activityId")
		if candidate == strings.TrimSpace(boxID) {
			selected = info
			break
		}
		if candidate == "" && idless == nil {
			idless = info
		}
	}
	if selected == nil && len(infos) == 1 {
		selected = idless
	}
	if selected == nil || !hasAnyKey(selected, "succeed", "success") {
		return drawOutcome{}
	}
	if !flagOn(selected, "succeed", "success") {
		return drawOutcome{status: "not_won", message: "未中奖"}
	}
	award := receiveAward(selected)
	message := "已中奖"
	if award != "" {
		message = "已中" + award
	}
	return drawOutcome{status: "won", message: message, award: award}
}

func firstMapString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(fmt.Sprint(values[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}

func receiveAward(info map[string]any) string {
	for _, key := range []string{"diamond_count", "cash_count", "diamond", "amount"} {
		if value := positiveParticipantInt(info[key]); value > 0 {
			return fmt.Sprintf("%d钻", value)
		}
	}
	name := firstMapString(info, "gift_name", "giftName", "prize_name", "prizeName", "reward_name", "rewardName")
	if name == "" {
		return ""
	}
	count := 1
	for _, key := range []string{"gift_count", "giftCount", "gift_num", "giftNum", "count"} {
		if value := positiveParticipantInt(info[key]); value > 0 {
			count = value
			break
		}
	}
	return fmt.Sprintf("%d个%s", count, name)
}

func positiveParticipantInt(value any) int {
	switch item := value.(type) {
	case float64:
		return int(item)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(item))
		return parsed
	default:
		return 0
	}
}

func (p *Participant) policyAllows(accountID string) bool {
	if policy, ok := p.recordStore.(participationPolicyStore); ok {
		allowed, _ := policy.ParticipationPolicy(accountID, time.Now())
		return allowed
	}
	return true
}

func (p *Participant) stillEligible(accountID string) bool {
	for _, item := range p.store.RedPacketParticipationCredentials(time.Now()) {
		if item.AccountID == accountID {
			return true
		}
	}
	return false
}

func (p *Participant) attempt(event Event, credential accounts.RedPacketParticipationCredential) participationResult {
	if p.pageExecutor != nil {
		return p.attemptInPage(event, credential)
	}
	client := p.clientFactory(event, credential)
	params := participationParams(event, false)
	result := p.postWithNetworkRetry(client, redPacketJoinURL, "join", params)
	if result.terminal {
		return result
	}
	if event.BoxType == "" || event.SendTime == "" || event.DelayTime == "" {
		return result
	}
	rush := p.postWithNetworkRetry(client, redPacketRushURL, "rush", participationParams(event, true))
	rush.attempts += result.attempts
	return rush
}

func (p *Participant) attemptInPage(event Event, credential accounts.RedPacketParticipationCredential) participationResult {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	task := PageParticipationTask{
		Action:       "join",
		EventID:      event.ID,
		AccountID:    credential.AccountID,
		AccountName:  credential.AccountName,
		WebRID:       strings.TrimSpace(event.WebRID),
		ActualRoomID: strings.TrimSpace(event.ActualRoomID),
		BoxID:        strings.TrimSpace(event.JoinBoxID),
		AnchorID:     strings.TrimSpace(event.AnchorID),
		BoxType:      strings.TrimSpace(event.BoxType),
		SendTime:     strings.TrimSpace(event.SendTime),
		DelayTime:    strings.TrimSpace(event.DelayTime),
	}
	response := p.pageExecutor.Execute(ctx, task)
	if traceStore, ok := p.recordStore.(participationTraceStore); ok {
		_ = traceStore.RecordParticipationTrace(task, response)
	}
	if response.ContextMissing {
		return participationResult{
			status: "context_required", message: firstNonEmpty(response.Error, "请先在浏览器实例中启用红包参与页面"),
			endpoint: "page", attempts: response.Attempts,
		}
	}
	if response.LoginExpired {
		return participationResult{
			status: "login_expired", message: firstNonEmpty(response.Error, "CK 已失效：直播页面未登录"),
			cookieExpired: true, terminal: true, endpoint: "page", attempts: response.Attempts,
		}
	}
	if strings.TrimSpace(response.Error) != "" {
		return participationResult{
			status: "network_error", message: response.Error, cooldown: defaultNetworkCooldown,
			endpoint: firstNonEmpty(response.Endpoint, "page"), attempts: max(1, response.Attempts),
		}
	}
	result := classifyParticipationResponse(&http.Response{
		StatusCode: response.HTTPStatus,
		Body:       io.NopCloser(strings.NewReader(response.Body)),
	}, nil, response.Endpoint)
	result.endpoint = firstNonEmpty(response.Endpoint, "page")
	result.attempts = max(1, response.Attempts)
	return result
}

func participationParams(event Event, rush bool) map[string]string {
	params := map[string]string{
		"aid":             "6383",
		"app_name":        "douyin_web",
		"live_id":         "1",
		"device_platform": "web",
		"room_id":         strings.TrimSpace(event.ActualRoomID),
		"box_id":          strings.TrimSpace(event.JoinBoxID),
	}
	if event.AnchorID != "" {
		params["anchor_id"] = strings.TrimSpace(event.AnchorID)
	}
	if rush {
		params["box_type"] = strings.TrimSpace(event.BoxType)
		params["send_time"] = strings.TrimSpace(event.SendTime)
		params["delay_time"] = strings.TrimSpace(event.DelayTime)
	}
	return params
}

func (p *Participant) postWithNetworkRetry(client signedPoster, endpoint, endpointName string, params map[string]string) participationResult {
	var last participationResult
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), defaultParticipantTimeout)
		response, err := client.PostSigned(ctx, endpoint, params)
		last = classifyParticipationResponse(response, err, endpointName)
		last.endpoint = endpointName
		last.attempts = attempt + 1
		cancel()
		if last.status != "network_error" || attempt == 1 {
			return last
		}
		if p.retryDelay > 0 {
			time.Sleep(p.retryDelay)
		}
	}
	return last
}

func classifyParticipationResponse(response *http.Response, requestErr error, endpoints ...string) participationResult {
	if requestErr != nil {
		return participationResult{status: "network_error", message: "红包接口网络请求暂时失败", cooldown: defaultNetworkCooldown}
	}
	if response == nil {
		return participationResult{status: "network_error", message: "红包接口未返回响应", cooldown: defaultNetworkCooldown}
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	if readErr != nil {
		return participationResult{status: "network_error", message: "红包接口响应读取失败", cooldown: defaultNetworkCooldown}
	}
	text := strings.TrimSpace(string(body))
	lower := strings.ToLower(text)
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || containsLoginFailure(lower) {
		return participationResult{status: "login_expired", message: "CK 已失效：红包接口返回未登录", cookieExpired: true, terminal: true}
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == 444 {
		return participationResult{status: "risk_control", message: fmt.Sprintf("红包接口触发频率限制（HTTP %d）", response.StatusCode), cooldown: defaultRiskCooldown, terminal: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode >= 500 {
			return participationResult{status: "network_error", message: fmt.Sprintf("红包接口暂时不可用（HTTP %d）", response.StatusCode), cooldown: defaultNetworkCooldown}
		}
		return participationResult{status: "failed", message: fmt.Sprintf("红包接口拒绝请求（HTTP %d）", response.StatusCode)}
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return participationResult{status: "failed", message: "红包接口返回了无法识别的响应"}
	}
	data, _ := payload["data"].(map[string]any)
	message := firstResponseMessage(payload, data)
	combined := strings.ToLower(text + " " + message)
	if containsLoginFailure(combined) {
		return participationResult{status: "login_expired", message: "CK 已失效：红包接口返回未登录", cookieExpired: true, terminal: true}
	}
	if flagOn(data, "rush_spam", "rush_too_often", "need_verify", "needVerify", "captcha", "risk", "risky") ||
		containsAny(combined, "风控", "访问过于频繁", "操作频繁", "验证码", "rush_spam", "rush_too_often") {
		return participationResult{status: "risk_control", message: firstNonEmpty(message, "红包接口触发风控，账号已进入冷却"), cooldown: defaultRiskCooldown, terminal: true}
	}
	if flagOn(data, "expired", "is_expired", "isExpired") || containsAny(combined, "已结束", "已过期", "来晚", "已错过") {
		return participationResult{status: "expired", message: firstNonEmpty(message, "红包已结束"), terminal: true}
	}
	if flagOn(data, "rush_too_much") || containsAny(combined, "已参与", "已经参与", "already joined", "already_joined") {
		return participationResult{status: "already_joined", message: firstNonEmpty(message, "红包已受理"), joined: true, terminal: true}
	}
	won := flagOn(data, "hit_bonus", "hitBonus", "won", "is_winner", "isWinner") || positiveNumber(data, "diamond_count", "diamondCount", "amount")
	if statusCode(payload) == 0 && (flagOn(data, "succeed", "succeeded", "joined", "has_joined", "hasJoined", "success", "is_success", "isSuccess") ||
		containsAny(combined, "参与成功", "成功参与", "等待开奖")) {
		return participationResult{status: "joined", message: firstNonEmpty(message, "红包参与请求已受理"), joined: true, won: won, terminal: true}
	}
	endpoint := ""
	if len(endpoints) > 0 {
		endpoint = strings.ToLower(strings.TrimSpace(endpoints[0]))
	}
	if endpoint == "join" && statusCode(payload) == 0 && hasAnyKey(data,
		"succeed", "succeeded", "hit_bonus", "hitBonus", "can_rush_gem", "canRushGem", "joined", "has_joined", "hasJoined") && !containsAny(combined,
		"未达到", "未达成", "未完成", "请先", "需要先", "未登录", "请登录", "风控", "验证码",
		"验证失败", "过期", "已结束", "已错过", "来晚", "频繁", "太快", "失败", "不能", "无法",
		"不可", "没有资格", "钻石不足", "余额不足", "支付失败", "充值", "电脑端暂未支持") {
		// Real luckybox/join responses often carry succeed=false and
		// hit_bonus=false while the page has already entered the draw pool.
		return participationResult{status: "joined", message: firstNonEmpty(message, "红包参与请求已受理，等待开奖"), joined: true, won: won, terminal: true}
	}
	return participationResult{status: "failed", message: firstNonEmpty(message, "红包接口未确认参与结果")}
}

func hasAnyKey(data map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, exists := data[key]; exists {
			return true
		}
	}
	return false
}

func positiveNumber(data map[string]any, keys ...string) bool {
	for _, key := range keys {
		switch value := data[key].(type) {
		case float64:
			if value > 0 {
				return true
			}
		case string:
			parsed, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if parsed > 0 {
				return true
			}
		}
	}
	return false
}

func statusCode(payload map[string]any) int64 {
	value, ok := payload["status_code"]
	if !ok {
		return -1
	}
	switch item := value.(type) {
	case float64:
		return int64(item)
	case json.Number:
		parsed, _ := item.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(item, 10, 64)
		return parsed
	default:
		return -1
	}
}

func flagOn(data map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, exists := data[key]
		if !exists {
			continue
		}
		switch item := value.(type) {
		case bool:
			if item {
				return true
			}
		case float64:
			if item != 0 {
				return true
			}
		case string:
			if normalized := strings.ToLower(strings.TrimSpace(item)); normalized != "" && normalized != "0" && normalized != "false" && normalized != "none" && normalized != "null" {
				return true
			}
		}
	}
	return false
}

func firstResponseMessage(payload, data map[string]any) string {
	for _, source := range []map[string]any{payload, data} {
		for _, key := range []string{"status_msg", "message", "msg", "toast"} {
			if value := strings.TrimSpace(fmt.Sprint(source[key])); value != "" && value != "<nil>" {
				return value
			}
		}
	}
	return ""
}

func containsLoginFailure(text string) bool {
	return containsAny(text, "未登录", "请登录", "登录失效", "登录已失效", "login required", "not logged", "session expired")
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, strings.ToLower(value)) {
			return true
		}
	}
	return false
}
