package redpacket

import (
	"net/http"
	"testing"
	"time"

	"fubao.ccvar.com/engine/internal/accounts"
)

func TestExtractRedPacketConditionReadsVisibleThresholds(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		payload   map[string]any
		condition string
		reason    string
	}{
		{
			name: "popularity ticket description",
			payload: map[string]any{
				"title": "灯牌免费上车直播中",
				"meta":  map[string]any{"before_unpack_desc": "送人气票 可抢红包 需1钻"},
			},
			condition: "需1钻",
			reason:    "红包需要钻石条件",
		},
		{
			name: "numeric diamond cost key",
			payload: map[string]any{
				"title":              "钻石红包",
				"need_diamond_count": float64(2),
			},
			condition: "需2钻",
			reason:    "红包需要钻石条件",
		},
		{
			name: "popularity ticket gift object",
			payload: map[string]any{
				"title":                  "钻石红包",
				"popularity_ticket_gift": map[string]any{"gift_name": "人气票", "diamond_count": float64(1)},
			},
			condition: "需1钻",
			reason:    "红包需要钻石条件",
		},
		{
			name: "fan club badge text",
			payload: map[string]any{
				"title": "钻石红包",
				"meta":  map[string]any{"before_unpack_desc": "需灯牌后可抢红包"},
			},
			condition: "需灯牌",
			reason:    "红包需要灯牌条件",
		},
		{
			name: "diamond and badge",
			payload: map[string]any{
				"title":       "钻石红包",
				"tips":        "需1钻",
				"condition":   "加入粉丝团",
				"description": "需1钻",
			},
			condition: "需1钻、需灯牌",
			reason:    "红包需要钻石/灯牌条件",
		},
		{
			name: "threshold free row keeps no condition",
			payload: map[string]any{
				"title": "灯牌免费上车直播中", "box_id_str": "7631803110771657522",
				"diamond_count": float64(20), "box_count": float64(15),
				"biz_extra": `{"tags":{"1009":"15"}}`,
			},
			condition: "",
			reason:    "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			condition := extractRedPacketCondition(testCase.payload)
			if condition != testCase.condition {
				t.Fatalf("condition = %q, want %q", condition, testCase.condition)
			}
			if reason := redPacketConditionSkipReason(condition); reason != testCase.reason {
				t.Fatalf("skip reason = %q, want %q", reason, testCase.reason)
			}
		})
	}
}

func TestExtractRedPacketCarriesCondition(t *testing.T) {
	packet, ok := extractRedPacket(map[string]any{
		"activity_kind": "red_packet", "activity_type": "红包",
		"title": "钻石红包", "box_id_str": "7631803110771657522",
		"diamond_count": float64(99), "box_count": float64(24),
		"items": []any{
			map[string]any{"meta": map[string]any{"before_unpack_desc": "送人气票 可抢红包 需1钻"}},
		},
	})
	if !ok {
		t.Fatal("red packet row was not recognized")
	}
	if packet.condition != "需1钻" {
		t.Fatalf("packet condition = %q, want 需1钻", packet.condition)
	}
}

func TestClassifyParticipationResponseLearnsThresholdFromMessage(t *testing.T) {
	gated := classifyParticipationResponse(
		jsonResponse(200, `{"status_code":0,"data":{"succeed":false,"hit_bonus":false,"can_rush_gem":false,"message":"请先加入粉丝团"}}`),
		nil, "join",
	)
	if gated.status != "failed" || gated.cooldown != 0 {
		t.Fatalf("threshold rejection must be a plain failure without cooldown: %+v", gated)
	}
	if gated.condition != "需灯牌" {
		t.Fatalf("condition = %q, want 需灯牌", gated.condition)
	}
	if gated.message != "红包需要灯牌条件" {
		t.Fatalf("message = %q, want 红包需要灯牌条件", gated.message)
	}

	bare := classifyParticipationResponse(
		jsonResponse(200, `{"status_code":0,"data":{"succeed":false,"hit_bonus":false,"can_rush_gem":false}}`),
		nil, "join",
	)
	if bare.status != "failed" || bare.condition != "" || bare.message != "参与失败" {
		t.Fatalf("bare soft-deny must stay 参与失败 without a learned threshold: %+v", bare)
	}
}

func TestStoreRecordEventConditionMakesPacketSkipped(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEventCondition("missing-event", "需1钻"); err != nil {
		t.Fatalf("unknown event must be ignored quietly: %v", err)
	}
	store.mu.Lock()
	store.events["event-learned"] = &Event{ID: "event-learned", MonitorID: "monitor-1", PacketID: "10001"}
	store.mu.Unlock()
	if err := store.RecordEventCondition("event-learned", "需灯牌"); err != nil {
		t.Fatal(err)
	}
	events := store.Events("monitor-1")
	if len(events) != 1 || events[0].Condition != "需灯牌" {
		t.Fatalf("learned threshold was not persisted on the event: %+v", events)
	}
	if redPacketConditionSkipReason(events[0].Condition) == "" {
		t.Fatal("learned threshold must make the packet skipped")
	}
}

func TestParticipantSkipsPacketWithParticipationThreshold(t *testing.T) {
	recordStore, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := recordStore.RecordParticipationStarted("account-threshold", "门槛账号"); err != nil {
		t.Fatal(err)
	}
	store := &fakeParticipationStore{
		credentials: []accounts.RedPacketParticipationCredential{{AccountID: "account-threshold", Cookie: "sessionid_ss=ok"}},
		notify:      make(chan struct{}, 1),
	}
	poster := &fakePoster{responses: []*http.Response{jsonResponse(200, `{"status_code":0,"data":{"succeed":true}}`)}}
	participant := newParticipant(store, 1, func(Event, accounts.RedPacketParticipationCredential) signedPoster { return poster }, recordStore)

	participant.HandleEvent(Event{
		ID: "event-threshold", JoinBoxID: "10001", ActualRoomID: "7001",
		Title: "钻石红包", Prize: "总99钻，24份红包", Condition: "需1钻",
	})
	time.Sleep(20 * time.Millisecond)
	poster.mu.Lock()
	blocked := poster.calls
	poster.mu.Unlock()
	if blocked != 0 {
		t.Fatalf("packet behind a diamond threshold must not be joined, calls=%d", blocked)
	}
	if records := recordStore.ParticipationRecords(); len(records) != 0 {
		t.Fatalf("blocked packet must not create a request record: %+v", records)
	}

	participant.HandleEvent(Event{
		ID: "event-open", JoinBoxID: "10002", ActualRoomID: "7001",
		Title: "钻石红包", Prize: "总99钻，24份红包",
	})
	select {
	case <-store.notify:
	case <-time.After(time.Second):
		t.Fatal("threshold-free packet must remain eligible")
	}
	poster.mu.Lock()
	defer poster.mu.Unlock()
	if poster.calls != 1 {
		t.Fatalf("threshold-free packet should be joined once, calls=%d", poster.calls)
	}
}
