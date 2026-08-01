package license

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultAPIBase  = "https://api.keygen.sh/v1/accounts"
	defaultAccount  = "grassvar"
	defaultProduct  = "c3bfe8c4-2b0d-4502-bdd8-ff0d50223de0"
	fingerprintSalt = "douyin-fubao-monitor-license-v1"
)

type Config struct {
	Account          string
	Product          string
	APIBase          string
	OfflineGraceDays int
	HTTPClient       *http.Client
}

type State struct {
	LicenseKey         string         `json:"license_key"`
	LicenseID          string         `json:"license_id"`
	MachineID          string         `json:"machine_id"`
	MachineFingerprint string         `json:"machine_fingerprint"`
	Customer           string         `json:"customer"`
	Plan               string         `json:"plan"`
	ExpiresAt          string         `json:"expires_at"`
	LastValidatedAt    string         `json:"last_validated_at"`
	OfflineUntil       string         `json:"offline_until"`
	Meta               map[string]any `json:"keygen_meta,omitempty"`
}

type Status struct {
	State            string `json:"state"`
	Edition          string `json:"edition"`
	Label            string `json:"label"`
	Tone             string `json:"tone"`
	Detail           string `json:"detail"`
	MachineCode      string `json:"machine_code"`
	Customer         string `json:"customer,omitempty"`
	Plan             string `json:"plan,omitempty"`
	ExpiresAt        string `json:"expires_at,omitempty"`
	LastValidatedAt  string `json:"last_validated_at,omitempty"`
	LicenseKeyMasked string `json:"license_key_masked,omitempty"`
}

type Operation struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Status  Status `json:"status"`
}

type Manager struct {
	path        string
	config      Config
	client      *http.Client
	state       State
	fingerprint string
	appVersion  string
	now         func() time.Time
}

func New(dataDir, appVersion string, cfg Config) (*Manager, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("授权数据目录不能为空")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建授权数据目录失败: %w", err)
	}
	if cfg.Account == "" {
		cfg.Account = defaultAccount
	}
	if cfg.Product == "" {
		cfg.Product = defaultProduct
	}
	if cfg.APIBase == "" {
		cfg.APIBase = defaultAPIBase
	}
	if cfg.OfflineGraceDays <= 0 {
		cfg.OfflineGraceDays = 14
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	m := &Manager{path: filepath.Join(dataDir, "license_state.json"), config: cfg, client: client, appVersion: appVersion, now: time.Now}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Status() Status {
	now := m.now().UTC()
	if m.state.LicenseKey == "" {
		return m.status("inactive", "免费版", "免费版", "neutral", "输入激活码可升级为专业版。")
	}
	if m.state.MachineFingerprint != "" && m.state.MachineFingerprint != m.machineFingerprint() {
		return m.status("machine_mismatch", "免费版", "设备不匹配", "danger", "本地授权属于另一台设备，请重新激活。")
	}
	if expired(m.state.ExpiresAt, now) {
		return m.status("expired", "免费版", "授权已过期", "danger", "授权已到期，请续期后刷新。")
	}
	if expired(m.state.OfflineUntil, now) {
		return m.status("stale", "免费版", "需联网刷新", "warning", "离线宽限已到期，请联网刷新授权。")
	}
	return m.status("active", "专业版", "专业版", "success", "当前设备授权有效。")
}

func (m *Manager) Activate(ctx context.Context, key, machineName string) Operation {
	key = strings.TrimSpace(key)
	if key == "" {
		return Operation{false, "请输入激活码。", m.Status()}
	}
	fp := m.machineFingerprint()
	validation := m.validate(ctx, key, fp)
	if validation.HTTP && validation.Valid {
		m.saveValidation(key, validation, result{})
		return Operation{true, "授权已激活。", m.Status()}
	}
	if validation.LicenseID == "" {
		return Operation{false, validation.message("激活码无效。"), m.Status()}
	}
	activation := m.activateMachine(ctx, key, validation.LicenseID, fp, machineName)
	if !activation.HTTP {
		follow := m.validate(ctx, key, fp)
		if follow.HTTP && follow.Valid {
			m.saveValidation(key, follow, activation)
			return Operation{true, "授权已激活。", m.Status()}
		}
		return Operation{false, activation.message("设备激活失败。"), m.Status()}
	}
	validation = m.validate(ctx, key, fp)
	if validation.HTTP && validation.Valid {
		m.saveValidation(key, validation, activation)
		return Operation{true, "授权已激活。", m.Status()}
	}
	return Operation{false, validation.message("设备已提交，但授权校验未通过。"), m.Status()}
}

func (m *Manager) Refresh(ctx context.Context) Operation {
	if m.state.LicenseKey == "" {
		return Operation{false, "当前没有已保存的激活码。", m.Status()}
	}
	r := m.validate(ctx, m.state.LicenseKey, m.machineFingerprint())
	if !r.HTTP || !r.Valid {
		return Operation{false, r.message("授权刷新失败。"), m.Status()}
	}
	m.saveValidation(m.state.LicenseKey, r, result{})
	return Operation{true, "授权状态已刷新。", m.Status()}
}

func (m *Manager) Deactivate(ctx context.Context) Operation {
	if m.state.LicenseKey != "" && m.state.MachineID != "" {
		req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, m.url("/machines/"+m.state.MachineID), nil)
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("Authorization", "License "+m.state.LicenseKey)
		resp, err := m.client.Do(req)
		if err != nil {
			return Operation{false, "解绑失败：" + err.Error(), m.Status()}
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 && resp.StatusCode != 202 && resp.StatusCode != 204 && resp.StatusCode != 404 {
			return Operation{false, fmt.Sprintf("解绑失败：HTTP %d", resp.StatusCode), m.Status()}
		}
	}
	m.state = State{}
	_ = m.persist()
	return Operation{true, "当前设备已解绑。", m.Status()}
}

type result struct {
	HTTP, Valid                        bool
	Status                             int
	LicenseID, MachineID, Detail, Code string
	Data                               map[string]any
}

func (r result) message(fallback string) string {
	if r.Detail != "" && r.Code != "" {
		return r.Detail + " (" + r.Code + ")"
	}
	if r.Detail != "" {
		return r.Detail
	}
	if r.Status != 0 {
		return fmt.Sprintf("%s HTTP %d", fallback, r.Status)
	}
	return fallback
}

func (m *Manager) validate(ctx context.Context, key, fingerprint string) result {
	scope := map[string]any{"product": m.config.Product}
	if fingerprint != "" {
		scope["fingerprint"] = fingerprint
	}
	body, _ := json.Marshal(map[string]any{"meta": map[string]any{"key": key, "scope": scope}})
	return m.request(ctx, http.MethodPost, "/licenses/actions/validate-key", "", body)
}

func (m *Manager) activateMachine(ctx context.Context, key, licenseID, fingerprint, machineName string) result {
	if strings.TrimSpace(machineName) == "" {
		machineName, _ = os.Hostname()
	}
	payload := map[string]any{"data": map[string]any{
		"type":          "machines",
		"attributes":    map[string]any{"fingerprint": fingerprint, "name": machineName, "platform": platformLabel(), "metadata": map[string]any{"app": "douyin-fubao-monitor", "app_version": m.appVersion, "machine_code": m.machineCode()}},
		"relationships": map[string]any{"license": map[string]any{"data": map[string]any{"type": "licenses", "id": licenseID}}},
	}}
	body, _ := json.Marshal(payload)
	return m.request(ctx, http.MethodPost, "/machines", key, body)
}

func (m *Manager) request(ctx context.Context, method, path, key string, body []byte) result {
	req, err := http.NewRequestWithContext(ctx, method, m.url(path), bytes.NewReader(body))
	if err != nil {
		return result{Detail: err.Error()}
	}
	req.Header.Set("Accept", "application/vnd.api+json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/vnd.api+json")
	}
	if key != "" {
		req.Header.Set("Authorization", "License "+key)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return result{Detail: err.Error()}
	}
	defer resp.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	r := result{HTTP: resp.StatusCode >= 200 && resp.StatusCode < 300, Status: resp.StatusCode, Data: map[string]any{}}
	if data, ok := payload["data"].(map[string]any); ok {
		r.Data = data
		r.LicenseID, _ = data["id"].(string)
		r.MachineID = r.LicenseID
	}
	if meta, ok := payload["meta"].(map[string]any); ok {
		r.Valid, _ = meta["valid"].(bool)
		r.Detail, _ = meta["detail"].(string)
		r.Code, _ = meta["code"].(string)
	}
	if errs, ok := payload["errors"].([]any); ok && len(errs) > 0 {
		if first, ok := errs[0].(map[string]any); ok {
			if detail, ok := first["detail"].(string); ok {
				r.Detail = detail
			}
			if code, ok := first["code"].(string); ok {
				r.Code = code
			}
		}
	}
	return r
}

func (m *Manager) saveValidation(key string, validation, activation result) {
	attrs, _ := validation.Data["attributes"].(map[string]any)
	metadata, _ := attrs["metadata"].(map[string]any)
	plan, _ := metadata["plan"].(string)
	if plan == "" {
		plan, _ = metadata["edition"].(string)
	}
	now := m.now().UTC()
	machineID := m.state.MachineID
	if activation.MachineID != "" {
		machineID = activation.MachineID
	}
	m.state = State{LicenseKey: key, LicenseID: validation.LicenseID, MachineID: machineID, MachineFingerprint: m.machineFingerprint(), Plan: plan, ExpiresAt: stringValue(attrs["expiry"]), LastValidatedAt: now.Format(time.RFC3339), OfflineUntil: now.Add(time.Duration(m.config.OfflineGraceDays) * 24 * time.Hour).Format(time.RFC3339), Meta: map[string]any{"validation_code": validation.Code}}
	_ = m.persist()
}

func (m *Manager) load() error {
	b, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取授权缓存失败: %w", err)
	}
	if err := json.Unmarshal(b, &m.state); err != nil {
		return fmt.Errorf("解析授权缓存失败: %w", err)
	}
	return nil
}

func (m *Manager) persist() error {
	b, _ := json.MarshalIndent(m.state, "", "  ")
	if err := os.WriteFile(m.path, b, 0o600); err != nil {
		return fmt.Errorf("保存授权缓存失败: %w", err)
	}
	return os.Chmod(m.path, 0o600)
}

func (m *Manager) status(state, edition, label, tone, detail string) Status {
	return Status{State: state, Edition: edition, Label: label, Tone: tone, Detail: detail, MachineCode: m.machineCode(), Customer: m.state.Customer, Plan: m.state.Plan, ExpiresAt: m.state.ExpiresAt, LastValidatedAt: m.state.LastValidatedAt, LicenseKeyMasked: mask(m.state.LicenseKey)}
}

func (m *Manager) machineFingerprint() string {
	if m.fingerprint != "" {
		return m.fingerprint
	}
	seed := strings.Join([]string{fingerprintSalt, platformSystem(), runtime.GOARCH, machineID()}, "|")
	sum := sha256.Sum256([]byte(seed))
	m.fingerprint = hex.EncodeToString(sum[:])
	return m.fingerprint
}
func (m *Manager) machineCode() string {
	v := strings.ToUpper(m.machineFingerprint()[:16])
	return v[:4] + "-" + v[4:8] + "-" + v[8:12] + "-" + v[12:16]
}
func (m *Manager) url(path string) string {
	return strings.TrimRight(m.config.APIBase, "/") + "/" + strings.Trim(m.config.Account, "/") + path
}
func platformSystem() string {
	if runtime.GOOS == "darwin" {
		return "Darwin"
	}
	if runtime.GOOS == "windows" {
		return "Windows"
	}
	return runtime.GOOS
}
func platformLabel() string {
	if runtime.GOOS == "darwin" {
		return "macOS"
	}
	if runtime.GOOS == "windows" {
		return "Windows"
	}
	return runtime.GOOS
}
func machineID() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, "IOPlatformUUID") {
					parts := strings.SplitN(line, "=", 2)
					if len(parts) == 2 {
						return strings.Trim(strings.TrimSpace(parts[1]), "\"")
					}
				}
			}
		}
	}
	host, _ := os.Hostname()
	return host + "|" + runtime.GOARCH
}
func expired(value string, now time.Time) bool {
	if value == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, value)
	return err == nil && t.Before(now)
}
func mask(key string) string {
	key = strings.Join(strings.Fields(key), "")
	if len(key) <= 10 {
		return key
	}
	return key[:4] + "..." + key[len(key)-4:]
}
func stringValue(v any) string { s, _ := v.(string); return s }
