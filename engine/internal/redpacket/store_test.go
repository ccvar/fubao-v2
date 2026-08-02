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
	if err := store.RecordParticipationStarted("account-1", "参与账号甲"); err != nil {
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
	if err := store.CompleteParticipation(event.ID, "account-1", "rush", "already_joined", "红包已受理", 2, true, false, time.Minute); err != nil {
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

func TestParticipationSettingsPolicyAndActivityPersist(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.SetParticipationSettings(ParticipationSettings{
		StopAfterJoins: 2, CooldownSeconds: 30, StopAfterWins: 1, PacketType: ParticipationPacketTypeGift,
	})
	if err != nil || settings.CooldownSeconds != 30 {
		t.Fatalf("unexpected saved settings: %+v err=%v", settings, err)
	}
	if err := store.RecordParticipationStarted("account-1", "账号甲"); err != nil {
		t.Fatal(err)
	}
	event := Event{ID: "event-1", RoomID: "room", WebRID: "123456", JoinBoxID: "123"}
	if ok, err := store.ReserveParticipation(event, "account-1", "账号甲"); err != nil || !ok {
		t.Fatalf("reserve: ok=%v err=%v", ok, err)
	}
	if err := store.CompleteParticipation(event.ID, "account-1", "join", "joined", "已受理", 1, true, false, 0); err != nil {
		t.Fatal(err)
	}
	if allowed, _ := store.ParticipationPolicy("account-1", time.Now()); allowed {
		t.Fatal("an unresolved accepted packet must block the next task")
	}
	store.mu.Lock()
	store.participations[participationRecordID("account-1", event.ID)].JoinedAt = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	store.participations[participationRecordID("account-1", event.ID)].UpdatedAt = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	store.mu.Unlock()
	if state := store.GetParticipationState("account-1", time.Now()); !state.WaitingDraw || state.WaitingReason == "" {
		t.Fatalf("pending personal result must be exposed as a safe temporary block: %+v", state)
	}
	if _, err := store.ResolveParticipationDraw(event.ID, "account-1", "not_won", "未中奖", "", 1); err != nil {
		t.Fatal(err)
	}
	if allowed, cooldown := store.ParticipationPolicy("account-1", time.Now()); !allowed || cooldown != 30*time.Second {
		t.Fatalf("expected account after cooldown to remain eligible: allowed=%v cooldown=%v", allowed, cooldown)
	}
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.GetParticipationSettings(); got != settings {
		t.Fatalf("settings did not persist: got=%+v want=%+v", got, settings)
	}
	activities := reloaded.Activities()
	if len(activities) != 1 || activities[0].Kind != "participation_started" || !strings.Contains(activities[0].Label, "账号甲") {
		t.Fatalf("unexpected persisted activity: %+v", activities)
	}
}

func TestParticipationSettingsDefaultDrawTimeoutAndSafeTrace(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := store.GetParticipationSettings().DrawResultTimeoutSeconds; got != 10 {
		t.Fatalf("default draw-result timeout=%d, want 10", got)
	}
	if got := store.GetParticipationSettings().MinimumDiamonds; got != 1 {
		t.Fatalf("default minimum diamonds=%d, want 1", got)
	}
	if got := store.GetParticipationSettings().PacketType; got != ParticipationPacketTypeDiamond {
		t.Fatalf("default packet type=%q, want %q", got, ParticipationPacketTypeDiamond)
	}
	if err := store.RecordParticipationStarted("account-log", "日志账号"); err != nil {
		t.Fatal(err)
	}
	task := PageParticipationTask{
		Action: "join", EventID: "event-log", AccountID: "account-log", AccountName: "日志账号",
		WebRID: "123456", ActualRoomID: "700001", BoxID: "7669063194534955828", AnchorID: "anchor",
	}
	response := PageParticipationResponse{
		Endpoint: "join", HTTPStatus: 200,
		Body: `{"status_code":0,"data":{"succeed":true,"diamond_count":8,"msToken":"secret-token","nested":{"a_bogus":"secret-sign","cookie":"sessionid=secret"}}}`,
	}
	if err := store.RecordParticipationTrace(task, response); err != nil {
		t.Fatal(err)
	}
	traces := store.ParticipationTraces()
	if len(traces) != 1 || traces[0].RequestParams["room_id"] != "700001" || traces[0].RequestParams["box_id"] == "" {
		t.Fatalf("safe request trace missing business params: %+v", traces)
	}
	encoded, err := json.Marshal(traces)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-token", "secret-sign", "sessionid=secret", "a_bogus", "msToken"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("participation trace leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestParticipationSchedulesPersistClaimAndAdvance(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 2, 9, 30, 0, 0, time.Local)
	once, err := store.CreateParticipationSchedule(ParticipationSchedule{
		Mode: ParticipationScheduleOnce, RunAt: now.Add(time.Hour).Format(time.RFC3339Nano),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	interval, err := store.CreateParticipationSchedule(ParticipationSchedule{
		Mode: ParticipationScheduleInterval, IntervalSeconds: 5 * 60,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if schedules := reloaded.ParticipationSchedules(); len(schedules) != 2 {
		t.Fatalf("participation schedules did not persist: %+v", schedules)
	}
	due, err := reloaded.ClaimDueParticipationSchedules(now)
	if err != nil || len(due) != 1 || due[0].ScheduleID != interval.ID {
		t.Fatalf("interval schedule should execute immediately once: due=%+v err=%v", due, err)
	}
	if repeated, err := reloaded.ClaimDueParticipationSchedules(now.Add(time.Second)); err != nil || len(repeated) != 0 {
		t.Fatalf("same occurrence must be claimed once: due=%+v err=%v", repeated, err)
	}
	due, err = reloaded.ClaimDueParticipationSchedules(now.Add(time.Hour))
	if err != nil || len(due) != 2 {
		t.Fatalf("once and next interval occurrence should be due: %+v err=%v", due, err)
	}
	remaining := reloaded.ParticipationSchedules()
	if len(remaining) != 1 || remaining[0].ID != interval.ID || !remaining[0].Enabled {
		t.Fatalf("one-shot schedule must be removed after claim: once=%s remaining=%+v", once.ID, remaining)
	}
}

func TestParticipationDailyScheduleAndBatchActivity(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 2, 20, 1, 0, 0, time.Local)
	schedule, err := store.CreateParticipationSchedule(ParticipationSchedule{Mode: ParticipationScheduleDaily, DailyTime: "20:00"}, now)
	if err != nil {
		t.Fatal(err)
	}
	next, err := time.Parse(time.RFC3339Nano, schedule.NextRunAt)
	if err != nil || next.Day() != 3 || next.Hour() != 20 || next.Minute() != 0 {
		t.Fatalf("daily schedule must roll to tomorrow: %+v parsed=%v err=%v", schedule, next, err)
	}
	if err := store.RecordParticipationBatchResult(schedule.ID, schedule.Mode, 3, 1, nil); err != nil {
		t.Fatal(err)
	}
	activities := store.Activities()
	foundBatch := false
	for _, activity := range activities {
		foundBatch = foundBatch || strings.Contains(activity.Label, "已执行“每天固定时间”：3 个实例参与，1 个实例跳过")
	}
	if len(activities) < 2 || !foundBatch {
		t.Fatalf("batch activity was not recorded safely: %+v", activities)
	}
}

func TestParticipationBatchActivityConsolidatesAccountStarts(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, name string }{{"account-1", "账号甲"}, {"account-2", "账号乙"}} {
		if err := store.RecordParticipationStarted(item.id, item.name); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordParticipationBatchResult("", "immediate", 2, 1, []string{"account-1", "account-2"}); err != nil {
		t.Fatal(err)
	}
	activities := store.Activities()
	if len(activities) != 1 || activities[0].Kind != "participation_batch_executed" || activities[0].Label != "已启动“立即执行”：2 个实例参与，1 个实例跳过" {
		t.Fatalf("account start activities were not consolidated: %+v", activities)
	}
	for _, accountID := range []string{"account-1", "account-2"} {
		if task := store.participationTasks[accountID]; task == nil || !task.Active {
			t.Fatalf("consolidating sidebar activity must preserve task %q: %+v", accountID, task)
		}
	}
	stopped, err := store.StopParticipationBatch(activities[0].ID)
	if err != nil || len(stopped) != 2 {
		t.Fatalf("batch stop failed: accounts=%+v err=%v", stopped, err)
	}
	for _, accountID := range stopped {
		if task := store.participationTasks[accountID]; task == nil || task.Active || task.EndReason != "批次手动停止" {
			t.Fatalf("batch stop did not close task %q: %+v", accountID, task)
		}
	}
	stoppedActivities := store.Activities()
	if len(stoppedActivities) != 1 || stoppedActivities[0].Active || stoppedActivities[0].StoppedAt == "" {
		t.Fatalf("batch activity did not persist stopped state: %+v", stoppedActivities)
	}
}

func TestLegacyBatchActivityMigrationRestoresCurrentAccounts(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordParticipationStarted("legacy-account", "旧批次账号"); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	batch := store.addActivityLocked("participation_batch_executed", "", "立即执行红包参与：成功启动 1 个实例", time.Now())
	migrated := store.migrateLegacyBatchActivitiesLocked()
	store.mu.Unlock()
	if !migrated || len(batch.AccountIDs) != 1 || batch.AccountIDs[0] != "legacy-account" || !batch.Active {
		t.Fatalf("legacy batch account membership was not restored: %+v", batch)
	}
	if activities := store.Activities(); len(activities) != 1 || activities[0].ID != batch.ID {
		t.Fatalf("legacy per-account activity was not consolidated: %+v", activities)
	}
}

func TestActivitiesReturnsFullPersistedSidebarHistory(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	store.mu.Lock()
	for index := 0; index < 105; index++ {
		store.addActivityLocked(
			"participation_schedule_created",
			"",
			"历史活动",
			base.Add(time.Duration(index)*time.Minute),
		)
	}
	err = store.saveLocked()
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if activities := store.Activities(); len(activities) != 100 {
		t.Fatalf("sidebar history length=%d, want persisted limit 100", len(activities))
	}
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if activities := reloaded.Activities(); len(activities) != 100 {
		t.Fatalf("reloaded sidebar history length=%d, want 100", len(activities))
	}
}

func TestReloadMigratesOverduePendingDrawToError(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordParticipationStarted("account-overdue", "过期账号"); err != nil {
		t.Fatal(err)
	}
	event := Event{
		ID: "event-overdue", WebRID: "123456", ActualRoomID: "700001", JoinBoxID: "7669063194534955828",
		ExpiresAt: time.Now().Add(-20 * time.Second).Format(time.RFC3339Nano),
	}
	if reserved, err := store.ReserveParticipation(event, "account-overdue", "过期账号"); err != nil || !reserved {
		t.Fatalf("reserve overdue event: %v %v", reserved, err)
	}
	if err := store.CompleteParticipation(event.ID, "account-overdue", "join", "joined", "等待开奖", 1, true, false, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishParticipationTask("account-overdue", "客户端关闭"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	records := reloaded.ParticipationRecords()
	if len(records) != 1 || records[0].Status != "draw_error" || !strings.Contains(records[0].Message, "10 秒") {
		t.Fatalf("overdue pending draw was not migrated: %+v", records)
	}
}

func TestParticipationPolicyStopsAtJoinAndWinLimits(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.SetParticipationSettings(ParticipationSettings{StopAfterJoins: 1})
	_ = store.RecordParticipationStarted("account", "账号")
	taskID := store.participationTasks["account"].ID
	store.participations["joined"] = &ParticipationRecord{ID: "joined", AccountID: "account", TaskID: taskID, Joined: true, UpdatedAt: time.Now().Add(-time.Hour).Format(time.RFC3339Nano)}
	if allowed, _ := store.ParticipationPolicy("account", time.Now()); allowed {
		t.Fatal("join limit must stop future tasks")
	}
	store.settings = ParticipationSettings{StopAfterWins: 1}
	store.participations["joined"].Won = true
	if allowed, _ := store.ParticipationPolicy("account", time.Now()); allowed {
		t.Fatal("win limit must stop future tasks")
	}
	if err := store.FinishParticipationTask("account", "本次任务达到上限"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordParticipationStarted("account", "账号"); err != nil {
		t.Fatal(err)
	}
	state := store.GetParticipationState("account", time.Now())
	if !state.Active || state.JoinCount != 0 || state.WinCount != 0 {
		t.Fatalf("a new explicit start must create a fresh task: %+v", state)
	}
	if allowed, _ := store.ParticipationPolicy("account", time.Now()); !allowed {
		t.Fatal("historical task counts must not block the next start")
	}
}

func TestParticipationStateExplainsStopAndDrawResultPersists(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetParticipationSettings(ParticipationSettings{StopAfterJoins: 2, StopAfterWins: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordParticipationStarted("account", "账号甲"); err != nil {
		t.Fatal(err)
	}
	for index, eventID := range []string{"event-a", "event-b"} {
		event := Event{
			ID: eventID, RoomID: "room", WebRID: "123456", ActualRoomID: "700001",
			JoinBoxID: "box-" + eventID, DrawAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano),
		}
		if reserved, reserveErr := store.ReserveParticipation(event, "account", "账号甲"); reserveErr != nil || !reserved {
			t.Fatalf("reserve %d: reserved=%v err=%v", index, reserved, reserveErr)
		}
		if err := store.CompleteParticipation(event.ID, "account", "join", "joined", "已受理，等待开奖", 1, true, false, 0); err != nil {
			t.Fatal(err)
		}
	}
	state := store.GetParticipationState("account", time.Now())
	if !state.Stopped || state.JoinCount != 2 || !strings.Contains(state.StopReason, "2 次") {
		t.Fatalf("join stop state must be safe and explanatory: %+v", state)
	}
	pending := store.PendingDraws("account")
	if len(pending) != 2 || pending[0].ActualRoomID == "" || pending[0].DrawAt == "" {
		t.Fatalf("pending draw metadata was not retained: %+v", pending)
	}
	newWin, err := store.ResolveParticipationDraw("event-a", "account", "won", "已中8钻", "8钻", 1)
	if err != nil || !newWin {
		t.Fatalf("resolve win: new=%v err=%v", newWin, err)
	}
	if duplicateWin, err := store.ResolveParticipationDraw("event-a", "account", "won", "已中8钻", "8钻", 1); err != nil || duplicateWin {
		t.Fatalf("draw resolution must be idempotent: new=%v err=%v", duplicateWin, err)
	}
	if _, err := store.ResolveParticipationDraw("event-b", "account", "not_won", "未中奖", "", 1); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	records := reloaded.ParticipationRecords()
	if len(records) != 2 || records[0].Endpoint != "receive" || records[1].Endpoint != "receive" {
		t.Fatalf("draw results did not persist: %+v", records)
	}
	foundWin, foundLoss := false, false
	for _, record := range records {
		foundWin = foundWin || record.Status == "won" && record.Award == "8钻" && record.Won
		foundLoss = foundLoss || record.Status == "not_won" && !record.Won
	}
	if !foundWin || !foundLoss {
		t.Fatalf("expected one real win and one loss: %+v", records)
	}
}

func TestPendingDrawBackfillsLegacyNativeMetadata(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.participationTasks["account"] = &ParticipationTask{ID: "task-current", AccountID: "account", Active: true, StartedAt: time.Now().Format(time.RFC3339Nano)}
	store.monitors["room_123456"] = &Monitor{ID: "room_123456", WebRID: "123456", ActualRoomID: "700001"}
	store.events["room_123456:event"] = &Event{
		ID: "room_123456:event", MonitorID: "room_123456", WebRID: "123456",
		PacketID: "activity", ExpiresAt: "2026-08-01T23:30:00+08:00",
	}
	store.participations[participationRecordID("account", "room_123456:event")] = &ParticipationRecord{
		ID: participationRecordID("account", "room_123456:event"), EventID: "room_123456:event",
		AccountID: "account", TaskID: "task-current", PacketID: "7669063194534955828", Joined: true, Status: "joined",
	}
	store.mu.Unlock()
	pending := store.PendingDraws("account")
	if len(pending) != 1 || pending[0].ActualRoomID != "700001" || pending[0].JoinBoxID != "7669063194534955828" || pending[0].ExpiresAt == "" {
		t.Fatalf("legacy accepted result cannot be resumed safely: %+v", pending)
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
	if packet.totalDiamonds != 99 || packet.shareCount != 3 {
		t.Fatalf("authoritative diamond/share metadata missing: diamonds=%v shares=%d", packet.totalDiamonds, packet.shareCount)
	}
}

func TestExtractRedPacketKeepsActivityForGroupingButUsesNumericBoxForParticipation(t *testing.T) {
	packet, ok := extractRedPacket(map[string]any{
		"activity_id":   "AC202509231602294103473098",
		"activity_kind": "red_packet",
		"title":         "钻石红包",
		"items": []any{
			map[string]any{
				"box_id_str": "7641414053302127375",
				"box_type":   1,
				"send_time":  1760000000,
				"delay_time": 60,
			},
		},
	})
	if !ok {
		t.Fatal("expected explicit red packet payload")
	}
	if packet.id != "AC202509231602294103473098" {
		t.Fatalf("activity grouping id changed unexpectedly: %q", packet.id)
	}
	if packet.boxID != "7641414053302127375" {
		t.Fatalf("participation must prefer numeric box_id_str, got %q", packet.boxID)
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
	if packet.totalDiamonds != 25 || packet.shareCount != 15 {
		t.Fatalf("biz_extra diamond/share metadata missing: diamonds=%v shares=%d", packet.totalDiamonds, packet.shareCount)
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
			"activity_kind": "red_packet", "red_packet_id": "7669047909329177395", "title": "直播红包",
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
		if event.ActualRoomID != "7000000000000000002" || event.JoinBoxID != "7669047909329177395" || event.AnchorID != "anchor-2" || event.BoxType != "1" || event.SendTime == "" || event.DelayTime != "100" {
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
