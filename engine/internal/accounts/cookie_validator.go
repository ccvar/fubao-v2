package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	cookieStatusUnknown = "unknown"
	cookieStatusValid   = "valid"
	cookieStatusExpired = "expired"

	douyinAccountInfoURL         = "https://www.douyin.com/passport/web/account/info/"
	douyinSelfProfileURL         = "https://www.douyin.com/aweme/v1/web/user/profile/self/?aid=6383&device_platform=webapp"
	validCookieMessage           = "CK 网页登录校验通过"
	validMonitoringCookieMessage = "监测接口请求正常，CK 可用于红包监测"
)

type CookieValidation struct {
	AccountID string `json:"account_id"`
	Role      Role   `json:"role,omitempty"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	CheckedAt string `json:"checked_at"`
}

type DouyinIdentity struct {
	Nickname string `json:"nickname"`
	UserID   string `json:"user_id"`
	SecUID   string `json:"sec_uid"`
}

type onlineCookieResult struct {
	Status  string
	Message string
}

// ValidateCookie checks only the selected account and caches the result. This
// keeps account-list rendering fast while giving browser instances an explicit
// login-state signal without exposing the raw Cookie to the frontend.
func (s *Store) ValidateCookie(ctx context.Context, accountID string, force bool) (CookieValidation, error) {
	return s.ValidateCookieForRole(ctx, accountID, RoleParticipation, force)
}

// ValidateCookieForRole keeps monitoring and participation health independent.
// Participation requires an authenticated browser login. Monitoring only
// requires that Douyin's business interface can be reached with the stored CK;
// a failed request remains unknown instead of being falsely labelled expired.
func (s *Store) ValidateCookieForRole(ctx context.Context, accountID string, role Role, force bool) (CookieValidation, error) {
	if role != RoleMonitoring && role != RoleParticipation {
		return CookieValidation{}, errors.New("未知账号分类")
	}
	s.mu.Lock()
	account := s.accounts[accountID]
	if account == nil {
		s.mu.Unlock()
		return CookieValidation{}, errors.New("账号不存在")
	}
	checkedAt, checkedStatus, checkedMessage := account.CookieChecked, account.CookieStatus, account.CookieMessage
	if role == RoleMonitoring && account.Monitoring != nil {
		checkedAt, checkedStatus, checkedMessage = account.Monitoring.CookieChecked, account.Monitoring.CookieStatus, account.Monitoring.CookieMessage
	}
	if !force && checkedAt != "" &&
		(checkedStatus != cookieStatusValid || checkedMessage == validCookieMessage || checkedMessage == validMonitoringCookieMessage) {
		if checked, err := time.Parse(time.RFC3339Nano, checkedAt); err == nil && time.Since(checked) < 15*time.Minute {
			result := cookieValidationViewForRole(account, role)
			s.mu.Unlock()
			return result, nil
		}
	}
	cookieText := account.Cookie
	s.mu.Unlock()

	result := validateDouyinCookie(ctx, cookieText)
	if role == RoleMonitoring {
		result = validateMonitoringCookie(ctx, cookieText)
	}
	validationAt := time.Now().Format(time.RFC3339Nano)

	s.mu.Lock()
	defer s.mu.Unlock()
	account = s.accounts[accountID]
	if account == nil {
		return CookieValidation{}, errors.New("账号不存在")
	}
	// Do not attach a stale network response to a Cookie replaced mid-check.
	if account.Cookie != cookieText {
		return cookieValidationViewForRole(account, role), nil
	}
	if role == RoleMonitoring {
		if account.Monitoring == nil {
			return CookieValidation{}, errors.New("账号不属于监测分类")
		}
		account.Monitoring.CookieStatus = result.Status
		account.Monitoring.CookieMessage = result.Message
		account.Monitoring.CookieChecked = validationAt
		account.Monitoring.LastValidatedAt = validationAt
		if result.Status == cookieStatusUnknown {
			account.Monitoring.LastError = result.Message
		} else {
			account.Monitoring.LastError = ""
		}
	} else {
		account.CookieStatus = result.Status
		account.CookieMessage = result.Message
		account.CookieChecked = validationAt
	}
	if err := s.saveLocked(); err != nil {
		return CookieValidation{}, err
	}
	return cookieValidationViewForRole(account, role), nil
}

func cookieValidationView(account *Account) CookieValidation {
	return cookieValidationViewForRole(account, RoleParticipation)
}

func cookieValidationViewForRole(account *Account, role Role) CookieValidation {
	status := account.CookieStatus
	message := account.CookieMessage
	checkedAt := account.CookieChecked
	if role == RoleMonitoring && account.Monitoring != nil {
		status = account.Monitoring.CookieStatus
		message = account.Monitoring.CookieMessage
		checkedAt = account.Monitoring.CookieChecked
	}
	if status == "" {
		status = cookieStatusUnknown
	}
	return CookieValidation{
		AccountID: account.ID,
		Role:      role,
		Status:    status,
		Message:   message,
		CheckedAt: checkedAt,
	}
}

func validateMonitoringCookie(ctx context.Context, cookieText string) onlineCookieResult {
	cookieText = strings.TrimSpace(cookieText)
	if cookieText == "" {
		return onlineCookieResult{cookieStatusExpired, "账号没有可用 Cookie，请重新登录或导入"}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	headers := map[string]string{
		"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		"Accept":          "application/json,text/plain,*/*",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
		"Referer":         "https://www.douyin.com/",
		"Cookie":          cookieText,
	}
	var lastErr error
	for _, url := range []string{douyinSelfProfileURL, douyinAccountInfoURL} {
		if _, err := getDouyinJSON(ctx, client, url, headers); err == nil {
			return onlineCookieResult{cookieStatusValid, validMonitoringCookieMessage}
		} else {
			lastErr = err
		}
	}
	message := "监测接口暂时不可用，尚不能判断 CK 状态"
	if lastErr != nil {
		message += "：" + lastErr.Error()
	}
	return onlineCookieResult{cookieStatusUnknown, message}
}

func validateDouyinCookie(ctx context.Context, cookieText string) onlineCookieResult {
	cookieText = strings.TrimSpace(cookieText)
	if cookieText == "" {
		return onlineCookieResult{cookieStatusExpired, "账号没有可用 Cookie，请重新登录或导入"}
	}
	if !hasLoginCookieMarker(cookieText) {
		return onlineCookieResult{cookieStatusExpired, "Cookie 缺少登录态字段，请重新登录或导入"}
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	headers := map[string]string{
		"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		"Accept":          "application/json,text/plain,*/*",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
		"Referer":         "https://www.douyin.com/",
		"Cookie":          cookieText,
	}

	accountPayload, accountErr := getDouyinJSON(ctx, client, douyinAccountInfoURL, headers)
	if accountErr == nil {
		if valid, message := accountInfoValid(accountPayload); valid {
			return onlineCookieResult{cookieStatusValid, validCookieMessage}
		} else if message != "" {
			// Browser instances need the full passport/web login state. The
			// self-profile endpoint can continue returning an identity after
			// that state has expired, so it must not override a definitive
			// passport failure.
			return onlineCookieResult{cookieStatusExpired, message}
		}
	}

	profilePayload, profileErr := getDouyinJSON(ctx, client, douyinSelfProfileURL, headers)
	if profileErr == nil {
		if valid, message := selfProfileValid(profilePayload); valid {
			return onlineCookieResult{cookieStatusValid, validCookieMessage}
		} else if message != "" {
			return onlineCookieResult{cookieStatusExpired, message}
		}
	}

	if accountErr != nil && profileErr != nil {
		return onlineCookieResult{cookieStatusUnknown, "CK 在线校验暂时不可用，请稍后重试"}
	}
	if accountPayload != nil {
		_, message := accountInfoValid(accountPayload)
		if message != "" {
			return onlineCookieResult{cookieStatusExpired, message}
		}
	}
	return onlineCookieResult{cookieStatusUnknown, "未能确认 CK 登录状态，请稍后重试"}
}

// ResolveDouyinIdentity reads the authenticated profile for a newly scanned
// login. It is used only inside the Go engine; the raw Cookie is never returned.
func ResolveDouyinIdentity(ctx context.Context, cookieText string) (DouyinIdentity, error) {
	cookieText = strings.TrimSpace(cookieText)
	if !hasLoginCookieMarker(cookieText) {
		return DouyinIdentity{}, errors.New("尚未检测到有效登录状态")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	headers := map[string]string{
		"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		"Accept":          "application/json,text/plain,*/*",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
		"Referer":         "https://www.douyin.com/",
		"Cookie":          cookieText,
	}
	var lastErr error
	for _, url := range []string{douyinAccountInfoURL, douyinSelfProfileURL} {
		payload, err := getDouyinJSON(ctx, client, url, headers)
		if err != nil {
			lastErr = err
			continue
		}
		user := findDouyinUser(payload)
		if user == nil {
			continue
		}
		identity := DouyinIdentity{
			Nickname: firstString(user, "screen_name", "nickname", "name", "username", "display_name", "user_name"),
			UserID:   firstString(user, "user_id", "uid", "unique_id", "short_id"),
			SecUID:   firstString(user, "sec_uid", "secUid", "sec_user_id", "secUserId"),
		}
		if identity.Nickname != "" || identity.UserID != "" || identity.SecUID != "" {
			return identity, nil
		}
	}
	if lastErr != nil {
		return DouyinIdentity{}, fmt.Errorf("读取抖音账号信息失败: %w", lastErr)
	}
	return DouyinIdentity{}, errors.New("未能读取当前登录账号，请确认已完成登录")
}

func getDouyinJSON(ctx context.Context, client *http.Client, url string, headers map[string]string) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("抖音返回 HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func accountInfoValid(payload map[string]any) (bool, string) {
	data, _ := payload["data"].(map[string]any)
	if data == nil {
		return false, "CK 已失效或账号信息接口未返回登录数据"
	}
	if user := findDouyinUser(data); user != nil {
		return true, ""
	}
	description := firstString(data, "description")
	if description == "" {
		description = firstString(payload, "message")
	}
	return false, expiredCookieMessage(description)
}

func selfProfileValid(payload map[string]any) (bool, string) {
	statusCode := intValue(payload["status_code"])
	if statusCode == 0 {
		if user := findDouyinUser(payload); user != nil {
			return true, ""
		}
		return false, "CK 已失效或主页资料接口未返回登录用户"
	}
	return false, expiredCookieMessage(firstString(payload, "status_msg", "message"))
}

func findDouyinUser(value any) map[string]any {
	switch item := value.(type) {
	case map[string]any:
		for _, key := range []string{"user", "user_info", "userInfo", "account", "account_info", "accountInfo"} {
			if child, ok := item[key].(map[string]any); ok && hasDouyinIdentity(child) {
				return child
			}
		}
		if hasDouyinIdentity(item) {
			return item
		}
		for _, child := range item {
			if found := findDouyinUser(child); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range item {
			if found := findDouyinUser(child); found != nil {
				return found
			}
		}
	}
	return nil
}

func hasDouyinIdentity(user map[string]any) bool {
	return firstString(user, "screen_name", "nickname", "name", "user_id", "uid", "unique_id", "short_id") != ""
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if item, ok := value[key]; ok && item != nil {
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func intValue(value any) int {
	var result int
	_, _ = fmt.Sscan(fmt.Sprint(value), &result)
	return result
}

func expiredCookieMessage(description string) string {
	description = strings.TrimSpace(description)
	if description == "" || strings.EqualFold(description, "error") {
		description = "会话已失效或未登录"
	}
	return "CK 已失效：" + description
}

func hasLoginCookieMarker(cookieText string) bool {
	markers := map[string]bool{
		"sessionid": true, "sessionid_ss": true, "sid_guard": true,
		"uid_tt": true, "uid_tt_ss": true, "passport_csrf_token": true, "odin_tt": true,
	}
	for _, part := range strings.Split(cookieText, ";") {
		name, _, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && markers[strings.ToLower(strings.TrimSpace(name))] {
			return true
		}
	}
	return false
}
