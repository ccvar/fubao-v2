package main

import (
	"context"
	"testing"
	"time"

	"fubao.ccvar.com/engine/internal/browsers"
	"fubao.ccvar.com/engine/internal/redpacket"
)

func TestPageParticipationBrokerUsesPreparedAccountInstance(t *testing.T) {
	browserStore, err := browsers.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := browserStore.Create("account-1", "参与账号", "")
	if err != nil {
		t.Fatal(err)
	}
	broker := newPageParticipationBroker(browserStore)
	accountID, err := broker.SetContext(instance.ID, true)
	if err != nil || accountID != "account-1" || !broker.Ready("account-1") {
		t.Fatalf("failed to prepare account context: account=%q err=%v", accountID, err)
	}
	if capacity := browserStore.Capacity(); capacity.Running != 1 {
		t.Fatalf("prepared context did not retain runtime: %+v", capacity)
	}

	resultChannel := make(chan redpacket.PageParticipationResponse, 1)
	go func() {
		resultChannel <- broker.Execute(context.Background(), redpacket.PageParticipationTask{
			AccountID: "account-1", WebRID: "7654321", ActualRoomID: "700001", BoxID: "box-1",
		})
	}()
	var task nativePageParticipationTask
	var ok bool
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		task, ok = broker.Next()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ok || task.InstanceID != instance.ID || task.AccountID != "account-1" || task.WebRID != "7654321" {
		t.Fatalf("unexpected native page task: ok=%v task=%+v", ok, task)
	}
	want := redpacket.PageParticipationResponse{Endpoint: "join", HTTPStatus: 200, Body: `{"status_code":0}`, Attempts: 1}
	if !broker.Complete(task.TaskID, want) {
		t.Fatal("broker rejected active native task completion")
	}
	select {
	case got := <-resultChannel:
		if got.Endpoint != want.Endpoint || got.HTTPStatus != want.HTTPStatus || got.Body != want.Body {
			t.Fatalf("unexpected broker result: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broker result")
	}
	if _, err := broker.SetContext(instance.ID, false); err != nil || broker.Ready("account-1") {
		t.Fatalf("browser context was not released: %v", err)
	}
	if capacity := browserStore.Capacity(); capacity.Running != 0 {
		t.Fatalf("released context retained runtime: %+v", capacity)
	}
}

func TestPageParticipationBrokerStopAccountReleasesRuntime(t *testing.T) {
	browserStore, err := browsers.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := browserStore.Create("account-finished", "完成账号", "")
	if err != nil {
		t.Fatal(err)
	}
	broker := newPageParticipationBroker(browserStore)
	if _, err := broker.SetContext(instance.ID, true); err != nil {
		t.Fatal(err)
	}
	broker.StopAccount("account-finished")
	if broker.Ready("account-finished") {
		t.Fatal("completed task kept page context ready")
	}
	if capacity := browserStore.Capacity(); capacity.Running != 0 {
		t.Fatalf("completed task retained runtime: %+v", capacity)
	}
}

func TestPageParticipationBrokerStopCancelsUndeliveredTask(t *testing.T) {
	browserStore, err := browsers.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := browserStore.Create("account-stop", "停止账号", "")
	if err != nil {
		t.Fatal(err)
	}
	broker := newPageParticipationBroker(browserStore)
	if _, err := broker.SetContext(instance.ID, true); err != nil {
		t.Fatal(err)
	}

	resultChannel := make(chan redpacket.PageParticipationResponse, 1)
	go func() {
		resultChannel <- broker.Execute(context.Background(), redpacket.PageParticipationTask{
			AccountID: "account-stop", WebRID: "7654321", ActualRoomID: "700001", BoxID: "7669047909329177395",
		})
	}()
	deadline := time.Now().Add(time.Second)
	queued := false
	for time.Now().Before(deadline) {
		broker.mu.Lock()
		queued = len(broker.pending) == 1
		broker.mu.Unlock()
		if queued {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !queued {
		t.Fatal("page task was not queued before stop")
	}
	if _, err := broker.SetContext(instance.ID, false); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-resultChannel:
		if !result.ContextMissing || result.Error != "浏览器实例已停止红包页面参与" {
			t.Fatalf("unexpected stopped task result: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("stopping context did not cancel the queued task")
	}
	if _, ok := broker.Next(); ok {
		t.Fatal("stopped context still exposed a queued native task")
	}
}
