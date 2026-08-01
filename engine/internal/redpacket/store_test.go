package redpacket

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"fubao.ccvar.com/engine/internal/live/poller"
	"fubao.ccvar.com/engine/internal/rooms"
)

type fakeMonitorSource struct {
	probe      LiveProbe
	probeErr   error
	snapshots  []poller.Snapshot
	fetchErr   error
	probeCalls int
	fetchCalls int
}

func (f *fakeMonitorSource) ProbeLive(context.Context) (LiveProbe, error) {
	f.probeCalls++
	return f.probe, f.probeErr
}

func (f *fakeMonitorSource) Fetch(context.Context) ([]poller.Snapshot, error) {
	f.fetchCalls++
	return f.snapshots, f.fetchErr
}

func TestSyncRoomsRemovesStaleMonitorsAndEvents(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SyncRooms([]rooms.Room{{ID: "valid", WebRID: "123456", Name: "有效直播间", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.events["stale-event"] = &Event{ID: "stale-event", MonitorID: "room_valid", RoomID: "valid"}
	store.mu.Unlock()
	if err := store.SyncRooms(nil); err != nil {
		t.Fatal(err)
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("expected stale monitor cleanup, got %+v", got)
	}
	if got := store.Events(""); len(got) != 0 {
		t.Fatalf("expected stale event cleanup, got %+v", got)
	}
}

func TestParticipationRecordPersistsAndDeduplicates(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{
		ID: "monitor:event-record", RoomID: "room-1", WebRID: "123456",
		RoomName: "记录直播间", StreamerName: "主播甲",
		PacketID: "box-1", JoinBoxID: "box-1", Title: "直播红包", Prize: "总8钻，2份红包",
	}
	reserved, err := store.ReserveParticipation(event, "account-1", "参与账号甲")
	if err != nil || !reserved {
		t.Fatalf("expected first reservation to succeed: reserved=%v err=%v", reserved, err)
	}
	if duplicate, err := store.ReserveParticipation(event, "account-1", "参与账号甲"); err != nil || duplicate {
		t.Fatalf("same account/event must be deduplicated: reserved=%v err=%v", duplicate, err)
	}
	if err := store.CompleteParticipation(event.ID, "account-1", "rush", "already_joined", "红包已受理", 2, true, time.Minute); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	records := reloaded.ParticipationRecords()
	if len(records) != 1 || records[0].Status != "already_joined" || records[0].Endpoint != "rush" || records[0].AttemptCount != 2 || !records[0].Joined || records[0].CooldownUntil == "" {
		t.Fatalf("unexpected persisted participation record: %+v", records)
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"sessionid", "a_bogus", "Cookie"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("safe participation records leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestExtractRedPacketFiltersFubao(t *testing.T) {
	if _, ok := extractRedPacket(map[string]any{
		"lottery_info": map[string]any{"activity_type": "福袋", "lottery_id": "bag-1", "title": "晚间福袋"},
	}); ok {
		t.Fatal("福袋 payload must not be treated as a red packet")
	}
	packet, ok := extractRedPacket(map[string]any{
		"lottery_info": map[string]any{
			"activity_kind":     "red_packet",
			"red_packet_id":     "packet-1",
			"title":             "直播间红包",
			"participant_count": 12,
		},
	})
	if !ok {
		t.Fatal("red packet payload should be detected")
	}
	if packet.id != "packet-1" || packet.title != "直播间红包" || packet.participants != 12 {
		t.Fatalf("unexpected packet metadata: %#v", packet)
	}
}

func TestExtractRedPacketIncludesPrizeAndExpiry(t *testing.T) {
	packet, ok := extractRedPacket(map[string]any{
		"activity_kind":       "red_packet",
		"red_packet_id":       "packet-prize",
		"title":               "限时红包",
		"total_diamond_count": 120,
		"packet_count":        20,
		"expire_time":         1767225600,
	})
	if !ok {
		t.Fatal("red packet payload should be detected")
	}
	if packet.prize != "总120钻，20份红包" {
		t.Fatalf("unexpected prize: %q", packet.prize)
	}
	if packet.expiresAt == "" {
		t.Fatalf("expiry should be normalized: %+v", packet)
	}
	if _, err := time.Parse(time.RFC3339Nano, packet.expiresAt); err != nil {
		t.Fatalf("expiry must be RFC3339, got %q: %v", packet.expiresAt, err)
	}
}

func TestExtractRedPacketDerivesLegacyLuckyboxExpiry(t *testing.T) {
	packet, ok := extractRedPacket(map[string]any{
		"activity_kind":       "red_packet",
		"box_id":              "packet-legacy-time",
		"total_diamond_count": 50,
		"box_num":             10,
		"send_time":           1767225600,
		"delay_time":          30,
	})
	if !ok {
		t.Fatal("legacy luckybox payload should be detected")
	}
	if packet.prize != "总50钻，10份红包" {
		t.Fatalf("unexpected legacy prize: %q", packet.prize)
	}
	want := time.Unix(1767225630, 0).Format(time.RFC3339Nano)
	if packet.expiresAt != want {
		t.Fatalf("expected send_time + delay_time expiry %q, got %q", want, packet.expiresAt)
	}
}

func TestLuckyboxItemsFlattensList(t *testing.T) {
	items := luckyboxItems(map[string]any{
		"room_id": "room-1",
		"box_list": []any{
			map[string]any{"box_id": "one", "title": "红包一"},
			map[string]any{"box_id": "two", "title": "红包二"},
		},
	})
	if len(items) != 2 || items[0]["room_id"] != "room-1" || items[1]["box_id"] != "two" {
		t.Fatalf("unexpected luckybox items: %#v", items)
	}
}

func TestAggregateLuckyboxItemsMatchesFubaoPrizeRule(t *testing.T) {
	items := aggregateLuckyboxItems(map[string]any{
		"box_list": []any{
			map[string]any{"activity_id": "activity-1", "box_id": "box-1", "title": "礼物红包", "diamond_count": 0},
			map[string]any{"activity_id": "activity-1", "box_id": "box-2", "title": "礼物红包", "diamond_count": 40},
			map[string]any{"activity_id": "activity-1", "box_id": "box-3", "title": "礼物红包", "diamond_count": 59},
		},
	})
	if len(items) != 1 {
		t.Fatalf("expected one grouped activity, got %#v", items)
	}
	packet, ok := extractRedPacket(items[0])
	if !ok {
		t.Fatal("grouped luckybox activity should be detected")
	}
	if packet.prize != "总99钻，3份红包" {
		t.Fatalf("unexpected grouped prize: %q (%#v)", packet.prize, items[0])
	}
}

func TestAggregateLuckyboxItemsReadsShareCountFromBizExtraTags(t *testing.T) {
	items := aggregateLuckyboxItems([]any{
		map[string]any{
			"box_id":        "7631803110771657522",
			"title":         "钻石红包",
			"diamond_count": 25,
			"business_type": 1009,
			"biz_extra":     `{"tags":{"1009":"15","quiz_group_id":"0","quiz_group_type":""}}`,
		},
	})
	if len(items) != 1 {
		t.Fatalf("expected one luckybox activity, got %#v", items)
	}
	packet, ok := extractRedPacket(items[0])
	if !ok {
		t.Fatal("luckybox payload should be detected")
	}
	if packet.prize != "总25钻，15份红包" {
		t.Fatalf("biz_extra share count must match 福包: %q (%#v)", packet.prize, items[0])
	}
}

func TestFormatPacketPrizeIgnoresZeroPlaceholder(t *testing.T) {
	packet, ok := extractRedPacket(map[string]any{
		"activity_kind":       "red_packet",
		"title":               "钻石红包",
		"total_diamond_count": 0,
		"total_amount":        99,
		"packet_count":        24,
	})
	if !ok {
		t.Fatal("red packet payload should be detected")
	}
	if packet.prize != "总99钻，24份红包" {
		t.Fatalf("zero placeholder must not hide the real prize: %q", packet.prize)
	}
}

func TestPollOnceSkipsRedPacketRequestUntilRoomIsLive(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SyncRooms([]rooms.Room{{ID: "room-1", WebRID: "123456", Name: "测试直播间", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.monitors["room_room-1"].Status = "running"
	store.mu.Unlock()
	source := &fakeMonitorSource{probe: LiveProbe{Status: "offline", RawStatus: "4", Source: "room_web_enter"}}
	if got := store.pollOnce(context.Background(), "room_room-1", source); got != offlineProbeInterval {
		t.Fatalf("offline room should use %s cadence, got %s", offlineProbeInterval, got)
	}
	if source.probeCalls != 1 || source.fetchCalls != 0 {
		t.Fatalf("offline room must only run live probe, probe=%d fetch=%d", source.probeCalls, source.fetchCalls)
	}
	monitor, _ := store.Get("room_room-1")
	if monitor.LiveStatus != "offline" || monitor.LiveRawStatus != "4" || monitor.LastRedPacketCheckedAt != "" {
		t.Fatalf("unexpected offline state: %+v", monitor)
	}
}

func TestPollOnceFetchesRedPacketsOnlyAfterPositiveLiveProbe(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SyncRooms([]rooms.Room{{ID: "room-2", WebRID: "654321", Name: "待更新", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.monitors["room_room-2"].Status = "running"
	store.monitors["room_room-2"].AccountID = "monitor-account-1"
	store.monitors["room_room-2"].AccountName = "监测账号甲"
	store.mu.Unlock()
	handled := make(chan Event, 1)
	store.SetEventHandler(func(event Event) { handled <- event })
	source := &fakeMonitorSource{
		probe: LiveProbe{Status: "live", RawStatus: "2", ActualRoomID: "7000000000000000002", Title: "正在直播", StreamerName: "主播甲", Source: "room_web_enter"},
		snapshots: []poller.Snapshot{{Source: "luckybox_api", Data: map[string]any{
			"activity_kind": "red_packet", "red_packet_id": "packet-2", "title": "直播红包",
			"prize_name": "8钻红包", "expire_time": 1767225600,
			"anchor_id": "anchor-2", "box_type": 1, "send_time": 1767225500, "delay_time": 100,
		}}},
	}
	if got := store.pollOnce(context.Background(), "room_room-2", source); got != activePacketInterval {
		t.Fatalf("active packet should use %s cadence, got %s", activePacketInterval, got)
	}
	if source.probeCalls != 1 || source.fetchCalls != 1 {
		t.Fatalf("live room must probe then fetch, probe=%d fetch=%d", source.probeCalls, source.fetchCalls)
	}
	monitor, _ := store.Get("room_room-2")
	if monitor.LiveStatus != "live" || monitor.LiveStartedAt == "" || monitor.ActualRoomID != "7000000000000000002" || monitor.StreamerName != "主播甲" || monitor.PacketCount != 1 || monitor.LastRedPacketCheckedAt == "" {
		t.Fatalf("unexpected live state: %+v", monitor)
	}
	events := store.Events("room_room-2")
	if len(events) != 1 || events[0].AccountID != "monitor-account-1" || events[0].AccountName != "监测账号甲" || events[0].Prize != "8钻红包" || events[0].ExpiresAt == "" {
		t.Fatalf("event must retain monitoring account, prize and expiry: %+v", events)
	}
	select {
	case event := <-handled:
		if event.ActualRoomID != "7000000000000000002" || event.JoinBoxID != "packet-2" || event.AnchorID != "anchor-2" || event.BoxType != "1" || event.SendTime == "" || event.DelayTime != "100" {
			t.Fatalf("event handler did not receive private join metadata: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("new red-packet handler was not called after polling")
	}
}

func TestPollOnceDoesNotClaimConnectedWhenLiveProbeFails(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SyncRooms([]rooms.Room{{ID: "room-3", WebRID: "987654", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.monitors["room_room-3"].Status = "running"
	store.mu.Unlock()
	source := &fakeMonitorSource{probeErr: errors.New("probe failed")}
	if got := store.pollOnce(context.Background(), "room_room-3", source); got != unknownProbeInterval {
		t.Fatalf("failed probe should retry after %s, got %s", unknownProbeInterval, got)
	}
	monitor, _ := store.Get("room_room-3")
	if monitor.Status != "running" || monitor.LiveStatus != "error" || monitor.ConnectionStatus != "error" || monitor.LastError == "" {
		t.Fatalf("unexpected error state: %+v", monitor)
	}
}

func TestMonitorStaggerDelayIsStableAndBounded(t *testing.T) {
	first := monitorStaggerDelay("room-1")
	if first != monitorStaggerDelay("room-1") {
		t.Fatal("stagger delay must be deterministic")
	}
	if first < 0 || first >= 10*time.Second {
		t.Fatalf("stagger delay out of range: %s", first)
	}
}
