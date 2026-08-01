package redpacket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"fubao.ccvar.com/engine/internal/accounts"
	"fubao.ccvar.com/engine/internal/live/httpclient"
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
	RecordRedPacketParticipation(accountID, status, message string, joined bool, cooldown time.Duration, cookieExpired bool)
}

type ParticipationRecordStore interface {
	ReserveParticipation(event Event, accountID, accountName string) (bool, error)
	CompleteParticipation(eventID, accountID, endpoint, status, message string, attempts int, joined bool, cooldown time.Duration) error
}

type signedPoster interface {
	PostSigned(context.Context, string, map[string]string) (*http.Response, error)
}

type participantClientFactory func(Event, accounts.RedPacketParticipationCredential) signedPoster

// Participant dispatches each explicit red-packet event to every currently
// eligible opted-in participation account. Attempts are idempotent per
// account/event for the lifetime of the engine; persisted events are not
// replayed when the engine starts, so a restart cannot re-enqueue old events.
type Participant struct {
	store         ParticipationStore
	recordStore   ParticipationRecordStore
	clientFactory participantClientFactory
	sem           chan struct{}
	retryDelay    time.Duration

	mu        sync.Mutex
	attempted map[string]struct{}
}

type participationResult struct {
	status        string
	message       string
	joined        bool
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
	for _, credential := range p.store.RedPacketParticipationCredentials(time.Now()) {
		credential := credential
		key := credential.AccountID + "\x00" + event.ID
		if !p.reserve(key) {
			continue
		}
		go p.run(event, credential, key)
	}
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

func (p *Participant) run(event Event, credential accounts.RedPacketParticipationCredential, key string) {
	p.sem <- struct{}{}
	defer func() { <-p.sem }()

	// A toggle may have been switched off while this task waited for the bounded
	// worker slot. Re-read eligibility immediately before sending native traffic.
	if !p.stillEligible(credential.AccountID) {
		p.forget(key)
		return
	}
	if p.recordStore != nil {
		reserved, err := p.recordStore.ReserveParticipation(event, credential.AccountID, credential.AccountName)
		if err != nil || !reserved {
			return
		}
	}

	result := p.attempt(event, credential)
	if p.recordStore != nil {
		_ = p.recordStore.CompleteParticipation(
			event.ID, credential.AccountID, result.endpoint, result.status, result.message,
			result.attempts, result.joined, result.cooldown,
		)
	}
	p.store.RecordRedPacketParticipation(
		credential.AccountID,
		result.status,
		result.message,
		result.joined,
		result.cooldown,
		result.cookieExpired,
	)
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
		last = classifyParticipationResponse(response, err)
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

func classifyParticipationResponse(response *http.Response, requestErr error) participationResult {
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
	if statusCode(payload) == 0 && (flagOn(data, "succeed", "succeeded", "joined", "has_joined", "hasJoined", "success", "is_success", "isSuccess") ||
		containsAny(combined, "参与成功", "成功参与", "等待开奖")) {
		return participationResult{status: "joined", message: firstNonEmpty(message, "红包参与请求已受理"), joined: true, terminal: true}
	}
	return participationResult{status: "failed", message: firstNonEmpty(message, "红包接口未确认参与结果")}
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
