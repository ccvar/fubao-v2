package redpacket

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"fubao.ccvar.com/engine/internal/accounts"
)

type participantRecord struct {
	accountID     string
	status        string
	joined        bool
	cooldown      time.Duration
	cookieExpired bool
}

type fakeParticipationStore struct {
	mu          sync.Mutex
	credentials []accounts.RedPacketParticipationCredential
	records     []participantRecord
	notify      chan struct{}
}

type fakeParticipationFollowMatcher struct {
	known   map[string]bool
	matches map[string]map[string]bool
}

func (m *fakeParticipationFollowMatcher) MatchRoom(accountID, webRID, actualRoomID, anchorID string) (bool, bool) {
	known := m.known[accountID]
	if !known {
		return false, false
	}
	for _, value := range []string{webRID, actualRoomID, anchorID} {
		if m.matches[accountID][value] {
			return true, true
		}
	}
	return false, true
}

func (s *fakeParticipationStore) RedPacketParticipationCredentials(time.Time) []accounts.RedPacketParticipationCredential {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]accounts.RedPacketParticipationCredential(nil), s.credentials...)
}

func (s *fakeParticipationStore) RecordRedPacketParticipation(accountID, status, _ string, joined, _ bool, cooldown time.Duration, cookieExpired bool) {
	s.mu.Lock()
	s.records = append(s.records, participantRecord{accountID, status, joined, cooldown, cookieExpired})
	s.mu.Unlock()
	if s.notify != nil {
		s.notify <- struct{}{}
	}
}

type fakePoster struct {
	mu        sync.Mutex
	responses []*http.Response
	errors    []error
	endpoints []string
	calls     int
}

type fakePageParticipationExecutor struct {
	mu       sync.Mutex
	ready    bool
	requests []PageParticipationTask
	response PageParticipationResponse
	stopped  []string
}

func (e *fakePageParticipationExecutor) StopAccount(accountID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ready = false
	e.stopped = append(e.stopped, accountID)
}

type serialPageParticipationExecutor struct {
	mu        sync.Mutex
	active    int
	maxActive int
	started   chan struct{}
	release   chan struct{}
}

func (e *serialPageParticipationExecutor) Ready(string) bool { return true }

func (e *serialPageParticipationExecutor) Execute(context.Context, PageParticipationTask) PageParticipationResponse {
	e.mu.Lock()
	e.active++
	if e.active > e.maxActive {
		e.maxActive = e.active
	}
	e.mu.Unlock()
	e.started <- struct{}{}
	<-e.release
	e.mu.Lock()
	e.active--
	e.mu.Unlock()
	return PageParticipationResponse{Endpoint: "join", HTTPStatus: 200, Body: `{"status_code":0,"data":{"succeed":true}}`, Attempts: 1}
}

func (e *fakePageParticipationExecutor) Ready(string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ready
}

func (e *fakePageParticipationExecutor) Execute(_ context.Context, task PageParticipationTask) PageParticipationResponse {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = append(e.requests, task)
	return e.response
}

func (p *fakePoster) PostSigned(_ context.Context, endpoint string, _ map[string]string) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := p.calls
	p.calls++
	p.endpoints = append(p.endpoints, endpoint)
	var response *http.Response
	var err error
	if index < len(p.responses) {
		response = p.responses[index]
	}
	if index < len(p.errors) {
		err = p.errors[index]
	}
	return response, err
}

func TestParticipantFallsBackToRushWhenJoinIsNotAccepted(t *testing.T) {
	store := &fakeParticipationStore{
		credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-1", Cookie: "sessionid_ss=ok"}},
		notify:      make(chan struct{}, 1),
	}
	poster := &fakePoster{responses: []*http.Response{
		jsonResponse(200, `{"status_code":1,"status_msg":"Request params error","data":{}}`),
		jsonResponse(200, `{"status_code":0,"data":{"rush_too_much":1}}`),
	}}
	participant := newParticipant(store, 1, func(Event, accounts.RedPacketParticipationCredential) signedPoster { return poster })
	participant.retryDelay = 0
	participant.HandleEvent(Event{
		ID: "monitor:event-rush", PacketID: "box-rush", JoinBoxID: "box-rush", ActualRoomID: "7002",
		BoxType: "1", SendTime: "100", DelayTime: "30",
	})
	select {
	case <-store.notify:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for rush fallback")
	}
	poster.mu.Lock()
	defer poster.mu.Unlock()
	if poster.calls != 2 || poster.endpoints[0] != redPacketJoinURL || poster.endpoints[1] != redPacketRushURL {
		t.Fatalf("expected join then rush fallback, got calls=%d endpoints=%v", poster.calls, poster.endpoints)
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

func TestParticipantDeduplicatesAccountAndEvent(t *testing.T) {
	recordDataDir := t.TempDir()
	recordStore, err := NewStore(recordDataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordStore.RecordParticipationStarted("account-1", "参与账号"); err != nil {
		t.Fatal(err)
	}
	store := &fakeParticipationStore{
		credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-1", Cookie: "sessionid_ss=ok"}},
		notify:      make(chan struct{}, 2),
	}
	poster := &fakePoster{responses: []*http.Response{jsonResponse(200, `{"status_code":0,"data":{"succeed":true}}`)}}
	participant := newParticipant(store, 1, func(Event, accounts.RedPacketParticipationCredential) signedPoster { return poster }, recordStore)
	event := Event{ID: "monitor:event-1", PacketID: "box-1", JoinBoxID: "box-1", ActualRoomID: "7001", Title: "钻石红包"}
	participant.HandleEvent(event)
	participant.HandleEvent(event)
	select {
	case <-store.notify:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for participation result")
	}
	time.Sleep(20 * time.Millisecond)
	poster.mu.Lock()
	calls := poster.calls
	poster.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected one native request for one account/event, got %d", calls)
	}
	store.mu.Lock()
	accountRecords := append([]participantRecord(nil), store.records...)
	store.mu.Unlock()
	if len(accountRecords) != 1 || accountRecords[0].status != "joined" || !accountRecords[0].joined {
		t.Fatalf("unexpected result records: %+v", accountRecords)
	}
	persisted := recordStore.ParticipationRecords()
	if len(persisted) != 1 || persisted[0].Status != "joined" || persisted[0].Endpoint != "join" || persisted[0].AttemptCount != 1 {
		t.Fatalf("native result was not persisted safely: %+v", persisted)
	}

	// A new Participant has an empty in-memory map. The persisted reservation
	// must still prevent the same account/event from being sent after restart.
	reloaded, err := NewStore(recordDataDir)
	if err != nil {
		t.Fatal(err)
	}
	secondPoster := &fakePoster{}
	second := newParticipant(store, 1, func(Event, accounts.RedPacketParticipationCredential) signedPoster { return secondPoster }, reloaded)
	second.HandleEvent(event)
	time.Sleep(20 * time.Millisecond)
	secondPoster.mu.Lock()
	defer secondPoster.mu.Unlock()
	if secondPoster.calls != 0 {
		t.Fatalf("persisted account/event reservation did not survive restart: %d calls", secondPoster.calls)
	}
}

func TestParticipantSkipsEventWithoutExplicitBoxID(t *testing.T) {
	store := &fakeParticipationStore{credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-1", Cookie: "sessionid_ss=ok"}}}
	poster := &fakePoster{}
	participant := newParticipant(store, 1, func(Event, accounts.RedPacketParticipationCredential) signedPoster { return poster })
	participant.HandleEvent(Event{ID: "monitor:hash-only", PacketID: "synthetic-history-id", ActualRoomID: "7001"})
	time.Sleep(20 * time.Millisecond)
	poster.mu.Lock()
	defer poster.mu.Unlock()
	if poster.calls != 0 {
		t.Fatalf("synthetic history IDs must never be sent as luckybox box_id, got %d calls", poster.calls)
	}
}

func TestParticipantMinimumDiamondsOnlyBlocksKnownBelowThreshold(t *testing.T) {
	recordStore, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordStore.SetParticipationSettings(ParticipationSettings{MinimumDiamonds: 2}); err != nil {
		t.Fatal(err)
	}
	if err := recordStore.RecordParticipationStarted("account-minimum", "门槛账号"); err != nil {
		t.Fatal(err)
	}
	store := &fakeParticipationStore{
		credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-minimum", Cookie: "sessionid_ss=ok"}},
		notify:      make(chan struct{}, 1),
	}
	poster := &fakePoster{responses: []*http.Response{jsonResponse(200, `{"status_code":0,"data":{"succeed":true}}`)}}
	participant := newParticipant(store, 1, func(Event, accounts.RedPacketParticipationCredential) signedPoster { return poster }, recordStore)

	participant.HandleEvent(Event{
		ID: "event-below-minimum", JoinBoxID: "10001", ActualRoomID: "7001",
		Title: "钻石红包", TotalDiamonds: 20, ShareCount: 15,
	})
	time.Sleep(20 * time.Millisecond)
	poster.mu.Lock()
	belowCalls := poster.calls
	poster.mu.Unlock()
	if belowCalls != 0 {
		t.Fatalf("known %.2f diamonds per share must be blocked by threshold 2, calls=%d", 20.0/15.0, belowCalls)
	}
	if records := recordStore.ParticipationRecords(); len(records) != 0 {
		t.Fatalf("blocked packet must not create a request record: %+v", records)
	}

	participant.HandleEvent(Event{
		ID: "event-unknown-amount", JoinBoxID: "10002", ActualRoomID: "7001", Title: "钻石红包", Prize: "红包金额待解析",
	})
	select {
	case <-store.notify:
	case <-time.After(time.Second):
		t.Fatal("unknown prize amount must remain eligible")
	}
	poster.mu.Lock()
	unknownCalls := poster.calls
	poster.mu.Unlock()
	if unknownCalls != 1 {
		t.Fatalf("unknown prize amount should not be guessed or blocked, calls=%d", unknownCalls)
	}

	if perShare, known := eventDiamondsPerShare(Event{Prize: "每份3钻"}); !known || perShare != 3 {
		t.Fatalf("explicit per-share amount not parsed: value=%v known=%v", perShare, known)
	}
	if perShare, known := eventDiamondsPerShare(Event{Prize: "总40钻，15份红包"}); !known || perShare != 40.0/15.0 {
		t.Fatalf("total/share prize not parsed: value=%v known=%v", perShare, known)
	}
}

func TestParticipantFiltersConfiguredPacketTypeBeforeDispatch(t *testing.T) {
	recordStore, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordStore.SetParticipationSettings(ParticipationSettings{PacketType: ParticipationPacketTypeGift}); err != nil {
		t.Fatal(err)
	}
	if err := recordStore.RecordParticipationStarted("account-type", "类型账号"); err != nil {
		t.Fatal(err)
	}
	store := &fakeParticipationStore{
		credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-type", Cookie: "sessionid_ss=ok"}},
		notify:      make(chan struct{}, 1),
	}
	poster := &fakePoster{responses: []*http.Response{jsonResponse(200, `{"status_code":0,"data":{"succeed":true}}`)}}
	participant := newParticipant(store, 1, func(Event, accounts.RedPacketParticipationCredential) signedPoster { return poster }, recordStore)

	participant.HandleEvent(Event{
		ID: "event-diamond", JoinBoxID: "20001", ActualRoomID: "7001", Title: "钻石红包",
	})
	participant.HandleEvent(Event{
		ID: "event-unknown", JoinBoxID: "20002", ActualRoomID: "7001", Title: "直播间红包",
	})
	time.Sleep(20 * time.Millisecond)
	poster.mu.Lock()
	blockedCalls := poster.calls
	poster.mu.Unlock()
	if blockedCalls != 0 {
		t.Fatalf("non-gift and unknown packets must be filtered before dispatch, calls=%d", blockedCalls)
	}

	participant.HandleEvent(Event{
		ID: "event-gift", JoinBoxID: "20003", ActualRoomID: "7001", Title: "礼物红包",
	})
	select {
	case <-store.notify:
	case <-time.After(time.Second):
		t.Fatal("matching gift packet was not dispatched")
	}
	poster.mu.Lock()
	defer poster.mu.Unlock()
	if poster.calls != 1 {
		t.Fatalf("matching gift packet should dispatch exactly once, calls=%d", poster.calls)
	}
}

func TestParticipantFollowOnlyDispatchesMatchingAccount(t *testing.T) {
	recordStore, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordStore.SetParticipationSettings(ParticipationSettings{
		PacketType: ParticipationPacketTypeDiamond, FollowPolicy: ParticipationFollowPolicyOnly,
	}); err != nil {
		t.Fatal(err)
	}
	for _, accountID := range []string{"account-followed", "account-other"} {
		if err := recordStore.RecordParticipationStarted(accountID, accountID); err != nil {
			t.Fatal(err)
		}
	}
	store := &fakeParticipationStore{
		credentials: []accounts.RedPacketParticipationCredential{
			{AccountID: "account-followed", Cookie: "sessionid_ss=one"},
			{AccountID: "account-other", Cookie: "sessionid_ss=two"},
		},
		notify: make(chan struct{}, 2),
	}
	poster := &fakePoster{responses: []*http.Response{jsonResponse(200, `{"status_code":0,"data":{"succeed":true}}`)}}
	participant := newParticipant(store, 1, func(Event, accounts.RedPacketParticipationCredential) signedPoster { return poster }, recordStore)
	participant.SetFollowMatcher(&fakeParticipationFollowMatcher{
		known: map[string]bool{"account-followed": true, "account-other": true},
		matches: map[string]map[string]bool{
			"account-followed": {"7654321": true},
			"account-other":    {},
		},
	})

	participant.HandleEvent(Event{
		ID: "event-follow-only", WebRID: "7654321", ActualRoomID: "700001",
		JoinBoxID: "box-follow-only", Title: "钻石红包",
	})
	select {
	case <-store.notify:
	case <-time.After(time.Second):
		t.Fatal("matching followed account was not dispatched")
	}
	time.Sleep(30 * time.Millisecond)
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.records) != 1 || store.records[0].accountID != "account-followed" {
		t.Fatalf("follow-only policy dispatched unexpected accounts: %+v", store.records)
	}
}

func TestParticipantFollowPriorityLetsFollowedPacketWinAccountSlot(t *testing.T) {
	recordStore, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordStore.SetParticipationSettings(ParticipationSettings{
		PacketType: ParticipationPacketTypeDiamond, FollowPolicy: ParticipationFollowPolicyPriority,
	}); err != nil {
		t.Fatal(err)
	}
	if err := recordStore.RecordParticipationStarted("account-priority", "优先账号"); err != nil {
		t.Fatal(err)
	}
	store := &fakeParticipationStore{
		credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-priority", Cookie: "sessionid_ss=ok"}},
		notify:      make(chan struct{}, 2),
	}
	nonFollowedPoster := &fakePoster{responses: []*http.Response{jsonResponse(200, `{"status_code":0,"data":{"succeed":true}}`)}}
	followedPoster := &fakePoster{responses: []*http.Response{jsonResponse(200, `{"status_code":0,"data":{"succeed":true}}`)}}
	participant := newParticipant(store, 1, func(event Event, _ accounts.RedPacketParticipationCredential) signedPoster {
		if event.WebRID == "followed-room" {
			return followedPoster
		}
		return nonFollowedPoster
	}, recordStore)
	participant.followPriorityDelay = 60 * time.Millisecond
	participant.SetFollowMatcher(&fakeParticipationFollowMatcher{
		known:   map[string]bool{"account-priority": true},
		matches: map[string]map[string]bool{"account-priority": {"followed-room": true}},
	})

	participant.HandleEvent(Event{
		ID: "event-other", WebRID: "other-room", ActualRoomID: "700001",
		JoinBoxID: "box-other", Title: "钻石红包",
	})
	participant.HandleEvent(Event{
		ID: "event-followed", WebRID: "followed-room", ActualRoomID: "700002",
		JoinBoxID: "box-followed", Title: "钻石红包",
	})
	select {
	case <-store.notify:
	case <-time.After(time.Second):
		t.Fatal("followed packet was not dispatched")
	}
	time.Sleep(100 * time.Millisecond)
	followedPoster.mu.Lock()
	followedCalls := followedPoster.calls
	followedPoster.mu.Unlock()
	nonFollowedPoster.mu.Lock()
	nonFollowedCalls := nonFollowedPoster.calls
	nonFollowedPoster.mu.Unlock()
	if followedCalls != 1 || nonFollowedCalls != 0 {
		t.Fatalf("priority policy did not preserve the account slot: followed=%d other=%d", followedCalls, nonFollowedCalls)
	}
}

func TestParticipantFollowPriorityFailsOpenButFollowOnlyFailsClosedWithoutSnapshot(t *testing.T) {
	for _, test := range []struct {
		name      string
		policy    string
		wantCalls int
	}{
		{name: "priority", policy: ParticipationFollowPolicyPriority, wantCalls: 1},
		{name: "only", policy: ParticipationFollowPolicyOnly, wantCalls: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			recordStore, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := recordStore.SetParticipationSettings(ParticipationSettings{
				PacketType: ParticipationPacketTypeDiamond, FollowPolicy: test.policy,
			}); err != nil {
				t.Fatal(err)
			}
			if err := recordStore.RecordParticipationStarted("account-unknown", "快照账号"); err != nil {
				t.Fatal(err)
			}
			store := &fakeParticipationStore{
				credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-unknown", Cookie: "sessionid_ss=ok"}},
				notify:      make(chan struct{}, 1),
			}
			poster := &fakePoster{responses: []*http.Response{jsonResponse(200, `{"status_code":0,"data":{"succeed":true}}`)}}
			participant := newParticipant(store, 1, func(Event, accounts.RedPacketParticipationCredential) signedPoster { return poster }, recordStore)
			participant.SetFollowMatcher(&fakeParticipationFollowMatcher{known: map[string]bool{}, matches: map[string]map[string]bool{}})
			participant.HandleEvent(Event{
				ID: "event-unknown-follow", WebRID: "7654321", ActualRoomID: "700001",
				JoinBoxID: "box-unknown-follow", Title: "钻石红包",
			})
			if test.wantCalls == 1 {
				select {
				case <-store.notify:
				case <-time.After(time.Second):
					t.Fatal("priority mode must fail open when no follow snapshot is available")
				}
			} else {
				time.Sleep(40 * time.Millisecond)
			}
			poster.mu.Lock()
			defer poster.mu.Unlock()
			if poster.calls != test.wantCalls {
				t.Fatalf("policy %s calls=%d want=%d", test.policy, poster.calls, test.wantCalls)
			}
		})
	}
}

func TestPageParticipantWaitsForPreparedLiveContext(t *testing.T) {
	store := &fakeParticipationStore{
		credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-page", AccountName: "页面账号"}},
		notify:      make(chan struct{}, 1),
	}
	executor := &fakePageParticipationExecutor{
		response: PageParticipationResponse{
			Endpoint: "rush", HTTPStatus: 200,
			Body: `{"status_code":0,"data":{"succeed":true}}`, Attempts: 2,
		},
	}
	participant := NewPageParticipant(store, executor)
	event := Event{
		ID: "monitor:page-event", WebRID: "7654321", ActualRoomID: "700001",
		JoinBoxID: "box-page", BoxType: "1", SendTime: "100", DelayTime: "30",
	}
	participant.HandleEvent(event)
	time.Sleep(25 * time.Millisecond)
	executor.mu.Lock()
	requestCount := len(executor.requests)
	executor.ready = true
	executor.mu.Unlock()
	if requestCount != 0 {
		t.Fatalf("unprepared browser context must not receive a page task, got %d", requestCount)
	}

	participant.HandleEvent(event)
	select {
	case <-store.notify:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for page-context participation")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.requests) != 1 {
		t.Fatalf("expected one page-context task, got %d", len(executor.requests))
	}
	request := executor.requests[0]
	if request.AccountID != "account-page" || request.WebRID != "7654321" || request.ActualRoomID != "700001" || request.BoxID != "box-page" {
		t.Fatalf("page task lost required native metadata: %+v", request)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.records) != 1 || store.records[0].status != "joined" || !store.records[0].joined {
		t.Fatalf("unexpected page participation record: %+v", store.records)
	}
}

func TestPageParticipantChallengeStopsNativeContextAndTask(t *testing.T) {
	recordStore, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := recordStore.RecordParticipationStarted("account-challenge", "验证账号"); err != nil {
		t.Fatal(err)
	}
	store := &fakeParticipationStore{
		credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-challenge", AccountName: "验证账号"}},
		notify:      make(chan struct{}, 1),
	}
	executor := &fakePageParticipationExecutor{
		ready: true,
		response: PageParticipationResponse{
			Endpoint: "page", Error: "验证码/安全验证拦截", ChallengeBlocked: true,
		},
	}
	participant := NewPageParticipant(store, executor, recordStore)
	participant.HandleEvent(Event{
		ID: "monitor:challenge-event", WebRID: "7654321", ActualRoomID: "700001",
		JoinBoxID: "7669047909329177395", Title: "钻石红包",
	})
	select {
	case <-store.notify:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for challenge classification")
	}

	store.mu.Lock()
	if len(store.records) != 1 || store.records[0].status != "challenge_blocked" || store.records[0].cookieExpired {
		t.Fatalf("challenge must remain distinct from CK expiry: %+v", store.records)
	}
	store.mu.Unlock()
	executor.mu.Lock()
	stopped := append([]string(nil), executor.stopped...)
	executor.mu.Unlock()
	if len(stopped) != 1 || stopped[0] != "account-challenge" {
		t.Fatalf("challenge must stop native account context: %+v", stopped)
	}
	deadline := time.Now().Add(time.Second)
	for recordStore.GetParticipationState("account-challenge", time.Now()).Active && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if state := recordStore.GetParticipationState("account-challenge", time.Now()); state.Active {
		t.Fatalf("challenge task must become terminal: %+v", state)
	}
}

func TestPageParticipantStoppedBeforeRequestReleasesReservation(t *testing.T) {
	recordStore, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := recordStore.RecordParticipationStarted("account-stopped", "停止账号"); err != nil {
		t.Fatal(err)
	}
	store := &fakeParticipationStore{
		credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-stopped", AccountName: "停止账号"}},
	}
	executor := &fakePageParticipationExecutor{
		ready: true,
		response: PageParticipationResponse{
			ContextMissing: true, Error: "浏览器实例已停止红包页面参与", Attempts: 0,
		},
	}
	participant := NewPageParticipant(store, executor, recordStore)
	event := Event{
		ID: "monitor:stopped-event", WebRID: "7654321", ActualRoomID: "700001",
		JoinBoxID: "7669047909329177395", Title: "钻石红包",
	}
	participant.HandleEvent(event)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		executor.mu.Lock()
		executed := len(executor.requests) == 1
		executor.mu.Unlock()
		if executed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	if records := recordStore.ParticipationRecords(); len(records) != 0 {
		t.Fatalf("stopped task left a misleading participation record: %+v", records)
	}
	store.mu.Lock()
	accountRecordCount := len(store.records)
	store.mu.Unlock()
	if accountRecordCount != 0 {
		t.Fatalf("stopped task changed account participation health: %d", accountRecordCount)
	}

	// The in-memory idempotency key is also released, so enabling the context
	// again can still participate while the packet remains current.
	executor.mu.Lock()
	executor.response = PageParticipationResponse{
		Endpoint: "join", HTTPStatus: 200,
		Body: `{"status_code":0,"data":{"succeed":true}}`, Attempts: 1,
	}
	executor.mu.Unlock()
	participant.HandleEvent(event)
	deadline = time.Now().Add(time.Second)
	var records []ParticipationRecord
	for time.Now().Before(deadline) {
		executor.mu.Lock()
		executedAgain := len(executor.requests) == 2
		executor.mu.Unlock()
		records = recordStore.ParticipationRecords()
		if executedAgain && len(records) == 1 && records[0].Status == "joined" && records[0].AttemptCount == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(records) != 1 || records[0].Status != "joined" || records[0].AttemptCount != 1 {
		t.Fatalf("re-enabled context did not participate once: %+v", records)
	}
}

func TestPageParticipantSerializesTasksPerAccount(t *testing.T) {
	store := &fakeParticipationStore{
		credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-serial"}},
		notify:      make(chan struct{}, 2),
	}
	executor := &serialPageParticipationExecutor{
		started: make(chan struct{}, 2),
		release: make(chan struct{}, 2),
	}
	participant := NewPageParticipant(store, executor)
	participant.HandleEvent(Event{ID: "event-1", WebRID: "7654321", ActualRoomID: "700001", JoinBoxID: "7669047909329177395"})
	participant.HandleEvent(Event{ID: "event-2", WebRID: "7654322", ActualRoomID: "700002", JoinBoxID: "7669047909329177396"})
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("first account task did not start")
	}
	select {
	case <-executor.started:
		t.Fatal("same account entered two page actions concurrently")
	case <-time.After(35 * time.Millisecond):
	}
	executor.release <- struct{}{}
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("second account task did not start after the first completed")
	}
	executor.release <- struct{}{}
	for range 2 {
		select {
		case <-store.notify:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for serialized participation result")
		}
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.maxActive != 1 {
		t.Fatalf("expected one active task per account, got %d", executor.maxActive)
	}
}

func TestPageParticipantAutoFinishesLimitedTaskAndNextStartIsFresh(t *testing.T) {
	recordStore, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordStore.SetParticipationSettings(ParticipationSettings{StopAfterJoins: 1}); err != nil {
		t.Fatal(err)
	}
	if err := recordStore.RecordParticipationStarted("account-task", "任务账号"); err != nil {
		t.Fatal(err)
	}
	store := &fakeParticipationStore{
		credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-task", AccountName: "任务账号"}},
		notify:      make(chan struct{}, 1),
	}
	executor := &fakePageParticipationExecutor{
		ready: true,
		response: PageParticipationResponse{
			Endpoint: "join", HTTPStatus: 200,
			Body: `{"status_code":0,"data":{"succeed":true,"hit_bonus":true,"diamond_count":1}}`, Attempts: 1,
		},
	}
	participant := NewPageParticipant(store, executor, recordStore)
	participant.HandleEvent(Event{ID: "task-event", WebRID: "7654321", ActualRoomID: "700001", JoinBoxID: "7669047909329177395", Title: "钻石红包"})
	select {
	case <-store.notify:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for limited task")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !recordStore.GetParticipationState("account-task", time.Now()).Active {
			break
		}
		time.Sleep(time.Millisecond)
	}
	state := recordStore.GetParticipationState("account-task", time.Now())
	if state.Active {
		t.Fatalf("limited task did not stop automatically: %+v", state)
	}
	executor.mu.Lock()
	stopped := append([]string(nil), executor.stopped...)
	executor.mu.Unlock()
	if len(stopped) != 1 || stopped[0] != "account-task" {
		t.Fatalf("native context was not stopped with the task: %+v", stopped)
	}
	if err := recordStore.RecordParticipationStarted("account-task", "任务账号"); err != nil {
		t.Fatal(err)
	}
	state = recordStore.GetParticipationState("account-task", time.Now())
	if !state.Active || state.JoinCount != 0 || state.WinCount != 0 {
		t.Fatalf("next explicit start inherited old task counters: %+v", state)
	}
}

func TestParticipantResponseClassification(t *testing.T) {
	tests := []struct {
		name          string
		response      *http.Response
		err           error
		status        string
		joined        bool
		cooldown      bool
		cookieExpired bool
	}{
		{name: "success", response: jsonResponse(200, `{"status_code":0,"data":{"succeed":true}}`), status: "joined", joined: true},
		{name: "join accepted without success flag", response: jsonResponse(200, `{"status_code":0,"data":{"succeed":false,"hit_bonus":false,"can_rush_gem":false}}`), status: "joined", joined: true},
		{name: "already joined", response: jsonResponse(200, `{"status_code":0,"data":{"rush_too_much":1}}`), status: "already_joined", joined: true},
		{name: "expired overrides already", response: jsonResponse(200, `{"status_code":0,"data":{"rush_too_much":1,"expired":true}}`), status: "expired"},
		{name: "risk", response: jsonResponse(200, `{"status_code":0,"data":{"rush_spam":true},"status_msg":"操作频繁"}`), status: "risk_control", cooldown: true},
		{name: "captcha", response: jsonResponse(200, `{"status_code":0,"data":{"need_verify":true},"status_msg":"请完成安全验证"}`), status: "challenge_blocked"},
		{name: "login body", response: jsonResponse(200, `{"status_code":1,"status_msg":"请登录后重试"}`), status: "login_expired", cookieExpired: true},
		{name: "login http", response: jsonResponse(403, `{}`), status: "login_expired", cookieExpired: true},
		{name: "network", err: errors.New("connection reset by peer"), status: "network_error", cooldown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint := "rush"
			if tt.name == "join accepted without success flag" {
				endpoint = "join"
			}
			result := classifyParticipationResponse(tt.response, tt.err, endpoint)
			if result.status != tt.status || result.joined != tt.joined || result.cookieExpired != tt.cookieExpired || (result.cooldown > 0) != tt.cooldown {
				t.Fatalf("unexpected classification: %+v", result)
			}
		})
	}
}

func TestParticipantChallengeDetectionDoesNotMatchGenericPageTerms(t *testing.T) {
	for _, value := range []string{
		`<script>const captcha = false; const challenge = false;</script>`,
		`{"status_code":0,"data":{"captcha":false,"challenge":false,"blocked":false,"succeed":true}}`,
		"验证码登录",
	} {
		if containsChallengeFailure(value) {
			t.Fatalf("generic page/login text was classified as a challenge: %q", value)
		}
	}
	for _, value := range []string{"请完成安全验证", "拖动滑块", `{"data":{"need_verify":true}}`} {
		if value[0] == '{' {
			result := classifyParticipationResponse(jsonResponse(200, value), nil, "join")
			if result.status != "challenge_blocked" {
				t.Fatalf("explicit verification flag was not blocked: %+v", result)
			}
			continue
		}
		if !containsChallengeFailure(value) {
			t.Fatalf("explicit challenge copy was not detected: %q", value)
		}
	}
	result := classifyParticipationResponse(jsonResponse(200, `{"status_code":0,"data":{"captcha":false,"challenge":false,"blocked":false,"succeed":true}}`), nil, "join")
	if result.status != "joined" || !result.joined {
		t.Fatalf("healthy response with generic challenge fields was misclassified: %+v", result)
	}
}

func TestReceiveResponseClassification(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		status   string
		message  string
		award    string
		alias    string
		fallback bool
	}{
		{name: "empty result stays pending", body: `{"status_code":0,"data":{"receive_info":[]}}`},
		{name: "explicit loss", body: `{"status_code":0,"data":{"receive_info":[{"box_id_str":"box-1","succeed":false}]}}`, status: "not_won", message: "未中奖"},
		{name: "diamond win", body: `{"status_code":0,"data":{"receive_info":[{"box_id_str":"box-1","succeed":true,"diamond_count":8}]}}`, status: "won", message: "已中8钻", award: "8钻"},
		{name: "gift win", body: `{"status_code":0,"data":{"receive_info":[{"box_id_str":"box-1","succeed":true,"gift_name":"小心心","gift_count":2}]}}`, status: "won", message: "已中2个小心心", award: "2个小心心"},
		{name: "mismatched result stays pending", body: `{"status_code":0,"data":{"receive_info":[{"box_id_str":"another-box","succeed":false}]}}`},
		{name: "single mismatched definitive result can use event fallback", body: `{"status_code":0,"data":{"receive_info":[{"box_id_str":"receive-box","succeed":true,"diamond_count":1}]}}`, status: "won", message: "已中1钻", award: "1钻", fallback: true},
		{name: "packet alias resolves numeric box response", body: `{"status_code":0,"data":{"receive_info":[{"succeed":true,"box_id":7654448853667941163,"box_id_str":"7654448853667941163","diamond_count":1,"gift_name":""}]}}`, status: "won", message: "已中1钻", award: "1钻", alias: "7654448853667941163"},
		{name: "missing personal result stays pending", body: `{"status_code":0,"data":{}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			aliases := []string{"box-1"}
			if tt.alias != "" {
				aliases = append(aliases, tt.alias)
			}
			var got drawOutcome
			if tt.fallback {
				got = classifyReceiveResponseWithSingleFallback(tt.body, aliases...)
			} else {
				got = classifyReceiveResponse(tt.body, aliases...)
			}
			if got.status != tt.status || got.message != tt.message || got.award != tt.award {
				t.Fatalf("unexpected receive classification: %+v", got)
			}
		})
	}
}

func TestDrawResultTimeoutMarksErrorAndReleasesNextRound(t *testing.T) {
	recordStore, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordStore.SetParticipationSettings(ParticipationSettings{DrawResultTimeoutSeconds: 1}); err != nil {
		t.Fatal(err)
	}
	if err := recordStore.RecordParticipationStarted("account-timeout", "超时账号"); err != nil {
		t.Fatal(err)
	}
	event := Event{
		ID: "event-timeout", WebRID: "123456", ActualRoomID: "700001", JoinBoxID: "7669063194534955828",
		ExpiresAt: time.Now().Add(-100 * time.Millisecond).Format(time.RFC3339Nano),
	}
	if reserved, err := recordStore.ReserveParticipation(event, "account-timeout", "超时账号"); err != nil || !reserved {
		t.Fatalf("reserve timeout draw: %v %v", reserved, err)
	}
	if err := recordStore.CompleteParticipation(event.ID, "account-timeout", "join", "joined", "等待开奖", 1, true, false, 0); err != nil {
		t.Fatal(err)
	}
	store := &fakeParticipationStore{credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-timeout"}}}
	executor := &fakePageParticipationExecutor{
		ready:    true,
		response: PageParticipationResponse{Endpoint: "receive", HTTPStatus: 200, Body: `{"status_code":0,"data":{}}`, Attempts: 1},
	}
	participant := NewPageParticipant(store, executor, recordStore)
	participant.resolveDraw(event, "account-timeout", "超时账号")
	records := recordStore.ParticipationRecords()
	if len(records) != 1 || records[0].Status != "draw_error" || !strings.Contains(records[0].Message, "1 秒") {
		t.Fatalf("draw timeout was not persisted as abnormal: %+v", records)
	}
	state := recordStore.GetParticipationState("account-timeout", time.Now())
	if state.WaitingDraw {
		t.Fatalf("draw timeout still blocks the next round: %+v", state)
	}
	if allowed, _ := recordStore.ParticipationPolicy("account-timeout", time.Now()); !allowed {
		t.Fatal("draw timeout must release the account for the next round")
	}
}

func TestParticipationParamsKeepRushFieldsNative(t *testing.T) {
	event := Event{PacketID: "box", JoinBoxID: "box", ActualRoomID: "room", AnchorID: "anchor", BoxType: "1", SendTime: "100", DelayTime: "30"}
	join := participationParams(event, false)
	rush := participationParams(event, true)
	if join["room_id"] != "room" || join["box_id"] != "box" || join["anchor_id"] != "anchor" {
		t.Fatalf("join params missing required values: %+v", join)
	}
	if _, exists := join["box_type"]; exists {
		t.Fatalf("join params unexpectedly contain rush-only fields: %+v", join)
	}
	if rush["box_type"] != "1" || rush["send_time"] != "100" || rush["delay_time"] != "30" {
		t.Fatalf("rush params missing required values: %+v", rush)
	}
}
