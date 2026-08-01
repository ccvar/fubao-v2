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

func (s *fakeParticipationStore) RedPacketParticipationCredentials(time.Time) []accounts.RedPacketParticipationCredential {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]accounts.RedPacketParticipationCredential(nil), s.credentials...)
}

func (s *fakeParticipationStore) RecordRedPacketParticipation(accountID, status, _ string, joined bool, cooldown time.Duration, cookieExpired bool) {
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
		jsonResponse(200, `{"status_code":0,"data":{"succeed":false}}`),
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
	store := &fakeParticipationStore{
		credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-1", Cookie: "sessionid_ss=ok"}},
		notify:      make(chan struct{}, 2),
	}
	poster := &fakePoster{responses: []*http.Response{jsonResponse(200, `{"status_code":0,"data":{"succeed":true}}`)}}
	participant := newParticipant(store, 1, func(Event, accounts.RedPacketParticipationCredential) signedPoster { return poster })
	event := Event{ID: "monitor:event-1", PacketID: "box-1", JoinBoxID: "box-1", ActualRoomID: "7001"}
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
	defer store.mu.Unlock()
	if len(store.records) != 1 || store.records[0].status != "joined" || !store.records[0].joined {
		t.Fatalf("unexpected result records: %+v", store.records)
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
		{name: "already joined", response: jsonResponse(200, `{"status_code":0,"data":{"rush_too_much":1}}`), status: "already_joined", joined: true},
		{name: "expired overrides already", response: jsonResponse(200, `{"status_code":0,"data":{"rush_too_much":1,"expired":true}}`), status: "expired"},
		{name: "risk", response: jsonResponse(200, `{"status_code":0,"data":{"rush_spam":true},"status_msg":"操作频繁"}`), status: "risk_control", cooldown: true},
		{name: "login body", response: jsonResponse(200, `{"status_code":1,"status_msg":"请登录后重试"}`), status: "login_expired", cookieExpired: true},
		{name: "login http", response: jsonResponse(403, `{}`), status: "login_expired", cookieExpired: true},
		{name: "network", err: errors.New("connection reset by peer"), status: "network_error", cooldown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyParticipationResponse(tt.response, tt.err)
			if result.status != tt.status || result.joined != tt.joined || result.cookieExpired != tt.cookieExpired || (result.cooldown > 0) != tt.cooldown {
				t.Fatalf("unexpected classification: %+v", result)
			}
		})
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
