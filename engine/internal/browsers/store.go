package browsers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const storeVersion = 1

type Status string

type RuntimeState string

const (
	StatusStopped Status = "stopped"
	StatusOnline  Status = "online"

	RuntimeStopped RuntimeState = "stopped"
	RuntimeWaiting RuntimeState = "waiting"
	RuntimeRunning RuntimeState = "running"
)

type Instance struct {
	ID                  string       `json:"id"`
	Name                string       `json:"name"`
	AccountID           string       `json:"account_id"`
	AccountName         string       `json:"account_name"`
	Status              Status       `json:"status"`
	Browser             string       `json:"browser"`
	CreatedAt           string       `json:"created_at"`
	UpdatedAt           string       `json:"updated_at"`
	OpenedAt            string       `json:"opened_at,omitempty"`
	CredentialUpdatedAt string       `json:"credential_updated_at,omitempty"`
	RuntimeState        RuntimeState `json:"runtime_state,omitempty"`
	QueuePosition       int          `json:"queue_position,omitempty"`
	PID                 int          `json:"-"`
}

type Admission struct {
	Granted       bool         `json:"granted"`
	State         RuntimeState `json:"state"`
	QueuePosition int          `json:"queue_position,omitempty"`
	Capacity      Capacity     `json:"capacity"`
}

type runtimeLease struct {
	embedded bool
	external bool
	touched  time.Time
}

type queueEntry struct {
	enqueuedAt time.Time
	lastSeen   time.Time
}

type instanceFile struct {
	Version   int         `json:"version"`
	Instances []*Instance `json:"instances"`
}

type Store struct {
	mu            sync.Mutex
	path          string
	profiles      string
	instances     map[string]*Instance
	cookieUpdater func(accountID, rawCookie string) error
	syncEndpoints map[string]*cookieSyncEndpoint
	runtimeLeases map[string]*runtimeLease
	runtimeQueue  map[string]*queueEntry
	resourceProbe func() ResourceSnapshot
	now           func() time.Time
	resourceCache ResourceSnapshot
	resourceAt    time.Time
}

type cookieSyncEndpoint struct {
	URL    string
	Token  string
	server *http.Server
}

func NewStore(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("浏览器实例数据目录为空")
	}
	profiles := filepath.Join(dataDir, "browser-profiles")
	if err := os.MkdirAll(profiles, 0o700); err != nil {
		return nil, fmt.Errorf("创建浏览器配置目录失败: %w", err)
	}
	store := &Store{
		path:          filepath.Join(dataDir, "browser-instances.json"),
		profiles:      profiles,
		instances:     map[string]*Instance{},
		syncEndpoints: map[string]*cookieSyncEndpoint{},
		runtimeLeases: map[string]*runtimeLease{},
		runtimeQueue:  map[string]*queueEntry{},
		resourceProbe: detectResources,
		now:           time.Now,
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) SetCookieUpdater(updater func(accountID, rawCookie string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cookieUpdater = updater
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取浏览器实例失败: %w", err)
	}
	var file instanceFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("解析浏览器实例失败: %w", err)
	}
	accountInstances := map[string]*Instance{}
	changed := false
	for _, instance := range file.Instances {
		if instance == nil || instance.ID == "" {
			continue
		}
		accountID := strings.TrimSpace(instance.AccountID)
		if accountID == "" {
			changed = true
			continue
		}
		// One canonical participation account owns one browser instance. Older
		// prototype builds could persist duplicates; retain the oldest instance
		// so its profile and stable identifier continue to be reused.
		if current := accountInstances[accountID]; current != nil {
			changed = true
			continue
		}
		// A process from a previous engine session cannot be trusted as online.
		instance.Status = StatusStopped
		instance.RuntimeState = ""
		instance.QueuePosition = 0
		instance.PID = 0
		s.instances[instance.ID] = instance
		accountInstances[accountID] = instance
	}
	if changed {
		return s.saveLocked()
	}
	return nil
}

func (s *Store) saveLocked() error {
	items := make([]*Instance, 0, len(s.instances))
	for _, instance := range s.instances {
		items = append(items, instance)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt < items[j].CreatedAt
	})
	payload, err := json.MarshalIndent(instanceFile{Version: storeVersion, Instances: items}, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化浏览器实例失败: %w", err)
	}
	tempPath := s.path + ".tmp"
	if err := os.WriteFile(tempPath, payload, 0o600); err != nil {
		return fmt.Errorf("写入浏览器实例失败: %w", err)
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("设置浏览器实例文件权限失败: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("保存浏览器实例失败: %w", err)
	}
	return nil
}

func (s *Store) List() []Instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneRuntimeLocked()
	changed := false
	items := make([]Instance, 0, len(s.instances))
	for _, instance := range s.instances {
		shouldProbe := instance.Status == StatusOnline
		if !shouldProbe && instance.OpenedAt != "" {
			if openedAt, err := time.Parse(time.RFC3339Nano, instance.OpenedAt); err == nil {
				shouldProbe = time.Since(openedAt) < 10*time.Second
			}
		}
		active := shouldProbe && browserSessionActive(instance)
		nextStatus := StatusStopped
		if active {
			nextStatus = StatusOnline
		}
		if instance.Status != nextStatus {
			instance.Status = nextStatus
			if !active {
				instance.PID = 0
				s.releaseExternalLocked(instance.ID)
			}
			instance.UpdatedAt = time.Now().Format(time.RFC3339Nano)
			changed = true
		}
		items = append(items, s.decorateInstanceLocked(instance))
	}
	if changed {
		_ = s.saveLocked()
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt < items[j].CreatedAt
	})
	return items
}

func (s *Store) Capacity() Capacity {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneRuntimeLocked()
	return s.capacityLocked()
}

func (s *Store) AcquireEmbedded(instanceID string) (Admission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.instances[instanceID] == nil {
		return Admission{}, errors.New("浏览器实例不存在")
	}
	return s.acquireRuntimeLocked(instanceID, false), nil
}

func (s *Store) ReleaseEmbedded(instanceID string) (Capacity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.instances[instanceID] == nil {
		return Capacity{}, errors.New("浏览器实例不存在")
	}
	if lease := s.runtimeLeases[instanceID]; lease != nil {
		lease.embedded = false
		if !lease.external {
			delete(s.runtimeLeases, instanceID)
		}
	}
	delete(s.runtimeQueue, instanceID)
	return s.capacityLocked(), nil
}

func (s *Store) Create(accountID, accountName, requestedName string) (Instance, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return Instance{}, errors.New("请选择参与账号")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, instance := range s.instances {
		if instance.AccountID == accountID {
			// Creation is idempotent: selecting the same account always reuses its
			// existing instance instead of creating a second login container.
			return s.decorateInstanceLocked(instance), nil
		}
	}
	id, err := randomID()
	if err != nil {
		return Instance{}, fmt.Errorf("生成实例编号失败: %w", err)
	}
	now := time.Now().Format(time.RFC3339Nano)
	name := strings.TrimSpace(requestedName)
	if name == "" {
		name = accountName
	}
	instance := &Instance{
		ID:          id,
		Name:        name,
		AccountID:   accountID,
		AccountName: accountName,
		Status:      StatusStopped,
		Browser:     browserLabel(),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	profileDir := s.profileDir(accountID)
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return Instance{}, fmt.Errorf("创建实例配置目录失败: %w", err)
	}
	s.instances[id] = instance
	if err := s.saveLocked(); err != nil {
		delete(s.instances, id)
		_ = os.RemoveAll(profileDir)
		return Instance{}, err
	}
	return s.decorateInstanceLocked(instance), nil
}

func (s *Store) Open(instanceID, cookie string) (Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneRuntimeLocked()
	instance := s.instances[instanceID]
	if instance == nil {
		return Instance{}, errors.New("浏览器实例不存在")
	}
	if strings.TrimSpace(cookie) == "" {
		return Instance{}, errors.New("参与账号没有可用 Cookie")
	}
	if browserSessionActive(instance) {
		lease := s.runtimeLeases[instanceID]
		if lease == nil {
			lease = &runtimeLease{}
			s.runtimeLeases[instanceID] = lease
		}
		lease.external = true
		lease.touched = s.now()
		return s.decorateInstanceLocked(instance), nil
	}
	admission := s.acquireRuntimeLocked(instanceID, true)
	if !admission.Granted {
		return s.decorateInstanceLocked(instance), fmt.Errorf("当前设备安全并发为 %d，实例已进入等待队列（第 %d 位）", admission.Capacity.EffectiveLimit, admission.QueuePosition)
	}
	chrome, err := findChrome()
	if err != nil {
		s.releaseExternalLocked(instanceID)
		return Instance{}, err
	}
	profileDir := s.profileDir(instance.AccountID)
	extensionDir := filepath.Join(profileDir, "fubao-cookie-sync")
	endpoint, err := s.ensureCookieSyncEndpointLocked(instance)
	if err != nil {
		s.releaseExternalLocked(instanceID)
		return Instance{}, err
	}
	if err := writeCookieExtension(extensionDir, cookie, endpoint.URL, endpoint.Token); err != nil {
		s.releaseExternalLocked(instanceID)
		return Instance{}, err
	}
	args := []string{
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-mode",
		"--disable-background-networking",
		"--remote-debugging-port=" + strconv.Itoa(PortHint(instance.ID)),
		"--disable-extensions-except=" + extensionDir,
		"--load-extension=" + extensionDir,
		"--app=https://www.douyin.com/",
		"--window-size=960,760",
	}
	command := exec.Command(chrome, args...)
	if err := command.Start(); err != nil {
		s.releaseExternalLocked(instanceID)
		return Instance{}, fmt.Errorf("启动浏览器失败: %w", err)
	}
	now := time.Now().Format(time.RFC3339Nano)
	instance.Status = StatusOnline
	instance.OpenedAt = now
	instance.UpdatedAt = now
	instance.PID = command.Process.Pid
	if err := s.saveLocked(); err != nil {
		_ = command.Process.Kill()
		s.releaseExternalLocked(instanceID)
		return Instance{}, err
	}
	go func() {
		_ = command.Wait()
		s.markStopped(instanceID)
	}()
	return s.decorateInstanceLocked(instance), nil
}

// Close removes the instance registration and stops any running browser while
// deliberately preserving the account-keyed profile directory. Recreating an
// instance for the same account therefore resumes the same isolated session.
func (s *Store) Close(instanceID string) (Instance, error) {
	s.mu.Lock()
	instance := s.instances[instanceID]
	if instance == nil {
		s.mu.Unlock()
		return Instance{}, errors.New("浏览器实例不存在")
	}
	closed := *instance
	endpoint := s.syncEndpoints[instanceID]
	lease := s.runtimeLeases[instanceID]
	queue := s.runtimeQueue[instanceID]
	delete(s.instances, instanceID)
	delete(s.syncEndpoints, instanceID)
	delete(s.runtimeLeases, instanceID)
	delete(s.runtimeQueue, instanceID)
	if err := s.saveLocked(); err != nil {
		s.instances[instanceID] = instance
		if endpoint != nil {
			s.syncEndpoints[instanceID] = endpoint
		}
		if lease != nil {
			s.runtimeLeases[instanceID] = lease
		}
		if queue != nil {
			s.runtimeQueue[instanceID] = queue
		}
		s.mu.Unlock()
		return Instance{}, err
	}
	s.mu.Unlock()

	if closed.PID > 0 {
		if process, err := os.FindProcess(closed.PID); err == nil {
			_ = process.Kill()
		}
	}
	if endpoint != nil && endpoint.server != nil {
		_ = endpoint.server.Close()
	}
	return closed, nil
}

func (s *Store) ensureCookieSyncEndpointLocked(instance *Instance) (*cookieSyncEndpoint, error) {
	if endpoint := s.syncEndpoints[instance.ID]; endpoint != nil {
		return endpoint, nil
	}
	if s.cookieUpdater == nil {
		return nil, errors.New("账号 CK 回传服务尚未就绪")
	}
	token, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("生成 CK 回传令牌失败: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("启动 CK 回传服务失败: %w", err)
	}
	endpoint := &cookieSyncEndpoint{
		URL:   "http://" + listener.Addr().String() + "/cookie",
		Token: token,
	}
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("X-Fubao-Token") != token {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		defer request.Body.Close()
		var payload struct {
			Cookies []browserCookie `json:"cookies"`
		}
		decoder := json.NewDecoder(io.LimitReader(request.Body, 2<<20))
		if err := decoder.Decode(&payload); err != nil {
			http.Error(response, "invalid cookie payload", http.StatusBadRequest)
			return
		}
		rawCookie := serializeBrowserCookies(payload.Cookies)
		if !hasBrowserLoginCookie(payload.Cookies) || rawCookie == "" {
			http.Error(response, "login cookie not found", http.StatusUnprocessableEntity)
			return
		}
		if err := s.cookieUpdater(instance.AccountID, rawCookie); err != nil {
			http.Error(response, "cookie update failed", http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		if current := s.instances[instance.ID]; current != nil {
			current.CredentialUpdatedAt = time.Now().Format(time.RFC3339Nano)
			current.UpdatedAt = current.CredentialUpdatedAt
			_ = s.saveLocked()
		}
		s.mu.Unlock()
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"ok":true}`))
	})
	endpoint.server = &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second}
	s.syncEndpoints[instance.ID] = endpoint
	go func() {
		_ = endpoint.server.Serve(listener)
	}()
	return endpoint, nil
}

func (s *Store) AccountID(instanceID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	instance := s.instances[instanceID]
	if instance == nil {
		return "", errors.New("浏览器实例不存在")
	}
	return instance.AccountID, nil
}

func (s *Store) markStopped(instanceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	instance := s.instances[instanceID]
	if instance == nil {
		return
	}
	instance.Status = StatusStopped
	instance.PID = 0
	s.releaseExternalLocked(instanceID)
	instance.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	_ = s.saveLocked()
}

func (s *Store) acquireRuntimeLocked(instanceID string, external bool) Admission {
	s.pruneRuntimeLocked()
	now := s.now()
	if lease := s.runtimeLeases[instanceID]; lease != nil {
		if external {
			lease.external = true
		} else {
			lease.embedded = true
		}
		lease.touched = now
		delete(s.runtimeQueue, instanceID)
		return Admission{Granted: true, State: RuntimeRunning, Capacity: s.capacityLocked()}
	}
	entry := s.runtimeQueue[instanceID]
	if entry == nil {
		entry = &queueEntry{enqueuedAt: now}
		s.runtimeQueue[instanceID] = entry
	}
	entry.lastSeen = now
	capacity := s.capacityLocked()
	position := s.queuePositionLocked(instanceID)
	if capacity.AvailableSlots > 0 && position == 1 {
		delete(s.runtimeQueue, instanceID)
		s.runtimeLeases[instanceID] = &runtimeLease{embedded: !external, external: external, touched: now}
		return Admission{Granted: true, State: RuntimeRunning, Capacity: s.capacityLocked()}
	}
	return Admission{Granted: false, State: RuntimeWaiting, QueuePosition: position, Capacity: capacity}
}

func (s *Store) releaseExternalLocked(instanceID string) {
	if lease := s.runtimeLeases[instanceID]; lease != nil {
		lease.external = false
		if !lease.embedded {
			delete(s.runtimeLeases, instanceID)
		}
	}
}

func (s *Store) pruneRuntimeLocked() {
	now := s.now()
	for instanceID, lease := range s.runtimeLeases {
		if s.instances[instanceID] == nil {
			delete(s.runtimeLeases, instanceID)
			continue
		}
		if lease.embedded && !lease.external && now.Sub(lease.touched) > 8*time.Second {
			delete(s.runtimeLeases, instanceID)
		}
	}
	for instanceID, entry := range s.runtimeQueue {
		if s.instances[instanceID] == nil || now.Sub(entry.lastSeen) > 8*time.Second {
			delete(s.runtimeQueue, instanceID)
		}
	}
}

func (s *Store) capacityLocked() Capacity {
	resources := s.resourcesLocked()
	recommended := recommendedLimit(resources)
	running := len(s.runtimeLeases)
	effective := effectiveLimit(resources, recommended, running)
	available := maxInt(0, effective-running)
	waiting := len(s.runtimeQueue)
	return Capacity{
		Mode:                 "auto",
		Total:                len(s.instances),
		Running:              running,
		Waiting:              waiting,
		RecommendedLimit:     recommended,
		EffectiveLimit:       effective,
		AvailableSlots:       available,
		EstimatedPerInstance: estimatedInstanceBytes,
		Resources:            resources,
		Message:              capacityMessage(resources, running, waiting, effective),
	}
}

func (s *Store) resourcesLocked() ResourceSnapshot {
	now := s.now()
	if !s.resourceAt.IsZero() && now.Sub(s.resourceAt) < 2*time.Second {
		return s.resourceCache
	}
	s.resourceCache = s.resourceProbe()
	s.resourceAt = now
	return s.resourceCache
}

func (s *Store) decorateInstanceLocked(instance *Instance) Instance {
	copy := *instance
	copy.RuntimeState = RuntimeStopped
	copy.QueuePosition = 0
	if s.runtimeLeases[instance.ID] != nil {
		copy.RuntimeState = RuntimeRunning
	} else if s.runtimeQueue[instance.ID] != nil {
		copy.RuntimeState = RuntimeWaiting
		copy.QueuePosition = s.queuePositionLocked(instance.ID)
	}
	return copy
}

func (s *Store) queuePositionLocked(instanceID string) int {
	type queued struct {
		id string
		at time.Time
	}
	items := make([]queued, 0, len(s.runtimeQueue))
	for id, entry := range s.runtimeQueue {
		items = append(items, queued{id: id, at: entry.enqueuedAt})
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].at.Equal(items[right].at) {
			return items[left].id < items[right].id
		}
		return items[left].at.Before(items[right].at)
	})
	for index, item := range items {
		if item.id == instanceID {
			return index + 1
		}
	}
	return 0
}

func (s *Store) profileDir(instanceID string) string {
	return filepath.Join(s.profiles, instanceID)
}

func randomID() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func browserLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return "Google Chrome · 独立配置"
	case "windows":
		return "Chrome · 独立配置"
	default:
		return "Chromium · 独立配置"
	}
}

func findChrome() (string, error) {
	candidates := []string{}
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	case "windows":
		candidates = []string{
			filepath.Join(os.Getenv("PROGRAMFILES"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("PROGRAMFILES(X86)"), "Google", "Chrome", "Application", "chrome.exe"),
		}
	default:
		candidates = []string{"google-chrome", "chromium", "chromium-browser"}
	}
	for _, candidate := range candidates {
		if strings.ContainsRune(candidate, filepath.Separator) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", errors.New("未找到 Chrome 或 Chromium，请先安装浏览器")
}

type browserCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func writeCookieExtension(extensionDir, rawCookie, callbackURL, callbackToken string) error {
	if err := os.MkdirAll(extensionDir, 0o700); err != nil {
		return fmt.Errorf("创建 Cookie 同步目录失败: %w", err)
	}
	cookies := parseCookies(rawCookie)
	if len(cookies) == 0 {
		return errors.New("账号 Cookie 格式无效")
	}
	cookieJSON, err := json.Marshal(cookies)
	if err != nil {
		return fmt.Errorf("序列化 Cookie 失败: %w", err)
	}
	manifest := `{
  "manifest_version": 3,
  "name": "福宝登录态同步",
  "version": "1.0.0",
  "permissions": ["cookies", "storage", "tabs"],
  "host_permissions": ["https://*.douyin.com/*", "https://douyin.com/*", "http://127.0.0.1/*"],
  "background": { "service_worker": "background.js" }
}`
	callbackURLJSON, _ := json.Marshal(callbackURL)
	callbackTokenJSON, _ := json.Marshal(callbackToken)
	background := fmt.Sprintf(`const cookies = %s;
const callbackUrl = %s;
const callbackToken = %s;
const loginCookieNames = new Set(["sessionid", "sessionid_ss", "sid_guard"]);
function loginFingerprint(items) {
  return items
    .filter((cookie) => loginCookieNames.has(cookie.name) && cookie.value)
    .sort((left, right) => left.name.localeCompare(right.name))
    .map((cookie) => cookie.name + "=" + cookie.value)
    .join(";");
}
let lastLoginFingerprint = loginFingerprint(cookies);
async function syncCookies() {
  for (const cookie of cookies) {
    try {
      await chrome.cookies.set({
        url: "https://www.douyin.com/",
        domain: ".douyin.com",
        path: "/",
        secure: true,
        sameSite: "no_restriction",
        name: cookie.name,
        value: cookie.value
      });
    } catch (_) {}
  }
  setTimeout(async () => {
    const tabs = await chrome.tabs.query({ url: ["https://*.douyin.com/*", "https://douyin.com/*"] });
    for (const tab of tabs) {
      if (tab.id) {
        try { await chrome.tabs.reload(tab.id); } catch (_) {}
      }
    }
  }, 1200);
}
let syncBackTimer = 0;
function scheduleCookieSyncBack() {
  clearTimeout(syncBackTimer);
  syncBackTimer = setTimeout(syncCookiesBackToEngine, 500);
}
async function syncCookiesBackToEngine() {
  try {
    const currentCookies = await chrome.cookies.getAll({ domain: "douyin.com" });
    const fingerprint = loginFingerprint(currentCookies);
    if (!fingerprint || fingerprint === lastLoginFingerprint) return;
    const response = await fetch(callbackUrl, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Fubao-Token": callbackToken
      },
      body: JSON.stringify({
        cookies: currentCookies.map((cookie) => ({ name: cookie.name, value: cookie.value }))
      })
    });
    if (response.ok) lastLoginFingerprint = fingerprint;
  } catch (_) {}
}
chrome.runtime.onInstalled.addListener(syncCookies);
chrome.runtime.onStartup.addListener(syncCookies);
chrome.cookies.onChanged.addListener((changeInfo) => {
  if (changeInfo.cookie.domain.endsWith("douyin.com")) scheduleCookieSyncBack();
});
chrome.tabs.onUpdated.addListener((_tabId, changeInfo, tab) => {
  if (changeInfo.status === "complete" && tab.url && tab.url.includes("douyin.com")) scheduleCookieSyncBack();
});
syncCookies();
`, string(cookieJSON), string(callbackURLJSON), string(callbackTokenJSON))
	if err := os.WriteFile(filepath.Join(extensionDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		return fmt.Errorf("写入浏览器扩展配置失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(extensionDir, "background.js"), []byte(background), 0o600); err != nil {
		return fmt.Errorf("写入 Cookie 同步脚本失败: %w", err)
	}
	return nil
}

func hasBrowserLoginCookie(cookies []browserCookie) bool {
	for _, cookie := range cookies {
		name := strings.ToLower(strings.TrimSpace(cookie.Name))
		if (name == "sessionid" || name == "sessionid_ss" || name == "sid_guard") && strings.TrimSpace(cookie.Value) != "" {
			return true
		}
	}
	return false
}

func serializeBrowserCookies(cookies []browserCookie) string {
	values := make(map[string]string, len(cookies))
	for _, cookie := range cookies {
		name := strings.TrimSpace(cookie.Name)
		if name == "" {
			continue
		}
		values[name] = strings.TrimSpace(cookie.Value)
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, "; ")
}

func parseCookies(raw string) []browserCookie {
	items := make([]browserCookie, 0)
	for _, part := range strings.Split(raw, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		items = append(items, browserCookie{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
	}
	return items
}

func PortHint(instanceID string) int {
	if len(instanceID) < 4 {
		return 0
	}
	value, _ := strconv.ParseInt(instanceID[:4], 16, 32)
	return 19000 + int(value%1000)
}

func browserSessionActive(instance *Instance) bool {
	if instance == nil || instance.OpenedAt == "" {
		return false
	}
	if openedAt, err := time.Parse(time.RFC3339Nano, instance.OpenedAt); err == nil && time.Since(openedAt) < 3*time.Second {
		return true
	}
	client := http.Client{
		Transport: &http.Transport{Proxy: nil},
		Timeout:   250 * time.Millisecond,
	}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", PortHint(instance.ID)))
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var targets []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	if err := json.NewDecoder(response.Body).Decode(&targets); err != nil {
		return false
	}
	for _, target := range targets {
		if target.Type == "page" && strings.Contains(target.URL, "douyin.com") {
			return true
		}
	}
	return false
}
