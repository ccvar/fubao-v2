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
	"strings"
	"sync"
	"time"
)

// storeVersion 3 adds the opt-in API red-packet participation state. Existing
// accounts intentionally default to off; participation must always be an
// explicit user choice in this client.
// The legacy app's counters describe a different runtime, so carrying them
// across makes the current desktop numbers misleading.
const storeVersion = 3

type Role string

const (
	RoleMonitoring    Role = "monitoring"
	RoleParticipation Role = "participation"
)

type MonitoringProfile struct {
	Enabled           bool   `json:"enabled"`
	CookieStatus      string `json:"cookie_status,omitempty"`
	CookieMessage     string `json:"cookie_message,omitempty"`
	CookieChecked     string `json:"cookie_checked_at,omitempty"`
	LastValidatedAt   string `json:"last_validated_at,omitempty"`
	LastError         string `json:"last_error,omitempty"`
	LastUsedAt        string `json:"last_used_at,omitempty"`
	LastUseStatus     string `json:"last_use_status,omitempty"`
	LastUseMessage    string `json:"last_use_message,omitempty"`
	TotalRequestCount int    `json:"total_request_count"`
	TodayRequestCount int    `json:"today_request_count"`
	TodayRequestDate  string `json:"today_request_date,omitempty"`
}

type ParticipationProfile struct {
	Enabled                bool     `json:"enabled"`
	RedPacketAPIEnabled    bool     `json:"red_packet_api_enabled"`
	RedPacketCooldownUntil string   `json:"red_packet_cooldown_until,omitempty"`
	LastRedPacketStatus    string   `json:"last_red_packet_status,omitempty"`
	LastRedPacketMessage   string   `json:"last_red_packet_message,omitempty"`
	JoinCount              int      `json:"join_count"`
	WinCount               int      `json:"win_count"`
	LastJoinAt             string   `json:"last_join_at,omitempty"`
	LastError              string   `json:"last_error,omitempty"`
	ProxyID                int      `json:"proxy_id"`
	FingerprintProfileID   int      `json:"fingerprint_profile_id"`
	Tags                   []string `json:"tags,omitempty"`
}

// RedPacketParticipationCredential is private engine-only data used by the Go
// scheduler. It is never returned by an IPC list API.
type RedPacketParticipationCredential struct {
	AccountID   string
	AccountName string
	Cookie      string
}

type Account struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Nickname      string                `json:"nickname,omitempty"`
	UserID        string                `json:"user_id,omitempty"`
	SecUID        string                `json:"sec_uid,omitempty"`
	Cookie        string                `json:"cookie"`
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

type accountFile struct {
	Version  int        `json:"version"`
	Accounts []*Account `json:"accounts"`
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
}

type Store struct {
	mu                            sync.Mutex
	path                          string
	accounts                      map[string]*Account
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
		path:     filepath.Join(dataDir, "accounts.json"),
		accounts: map[string]*Account{},
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
	payload, err := json.MarshalIndent(accountFile{Version: storeVersion, Accounts: items}, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化账号数据失败: %w", err)
	}
	tempPath := s.path + ".tmp"
	if err := os.WriteFile(tempPath, payload, 0o600); err != nil {
		return fmt.Errorf("写入账号临时文件失败: %w", err)
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("设置账号文件权限失败: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("保存账号数据失败: %w", err)
	}
	return nil
}

func (s *Store) List(role Role) []AccountView {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]AccountView, 0, len(s.accounts))
	for _, account := range s.accounts {
		if role != "" && !hasRole(account, role) {
			continue
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
		AccountID:    account.ID,
		AccountName:  firstNonEmpty(account.Nickname, account.Name, account.UserID, "抖音账号"),
		Cookie:       account.Cookie,
		CookieStatus: account.CookieStatus,
	}, nil
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
	account.Participation.RedPacketAPIEnabled = enabled
	if !enabled {
		account.Participation.RedPacketCooldownUntil = ""
		account.Participation.LastRedPacketStatus = "disabled"
		account.Participation.LastRedPacketMessage = "已关闭红包接口参与"
	} else {
		account.Participation.LastRedPacketStatus = "ready"
		account.Participation.LastRedPacketMessage = "已启用红包接口参与"
	}
	account.UpdatedAt = time.Now().Format(time.RFC3339Nano)
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
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AccountName < items[j].AccountName })
	return items
}

// RecordRedPacketParticipation stores only safe result metadata and counters.
// Raw response bodies and Cookie values never enter the account view.
func (s *Store) RecordRedPacketParticipation(accountID, status, message string, joined bool, cooldown time.Duration, cookieExpired bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil || account.Participation == nil {
		return
	}
	now := time.Now()
	profile := account.Participation
	profile.LastRedPacketStatus = strings.TrimSpace(status)
	profile.LastRedPacketMessage = strings.TrimSpace(message)
	if joined {
		profile.JoinCount++
		profile.LastJoinAt = now.Format(time.RFC3339Nano)
	}
	if cooldown > 0 {
		profile.RedPacketCooldownUntil = now.Add(cooldown).Format(time.RFC3339Nano)
	} else {
		profile.RedPacketCooldownUntil = ""
	}
	if cookieExpired {
		account.CookieStatus = cookieStatusExpired
		account.CookieMessage = "CK 已失效：红包接口返回未登录"
		account.CookieChecked = now.Format(time.RFC3339Nano)
	}
	account.UpdatedAt = now.Format(time.RFC3339Nano)
	_ = s.saveLocked()
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
	}, nil
}

// UpsertAuthenticatedCookie creates a canonical account from a native login,
// or refreshes the existing canonical account when the same Douyin identity
// is already present. Raw Cookie data never leaves the Go store.
func (s *Store) UpsertAuthenticatedCookie(rawCookie, nickname, userID, secUID string, role Role) (AccountView, bool, error) {
	rawCookie = strings.TrimSpace(rawCookie)
	if rawCookie == "" {
		return AccountView{}, false, errors.New("登录窗口没有读取到 Cookie")
	}
	if role != RoleMonitoring && role != RoleParticipation {
		return AccountView{}, false, errors.New("未知账号分类")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	identityIndex := map[string]*Account{}
	for _, item := range s.accounts {
		indexIdentity(identityIndex, item)
	}
	account, existed := findIdentity(identityIndex, rawCookie, userID, secUID)
	if account == nil {
		account = newAccount("", nickname, nickname, userID, secUID, rawCookie, "qr-login", "", "")
		s.accounts[account.ID] = account
	} else {
		mergeIdentity(account, nickname, nickname, userID, secUID, rawCookie, "qr-login", time.Now().Format(time.RFC3339Nano))
	}
	if !hasRole(account, role) {
		account.Roles = append(account.Roles, role)
	}
	if role == RoleMonitoring && account.Monitoring == nil {
		account.Monitoring = &MonitoringProfile{Enabled: true}
	}
	if role == RoleParticipation && account.Participation == nil {
		account.Participation = &ParticipationProfile{Enabled: true}
	}
	account.CookieStatus = cookieStatusValid
	account.CookieMessage = validCookieMessage
	account.CookieChecked = time.Now().Format(time.RFC3339Nano)
	if role == RoleMonitoring {
		account.Monitoring.CookieStatus = cookieStatusValid
		account.Monitoring.CookieMessage = validMonitoringCookieMessage
		account.Monitoring.CookieChecked = account.CookieChecked
		account.Monitoring.LastValidatedAt = account.CookieChecked
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
func (s *Store) SetBrowserLoginState(accountID string, loggedIn bool) (AccountView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[accountID]
	if account == nil {
		return AccountView{}, errors.New("账号不存在")
	}
	if loggedIn {
		account.CookieStatus = cookieStatusValid
		account.CookieMessage = "浏览器实例登录状态正常"
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
