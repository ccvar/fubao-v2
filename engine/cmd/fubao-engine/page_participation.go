package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"

	"fubao.ccvar.com/engine/internal/browsers"
	"fubao.ccvar.com/engine/internal/redpacket"
)

type nativePageParticipationTask struct {
	TaskID       string `json:"task_id"`
	Action       string `json:"action"`
	InstanceID   string `json:"instance_id"`
	AccountID    string `json:"account_id"`
	WebRID       string `json:"web_rid"`
	ActualRoomID string `json:"actual_room_id"`
	BoxID        string `json:"box_id"`
	AnchorID     string `json:"anchor_id,omitempty"`
	BoxType      string `json:"box_type,omitempty"`
	SendTime     string `json:"send_time,omitempty"`
	DelayTime    string `json:"delay_time,omitempty"`
}

type pageParticipationContext struct {
	InstanceID string `json:"instance_id"`
	AccountID  string `json:"account_id"`
}

type pageParticipationPending struct {
	task      nativePageParticipationTask
	accountID string
	delivered bool
	result    chan redpacket.PageParticipationResponse
}

// pageParticipationBroker is a native-only handoff between the Go scheduler
// and Rust's account-keyed WKWebViews. It never serializes Cookie values or
// signed request URLs.
type pageParticipationBroker struct {
	mu             sync.Mutex
	browsers       *browsers.Store
	readyByAccount map[string]string
	pending        map[string]*pageParticipationPending
	queue          []*pageParticipationPending
	busyAccounts   map[string]bool
}

func newPageParticipationBroker(browserStore *browsers.Store) *pageParticipationBroker {
	return &pageParticipationBroker{
		browsers:       browserStore,
		readyByAccount: map[string]string{},
		pending:        map[string]*pageParticipationPending{},
		busyAccounts:   map[string]bool{},
	}
}

func (b *pageParticipationBroker) SetContext(instanceID string, ready bool) (string, error) {
	if b == nil || b.browsers == nil {
		return "", errors.New("浏览器参与上下文不可用")
	}
	accountID, err := b.browsers.AccountID(strings.TrimSpace(instanceID))
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	if ready {
		b.readyByAccount[accountID] = instanceID
	} else if b.readyByAccount[accountID] == instanceID {
		delete(b.readyByAccount, accountID)
	}
	var cancelled []*pageParticipationPending
	if !ready {
		// Stopping the card context must be a real native stop, not just a
		// frontend color change. Tasks already delivered to WKWebView may finish,
		// but every task that has not issued a request yet is cancelled here.
		for taskID, pending := range b.pending {
			if pending != nil && pending.accountID == accountID && !pending.delivered {
				delete(b.pending, taskID)
				cancelled = append(cancelled, pending)
			}
		}
	}
	b.mu.Unlock()
	for _, pending := range cancelled {
		pending.result <- redpacket.PageParticipationResponse{
			ContextMissing: true,
			Error:          "浏览器实例已停止红包页面参与",
		}
	}
	return accountID, nil
}

func (b *pageParticipationBroker) Ready(accountID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.readyByAccount[accountID]) != ""
}

// StopAccount ends one completed participation task while allowing an already
// delivered native request to return. Undelivered joins are cancelled.
func (b *pageParticipationBroker) StopAccount(accountID string) {
	b.mu.Lock()
	delete(b.readyByAccount, strings.TrimSpace(accountID))
	var cancelled []*pageParticipationPending
	for taskID, pending := range b.pending {
		if pending != nil && pending.accountID == accountID && !pending.delivered {
			delete(b.pending, taskID)
			cancelled = append(cancelled, pending)
		}
	}
	b.mu.Unlock()
	for _, pending := range cancelled {
		pending.result <- redpacket.PageParticipationResponse{ContextMissing: true, Error: "本次红包参与任务已结束"}
	}
}

func (b *pageParticipationBroker) Execute(ctx context.Context, request redpacket.PageParticipationTask) redpacket.PageParticipationResponse {
	b.mu.Lock()
	instanceID := b.readyByAccount[request.AccountID]
	if instanceID == "" {
		b.mu.Unlock()
		return redpacket.PageParticipationResponse{ContextMissing: true, Error: "请先点击浏览器实例卡片的红包图标，进入直播间后再参与"}
	}
	taskID, err := randomPageParticipationID()
	if err != nil {
		b.mu.Unlock()
		return redpacket.PageParticipationResponse{Error: "生成原生红包任务失败"}
	}
	pending := &pageParticipationPending{
		task: nativePageParticipationTask{
			TaskID: taskID, Action: request.Action, InstanceID: instanceID, AccountID: request.AccountID,
			WebRID: request.WebRID, ActualRoomID: request.ActualRoomID, BoxID: request.BoxID,
			AnchorID: request.AnchorID, BoxType: request.BoxType,
			SendTime: request.SendTime, DelayTime: request.DelayTime,
		},
		accountID: request.AccountID,
		result:    make(chan redpacket.PageParticipationResponse, 1),
	}
	b.pending[taskID] = pending
	b.queue = append(b.queue, pending)
	b.mu.Unlock()

	select {
	case response := <-pending.result:
		return response
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, taskID)
		delete(b.busyAccounts, request.AccountID)
		b.mu.Unlock()
		return redpacket.PageParticipationResponse{Error: "直播页面红包请求等待超时"}
	}
}

func (b *pageParticipationBroker) Contexts() []pageParticipationContext {
	b.mu.Lock()
	defer b.mu.Unlock()
	items := make([]pageParticipationContext, 0, len(b.readyByAccount))
	for accountID, instanceID := range b.readyByAccount {
		items = append(items, pageParticipationContext{InstanceID: instanceID, AccountID: accountID})
	}
	return items
}

func (b *pageParticipationBroker) Next() (nativePageParticipationTask, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	active := b.queue[:0]
	for _, pending := range b.queue {
		if pending != nil && b.pending[pending.task.TaskID] != nil {
			active = append(active, pending)
		}
	}
	b.queue = active
	for _, pending := range b.queue {
		if pending.delivered || b.busyAccounts[pending.accountID] {
			continue
		}
		pending.delivered = true
		b.busyAccounts[pending.accountID] = true
		return pending.task, true
	}
	return nativePageParticipationTask{}, false
}

func (b *pageParticipationBroker) Complete(taskID string, response redpacket.PageParticipationResponse) bool {
	b.mu.Lock()
	pending := b.pending[strings.TrimSpace(taskID)]
	if pending == nil {
		b.mu.Unlock()
		return false
	}
	delete(b.pending, taskID)
	delete(b.busyAccounts, pending.accountID)
	b.mu.Unlock()
	pending.result <- response
	return true
}

func randomPageParticipationID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
