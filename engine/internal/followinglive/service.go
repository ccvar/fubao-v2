// Package followinglive fetches the live rooms belonging to accounts followed
// by one canonical Douyin account. Raw account cookies stay inside the Go
// engine; callers receive only safe room and presenter metadata.
package followinglive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"fubao.ccvar.com/engine/internal/live/httpclient"
)

const endpoint = "https://www.douyin.com/webcast/web/feed/follow/"

// Item is the safe subset of a followed live room that may cross engine IPC.
type Item struct {
	RoomID      string `json:"room_id"`
	WebRID      string `json:"web_rid"`
	UserID      string `json:"user_id,omitempty"`
	SecUID      string `json:"sec_uid,omitempty"`
	Nickname    string `json:"nickname"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	Title       string `json:"title,omitempty"`
	ViewerCount string `json:"viewer_count,omitempty"`
}

// Result is the account-scoped followed-live snapshot returned to the UI.
type Result struct {
	AccountID   string `json:"account_id"`
	Total       int    `json:"total"`
	Items       []Item `json:"items"`
	RefreshedAt string `json:"refreshed_at"`
	Stale       bool   `json:"stale,omitempty"`
}

type cacheEntry struct {
	result    Result
	expiresAt time.Time
}

// Service keeps a short account-scoped cache so opening the instance page does
// not repeatedly hit Douyin while cards rerender or native WebViews resize.
type Service struct {
	mu    sync.Mutex
	cache map[string]cacheEntry
	ttl   time.Duration
}

func NewService() *Service {
	return &Service{cache: make(map[string]cacheEntry), ttl: 60 * time.Second}
}

// StoreNative records a safe snapshot produced inside the account's real
// browser page. The page owns the exact Cookie jar and dynamic request context;
// only the already-reduced public room fields cross the authenticated native
// channel into the Go engine.
func (s *Service) StoreNative(accountID string, items []Item) Result {
	accountID = strings.TrimSpace(accountID)
	items = normalizeItems(items)
	result := Result{
		AccountID:   accountID,
		Total:       len(items),
		Items:       items,
		RefreshedAt: time.Now().Format(time.RFC3339),
	}
	s.mu.Lock()
	s.cache[accountID] = cacheEntry{result: result, expiresAt: time.Now().Add(s.ttl)}
	s.mu.Unlock()
	return result
}

// MatchRoom reports whether a live-room event belongs to the latest
// successful followed-live snapshot for one participation account. The second
// result is false when no sufficiently recent snapshot exists, allowing the
// default priority policy to fail open without mistaking a transport problem
// for an empty follow list.
func (s *Service) MatchRoom(accountID, webRID, actualRoomID, anchorID string) (bool, bool) {
	accountID = strings.TrimSpace(accountID)
	webRID = strings.TrimSpace(webRID)
	actualRoomID = strings.TrimSpace(actualRoomID)
	anchorID = strings.TrimSpace(anchorID)
	if accountID == "" {
		return false, false
	}

	s.mu.Lock()
	entry, exists := s.cache[accountID]
	s.mu.Unlock()
	if !exists || time.Now().After(entry.expiresAt.Add(s.ttl)) {
		return false, false
	}
	for _, item := range entry.result.Items {
		if sameNonEmpty(webRID, item.WebRID) ||
			sameNonEmpty(actualRoomID, item.RoomID) ||
			sameNonEmpty(anchorID, item.UserID) ||
			sameNonEmpty(anchorID, item.SecUID) {
			return true, true
		}
	}
	return false, true
}

func sameNonEmpty(left, right string) bool {
	return left != "" && left == strings.TrimSpace(right)
}

// Fetch returns the current followed accounts that are live. A stale cached
// result is preferred over turning a temporary network failure into a false
// empty state.
func (s *Service) Fetch(ctx context.Context, accountID, cookie string, force bool) (Result, error) {
	accountID = strings.TrimSpace(accountID)
	cookie = strings.TrimSpace(cookie)
	if accountID == "" || cookie == "" {
		return Result{}, errors.New("账号登录信息不可用")
	}

	s.mu.Lock()
	entry, cached := s.cache[accountID]
	if cached && !force && time.Now().Before(entry.expiresAt) {
		result := entry.result
		s.mu.Unlock()
		return result, nil
	}
	s.mu.Unlock()

	client := httpclient.New(httpclient.WithCookie(cookie), httpclient.WithTimeout(12*time.Second))
	params := map[string]string{
		"device_platform":             "webapp",
		"aid":                         "6383",
		"channel":                     "channel_pc_web",
		"pc_client_type":              "1",
		"version_code":                "290100",
		"version_name":                "29.1.0",
		"cookie_enabled":              "true",
		"screen_width":                "1920",
		"screen_height":               "1080",
		"browser_language":            "zh-CN",
		"from_user_page":              "1",
		"locate_query":                "false",
		"need_time_list":              "1",
		"pc_libra_divert":             "Windows",
		"publish_video_strategy_type": "2",
		"round_trip_time":             "0",
		"show_live_replay_strategy":   "1",
		"time_list_query":             "0",
		"update_version_code":         "170400",
		"whale_cut_token":             "",
		"scene":                       "aweme_pc_follow_top",
	}
	req, err := client.NewSignedRequest(ctx, http.MethodGet, endpoint, params)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Origin", "https://www.douyin.com")
	req.Header.Set("Referer", "https://www.douyin.com/follow")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("x-common-params-v2", client.SignXCommonParamsV2(req.URL.String()))

	resp, err := client.Do(req)
	if err != nil {
		return staleOrError(entry, cached, fmt.Errorf("读取关注直播失败: %w", err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return staleOrError(entry, cached, fmt.Errorf("读取关注直播响应失败: %w", err))
	}
	if resp.StatusCode != http.StatusOK {
		return staleOrError(entry, cached, fmt.Errorf("关注直播接口返回 HTTP %d", resp.StatusCode))
	}

	items, err := parseItems(body)
	if err != nil {
		return staleOrError(entry, cached, err)
	}
	result := Result{
		AccountID:   accountID,
		Total:       len(items),
		Items:       items,
		RefreshedAt: time.Now().Format(time.RFC3339),
	}
	s.mu.Lock()
	s.cache[accountID] = cacheEntry{result: result, expiresAt: time.Now().Add(s.ttl)}
	s.mu.Unlock()
	return result, nil
}

func staleOrError(entry cacheEntry, cached bool, err error) (Result, error) {
	if cached {
		result := entry.result
		result.Stale = true
		return result, nil
	}
	return Result{}, err
}

func parseItems(body []byte) ([]Item, error) {
	var payload struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
		Data       struct {
			Data []json.RawMessage `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("关注直播响应无法解析: %w", err)
	}
	if payload.StatusCode != 0 {
		message := strings.TrimSpace(payload.StatusMsg)
		if message == "" {
			message = fmt.Sprintf("状态码 %d", payload.StatusCode)
		}
		return nil, fmt.Errorf("关注直播请求失败: %s", message)
	}

	items := make([]Item, 0, len(payload.Data.Data))
	seen := make(map[string]struct{}, len(payload.Data.Data))
	for _, raw := range payload.Data.Data {
		var record struct {
			WebRID string `json:"web_rid"`
			Room   struct {
				IDStr        string `json:"id_str"`
				Title        string `json:"title"`
				UserCountStr string `json:"user_count_str"`
				Owner        struct {
					IDStr       string `json:"id_str"`
					SecUID      string `json:"sec_uid"`
					Nickname    string `json:"nickname"`
					AvatarThumb struct {
						URLList []string `json:"url_list"`
					} `json:"avatar_thumb"`
				} `json:"owner"`
				Stats struct {
					UserCountStr string `json:"user_count_str"`
				} `json:"stats"`
			} `json:"room"`
		}
		if json.Unmarshal(raw, &record) != nil {
			continue
		}
		roomID := strings.TrimSpace(record.Room.IDStr)
		webRID := strings.TrimSpace(record.WebRID)
		key := roomID
		if key == "" {
			key = webRID
		}
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		viewerCount := strings.TrimSpace(record.Room.UserCountStr)
		if viewerCount == "" {
			viewerCount = strings.TrimSpace(record.Room.Stats.UserCountStr)
		}
		avatar := ""
		if len(record.Room.Owner.AvatarThumb.URLList) > 0 {
			avatar = strings.TrimSpace(record.Room.Owner.AvatarThumb.URLList[0])
		}
		items = append(items, Item{
			RoomID:      roomID,
			WebRID:      webRID,
			UserID:      strings.TrimSpace(record.Room.Owner.IDStr),
			SecUID:      strings.TrimSpace(record.Room.Owner.SecUID),
			Nickname:    strings.TrimSpace(record.Room.Owner.Nickname),
			AvatarURL:   avatar,
			Title:       strings.TrimSpace(record.Room.Title),
			ViewerCount: viewerCount,
		})
	}
	return normalizeItems(items), nil
}

func normalizeItems(items []Item) []Item {
	normalized := make([]Item, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		item.RoomID = trimField(item.RoomID, 32)
		item.WebRID = trimField(item.WebRID, 32)
		key := item.RoomID
		if key == "" {
			key = item.WebRID
		}
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		item.UserID = trimField(item.UserID, 64)
		item.SecUID = trimField(item.SecUID, 160)
		item.Nickname = trimField(item.Nickname, 120)
		item.AvatarURL = trimField(item.AvatarURL, 800)
		if item.AvatarURL != "" && !strings.HasPrefix(item.AvatarURL, "https://") && !strings.HasPrefix(item.AvatarURL, "http://") {
			item.AvatarURL = ""
		}
		item.Title = trimField(item.Title, 240)
		item.ViewerCount = trimField(item.ViewerCount, 48)
		normalized = append(normalized, item)
		if len(normalized) == 200 {
			break
		}
	}
	return normalized
}

func trimField(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}
