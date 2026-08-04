package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const douyinWalletInfoURL = "https://live.douyin.com/webcast/wallet/info/?account_type=0&aid=1128"

// RedPacketWalletBalance is safe account metadata. It contains only the
// current wallet snapshot and never includes Cookie, headers, or response
// bodies.
type RedPacketWalletBalance struct {
	UserID     string
	Diamond    int64
	DiamondX10 int64
	Money      int64
	CheckedAt  string
}

// RefreshRedPacketWalletBalance performs the wallet read in the Go native
// layer and persists only the safe balance fields to the participation profile.
// The raw Cookie is read under the store's native credential boundary and is
// never returned to an IPC caller.
func (s *Store) RefreshRedPacketWalletBalance(ctx context.Context, accountID string) (RedPacketWalletBalance, error) {
	credential, err := s.Credential(accountID)
	if err != nil {
		return RedPacketWalletBalance{}, err
	}
	if strings.TrimSpace(credential.Cookie) == "" {
		return RedPacketWalletBalance{}, errors.New("账号没有可用 Cookie")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, douyinWalletInfoURL, nil)
	if err != nil {
		return RedPacketWalletBalance{}, errors.New("创建钱包请求失败")
	}
	request.Header.Set("Cookie", credential.Cookie)
	request.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Referer", "https://live.douyin.com/")

	client := &http.Client{}
	response, err := client.Do(request)
	if err != nil {
		return RedPacketWalletBalance{}, fmt.Errorf("钱包接口请求失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return RedPacketWalletBalance{}, fmt.Errorf("钱包接口 HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 128*1024))
	if err != nil {
		return RedPacketWalletBalance{}, errors.New("读取钱包接口响应失败")
	}
	balance, err := parseRedPacketWalletBalance(body)
	if err != nil {
		return RedPacketWalletBalance{}, err
	}
	if err := s.RecordRedPacketWalletBalance(accountID, balance); err != nil {
		return RedPacketWalletBalance{}, err
	}
	return balance, nil
}

func parseRedPacketWalletBalance(body []byte) (RedPacketWalletBalance, error) {
	var payload struct {
		Data       map[string]json.RawMessage `json:"data"`
		StatusCode json.RawMessage            `json:"status_code"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return RedPacketWalletBalance{}, errors.New("钱包接口响应格式无效")
	}
	if status := rawInt64(payload.StatusCode); status != 0 {
		return RedPacketWalletBalance{}, fmt.Errorf("钱包接口状态码 %d", status)
	}
	if payload.Data == nil {
		return RedPacketWalletBalance{}, errors.New("钱包接口未返回账户数据")
	}
	return RedPacketWalletBalance{
		UserID:     rawString(payload.Data["user_id"]),
		Diamond:    rawInt64(payload.Data["diamond"]),
		DiamondX10: rawInt64(payload.Data["diamond_x10"]),
		Money:      rawInt64(payload.Data["money"]),
		CheckedAt:  time.Now().Format(time.RFC3339Nano),
	}, nil
}

func rawString(value json.RawMessage) string {
	if len(value) == 0 || string(value) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return strings.TrimSpace(text)
	}
	return strings.Trim(strings.TrimSpace(string(value)), "\"")
}

func rawInt64(value json.RawMessage) int64 {
	text := strings.TrimSpace(string(value))
	if text == "" || text == "null" {
		return 0
	}
	if parsed, err := strconv.ParseInt(strings.Trim(text, "\""), 10, 64); err == nil {
		return parsed
	}
	if parsed, err := strconv.ParseFloat(strings.Trim(text, "\""), 64); err == nil {
		return int64(parsed)
	}
	return 0
}

// RecordRedPacketWalletBalance persists safe wallet metadata for a
// participation account. It intentionally does not modify participation
// counters or CK health.
func (s *Store) RecordRedPacketWalletBalance(accountID string, balance RedPacketWalletBalance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[strings.TrimSpace(accountID)]
	if account == nil || account.Participation == nil || !hasRole(account, RoleParticipation) {
		return errors.New("只能为参与账号记录钱包余额")
	}
	profile := account.Participation
	profile.DiamondBalance = balance.Diamond
	profile.DiamondX10 = balance.DiamondX10
	profile.DiamondCheckedAt = firstNonEmpty(balance.CheckedAt, time.Now().Format(time.RFC3339Nano))
	profile.DiamondStatus = "valid"
	account.UpdatedAt = profile.DiamondCheckedAt
	return s.saveLocked()
}
