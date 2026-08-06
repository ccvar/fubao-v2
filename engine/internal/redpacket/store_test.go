package redpacket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestMonitorPageReturnsVisibleRowsAndGlobalSummary(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := []rooms.Room{
		{ID: "one", WebRID: "123456", Name: "一号", Enabled: true},
		{ID: "two", WebRID: "654321", Name: "二号", Enabled: true},
		{ID: "three", WebRID: "987654", Name: "三号", Enabled: false},
	}
	if err := store.SyncRooms(items); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.monitors["room_one"].Status = "running"
	store.monitors["room_one"].LiveStatus = "live"
	store.monitors["room_two"].ConnectionStatus = "error"
	store.mu.Unlock()
	page := store.PageForRooms([]string{"one"})
	if len(page.Items) != 1 || page.Items[0].RoomID != "one" {
		t.Fatalf("page leaked non-visible rows: %+v", page.Items)
	}
	if page.Summary.Total != 3 || page.Summary.Enabled != 2 || page.Summary.Running != 1 || page.Summary.FirstChecked != 1 || page.Summary.PendingFirst != 0 || page.Summary.LiveRunning != 1 || page.Summary.Errors != 1 {
		t.Fatalf("unexpected global monitor summary: %+v", page.Summary)
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
	if err := store.CompleteParticipation(event.ID, "account-1", "rush", "already_joined", "红包已受理", 2, true, false, "", time.Minute); err != nil {
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
		FollowPolicy: ParticipationFollowPolicyOnly,
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
	if err := store.CompleteParticipation(event.ID, "account-1", "join", "joined", "已受理", 1, true, false, "", 0); err != nil {
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

func TestParticipationTaskCapturesSettingsSnapshot(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := ParticipationSettings{
		StopAfterJoins: 2, CooldownSeconds: 30, StopAfterWins: 1,
		DrawResultDelaySeconds: 2, DrawResultMaxAttempts: 5, MinimumDiamonds: 3,
		RiskControlCooldownMinutes: 60,
		PacketType:                 ParticipationPacketTypeGift, FollowPolicy: ParticipationFollowPolicyOnly,
	}
	want.DrawResultTimeoutSeconds = 10 // legacy compatibility field is normalized on write
	if _, err := store.SetParticipationSettings(want); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordParticipationStarted("snapshot-account", "快照账号"); err != nil {
		t.Fatal(err)
	}
	changed := ParticipationSettings{
		StopAfterJoins: 1, CooldownSeconds: 1, StopAfterWins: 9,
		DrawResultDelaySeconds: 10, DrawResultMaxAttempts: 2, MinimumDiamonds: 1,
		PacketType: ParticipationPacketTypeDiamond, FollowPolicy: ParticipationFollowPolicyAll,
	}
	if _, err := store.SetParticipationSettings(changed); err != nil {
		t.Fatal(err)
	}
	if got := store.GetParticipationSettingsForAccount("snapshot-account"); got != want {
		t.Fatalf("active task changed when global settings changed: got=%+v want=%+v", got, want)
	}
	event := Event{ID: "snapshot-event", ActualRoomID: "700001", JoinBoxID: "box-snapshot"}
	if reserved, err := store.ReserveParticipation(event, "snapshot-account", "快照账号"); err != nil || !reserved {
		t.Fatalf("reserve: reserved=%v err=%v", reserved, err)
	}
	if got := store.ParticipationSettingsForEvent(event.ID, "snapshot-account"); got != want {
		t.Fatalf("record did not retain task settings: got=%+v want=%+v", got, want)
	}
	if err := store.CompleteParticipation(event.ID, "snapshot-account", "join", "joined", "已受理", 1, true, false, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveParticipationDraw(event.ID, "snapshot-account", "not_won", "未中奖", "", 1); err != nil {
		t.Fatal(err)
	}
	if allowed, cooldown := store.ParticipationPolicy("snapshot-account", time.Now()); allowed || cooldown <= 29*time.Second || cooldown > 30*time.Second {
		t.Fatalf("task policy did not retain cooldown snapshot: allowed=%v cooldown=%v", allowed, cooldown)
	}
}

func TestParticipationSettingsDefaultDrawQueryAndSafeTrace(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := store.GetParticipationSettings().DrawResultDelaySeconds; got != 1 {
		t.Fatalf("default draw-result delay=%d, want 1", got)
	}
	if got := store.GetParticipationSettings().DrawResultMaxAttempts; got != 3 {
		t.Fatalf("default draw-result attempts=%d, want 3", got)
	}
	if got := store.GetParticipationSettings().ParticipationCountdownSeconds; got != 10 {
		t.Fatalf("default participation countdown=%d, want 10", got)
	}
	if got := store.GetParticipationSettings().MinimumDiamonds; got != 1 {
		t.Fatalf("default minimum diamonds=%d, want 1", got)
	}
	if got := store.GetParticipationSettings().PacketType; got != ParticipationPacketTypeDiamond {
		t.Fatalf("default packet type=%q, want %q", got, ParticipationPacketTypeDiamond)
	}
	if got := store.GetParticipationSettings().FollowPolicy; got != ParticipationFollowPolicyPriority {
		t.Fatalf("default follow policy=%q, want %q", got, ParticipationFollowPolicyPriority)
	}
	if got := store.GetParticipationSettings().RiskControlCooldownMinutes; got != 60 {
		t.Fatalf("default risk-control cooldown=%d, want 60", got)
	}
	if err := store.RecordParticipationStarted("account-log", "日志账号"); err != nil {
		t.Fatal(err)
	}
	task := PageParticipationTask{
		Action: "join", EventID: "event-log", AccountID: "account-log", AccountName: "日志账号",
		WebRID: "123456", ActualRoomID: "700001", BoxID: "7669063194534955828", AnchorID: "anchor",
		FollowPolicy: ParticipationFollowPolicyPriority, Followed: true, FollowMatchKnown: true,
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
	if traces[0].FollowPolicy != ParticipationFollowPolicyPriority || !traces[0].FollowMatchKnown || !traces[0].Followed {
		t.Fatalf("safe follow decision missing from trace: %+v", traces[0])
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

func TestParticipationCountdownSettingsPersistAndAllowZero(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.SetParticipationSettings(ParticipationSettings{ParticipationCountdownSeconds: 0})
	if err != nil {
		t.Fatal(err)
	}
	if settings.ParticipationCountdownSeconds != 0 {
		t.Fatalf("explicit zero countdown was normalized away: %+v", settings)
	}
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.GetParticipationSettings().ParticipationCountdownSeconds; got != 0 {
		t.Fatalf("explicit zero countdown did not persist: %d", got)
	}
}

func TestMonitoringSettingsPersistAndHotSwapProbeWindow(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defaults := store.GetMonitoringSettings()
	if defaults.GlobalRequestIntervalMS != 80 || defaults.AccountRequestIntervalMS != 750 ||
		defaults.GlobalConcurrency != 32 || defaults.AccountConcurrency != 3 || defaults.ProbeConcurrency != 64 {
		t.Fatalf("unexpected monitoring defaults: %+v", defaults)
	}

	// Hold one old slot while replacing the channel. Its release must still
	// target the captured old channel instead of blocking on the new one.
	releaseOld, acquired := store.acquireProbeSlot(context.Background())
	if !acquired {
		t.Fatal("failed to acquire initial probe slot")
	}
	settings, err := store.SetMonitoringSettings(MonitoringSettings{
		GlobalRequestIntervalMS:  120,
		AccountRequestIntervalMS: 900,
		GlobalConcurrency:        20,
		AccountConcurrency:       2,
		ProbeConcurrency:         48,
	})
	if err != nil {
		t.Fatal(err)
	}
	releaseOld()
	if cap(store.probeSlots) != 48 {
		t.Fatalf("probe window was not hot replaced: %d", cap(store.probeSlots))
	}

	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.GetMonitoringSettings(); got != settings {
		t.Fatalf("monitoring settings did not persist: got=%+v want=%+v", got, settings)
	}
	if cap(reloaded.probeSlots) != settings.ProbeConcurrency {
		t.Fatalf("persisted probe window not restored: %d", cap(reloaded.probeSlots))
	}
}

func TestMonitoringSettingsClampUnsafeValues(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.SetMonitoringSettings(MonitoringSettings{
		GlobalRequestIntervalMS:  1,
		AccountRequestIntervalMS: 1,
		GlobalConcurrency:        9999,
		AccountConcurrency:       999,
		ProbeConcurrency:         9999,
	})
	if err != nil {
		t.Fatal(err)
	}
	if settings.GlobalRequestIntervalMS != 40 || settings.AccountRequestIntervalMS != 250 ||
		settings.GlobalConcurrency != maxGlobalConcurrency || settings.AccountConcurrency != maxAccountConcurrency || settings.ProbeConcurrency != maxProbeConcurrency {
		t.Fatalf("unsafe settings were not clamped: %+v", settings)
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

func TestParticipationScheduleMonitorPrewarmWindowAndIdempotency(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 2, 9, 30, 0, 0, time.Local)
	schedule, err := store.CreateParticipationSchedule(ParticipationSchedule{
		Mode:  ParticipationScheduleOnce,
		RunAt: now.Add(time.Hour).Format(time.RFC3339Nano),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if pending := store.PendingParticipationMonitorPrewarms(now.Add(49*time.Minute), 10*time.Minute); len(pending) != 0 {
		t.Fatalf("prewarm must not run before its configured window: %+v", pending)
	}
	pending := store.PendingParticipationMonitorPrewarms(now.Add(50*time.Minute), 10*time.Minute)
	if len(pending) != 1 || pending[0].ScheduleID != schedule.ID || pending[0].DueAt != schedule.NextRunAt {
		t.Fatalf("expected one due monitor prewarm: %+v", pending)
	}
	if err := store.RecordParticipationMonitorPrewarm(pending[0], 10*time.Minute, 12, 2); err != nil {
		t.Fatal(err)
	}
	if repeated := store.PendingParticipationMonitorPrewarms(now.Add(55*time.Minute), 10*time.Minute); len(repeated) != 0 {
		t.Fatalf("same schedule occurrence must prewarm once: %+v", repeated)
	}
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if repeated := reloaded.PendingParticipationMonitorPrewarms(now.Add(55*time.Minute), 10*time.Minute); len(repeated) != 0 {
		t.Fatalf("prewarm marker must persist across restarts: %+v", repeated)
	}
	found := false
	for _, activity := range reloaded.Activities() {
		found = found || strings.Contains(activity.Label, "已提前 10 分钟检查“指定日期”：12 个直播间监测已启用，使用 2 个监测账号")
	}
	if !found {
		t.Fatalf("safe prewarm activity missing: %+v", reloaded.Activities())
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

func TestParticipationTaskCompletionActivityAndOverviewPersist(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordParticipationStarted("account-summary", "账号甲"); err != nil {
		t.Fatal(err)
	}
	for index, won := range []bool{true, false} {
		event := Event{
			ID: fmt.Sprintf("event-summary-%d", index), RoomID: "room-summary", WebRID: "123456",
			JoinBoxID: fmt.Sprintf("box-summary-%d", index), Title: "钻石红包",
		}
		reserved, reserveErr := store.ReserveParticipation(event, "account-summary", "账号甲")
		if reserveErr != nil || !reserved {
			t.Fatalf("reserve %d failed: reserved=%v err=%v", index, reserved, reserveErr)
		}
		if err := store.CompleteParticipation(event.ID, "account-summary", "join", "joined", "已受理", 1, true, false, "", 0); err != nil {
			t.Fatal(err)
		}
		if won {
			if _, err := store.ResolveParticipationDraw(event.ID, "account-summary", "won", "已中8钻", "8钻", 1); err != nil {
				t.Fatal(err)
			}
		} else if _, err := store.ResolveParticipationDraw(event.ID, "account-summary", "not_won", "未中奖", "", 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.FinishParticipationTask("account-summary", "本次任务达到上限"); err != nil {
		t.Fatal(err)
	}
	// Finishing the same task again must not append a duplicate activity.
	if err := store.FinishParticipationTask("account-summary", "重复结束"); err != nil {
		t.Fatal(err)
	}
	activities := store.Activities()
	wantLabel := "账号甲已完成:8钻/1次, 参与2次"
	if len(activities) != 1 || activities[0].Kind != "participation_task_completed" || activities[0].Label != wantLabel {
		t.Fatalf("unexpected completion activity: %+v", activities)
	}
	if activities[0].JoinCount != 2 || activities[0].WinCount != 1 || activities[0].WinDiamonds != 8 || len(activities[0].AccountSummaries) != 1 {
		t.Fatalf("unexpected completion summary: %+v", activities[0])
	}
	if overview := store.ParticipationOverview(); overview.JoinCount != 2 || overview.WinCount != 1 || overview.WinDiamonds != 8 ||
		overview.TodayJoinCount != 2 || overview.TodayWinCount != 1 || overview.TodayWinDiamonds != 8 {
		t.Fatalf("unexpected participation overview: %+v", overview)
	}
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if overview := reloaded.ParticipationOverview(); overview.JoinCount != 2 || overview.WinCount != 1 || overview.WinDiamonds != 8 ||
		overview.TodayJoinCount != 2 || overview.TodayWinCount != 1 || overview.TodayWinDiamonds != 8 {
		t.Fatalf("overview did not persist: %+v", overview)
	}
	if activities := reloaded.Activities(); len(activities) != 1 || activities[0].Label != wantLabel {
		t.Fatalf("completion activity did not persist: %+v", activities)
	}
}

func TestParticipationOverviewAndAccountStatsExcludeNonSuccess(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordParticipationStarted("acc-stats", "统计账号"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// One accepted join → not_won, plus failure shapes that must never count.
	cases := []struct {
		eventID, status string
		joined, won     bool
	}{
		{"evt-ok", "not_won", true, false},
		{"evt-win", "won", true, true},
		{"evt-risk", "risk_control", false, false},
		{"evt-fail", "failed", false, false},
		{"evt-net", "network_error", false, false},
		{"evt-login", "login_expired", false, false},
		{"evt-pending", "pending", false, false},
		// Mis-flagged Joined must still be rejected by status.
		{"evt-risk-joined-flag", "risk_control", true, false},
		// Soft-deny false success: not_won without Joined must not count.
		{"evt-fake-not-won", "not_won", false, false},
	}
	for _, item := range cases {
		event := Event{ID: item.eventID, RoomID: "room-x", WebRID: "123456789", JoinBoxID: item.eventID, Title: "钻石红包"}
		reserved, reserveErr := store.ReserveParticipation(event, "acc-stats", "统计账号")
		if reserveErr != nil || !reserved {
			t.Fatalf("reserve %s: reserved=%v err=%v", item.eventID, reserved, reserveErr)
		}
		if err := store.CompleteParticipation(event.ID, "acc-stats", "join", item.status, item.status, 1, item.joined, item.won, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	overview := store.ParticipationOverview()
	if overview.JoinCount != 2 || overview.WinCount != 1 {
		t.Fatalf("overview must count only successful joins/wins: %+v", overview)
	}
	if overview.TodayJoinCount != 2 || overview.TodayWinCount != 1 {
		t.Fatalf("today overview must exclude failures: %+v", overview)
	}
	stats := store.ParticipationAccountStats(now)
	if len(stats) != 1 || stats[0].JoinCount != 2 || stats[0].WinCount != 1 || stats[0].TodayJoinCount != 2 || stats[0].TodayWinCount != 1 {
		t.Fatalf("account stats must exclude failures: %+v", stats)
	}
	// Helper itself rejects failure statuses even when Joined is true.
	bad := &ParticipationRecord{Status: "risk_control", Joined: true}
	if participationIsSuccessfulJoin(bad) {
		t.Fatal("risk_control must never count as successful join")
	}
	if participationIsSuccessfulJoin(&ParticipationRecord{Status: "not_won", Joined: false}) {
		t.Fatal("not_won without Joined must never count as successful join")
	}
}

func TestPurgeNoiseParticipationRecords(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordParticipationStarted("acc-purge", "清理账号"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		eventID, status string
		joined          bool
	}{
		{"keep-won", "won", true},
		{"keep-not-won", "not_won", true},
		{"keep-risk", "risk_control", false},
		{"drop-failed", "failed", false},
		{"drop-net", "network_error", false},
		{"drop-pending", "pending", false},
	}
	for _, item := range cases {
		event := Event{ID: item.eventID, RoomID: "room-p", WebRID: "1234567890", JoinBoxID: item.eventID, Title: "钻石红包"}
		reserved, reserveErr := store.ReserveParticipation(event, "acc-purge", "清理账号")
		if reserveErr != nil || !reserved {
			t.Fatalf("reserve %s: %v %v", item.eventID, reserved, reserveErr)
		}
		if item.status == "pending" {
			continue
		}
		if err := store.CompleteParticipation(event.ID, "acc-purge", "join", item.status, item.status, 1, item.joined, item.status == "won", "", 0); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := store.PurgeNoiseParticipationRecords()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Fatalf("removed=%d, want 3", removed)
	}
	left := store.ParticipationRecords()
	if len(left) != 3 {
		t.Fatalf("left=%d records: %+v", len(left), left)
	}
	for _, record := range left {
		switch record.Status {
		case "won", "not_won", "risk_control":
		default:
			t.Fatalf("noise status still present: %+v", record)
		}
	}
}

func TestDemoteFalseSuccessfulJoinsClearsSoftDenyHistory(t *testing.T) {
	records := map[string]*ParticipationRecord{
		"fake": {
			ID: "fake", Status: "not_won", Message: "未中奖", Joined: true,
			JoinedAt: "2026-08-05T12:00:00+08:00", ResultSource: "wallet_snapshot",
		},
		"draw": {
			ID: "draw", Status: "draw_error", Message: "开奖查询失败：已尝试 3 次，接口未返回", Joined: true,
			JoinedAt: "2026-08-05T13:00:00+08:00",
		},
		"win": {
			ID: "win", Status: "won", Message: "已中1钻", Award: "1钻", Joined: true, Won: true,
			JoinedAt: "2026-08-05T14:00:00+08:00",
		},
		"wallet": {
			ID: "wallet", Status: "won", Message: "已中2钻（钱包增量确认）", Award: "2钻", Joined: true, Won: true,
			JoinedAt: "2026-08-05T15:00:00+08:00", ResultSource: "wallet_delta", WalletDiamondDelta: 2,
		},
		"post": {
			ID: "post", Status: "not_won", Message: "未中奖", Joined: true,
			JoinedAt: "2026-08-06T16:30:00+08:00", ResultSource: "wallet_snapshot",
		},
	}
	got := demoteFalseSuccessfulJoins(records)
	if got != 2 {
		t.Fatalf("demoted=%d, want 2 (fake+draw)", got)
	}
	if records["fake"].Joined || records["fake"].Status != "failed" {
		t.Fatalf("fake soft-deny not demoted: %+v", records["fake"])
	}
	if records["draw"].Joined || records["draw"].Status != "failed" {
		t.Fatalf("draw_error soft-deny not demoted: %+v", records["draw"])
	}
	if !records["win"].Joined || records["win"].Status != "won" {
		t.Fatalf("real win must stay: %+v", records["win"])
	}
	if !records["wallet"].Joined {
		t.Fatalf("wallet-delta win must stay: %+v", records["wallet"])
	}
	if !records["post"].Joined || records["post"].Status != "not_won" {
		t.Fatalf("post-cutover real join must stay: %+v", records["post"])
	}
	// Overview after demotion only counts real accepts.
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	for id, record := range records {
		store.participations[id] = record
	}
	store.mu.Unlock()
	overview := store.ParticipationOverview()
	if overview.JoinCount != 3 || overview.WinCount != 2 {
		t.Fatalf("overview after demote: %+v want joins=3 wins=2", overview)
	}
}

func TestParticipationBatchCompletesAsOneAggregatedActivity(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []struct{ id, name string }{{"batch-a", "账号甲"}, {"batch-b", "账号乙"}} {
		if err := store.RecordParticipationStarted(account.id, account.name); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordParticipationBatchResult("", "immediate", 2, 0, []string{"batch-a", "batch-b"}); err != nil {
		t.Fatal(err)
	}
	for index, accountID := range []string{"batch-a", "batch-b"} {
		event := Event{ID: fmt.Sprintf("batch-event-%d", index), RoomID: "room", WebRID: "123456", JoinBoxID: fmt.Sprintf("batch-box-%d", index), Title: "钻石红包"}
		if reserved, err := store.ReserveParticipation(event, accountID, ""); err != nil || !reserved {
			t.Fatalf("batch reserve failed: reserved=%v err=%v", reserved, err)
		}
		if err := store.CompleteParticipation(event.ID, accountID, "join", "joined", "已受理", 1, true, false, "", 0); err != nil {
			t.Fatal(err)
		}
		award := "8钻"
		if index == 1 {
			award = "2个小心心"
		}
		if _, err := store.ResolveParticipationDraw(event.ID, accountID, "won", "已中奖", award, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.FinishParticipationTask("batch-a", "已完成"); err != nil {
		t.Fatal(err)
	}
	activities := store.Activities()
	if len(activities) != 1 || !activities[0].Active || strings.Contains(activities[0].Label, "已完成") {
		t.Fatalf("batch completed before every child task ended: %+v", activities)
	}
	if err := store.FinishParticipationTask("batch-b", "已完成"); err != nil {
		t.Fatal(err)
	}
	activities = store.Activities()
	if len(activities) != 1 || activities[0].Active || activities[0].Label != "“立即执行”已完成:8钻/2次, 参与2次" {
		t.Fatalf("unexpected aggregated completion activity: %+v", activities)
	}
	if len(activities[0].AccountSummaries) != 2 || activities[0].JoinCount != 2 || activities[0].WinCount != 2 || activities[0].WinDiamonds != 8 {
		t.Fatalf("unexpected aggregated account details: %+v", activities[0])
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
	if err := store.CompleteParticipation(event.ID, "account-overdue", "join", "joined", "等待开奖", 1, true, false, "", 0); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	// Reload cleanup only rewrites records that are already beyond the full
	// native query budget (delay + attempts * page-participation timeout).
	store.participations[participationRecordID("account-overdue", event.ID)].JoinedAt = time.Now().Add(-3 * time.Minute).Format(time.RFC3339Nano)
	if err := store.saveLocked(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()
	if err := store.FinishParticipationTask("account-overdue", "客户端关闭"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	records := reloaded.ParticipationRecords()
	if len(records) != 1 || records[0].Status != "draw_error" || !strings.Contains(records[0].Message, "3 次") {
		t.Fatalf("overdue pending draw was not migrated: %+v", records)
	}
}

func TestRestartReconciliationStopsStaleTaskButKeepsPendingDrawResumable(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordParticipationStarted("account-stale", "旧任务账号"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordParticipationStarted("account-pending", "待开奖账号"); err != nil {
		t.Fatal(err)
	}
	event := Event{
		ID: "event-pending-restart", WebRID: "123456", ActualRoomID: "700001", JoinBoxID: "7669063194534955828",
		ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano),
	}
	if reserved, reserveErr := store.ReserveParticipation(event, "account-pending", "待开奖账号"); reserveErr != nil || !reserved {
		t.Fatalf("reserve pending event: reserved=%v err=%v", reserved, reserveErr)
	}
	if err := store.CompleteParticipation(event.ID, "account-pending", "join", "joined", "等待开奖", 1, true, false, "", 0); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	result, err := store.ReconcileParticipationTasksAfterRestart(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.StoppedAccountIDs) != 1 || result.StoppedAccountIDs[0] != "account-stale" {
		t.Fatalf("unexpected stopped accounts: %+v", result)
	}
	if len(result.PendingAccountIDs) != 1 || result.PendingAccountIDs[0] != "account-pending" {
		t.Fatalf("unexpected pending accounts: %+v", result)
	}
	stale := store.participationTasks["account-stale"]
	if stale == nil || stale.Active || stale.EndedAt == "" || !strings.Contains(stale.EndReason, "客户端重启") {
		t.Fatalf("stale task was not closed: %+v", stale)
	}
	if pending := store.participationTasks["account-pending"]; pending == nil || !pending.Active {
		t.Fatalf("pending draw task must remain resumable: %+v", pending)
	}

	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if state := reloaded.GetParticipationState("account-stale", now); state.Active {
		t.Fatalf("stale task became active after reload: %+v", state)
	}
	if draws := reloaded.PendingDraws("account-pending"); len(draws) != 1 || draws[0].ID != event.ID {
		t.Fatalf("pending draw was not retained for explicit recovery: %+v", draws)
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
		if err := store.CompleteParticipation(event.ID, "account", "join", "joined", "已受理，等待开奖", 1, true, false, "", 0); err != nil {
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
	liveResults := make(chan string, 1)
	store.SetLiveResultHandler(func(roomID, status string, _ time.Time) {
		liveResults <- roomID + ":" + status
	})
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
	select {
	case result := <-liveResults:
		if result != "room-1:offline" {
			t.Fatalf("unexpected definitive live result callback: %s", result)
		}
	case <-time.After(time.Second):
		t.Fatal("offline result callback was not invoked")
	}
}

func TestExtractAnchorIDPrefersNestedOwnerUser(t *testing.T) {
	payload := map[string]any{
		"box_id_str": "7669047909329177395",
		"title":      "钻石红包",
		"owner": map[string]any{
			"nickname": "主播甲",
			"id_str":   "7500165983340938297",
		},
		"activity_kind": "red_packet",
	}
	packet, ok := extractRedPacket(payload)
	if !ok {
		t.Fatal("expected red-packet extraction")
	}
	if packet.anchorID != "7500165983340938297" {
		t.Fatalf("expected nested owner id as anchor, got %q", packet.anchorID)
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

func TestPollOnceDispatchesCenterEventAfterLocalRequestMetadataIsEnriched(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SyncRooms([]rooms.Room{{ID: "room-center", WebRID: "123456789", Name: "中心直播间", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Minute).Format(time.RFC3339Nano)
	if imported, err := store.MergeCenter([]CenterEvent{{
		WebRID: "123456789", PacketID: "AC202509231602294103473098",
		Title: "钻石红包", Prize: "总25钻，15份红包", ExpiresAt: future,
	}}); err != nil || imported != 1 {
		t.Fatalf("merge center event: imported=%d err=%v", imported, err)
	}
	store.mu.Lock()
	store.monitors["room_room-center"].Status = "running"
	store.monitors["room_room-center"].AccountID = "monitor-account"
	store.monitors["room_room-center"].AccountName = "监测账号"
	store.mu.Unlock()

	handled := make(chan Event, 2)
	store.SetEventHandler(func(event Event) { handled <- event })
	source := &fakeMonitorSource{
		probe: LiveProbe{Status: "live", ActualRoomID: "7000000000000000001", Source: "room_web_enter"},
		snapshots: []poller.Snapshot{{Source: "luckybox_api", ActualRoomID: "7000000000000000001", Data: map[string]any{
			"activity_kind": "red_packet", "activity_id": "AC202509231602294103473098",
			"box_id_str": "7669047909329177395", "title": "钻石红包",
			"total_diamond_count": 25, "box_count": 15, "expire_time": future,
		}}},
	}
	store.pollOnce(context.Background(), "room_room-center", source)

	select {
	case event := <-handled:
		if event.DataSource != "" || event.MonitorID != "room_room-center" || event.ActualRoomID != "7000000000000000001" || event.JoinBoxID != "7669047909329177395" {
			t.Fatalf("enriched center event missing native participation metadata: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("enriched center event was not dispatched to participation handler")
	}

	store.pollOnce(context.Background(), "room_room-center", source)
	select {
	case duplicate := <-handled:
		t.Fatalf("already actionable event was dispatched twice: %+v", duplicate)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMergeCenterDispatchesAndPersistsNativeParticipationMetadata(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	handled := make(chan Event, 2)
	store.SetEventHandler(func(event Event) { handled <- event })
	future := time.Now().Add(2 * time.Minute).Format(time.RFC3339Nano)
	item := CenterEvent{
		WebRID: "123456789", PacketID: "AC202509231602294103473098",
		ActualRoomID: "7000000000000000001", JoinBoxID: "7669047909329177395",
		AnchorID: "1234567890", BoxType: "1", SendTime: "100", DelayTime: "30",
		Title: "钻石红包", Prize: "总25钻，15份红包", ExpiresAt: future,
	}
	if imported, mergeErr := store.MergeCenter([]CenterEvent{item}); mergeErr != nil || imported != 1 {
		t.Fatalf("merge native center event: imported=%d err=%v", imported, mergeErr)
	}
	select {
	case event := <-handled:
		if event.ActualRoomID != item.ActualRoomID || event.JoinBoxID != item.JoinBoxID || event.AnchorID != item.AnchorID {
			t.Fatalf("center event lost native metadata: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("actionable center event was not dispatched")
	}

	safeJSON, err := json.Marshal(store.EventsAll())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(safeJSON, []byte(item.ActualRoomID)) || bytes.Contains(safeJSON, []byte(item.JoinBoxID)) {
		t.Fatalf("native participation metadata leaked through event JSON: %s", safeJSON)
	}
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	events := reloaded.EventsAll()
	if len(events) != 1 || events[0].ActualRoomID != item.ActualRoomID || events[0].JoinBoxID != item.JoinBoxID || events[0].DelayTime != item.DelayTime {
		t.Fatalf("native participation metadata did not survive reload: %+v", events)
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
	liveResults := make(chan string, 1)
	store.SetLiveResultHandler(func(roomID, status string, _ time.Time) {
		liveResults <- roomID + ":" + status
	})
	source := &fakeMonitorSource{probeErr: errors.New("probe failed")}
	if got := store.pollOnce(context.Background(), "room_room-3", source); got != unknownProbeInterval {
		t.Fatalf("failed probe should retry after %s, got %s", unknownProbeInterval, got)
	}
	monitor, _ := store.Get("room_room-3")
	if monitor.Status != "running" || monitor.LiveStatus != "error" || monitor.ConnectionStatus != "error" || monitor.LastError == "" {
		t.Fatalf("unexpected error state: %+v", monitor)
	}
	select {
	case result := <-liveResults:
		t.Fatalf("probe failures must not emit offline evidence, got %s", result)
	default:
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

func TestBulkMonitoringPrioritizesLocalRoomsBeforeCenterRooms(t *testing.T) {
	monitors := map[string]*Monitor{
		"room_center_b": {ID: "room_center_b", Source: "center"},
		"room_manual_b": {ID: "room_manual_b", Source: "manual"},
		"room_center_a": {ID: "room_center_a", Source: "center"},
		"room_follow_a": {ID: "room_follow_a", Source: "following-live"},
		"room_legacy_a": {ID: "room_legacy_a", Source: "dy-kiro"},
	}
	ids := []string{"room_center_b", "room_manual_b", "room_center_a", "room_follow_a", "room_legacy_a"}
	localCount := prioritizeBulkMonitorIDs(ids, monitors)
	if localCount != 3 {
		t.Fatalf("expected three local rooms before center rows, got %d: %v", localCount, ids)
	}
	want := []string{"room_follow_a", "room_legacy_a", "room_manual_b", "room_center_a", "room_center_b"}
	for index := range want {
		if ids[index] != want[index] {
			t.Fatalf("unexpected source priority order: got %v want %v", ids, want)
		}
	}
}

func TestBulkMonitoringUsesFollowingThenImportedThenCenterTiers(t *testing.T) {
	monitors := map[string]*Monitor{
		"a-imported": {ID: "a-imported", Source: "manual"},
		"m-center":   {ID: "m-center", Source: "center"},
		"z-follow":   {ID: "z-follow", Source: "following-live"},
	}
	ids := []string{"a-imported", "m-center", "z-follow"}
	prioritizeBulkMonitorIDs(ids, monitors)
	want := []string{"z-follow", "a-imported", "m-center"}
	for index := range want {
		if ids[index] != want[index] {
			t.Fatalf("source tier %d=%q, want %q; order=%v", index, ids[index], want[index], ids)
		}
	}
}

func TestBulkMonitoringPriorityBurstAllowsLowerTiers(t *testing.T) {
	now := time.Now()
	queues := [3]bulkMonitorHeap{
		{{id: "follow", due: now}},
		{{id: "imported", due: now}},
		{{id: "center", due: now}},
	}
	if rank, ok := nextReadyBulkQueue(queues, now, 0); !ok || rank != 0 {
		t.Fatalf("first ready slot should prefer following tier, rank=%d ready=%v", rank, ok)
	}
	if rank, ok := nextReadyBulkQueue(queues, now, bulkPriorityBurst); !ok || rank != 1 {
		t.Fatalf("priority burst should yield to imported tier, rank=%d ready=%v", rank, ok)
	}
	queues[1] = nil
	if rank, ok := nextReadyBulkQueue(queues, now, bulkPriorityBurst); !ok || rank != 2 {
		t.Fatalf("priority burst should yield to center tier when imports are absent, rank=%d ready=%v", rank, ok)
	}
}

func TestSyncRoomsPersistsRoomSourceForNativeScheduling(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SyncRooms([]rooms.Room{
		{ID: "local", WebRID: "123456", Source: "manual", Enabled: true},
		{ID: "remote", WebRID: "654321", Source: "center", Enabled: true},
		{ID: "followed", WebRID: "777777", Source: "manual", FollowSources: []rooms.FollowSource{{AccountID: "account-1"}}, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	if err := store.saveLocked(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()
	reloaded, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	local, _ := reloaded.Get("room_local")
	remote, _ := reloaded.Get("room_remote")
	followed, _ := reloaded.Get("room_followed")
	if local.Source != "manual" || remote.Source != "center" {
		t.Fatalf("monitor sources were not persisted: local=%q center=%q", local.Source, remote.Source)
	}
	if followed.Source != "following-live" {
		t.Fatalf("follow attribution must raise a monitor into the following tier, got %q", followed.Source)
	}
}

func TestLargeMonitorStoreUsesBoundedExpandedProbeWindow(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cap(store.probeSlots) != defaultProbeSlots || defaultBulkWorkers != 64 {
		t.Fatalf("unexpected large-monitor bounds: slots=%d workers=%d", cap(store.probeSlots), defaultBulkWorkers)
	}
}
