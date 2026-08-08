package accounts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// storeVersion 3 adds the opt-in API red-packet participation state. Existing
// accounts intentionally default to off; participation must always be an
// explicit user choice in this client.
// The legacy app's counters describe a different runtime, so carrying them
// across makes the current desktop numbers misleading. Version 5 adds the
// safe native wallet snapshot used to show current diamonds and reconcile a
// missing luckybox result.
const storeVersion = 5

type Role string

const (
	RoleMonitoring    Role = "monitoring"
	RoleParticipation Role = "participation"
)

type MonitoringProfile struct {
	Enabled         bool   `json:"enabled"`
	CookieStatus    string `json:"cookie_status,omitempty"`
	CookieMessage   string `json:"cookie_message,omitempty"`
	CookieChecked   string `json:"cookie_checked_at,omitempty"`
	LastValidatedAt string `json:"last_validated_at,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	LastUsedAt      string `json:"last_used_at,omitempty"`
	LastUseStatus   string `json:"last_use_status,omitempty"`
	LastUseMessage  string `json:"last_use_message,omitempty"`
	// MonitorCooldownUntil is the account pool's own back-off window, set when
	// Douyin rate-limits or rejects this monitoring account. Without it a
	// rate-limited account keeps rendering as 可用 while it silently stops
	// issuing requests, which is exactly the signal an operator needs.
	// This is throughput state, never CK health: a temporary request failure
	// must not mark a monitoring Cookie expired.
	MonitorCooldownUntil  string `json:"monitor_cooldown_until,omitempty"`
	MonitorCooldownReason string `json:"monitor_cooldown_reason,omitempty"`
	TotalRequestCount     int    `json:"total_request_count"`
	TodayRequestCount     int    `json:"today_request_count"`
	TodayRequestDate      string `json:"today_request_date,omitempty"`
}

// Monitoring cooldown reasons. They separate a throughput problem (the machine
// is asking too fast) from an account problem (Douyin rejected this session),
// which need very different operator responses.
const (
	MonitorCooldownRateLimited = "rate_limited"
	MonitorCooldownAuth        = "auth"
	MonitorCooldownNetwork     = "network"
)

// ActiveMonitorCooldown reports whether a persisted monitoring cooldown window
// has not expired yet.
func ActiveMonitorCooldown(until string, now time.Time) bool {
	until = strings.TrimSpace(until)
	if until == "" {
		return false
	}
	expiry, err := time.Parse(time.RFC3339Nano, until)
	if err != nil {
		return false
	}
	return now.Before(expiry)
}

type ParticipationProfile struct {
	Enabled                bool   `json:"enabled"`
	RedPacketAPIEnabled    bool   `json:"red_packet_api_enabled"`
	RedPacketCooldownUntil string `json:"red_packet_cooldown_until,omitempty"`
	LastRedPacketStatus    string `json:"last_red_packet_status,omitempty"`
	LastRedPacketMessage   string `json:"last_red_packet_message,omitempty"`
	JoinCount              int    `json:"join_count"`
	WinCount               int    `json:"win_count"`
	// TodayStatDate is the local calendar day (YYYY-MM-DD) for the today_* counters.
	TodayStatDate        string   `json:"today_stat_date,omitempty"`
	TodayJoinCount       int      `json:"today_join_count"`
	TodayWinCount        int      `json:"today_win_count"`
	TodayWinDiamonds     float64  `json:"today_win_diamonds"`
	LastJoinAt           string   `json:"last_join_at,omitempty"`
	LastError            string   `json:"last_error,omitempty"`
	ProxyID              int      `json:"proxy_id"`
	FingerprintProfileID int      `json:"fingerprint_profile_id"`
	Tags                 []string `json:"tags,omitempty"`
	GroupID              string   `json:"group_id,omitempty"`
	DiamondBalance       int64    `json:"diamond_balance"`
	DiamondX10           int64    `json:"diamond_x10"`
	DiamondCheckedAt     string   `json:"diamond_checked_at,omitempty"`
	DiamondStatus        string   `json:"diamond_status,omitempty"`
}

const (
	redPacketStatusChallengeBlocked = "challenge_blocked"
	redPacketStatusRiskControl      = "risk_control"
)

// activeRedPacketCooldown reports whether a persisted cooldown expiry is still
// in the future. Risk-control windows must survive task stops and API toggles.
func activeRedPacketCooldown(until string, now time.Time) bool {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(until))
	return err == nil && now.Before(parsed)
}

type ParticipationGroup struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// RedPacketParticipationCredential is private engine-only data used by the Go
// scheduler. It is never returned by an IPC list API.
type RedPacketParticipationCredential struct {
	AccountID   string
	AccountName string
	Cookie      string
	// UserID / SecUID are safe Douyin identity fields required by the real
	// live-page luckybox/join and rush query. They are never cookies or
	// signatures and stay native-only.
	UserID string
	SecUID string
}

type Account struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Nickname      string `json:"nickname,omitempty"`
	UserID        string `json:"user_id,omitempty"`
	SecUID        string `json:"sec_uid,omitempty"`
	Cookie        string `json:"cookie"`
	CookieStatus  string `json:"cookie_status,omitempty"`
	CookieMessage string `json:"cookie_message,omitempty"`
	CookieChecked string `json:"cookie_checked_at,omitempty"`
	Source        string `json:"source,omitempty"`
	// BrowserProfileKey selects the native WebView data-store identity used by
	// browser instances. Scan-login sets this to "create-{session}" so the
	// instance reuses the already-authenticated create WebView store instead
	// of an empty account-id store that cannot re-inject a live session.
	// Empty means "use account id" (import / rebind default).
	BrowserProfileKey string                `json:"browser_profile_key,omitempty"`
	Roles             []Role                `json:"roles"`
	Monitoring        *MonitoringProfile    `json:"monitoring,omitempty"`
	Participation     *ParticipationProfile `json:"participation,omitempty"`
	CreatedAt         string                `json:"created_at"`
	UpdatedAt         string                `json:"updated_at"`
}

type accountFile struct {
	Version             int                   `json:"version"`
	Accounts            []*Account            `json:"accounts"`
	ParticipationGroups []*ParticipationGroup `json:"participation_groups,omitempty"`
}

type AccountView struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Nickname      string                `json:"nickname,omitempty"`
	UserID        string                `json:"user_id,omitempty"`
	CookieStatus  string                `json:"cookie_status,omitempty"`
	CookieMessage string                `json:"cookie_message,omitempty"`
	CookieChecked string                `json:"cookie_checked_at,omitempty"`
	Source        string                `json:"source,omitempty"`
	Roles         []Role                `json:"roles"`
	Monitoring    *MonitoringProfile    `json:"monitoring,omitempty"`
	Participation *ParticipationProfile `json:"participation,omitempty"`
	CreatedAt     string                `json:"created_at"`
	UpdatedAt     string                `json:"updated_at"`
}

type MigrationResult struct {
	Imported                 int `json:"imported"`
	Merged                   int `json:"merged"`
	MonitoringAssignments    int `json:"monitoring_assignments"`
	ParticipationAssignments int `json:"participation_assignments"`
	Total                    int `json:"total"`
}

type BrowserCredential struct {
	AccountID    string
	AccountName  string
	Cookie       string
	CookieStatus string
	// Source is the account provenance used by the browser surface router
	// (manual-import → external Chrome; qr/native → embedded WebView).
	Source string
	// BrowserProfileKey is the native WebView data-store key for this account
	// (see Account.BrowserProfileKey). Empty means account id.
	BrowserProfileKey string
}

type Store struct {
	mu                            sync.Mutex
	path                          string
	accounts                      map[string]*Account
	participationGroups           map[string]*ParticipationGroup
	monitoringUsageDirty          bool
	monitoringUsageFlushScheduled bool
}

func DefaultDataDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("FUBAO_DATA_DIR")); override != "" {
		return override, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("读取系统配置目录失败: %w", err)
	}
	return filepath.Join(configDir, "com.ccvar.fubao.desktop", "data"), nil
}

func NewStore(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		var err error
		dataDir, err = DefaultDataDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建账号数据目录失败: %w", err)
	}
	store := &Store{
		path:                filepath.Join(dataDir, "accounts.json"),
		accounts:            map[string]*Account{},
		participationGroups: map[string]*ParticipationGroup{},
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取账号数据失败: %w", err)
	}
	var file accountFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("解析账号数据失败: %w", err)
	}
	resetLegacyUsage := file.Version < 2
	needsUpgrade := file.Version < storeVersion
	for _, account := range file.Accounts {
		if account == nil || account.ID == "" {
			continue
		}
		normalizeRoles(account)
		if resetLegacyUsage && account.Monitoring != nil {
			// Imported counters belong to 福宝's process rather than this
			// desktop client. Keep validation information, but begin local
			// request accounting at zero.
			account.Monitoring.LastUsedAt = ""
			account.Monitoring.LastUseStatus = ""
			account.Monitoring.LastUseMessage = ""
			account.Monitoring.TotalRequestCount = 0
			account.Monitoring.TodayRequestCount = 0
		}
		s.accounts[account.ID] = account
	}
	for _, group := range file.ParticipationGroups {
		if group == nil || strings.TrimSpace(group.ID) == "" || strings.TrimSpace(group.Name) == "" {
			continue
		}
		copy := *group
		copy.Name = strings.TrimSpace(copy.Name)
		s.participationGroups[copy.ID] = &copy
	}
	if needsUpgrade {
		if err := s.saveLocked(); err != nil {
			return err
		}
	}
	return nil
}

// RecordMonitoringRequest records one actual request made by the local
// red-packet monitor. Writes are coalesced so large room batches do not turn
// every poll into a disk write.
func (s *Store) RecordMonitoringRequest(accountID string, requestErr error) {
	if strings.TrimSpace(accountID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil || account.Monitoring == nil {
		return
	}
	profile := account.Monitoring
	now := time.Now()
	today := now.Format("2006-01-02")
	if profile.TodayRequestDate != today {
		profile.TodayRequestCount = 0
		profile.TodayRequestDate = today
	}
	profile.TotalRequestCount++
	profile.TodayRequestCount++
	profile.LastUsedAt = now.Format(time.RFC3339Nano)
	if requestErr != nil {
		profile.LastUseStatus = "error"
		profile.LastUseMessage = requestErr.Error()
	} else {
		profile.LastUseStatus = "success"
		profile.LastUseMessage = ""
		// A successful request proves the pool's back-off is over.
		profile.MonitorCooldownUntil = ""
		profile.MonitorCooldownReason = ""
		// Monitoring CK health is deliberately independent from the browser /
		// participation login check. A successful room or red-packet request is
		// authoritative evidence that this Cookie can still perform monitoring.
		profile.CookieStatus = cookieStatusValid
		profile.CookieMessage = validMonitoringCookieMessage
		profile.CookieChecked = profile.LastUsedAt
		profile.LastValidatedAt = profile.LastUsedAt
		profile.LastError = ""
	}
	account.UpdatedAt = profile.LastUsedAt
	s.monitoringUsageDirty = true
	s.scheduleMonitoringUsageFlushLocked()
}

// RecordMonitoringCooldown persists the account pool's back-off window so the
// account row can show why a monitoring account stopped issuing requests. A
// zero `until` clears the window. It deliberately never touches CookieStatus:
// per the monitoring health rule only an absent Cookie may be marked expired,
// and a rejected request is not proof that the session is dead.
func (s *Store) RecordMonitoringCooldown(accountID string, until time.Time, reason, message string) {
	if strings.TrimSpace(accountID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil || account.Monitoring == nil {
		return
	}
	profile := account.Monitoring
	if until.IsZero() {
		if profile.MonitorCooldownUntil == "" && profile.MonitorCooldownReason == "" {
			return
		}
		profile.MonitorCooldownUntil = ""
		profile.MonitorCooldownReason = ""
	} else {
		profile.MonitorCooldownUntil = until.Format(time.RFC3339Nano)
		profile.MonitorCooldownReason = strings.TrimSpace(reason)
		if text := strings.TrimSpace(message); text != "" {
			profile.LastUseStatus = "cooling"
			profile.LastUseMessage = text
		}
	}
	account.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	s.monitoringUsageDirty = true
	s.scheduleMonitoringUsageFlushLocked()
}

// ClearAllMonitoringCooldowns drops every persisted monitoring back-off window.
// Used when the user stops monitoring so a stale countdown cannot outlive the
// pool that created it.
func (s *Store) ClearAllMonitoringCooldowns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cleared := 0
	for _, account := range s.accounts {
		if account == nil || account.Monitoring == nil {
			continue
		}
		if account.Monitoring.MonitorCooldownUntil == "" && account.Monitoring.MonitorCooldownReason == "" {
			continue
		}
		account.Monitoring.MonitorCooldownUntil = ""
		account.Monitoring.MonitorCooldownReason = ""
		if account.Monitoring.LastUseStatus == "cooling" {
			account.Monitoring.LastUseStatus = ""
			account.Monitoring.LastUseMessage = ""
		}
		cleared++
	}
	if cleared > 0 {
		s.monitoringUsageDirty = true
		s.scheduleMonitoringUsageFlushLocked()
	}
	return cleared
}

func (s *Store) scheduleMonitoringUsageFlushLocked() {
	if s.monitoringUsageFlushScheduled {
		return
	}
	s.monitoringUsageFlushScheduled = true
	go func() {
		time.Sleep(time.Second)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.monitoringUsageFlushScheduled = false
		if !s.monitoringUsageDirty {
			return
		}
		if err := s.saveLocked(); err == nil {
			s.monitoringUsageDirty = false
			return
		}
		// Retain in-memory counts and retry on the next small batch instead
		// of risking a high-frequency retry loop on a failing disk.
		s.scheduleMonitoringUsageFlushLocked()
	}()
}

func (s *Store) saveLocked() error {
	items := make([]*Account, 0, len(s.accounts))
	for _, account := range s.accounts {
		items = append(items, account)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt < items[j].CreatedAt
	})
	groups := make([]*ParticipationGroup, 0, len(s.participationGroups))
	for _, group := range s.participationGroups {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].CreatedAt < groups[j].CreatedAt })
	payload, err := json.MarshalIndent(accountFile{Version: storeVersion, Accounts: items, ParticipationGroups: groups}, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化账号数据失败: %w", err)
	}
	// A writer-unique temp name. A fixed sibling name is shared by every
	// process using this data directory — a dev build running beside the
	// installed app writes the exact same path, the two byte streams interleave
	// and the rename then publishes a corrupt file. Cookies live in this file,
	// so a torn write costs every stored credential.
	temp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-")
	if err != nil {
		return fmt.Errorf("创建账号临时文件失败: %w", err)
	}
	tempPath := temp.Name()
	// A no-op once the rename has consumed the path; guarantees no leftover
	// temp file on any failure path.
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return fmt.Errorf("写入账号临时文件失败: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("关闭账号临时文件失败: %w", err)
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return fmt.Errorf("设置账号文件权限失败: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("保存账号数据失败: %w", err)
	}
	return nil
}

func (s *Store) List(role Role) []AccountView {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	items := make([]AccountView, 0, len(s.accounts))
	for _, account := range s.accounts {
		if role != "" && !hasRole(account, role) {
			continue
		}
		// Roll local-day counters before exposing the row so midnight never leaves
		// yesterday's "今日" totals on the participation account table.
		if account.Participation != nil {
			rollParticipationDayLocked(account.Participation, now)
		}
		items = append(items, safeViewForRole(account, role))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].CreatedAt < items[j].CreatedAt
		}
		return items[i].Name < items[j].Name
	})
	return items
}

func (s *Store) ListParticipationGroups() []ParticipationGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]ParticipationGroup, 0, len(s.participationGroups))
	for _, group := range s.participationGroups {
		items = append(items, *group)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt == items[j].CreatedAt {
			return items[i].Name < items[j].Name
		}
		return items[i].CreatedAt < items[j].CreatedAt
	})
	return items
}

func (s *Store) CreateParticipationGroup(name string) (ParticipationGroup, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ParticipationGroup{}, errors.New("分组名称不能为空")
	}
	if len([]rune(name)) > 24 {
		return ParticipationGroup{}, errors.New("分组名称最多 24 个字")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, group := range s.participationGroups {
		if strings.EqualFold(group.Name, name) {
			return *group, nil
		}
	}
	now := time.Now().Format(time.RFC3339Nano)
	sum := sha256.Sum256([]byte("participation-group:" + name + ":" + now))
	group := &ParticipationGroup{ID: hex.EncodeToString(sum[:8]), Name: name, CreatedAt: now}
	s.participationGroups[group.ID] = group
	if err := s.saveLocked(); err != nil {
		delete(s.participationGroups, group.ID)
		return ParticipationGroup{}, err
	}
	return *group, nil
}

func (s *Store) SetParticipationGroup(accountID, groupID string) (AccountView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[strings.TrimSpace(accountID)]
	if account == nil {
		return AccountView{}, errors.New("账号不存在")
	}
	if !hasRole(account, RoleParticipation) || account.Participation == nil {
		return AccountView{}, errors.New("只有参与账号可以设置分组")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID != "" && s.participationGroups[groupID] == nil {
		return AccountView{}, errors.New("参与账号分组不存在")
	}
	account.Participation.GroupID = groupID
	account.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	if err := s.saveLocked(); err != nil {
		return AccountView{}, err
	}
	return safeView(account), nil
}

func (s *Store) ParticipationCredential(accountID string) (BrowserCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil {
		return BrowserCredential{}, errors.New("账号不存在")
	}
	if !hasRole(account, RoleParticipation) {
		return BrowserCredential{}, errors.New("只能为参与账号创建浏览器实例")
	}
	if account.Participation == nil || !account.Participation.Enabled {
		return BrowserCredential{}, errors.New("参与账号已停用")
	}
	if strings.TrimSpace(account.Cookie) == "" {
		return BrowserCredential{}, errors.New("账号没有可用 Cookie，请重新登录或导入")
	}
	return BrowserCredential{
		AccountID:         account.ID,
		AccountName:       firstNonEmpty(account.Nickname, account.Name, account.UserID, "抖音账号"),
		Cookie:            account.Cookie,
		CookieStatus:      account.CookieStatus,
		Source:            account.Source,
		BrowserProfileKey: strings.TrimSpace(account.BrowserProfileKey),
	}, nil
}

// SetBrowserProfileKey persists the native WebView data-store key for an
// account. Pass empty to fall back to the account id.
func (s *Store) SetBrowserProfileKey(accountID, profileKey string) (AccountView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[strings.TrimSpace(accountID)]
	if account == nil {
		return AccountView{}, errors.New("账号不存在")
	}
	account.BrowserProfileKey = strings.TrimSpace(profileKey)
	account.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	if err := s.saveLocked(); err != nil {
		return AccountView{}, err
	}
	return safeView(account), nil
}

// AccountSource returns the persisted provenance for surface routing without
// exposing Cookie data.
func (s *Store) AccountSource(accountID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[strings.TrimSpace(accountID)]
	if account == nil {
		return ""
	}
	return account.Source
}

// SetRedPacketAPIEnabled persists the per-account opt-in controlled by the
// compact gift icon in the participation account table.
func (s *Store) SetRedPacketAPIEnabled(accountID string, enabled bool) (AccountView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil {
		return AccountView{}, errors.New("账号不存在")
	}
	if !hasRole(account, RoleParticipation) || account.Participation == nil {
		return AccountView{}, errors.New("只能为参与账号设置红包接口参与")
	}
	previousProfile := *account.Participation
	previousUpdatedAt := account.UpdatedAt
	now := time.Now()
	cooling := activeRedPacketCooldown(account.Participation.RedPacketCooldownUntil, now)
	riskCooling := cooling && account.Participation.LastRedPacketStatus == redPacketStatusRiskControl
	account.Participation.RedPacketAPIEnabled = enabled
	if !enabled {
		// Closing the participation switch (manual stop or auto task end) must
		// not erase an active risk-control cooldown; the UI badge and skip
		// logic depend on red_packet_cooldown_until until it expires.
		if !cooling {
			account.Participation.RedPacketCooldownUntil = ""
		}
		if riskCooling {
			// Keep status/message so rows still show 风控冷却 + countdown.
		} else {
			account.Participation.LastRedPacketStatus = "disabled"
			account.Participation.LastRedPacketMessage = "已关闭红包接口参与"
		}
	} else if !riskCooling {
		account.Participation.LastRedPacketStatus = "ready"
		account.Participation.LastRedPacketMessage = "已启用红包接口参与"
	}
	account.UpdatedAt = now.Format(time.RFC3339Nano)
	if err := s.saveLocked(); err != nil {
		*account.Participation = previousProfile
		account.UpdatedAt = previousUpdatedAt
		return AccountView{}, err
	}
	return safeView(account), nil
}

// RedPacketParticipationCredentials returns only explicitly opted-in accounts
// that are enabled, CK-valid and outside their persisted cooldown window.
func (s *Store) RedPacketParticipationCredentials(now time.Time) []RedPacketParticipationCredential {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]RedPacketParticipationCredential, 0)
	for _, account := range s.accounts {
		profile := account.Participation
		if profile == nil || !profile.Enabled || !profile.RedPacketAPIEnabled || !hasRole(account, RoleParticipation) {
			continue
		}
		// A captcha/security challenge is an account-level native page block,
		// not a temporary request cooldown and not CK expiry. Keep the account
		// out of the scheduler until the user explicitly restarts participation
		// after handling the challenge in its isolated browser instance.
		if profile.LastRedPacketStatus == redPacketStatusChallengeBlocked {
			continue
		}
		if strings.TrimSpace(account.Cookie) == "" || account.CookieStatus != cookieStatusValid {
			continue
		}
		if until, err := time.Parse(time.RFC3339Nano, profile.RedPacketCooldownUntil); err == nil && now.Before(until) {
			continue
		}
		items = append(items, RedPacketParticipationCredential{
			AccountID:   account.ID,
			AccountName: firstNonEmpty(account.Nickname, account.Name, account.UserID, "抖音账号"),
			Cookie:      account.Cookie,
			UserID:      strings.TrimSpace(account.UserID),
			SecUID:      strings.TrimSpace(account.SecUID),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AccountName < items[j].AccountName })
	return items
}

// ParticipationStatsPatch is applied when repairing account-row counters from
// the durable red-packet participation record store.
type ParticipationStatsPatch struct {
	AccountID        string
	JoinCount        int
	WinCount         int
	TodayJoinCount   int
	TodayWinCount    int
	TodayWinDiamonds float64
	TodayStatDate    string
}

// ReconcileParticipationStatsFromRecords rewrites join/win and today_* fields
// from durable successful-join records. Risk-control / failed / network-error
// rows must never contribute. Profile counters are replaced by the record
// rollup so inflated legacy totals cannot keep showing as “参与成功”.
func (s *Store) ReconcileParticipationStatsFromRecords(patches []ParticipationStatsPatch, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.IsZero() {
		now = time.Now()
	}
	today := now.Format("2006-01-02")
	changed := 0
	seen := map[string]struct{}{}
	for _, patch := range patches {
		accountID := strings.TrimSpace(patch.AccountID)
		account := s.accounts[accountID]
		if account == nil || account.Participation == nil {
			continue
		}
		seen[accountID] = struct{}{}
		profile := account.Participation
		statDate := strings.TrimSpace(patch.TodayStatDate)
		if statDate == "" {
			statDate = today
		}
		nextJoin := patch.JoinCount
		if nextJoin < 0 {
			nextJoin = 0
		}
		nextWin := patch.WinCount
		if nextWin < 0 {
			nextWin = 0
		}
		// Always refresh today counters from records for the current local day.
		todayJoin, todayWin, todayDiamonds := 0, 0, 0.0
		if statDate == today {
			todayJoin = patch.TodayJoinCount
			if todayJoin < 0 {
				todayJoin = 0
			}
			todayWin = patch.TodayWinCount
			if todayWin < 0 {
				todayWin = 0
			}
			todayDiamonds = patch.TodayWinDiamonds
			if todayDiamonds < 0 {
				todayDiamonds = 0
			}
		}
		if profile.JoinCount == nextJoin &&
			profile.WinCount == nextWin &&
			profile.TodayStatDate == today &&
			profile.TodayJoinCount == todayJoin &&
			profile.TodayWinCount == todayWin &&
			profile.TodayWinDiamonds == todayDiamonds {
			continue
		}
		profile.JoinCount = nextJoin
		profile.WinCount = nextWin
		profile.TodayStatDate = today
		profile.TodayJoinCount = todayJoin
		profile.TodayWinCount = todayWin
		profile.TodayWinDiamonds = todayDiamonds
		account.UpdatedAt = now.Format(time.RFC3339Nano)
		changed++
	}
	// Full record rollup is authoritative: participation accounts missing from
	// the patch set have zero successful-join / win rows and must not keep
	// inflated legacy totals (risk_control / failed / network_error used to
	// bump join_count incorrectly).
	for accountID, account := range s.accounts {
		if account == nil || account.Participation == nil {
			continue
		}
		if _, ok := seen[accountID]; ok {
			continue
		}
		profile := account.Participation
		if profile.JoinCount == 0 && profile.WinCount == 0 &&
			profile.TodayStatDate == today && profile.TodayJoinCount == 0 &&
			profile.TodayWinCount == 0 && profile.TodayWinDiamonds == 0 {
			continue
		}
		profile.JoinCount = 0
		profile.WinCount = 0
		profile.TodayStatDate = today
		profile.TodayJoinCount = 0
		profile.TodayWinCount = 0
		profile.TodayWinDiamonds = 0
		account.UpdatedAt = now.Format(time.RFC3339Nano)
		changed++
	}
	// Also roll every other participation profile's today counters to the
	// current local day so stale today_* values cannot linger after midnight.
	for _, account := range s.accounts {
		if account == nil || account.Participation == nil {
			continue
		}
		beforeDate := account.Participation.TodayStatDate
		beforeJoin := account.Participation.TodayJoinCount
		beforeWin := account.Participation.TodayWinCount
		beforeDiamonds := account.Participation.TodayWinDiamonds
		rollParticipationDayLocked(account.Participation, now)
		if account.Participation.TodayStatDate != beforeDate ||
			account.Participation.TodayJoinCount != beforeJoin ||
			account.Participation.TodayWinCount != beforeWin ||
			account.Participation.TodayWinDiamonds != beforeDiamonds {
			changed++
			account.UpdatedAt = now.Format(time.RFC3339Nano)
		}
	}
	if changed == 0 {
		return 0, nil
	}
	if err := s.saveLocked(); err != nil {
		return 0, err
	}
	return changed, nil
}

// ClearAllParticipationRiskCooldowns clears every participation risk-control
// timer and restores the red-packet API switch so cooled accounts can accept
// tasks again. Challenge blocks are left alone — those need a manual restart
// after the user handles captcha.
func (s *Store) ClearAllParticipationRiskCooldowns() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	changed := 0
	for _, account := range s.accounts {
		if account == nil || account.Participation == nil {
			continue
		}
		profile := account.Participation
		cleared := false
		if strings.TrimSpace(profile.RedPacketCooldownUntil) != "" {
			profile.RedPacketCooldownUntil = ""
			cleared = true
		}
		if profile.LastRedPacketStatus == redPacketStatusRiskControl {
			profile.LastRedPacketStatus = ""
			profile.LastRedPacketMessage = ""
			cleared = true
		}
		// Risk control previously forced the API switch off; turn it back on so
		// “清空冷却” is enough to resume without per-account re-enable clicks.
		if !profile.RedPacketAPIEnabled && cleared {
			profile.RedPacketAPIEnabled = true
		}
		if !cleared {
			continue
		}
		account.UpdatedAt = now.Format(time.RFC3339Nano)
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	if err := s.saveLocked(); err != nil {
		return 0, err
	}
	return changed, nil
}

// RecordRedPacketParticipation stores only safe result metadata and counters.
// Raw response bodies and Cookie values never enter the account view.
func (s *Store) RecordRedPacketParticipation(accountID, status, message string, joined, won bool, cooldown time.Duration, cookieExpired bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil || account.Participation == nil {
		return
	}
	now := time.Now()
	profile := account.Participation
	status = strings.TrimSpace(status)
	message = strings.TrimSpace(message)
	// A later non-risk row (for example a concurrent context-missing attempt
	// after the task already stopped) must not wipe an active risk cooldown.
	// Only challenge_blocked intentionally clears the timer below.
	previousStatus := profile.LastRedPacketStatus
	previousMessage := profile.LastRedPacketMessage
	existingCooldown := profile.RedPacketCooldownUntil
	wasRiskCooling := previousStatus == redPacketStatusRiskControl && activeRedPacketCooldown(existingCooldown, now)

	profile.LastRedPacketStatus = status
	profile.LastRedPacketMessage = message
	if profile.LastRedPacketStatus == redPacketStatusChallengeBlocked {
		profile.RedPacketAPIEnabled = false
		profile.RedPacketCooldownUntil = ""
		existingCooldown = ""
		wasRiskCooling = false
	}
	if profile.LastRedPacketStatus == redPacketStatusRiskControl {
		// Risk control ends the current task and cools the account; keep the
		// API switch off so UI matches “not accepting new joins”.
		profile.RedPacketAPIEnabled = false
	}
	rollParticipationDayLocked(profile, now)
	// Only count true join accepts. Risk-control / failed / network / login /
	// challenge must never inflate “参与成功” even if a caller passes joined.
	if joined && accountParticipationStatusIsSuccessfulJoin(status) {
		profile.JoinCount++
		profile.TodayJoinCount++
		profile.LastJoinAt = now.Format(time.RFC3339Nano)
	}
	if won && (status == "" || status == "won" || !accountParticipationStatusIsFailure(status)) {
		profile.WinCount++
		profile.TodayWinCount++
		if diamonds := parseWinDiamonds(message); diamonds > 0 {
			profile.TodayWinDiamonds += diamonds
		}
	}
	if cooldown > 0 {
		nextUntil := now.Add(cooldown)
		// Keep the later of the new window and any already-running risk window.
		if existing, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(existingCooldown)); err == nil && existing.After(nextUntil) {
			profile.RedPacketCooldownUntil = existing.Format(time.RFC3339Nano)
		} else {
			profile.RedPacketCooldownUntil = nextUntil.Format(time.RFC3339Nano)
		}
	} else if profile.LastRedPacketStatus != redPacketStatusChallengeBlocked {
		// cooldown == 0 means "no additional cool-down from this event", not
		// "clear risk control". Preserve any still-active window.
		if activeRedPacketCooldown(existingCooldown, now) {
			profile.RedPacketCooldownUntil = existingCooldown
		}
		// Keep the risk badge copy until the timer expires unless this event is
		// itself a stronger account-level block (challenge/login).
		if wasRiskCooling && status != redPacketStatusChallengeBlocked && status != "login_expired" {
			profile.LastRedPacketStatus = previousStatus
			profile.LastRedPacketMessage = previousMessage
			profile.RedPacketAPIEnabled = false
		}
	}
	if cookieExpired {
		account.CookieStatus = cookieStatusExpired
		account.CookieMessage = "CK 已失效：红包接口返回未登录"
		account.CookieChecked = now.Format(time.RFC3339Nano)
	}
	account.UpdatedAt = now.Format(time.RFC3339Nano)
	_ = s.saveLocked()
}

// RecordRedPacketDrawResult stores only a definitive personal draw outcome.
// It is separate from join accounting so a pending join never counts as a win.
func (s *Store) RecordRedPacketDrawResult(accountID, message string, won bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil || account.Participation == nil {
		return
	}
	now := time.Now()
	profile := account.Participation
	rollParticipationDayLocked(profile, now)
	profile.LastRedPacketStatus = "not_won"
	if won {
		profile.WinCount++
		profile.TodayWinCount++
		if diamonds := parseWinDiamonds(message); diamonds > 0 {
			profile.TodayWinDiamonds += diamonds
		}
		profile.LastRedPacketStatus = "won"
	}
	profile.LastRedPacketMessage = strings.TrimSpace(message)
	account.UpdatedAt = now.Format(time.RFC3339Nano)
	_ = s.saveLocked()
}

// rollParticipationDayLocked resets today counters when the local calendar day
// changes. Callers must hold s.mu.
func rollParticipationDayLocked(profile *ParticipationProfile, now time.Time) {
	if profile == nil {
		return
	}
	today := now.Format("2006-01-02")
	if profile.TodayStatDate == today {
		return
	}
	profile.TodayStatDate = today
	profile.TodayJoinCount = 0
	profile.TodayWinCount = 0
	profile.TodayWinDiamonds = 0
}

func accountParticipationStatusIsFailure(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "risk_control", "failed", "network_error", "login_expired", "challenge_blocked",
		"context_required", "expired", "pending":
		return true
	default:
		return false
	}
}

func accountParticipationStatusIsSuccessfulJoin(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	if accountParticipationStatusIsFailure(status) {
		return false
	}
	switch status {
	case "joined", "already_joined", "not_won", "won", "draw_error", "":
		return true
	default:
		// Unknown status with joined=true still counts as a successful accept.
		return true
	}
}

// parseWinDiamonds extracts a positive diamond amount from safe award copy such
// as "8钻", "已中8钻", or "已中1.5钻（钱包增量确认）".
func parseWinDiamonds(text string) float64 {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	// Collapse spaces so "1.5 钻" still parses.
	compact := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, text)
	for i := 0; i < len(compact); {
		r, size := utf8.DecodeRuneInString(compact[i:])
		if r == '钻' && i > 0 {
			end := i
			start := end
			dot := false
			for start > 0 {
				prev, psz := utf8.DecodeLastRuneInString(compact[:start])
				if prev >= '0' && prev <= '9' {
					start -= psz
					continue
				}
				if prev == '.' && !dot {
					dot = true
					start -= psz
					continue
				}
				break
			}
			if start < end {
				value, err := strconv.ParseFloat(compact[start:end], 64)
				if err == nil && value > 0 {
					return value
				}
			}
		}
		if size <= 0 {
			break
		}
		i += size
	}
	return 0
}

// MonitoringCredential returns the internal credential used by the local Go
// monitor.  It is deliberately kept separate from AccountView so the raw
// Cookie never crosses the engine IPC boundary into the frontend.
func (s *Store) MonitoringCredential(accountID string) (BrowserCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil {
		return BrowserCredential{}, errors.New("账号不存在")
	}
	if !hasRole(account, RoleMonitoring) {
		return BrowserCredential{}, errors.New("只能使用监测账号启动红包监测")
	}
	if account.Monitoring == nil || !account.Monitoring.Enabled {
		return BrowserCredential{}, errors.New("监测账号已停用")
	}
	if strings.TrimSpace(account.Cookie) == "" {
		return BrowserCredential{}, errors.New("账号没有可用 Cookie，请重新登录或导入")
	}
	return BrowserCredential{
		AccountID:    account.ID,
		AccountName:  firstNonEmpty(account.Nickname, account.Name, account.UserID, "抖音账号"),
		Cookie:       account.Cookie,
		CookieStatus: monitoringCookieStatus(account),
	}, nil
}

// Credential returns the canonical account credential for trusted native
// callers. It intentionally does not require a particular role because CK
// rebinding is shared by monitoring and participation assignments.
func (s *Store) Credential(accountID string) (BrowserCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil {
		return BrowserCredential{}, errors.New("账号不存在")
	}
	return BrowserCredential{
		AccountID:    account.ID,
		AccountName:  firstNonEmpty(account.Nickname, account.Name, account.UserID, "抖音账号"),
		Cookie:       account.Cookie,
		CookieStatus: account.CookieStatus,
		Source:       account.Source,
	}, nil
}

// UpsertAuthenticatedCookie creates a canonical account from a native login,
// or refreshes the existing canonical account when the same Douyin identity
// is already present. Raw Cookie data never leaves the Go store.
func (s *Store) UpsertAuthenticatedCookie(rawCookie, nickname, userID, secUID string, role Role) (AccountView, bool, error) {
	return s.UpsertAuthenticatedCookieWithGroup(rawCookie, nickname, userID, secUID, role, "")
}

func (s *Store) UpsertAuthenticatedCookieWithGroup(rawCookie, nickname, userID, secUID string, role Role, groupID string) (AccountView, bool, error) {
	return s.upsertCookie(rawCookie, nickname, userID, secUID, role, groupID, "qr-login", true)
}

// UpsertImportedCookie stores an explicitly imported Cookie in the selected
// role without claiming it has already passed an online check. The safe view
// never exposes the imported Cookie.
func (s *Store) UpsertImportedCookie(rawCookie, nickname, userID, secUID string, role Role) (AccountView, bool, error) {
	return s.UpsertImportedCookieWithGroup(rawCookie, nickname, userID, secUID, role, "")
}

func (s *Store) UpsertImportedCookieWithGroup(rawCookie, nickname, userID, secUID string, role Role, groupID string) (AccountView, bool, error) {
	return s.upsertCookie(rawCookie, nickname, userID, secUID, role, groupID, "manual-import", false)
}

func (s *Store) upsertCookie(rawCookie, nickname, userID, secUID string, role Role, groupID, source string, authenticated bool) (AccountView, bool, error) {
	rawCookie = strings.TrimSpace(rawCookie)
	if rawCookie == "" {
		return AccountView{}, false, errors.New("没有读取到可导入的 Cookie")
	}
	if role != RoleMonitoring && role != RoleParticipation {
		return AccountView{}, false, errors.New("未知账号分类")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	groupID = strings.TrimSpace(groupID)
	if role == RoleParticipation && groupID != "" && s.participationGroups[groupID] == nil {
		return AccountView{}, false, errors.New("参与账号分组不存在")
	}
	identityIndex := map[string]*Account{}
	for _, item := range s.accounts {
		indexIdentity(identityIndex, item)
	}
	account, existed := findIdentity(identityIndex, rawCookie, userID, secUID)
	if account == nil {
		account = newAccount("", nickname, nickname, userID, secUID, rawCookie, source, "", "")
		s.accounts[account.ID] = account
	} else {
		mergeIdentity(account, nickname, nickname, userID, secUID, rawCookie, source, time.Now().Format(time.RFC3339Nano))
	}
	roleAdded := !hasRole(account, role)
	if roleAdded {
		account.Roles = append(account.Roles, role)
	}
	if role == RoleMonitoring && account.Monitoring == nil {
		account.Monitoring = &MonitoringProfile{Enabled: true}
	}
	if role == RoleParticipation && account.Participation == nil {
		account.Participation = &ParticipationProfile{Enabled: true}
	}
	if role == RoleParticipation {
		account.Participation.GroupID = groupID
	}
	if authenticated {
		account.CookieStatus = cookieStatusValid
		account.CookieMessage = validCookieMessage
		account.CookieChecked = time.Now().Format(time.RFC3339Nano)
		if role == RoleMonitoring {
			account.Monitoring.CookieStatus = cookieStatusValid
			account.Monitoring.CookieMessage = validMonitoringCookieMessage
			account.Monitoring.CookieChecked = account.CookieChecked
			account.Monitoring.LastValidatedAt = account.CookieChecked
		}
	} else if !existed || roleAdded {
		account.CookieStatus = cookieStatusUnknown
		account.CookieMessage = "Cookie 已导入，等待在线校验"
		account.CookieChecked = ""
		if role == RoleMonitoring {
			account.Monitoring.CookieStatus = cookieStatusUnknown
			account.Monitoring.CookieMessage = "Cookie 已导入，等待监测接口校验"
			account.Monitoring.CookieChecked = ""
		}
	}
	account.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	if err := s.saveLocked(); err != nil {
		if !existed {
			delete(s.accounts, account.ID)
		}
		return AccountView{}, false, err
	}
	return safeView(account), !existed, nil
}

// ReplaceCookie stores a freshly authenticated Cookie without exposing it in
// the returned safe view. Validation is performed separately so temporary
// network failures can remain unknown instead of being marked as expired.
func (s *Store) ReplaceCookie(accountID, rawCookie string) (AccountView, error) {
	rawCookie = strings.TrimSpace(rawCookie)
	if rawCookie == "" {
		return AccountView{}, errors.New("登录窗口没有读取到 Cookie")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil {
		return AccountView{}, errors.New("账号不存在")
	}
	account.Cookie = rawCookie
	account.CookieStatus = cookieStatusUnknown
	account.CookieMessage = "CK 已更新，等待校验"
	account.CookieChecked = ""
	if account.Monitoring != nil {
		account.Monitoring.CookieStatus = cookieStatusUnknown
		account.Monitoring.CookieMessage = "CK 已更新，等待监测接口校验"
		account.Monitoring.CookieChecked = ""
		account.Monitoring.LastValidatedAt = ""
	}
	account.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	if err := s.saveLocked(); err != nil {
		return AccountView{}, err
	}
	return safeView(account), nil
}

// SetBrowserLoginState records an explicit login signal observed inside the
// account's native Douyin WebView. This is deliberately separate from the
// online API validator: a visible login dialog is authoritative evidence that
// the browser session is invalid, while a loaded authenticated page confirms
// the same native profile is usable even when an API endpoint is throttled.
//
// promoteNativeSurface must only be true for an explicit rebind/scan completion.
// Card cookie polling must never promote manual-import accounts — that silently
// switches them onto the embedded WebView path and can freeze macOS when the
// card and instance window fight over one account data store.
func (s *Store) SetBrowserLoginState(accountID string, loggedIn bool) (AccountView, error) {
	return s.SetBrowserLoginStateWithPromotion(accountID, loggedIn, false)
}

func (s *Store) SetBrowserLoginStateWithPromotion(accountID string, loggedIn, promoteNativeSurface bool) (AccountView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil {
		return AccountView{}, errors.New("账号不存在")
	}
	if loggedIn {
		account.CookieStatus = cookieStatusValid
		account.CookieMessage = "浏览器实例登录状态正常"
		if promoteNativeSurface && strings.TrimSpace(account.Source) == "manual-import" {
			account.Source = "native-rebind"
		}
	} else {
		account.CookieStatus = cookieStatusExpired
		account.CookieMessage = "CK 已失效：浏览器实例未登录"
	}
	account.CookieChecked = time.Now().Format(time.RFC3339Nano)
	account.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	if err := s.saveLocked(); err != nil {
		return AccountView{}, err
	}
	return safeView(account), nil
}

func (s *Store) AddRole(accountID string, role Role) (AccountView, error) {
	if role != RoleMonitoring && role != RoleParticipation {
		return AccountView{}, errors.New("未知账号分类")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil {
		return AccountView{}, errors.New("账号不存在")
	}
	if !hasRole(account, role) {
		account.Roles = append(account.Roles, role)
		if role == RoleMonitoring {
			account.Monitoring = &MonitoringProfile{
				Enabled:       true,
				CookieStatus:  cookieStatusUnknown,
				CookieMessage: "等待监测接口校验",
			}
		} else {
			account.Participation = &ParticipationProfile{Enabled: true}
		}
		account.UpdatedAt = time.Now().Format(time.RFC3339Nano)
		if err := s.saveLocked(); err != nil {
			return AccountView{}, err
		}
	}
	return safeView(account), nil
}

func (s *Store) RemoveRole(accountID string, role Role) (AccountView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil {
		return AccountView{}, errors.New("账号不存在")
	}
	if len(account.Roles) <= 1 && hasRole(account, role) {
		return AccountView{}, errors.New("账号至少需要保留一个分类")
	}
	filtered := account.Roles[:0]
	for _, item := range account.Roles {
		if item != role {
			filtered = append(filtered, item)
		}
	}
	account.Roles = filtered
	if role == RoleMonitoring {
		account.Monitoring = nil
	} else if role == RoleParticipation {
		account.Participation = nil
	}
	account.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	if err := s.saveLocked(); err != nil {
		return AccountView{}, err
	}
	return safeView(account), nil
}

func (s *Store) Delete(accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil {
		return errors.New("账号不存在")
	}
	delete(s.accounts, accountID)
	if err := s.saveLocked(); err != nil {
		s.accounts[accountID] = account
		return err
	}
	return nil
}

func (s *Store) MigrateLegacy(legacyDir string) (MigrationResult, error) {
	if strings.TrimSpace(legacyDir) == "" {
		legacyDir = defaultLegacyDataDir()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var monitor legacyMonitorFile
	if err := readOptionalJSON(filepath.Join(legacyDir, "douyin_accounts.json"), &monitor); err != nil {
		return MigrationResult{}, err
	}
	var participation legacyParticipationFile
	if err := readOptionalJSON(filepath.Join(legacyDir, "lottery_accounts.json"), &participation); err != nil {
		return MigrationResult{}, err
	}
	if len(monitor.Accounts) == 0 && len(participation.Accounts) == 0 {
		return MigrationResult{}, errors.New("未找到 DY-KIRO 账号数据")
	}

	result := MigrationResult{}
	identityIndex := map[string]*Account{}
	for _, account := range s.accounts {
		for _, key := range identityKeys(account.Cookie, account.UserID, account.SecUID) {
			identityIndex[key] = account
		}
	}

	for _, old := range monitor.Accounts {
		if strings.TrimSpace(old.Cookie) == "" {
			continue
		}
		account, existed := findIdentity(identityIndex, old.Cookie, old.UserID, old.SecUID)
		if account == nil {
			account = newAccount(old.AccountID, old.Name, old.Nickname, old.UserID, old.SecUID, old.Cookie, old.Source, old.CreatedAt, old.UpdatedAt)
			s.accounts[account.ID] = account
			result.Imported++
		} else if existed {
			result.Merged++
		}
		if !hasRole(account, RoleMonitoring) {
			account.Roles = append(account.Roles, RoleMonitoring)
			result.MonitoringAssignments++
		}
		account.Monitoring = &MonitoringProfile{
			Enabled:         old.Enabled,
			LastValidatedAt: old.LastValidatedAt,
			LastError:       old.LastError,
			// Monitoring counters are intentionally local-only. Importing is
			// copy-only and never carries the old application's activity data.
			TotalRequestCount: 0,
			TodayRequestCount: 0,
		}
		mergeIdentity(account, old.Name, old.Nickname, old.UserID, old.SecUID, old.Cookie, old.Source, old.UpdatedAt)
		indexIdentity(identityIndex, account)
	}

	for _, old := range participation.Accounts {
		if strings.TrimSpace(old.Cookie) == "" {
			continue
		}
		account, existed := findIdentity(identityIndex, old.Cookie, old.UserID, old.SecUID)
		if account == nil {
			account = newAccount(old.AccountID, old.Name, old.Nickname, old.UserID, old.SecUID, old.Cookie, old.Source, old.CreatedAt, old.UpdatedAt)
			s.accounts[account.ID] = account
			result.Imported++
		} else if existed {
			result.Merged++
		}
		if !hasRole(account, RoleParticipation) {
			account.Roles = append(account.Roles, RoleParticipation)
			result.ParticipationAssignments++
		}
		account.Participation = &ParticipationProfile{
			Enabled:              old.Enabled,
			JoinCount:            old.JoinCount,
			WinCount:             old.WinCount,
			LastJoinAt:           old.LastJoinAt,
			LastError:            old.LastError,
			ProxyID:              old.ProxyID,
			FingerprintProfileID: old.FingerprintProfileID,
			Tags:                 append([]string(nil), old.Tags...),
		}
		mergeIdentity(account, old.Name, old.Nickname, old.UserID, old.SecUID, old.Cookie, old.Source, old.UpdatedAt)
		indexIdentity(identityIndex, account)
	}

	result.Total = len(s.accounts)
	if err := s.saveLocked(); err != nil {
		return MigrationResult{}, err
	}
	return result, nil
}

func safeView(account *Account) AccountView {
	var monitoring *MonitoringProfile
	if account.Monitoring != nil {
		copy := *account.Monitoring
		monitoring = &copy
	}
	var participation *ParticipationProfile
	if account.Participation != nil {
		copy := *account.Participation
		copy.Tags = append([]string(nil), account.Participation.Tags...)
		participation = &copy
	}
	return AccountView{
		ID:            account.ID,
		Name:          account.Name,
		Nickname:      account.Nickname,
		UserID:        account.UserID,
		CookieStatus:  account.CookieStatus,
		CookieMessage: account.CookieMessage,
		CookieChecked: account.CookieChecked,
		Source:        account.Source,
		Roles:         append([]Role(nil), account.Roles...),
		Monitoring:    monitoring,
		Participation: participation,
		CreatedAt:     account.CreatedAt,
		UpdatedAt:     account.UpdatedAt,
	}
}

func safeViewForRole(account *Account, role Role) AccountView {
	view := safeView(account)
	if role == RoleMonitoring && account.Monitoring != nil {
		view.CookieStatus = monitoringCookieStatus(account)
		view.CookieMessage = account.Monitoring.CookieMessage
		view.CookieChecked = account.Monitoring.CookieChecked
	}
	return view
}

func monitoringCookieStatus(account *Account) string {
	if account == nil || account.Monitoring == nil {
		return cookieStatusUnknown
	}
	if status := strings.TrimSpace(account.Monitoring.CookieStatus); status != "" {
		return status
	}
	return cookieStatusUnknown
}

func normalizeRoles(account *Account) {
	roles := make([]Role, 0, 2)
	if account.Monitoring != nil || hasRole(account, RoleMonitoring) {
		roles = append(roles, RoleMonitoring)
	}
	if account.Participation != nil || hasRole(account, RoleParticipation) {
		roles = append(roles, RoleParticipation)
	}
	account.Roles = roles
}

func hasRole(account *Account, role Role) bool {
	for _, item := range account.Roles {
		if item == role {
			return true
		}
	}
	return false
}

func newAccount(id, name, nickname, userID, secUID, cookie, source, createdAt, updatedAt string) *Account {
	now := time.Now().Format(time.RFC3339Nano)
	if strings.TrimSpace(id) == "" {
		sum := sha256.Sum256([]byte(cookie + now))
		id = hex.EncodeToString(sum[:16])
	}
	if strings.TrimSpace(name) == "" {
		name = firstNonEmpty(nickname, userID, "抖音账号")
	}
	return &Account{
		ID:        id,
		Name:      name,
		Nickname:  nickname,
		UserID:    userID,
		SecUID:    secUID,
		Cookie:    cookie,
		Source:    source,
		Roles:     []Role{},
		CreatedAt: firstNonEmpty(createdAt, now),
		UpdatedAt: firstNonEmpty(updatedAt, now),
	}
}

func mergeIdentity(account *Account, name, nickname, userID, secUID, cookie, source, updatedAt string) {
	account.Name = firstNonEmpty(nickname, name, account.Name)
	account.Nickname = firstNonEmpty(nickname, account.Nickname)
	account.UserID = firstNonEmpty(userID, account.UserID)
	account.SecUID = firstNonEmpty(secUID, account.SecUID)
	if nextCookie := strings.TrimSpace(cookie); nextCookie != "" && nextCookie != strings.TrimSpace(account.Cookie) {
		account.Cookie = cookie
		account.CookieStatus = "unknown"
		account.CookieMessage = "Cookie 已更新，等待在线校验"
		account.CookieChecked = ""
		if account.Monitoring != nil {
			account.Monitoring.CookieStatus = cookieStatusUnknown
			account.Monitoring.CookieMessage = "Cookie 已更新，等待监测接口校验"
			account.Monitoring.CookieChecked = ""
			account.Monitoring.LastValidatedAt = ""
		}
	}
	account.Source = firstNonEmpty(source, account.Source)
	account.UpdatedAt = firstNonEmpty(updatedAt, account.UpdatedAt, time.Now().Format(time.RFC3339Nano))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func findIdentity(index map[string]*Account, cookie, userID, secUID string) (*Account, bool) {
	for _, key := range identityKeys(cookie, userID, secUID) {
		if account := index[key]; account != nil {
			return account, true
		}
	}
	return nil, false
}

func indexIdentity(index map[string]*Account, account *Account) {
	for _, key := range identityKeys(account.Cookie, account.UserID, account.SecUID) {
		index[key] = account
	}
}

func identityKeys(cookie, userID, secUID string) []string {
	keys := make([]string, 0, 3)
	if session := cookieValue(cookie, "sessionid_ss", "sessionid", "sid_guard"); session != "" {
		sum := sha256.Sum256([]byte(session))
		keys = append(keys, "session:"+hex.EncodeToString(sum[:]))
	}
	if strings.TrimSpace(userID) != "" {
		keys = append(keys, "user:"+strings.TrimSpace(userID))
	}
	if strings.TrimSpace(secUID) != "" {
		keys = append(keys, "sec:"+strings.TrimSpace(secUID))
	}
	return keys
}

func cookieValue(cookie string, names ...string) string {
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	for _, part := range strings.Split(cookie, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && wanted[key] && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func readOptionalJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取旧账号文件失败: %w", err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("解析旧账号文件失败: %w", err)
	}
	return nil
}

func defaultLegacyDataDir() string {
	if override := strings.TrimSpace(os.Getenv("FUBAO_LEGACY_DATA_DIR")); override != "" {
		return override
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, "Library", "Application Support", "DouyinFubaoMonitor", "data"),
		filepath.Join(os.Getenv("APPDATA"), "DouyinFubaoMonitor", "data"),
		filepath.Join(home, ".local", "share", "DouyinFubaoMonitor", "data"),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return candidates[0]
}

type legacyMonitorFile struct {
	Accounts []legacyMonitorAccount `json:"accounts"`
}

type legacyMonitorAccount struct {
	AccountID         string `json:"account_id"`
	Name              string `json:"name"`
	Cookie            string `json:"cookie"`
	Nickname          string `json:"nickname"`
	UserID            string `json:"user_id"`
	SecUID            string `json:"sec_uid"`
	Enabled           bool   `json:"enabled"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
	LastValidatedAt   string `json:"last_validated_at"`
	LastError         string `json:"last_error"`
	LastUsedAt        string `json:"last_used_at"`
	LastUseStatus     string `json:"last_use_status"`
	LastUseMessage    string `json:"last_use_message"`
	TotalRequestCount int    `json:"total_request_count"`
	TodayRequestCount int    `json:"today_request_count"`
	Source            string `json:"source"`
}

type legacyParticipationFile struct {
	Accounts []legacyParticipationAccount `json:"accounts"`
}

type legacyParticipationAccount struct {
	AccountID            string   `json:"account_id"`
	Name                 string   `json:"name"`
	Cookie               string   `json:"cookie"`
	Nickname             string   `json:"nickname"`
	UserID               string   `json:"user_id"`
	SecUID               string   `json:"sec_uid"`
	Enabled              bool     `json:"enabled"`
	Source               string   `json:"source"`
	JoinCount            int      `json:"join_count"`
	WinCount             int      `json:"win_count"`
	LastJoinAt           string   `json:"last_join_at"`
	LastError            string   `json:"last_error"`
	ProxyID              int      `json:"proxy_id"`
	FingerprintProfileID int      `json:"fingerprint_profile_id"`
	Tags                 []string `json:"tags"`
	CreatedAt            string   `json:"created_at"`
	UpdatedAt            string   `json:"updated_at"`
}
