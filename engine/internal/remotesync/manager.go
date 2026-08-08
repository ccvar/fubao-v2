package remotesync

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"fubao.ccvar.com/engine/internal/redpacket"
	"fubao.ccvar.com/engine/internal/rooms"
	"fubao.ccvar.com/engine/internal/syncprotocol"
)

const (
	configVersion      = 1
	flushInterval      = 3 * time.Second
	maxRoomQueue       = 20_000
	healthProbeTimeout = 5 * time.Second
	fallbackHold       = time.Minute
)

type PullScope string

const (
	PullNone       PullScope = "none"
	PullRedPackets PullScope = "red_packets"
	PullAll        PullScope = "all"
)

type Config struct {
	Version               int    `json:"version"`
	Enabled               bool   `json:"enabled"`
	Endpoint              string `json:"endpoint"`
	FallbackEndpoint      string `json:"fallback_endpoint"`
	EnrollmentToken       string `json:"enrollment_token,omitempty"`
	EnrollmentTokenMasked string `json:"enrollment_token_masked,omitempty"`
	DeviceToken           string `json:"device_token,omitempty"`
	DeviceAccess          string `json:"device_access,omitempty"`
	PullCursor            int64  `json:"pull_cursor,omitempty"`
	RoomPullCursor        int64  `json:"room_pull_cursor,omitempty"`
	PacketPullCursor      int64  `json:"packet_pull_cursor,omitempty"`
}

type Status struct {
	Enabled          bool   `json:"enabled"`
	Configured       bool   `json:"configured"`
	UploadOnly       bool   `json:"upload_only"`
	TokenMasked      string `json:"token_masked,omitempty"`
	Endpoint         string `json:"endpoint"`
	FallbackEndpoint string `json:"fallback_endpoint"`
	ActiveEndpoint   string `json:"active_endpoint,omitempty"`
	Pending          int    `json:"pending"`
	ClientID         string `json:"client_id"`
	LastSuccessAt    string `json:"last_success_at,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	LastErrorAt      string `json:"last_error_at,omitempty"`
}

type outboxFile struct {
	Version int                      `json:"version"`
	Items   []syncprotocol.BatchItem `json:"items"`
}

type Manager struct {
	mu             sync.Mutex
	connectMu      sync.Mutex
	dataDir        string
	configPath     string
	outboxPath     string
	clientIDPath   string
	config         Config
	clientID       string
	clientName     string
	httpClient     *http.Client
	outbox         []syncprotocol.BatchItem
	lastHashes     map[string]string
	lastSuccessAt  string
	lastError      string
	lastErrorAt    string
	activeEndpoint string
	activeSince    time.Time
}

func New(dataDir string) (*Manager, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return nil, errors.New("远程同步数据目录为空")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建远程同步数据目录失败: %w", err)
	}
	manager := &Manager{
		dataDir:      dataDir,
		configPath:   filepath.Join(dataDir, "remote_sync.json"),
		outboxPath:   filepath.Join(dataDir, "remote_sync_outbox.json"),
		clientIDPath: filepath.Join(dataDir, "remote_sync_client_id"),
		httpClient:   &http.Client{Timeout: 15 * time.Second},
		lastHashes:   map[string]string{},
	}
	if err := manager.loadConfig(); err != nil {
		return nil, err
	}
	if err := manager.loadClientID(); err != nil {
		return nil, err
	}
	if err := manager.loadOutbox(); err != nil {
		return nil, err
	}
	manager.clientName, _ = os.Hostname()
	return manager, nil
}

func (m *Manager) loadConfig() error {
	m.config = Config{
		Version: configVersion, Endpoint: syncprotocol.DefaultEndpoint,
		FallbackEndpoint: syncprotocol.DefaultFallbackEndpoint,
	}
	defaultConfig := m.config
	if _, err := loadRecoverablePrivateJSON(m.configPath, "远程同步配置", &m.config, defaultConfig); err != nil {
		return err
	}
	if endpoint := strings.TrimSpace(os.Getenv("FUBAO_SYNC_ENDPOINT")); endpoint != "" {
		m.config.Endpoint = endpoint
	}
	if endpoint := strings.TrimSpace(os.Getenv("FUBAO_SYNC_FALLBACK_ENDPOINT")); endpoint != "" {
		m.config.FallbackEndpoint = endpoint
	}
	if strings.TrimSpace(m.config.FallbackEndpoint) == "" {
		m.config.FallbackEndpoint = syncprotocol.DefaultFallbackEndpoint
	}
	if token := strings.TrimSpace(os.Getenv("FUBAO_SYNC_ENROLLMENT_TOKEN")); token != "" {
		m.config.EnrollmentToken = token
		m.config.EnrollmentTokenMasked = maskToken(token)
		m.config.Enabled = true
	}
	if token := strings.TrimSpace(os.Getenv("FUBAO_SYNC_DEVICE_TOKEN")); token != "" {
		m.config.DeviceToken = token
		m.config.DeviceAccess = syncprotocol.DeviceAccessFull
		m.config.Enabled = true
	}
	endpoint, err := syncprotocol.NormalizeEndpoint(m.config.Endpoint)
	if err != nil {
		return err
	}
	fallbackEndpoint, err := syncprotocol.NormalizeEndpoint(m.config.FallbackEndpoint)
	if err != nil {
		return err
	}
	m.config.Version = configVersion
	m.config.Endpoint = endpoint
	m.config.FallbackEndpoint = fallbackEndpoint
	if m.config.DeviceToken != "" && m.config.DeviceAccess == "" {
		m.config.DeviceAccess = syncprotocol.DeviceAccessFull
	}
	if m.config.EnrollmentTokenMasked == "" && m.config.EnrollmentToken != "" {
		m.config.EnrollmentTokenMasked = maskToken(m.config.EnrollmentToken)
	}
	if m.config.DeviceToken != "" && m.config.DeviceAccess == syncprotocol.DeviceAccessFull {
		m.config.Enabled = true
	}
	if m.config.PullCursor > 0 && m.config.RoomPullCursor == 0 && m.config.PacketPullCursor == 0 {
		m.config.RoomPullCursor = m.config.PullCursor
		m.config.PacketPullCursor = m.config.PullCursor
	}
	return m.saveConfigLocked()
}

func (m *Manager) loadClientID() error {
	content, err := os.ReadFile(m.clientIDPath)
	if err == nil && strings.TrimSpace(string(content)) != "" {
		m.clientID = strings.TrimSpace(string(content))
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取远程同步客户端标识失败: %w", err)
	}
	clientID, err := randomID()
	if err != nil {
		return err
	}
	m.clientID = "desktop_" + clientID
	return writePrivateFile(m.clientIDPath, []byte(m.clientID+"\n"))
}

func (m *Manager) loadOutbox() error {
	payload := outboxFile{Version: configVersion}
	loaded, err := loadRecoverablePrivateJSON(m.outboxPath, "远程同步队列", &payload, payload)
	if err != nil {
		return err
	}
	if !loaded {
		return nil
	}
	for _, item := range payload.Items {
		if item.IdempotencyKey == "" || !json.Valid(item.Payload) {
			continue
		}
		m.outbox = append(m.outbox, item)
		m.lastHashes[item.IdempotencyKey] = itemHash(item)
	}
	return nil
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	accessMode := m.deviceAccessLocked()
	return Status{
		Enabled:          m.config.Enabled && accessMode == syncprotocol.DeviceAccessFull,
		Configured:       m.config.EnrollmentToken != "" || accessMode == syncprotocol.DeviceAccessFull,
		UploadOnly:       accessMode == syncprotocol.DeviceAccessUploadOnly,
		TokenMasked:      m.tokenMaskedLocked(),
		Endpoint:         m.config.Endpoint,
		FallbackEndpoint: m.config.FallbackEndpoint,
		ActiveEndpoint:   m.activeEndpoint,
		Pending:          len(m.outbox),
		ClientID:         m.clientID,
		LastSuccessAt:    m.lastSuccessAt,
		LastError:        m.lastError,
		LastErrorAt:      m.lastErrorAt,
	}
}

func (m *Manager) Run(ctx context.Context) {
	m.attempt(ctx)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.attempt(ctx)
		}
	}
}

func (m *Manager) attempt(ctx context.Context) {
	if err := m.ensureRegistered(ctx); err != nil {
		m.recordError(err)
		return
	}
	for {
		sent, err := m.flushOnce(ctx)
		if err != nil {
			m.recordError(err)
			return
		}
		if !sent {
			return
		}
		m.recordSuccess()
	}
}

func (m *Manager) ensureRegistered(ctx context.Context) error {
	m.connectMu.Lock()
	defer m.connectMu.Unlock()
	return m.ensureRegisteredLocked(ctx)
}

func (m *Manager) ensureRegisteredLocked(ctx context.Context) error {
	m.mu.Lock()
	if m.config.DeviceToken != "" {
		m.mu.Unlock()
		return nil
	}
	enrollmentToken := m.config.EnrollmentToken
	clientID := m.clientID
	clientName := m.clientName
	m.mu.Unlock()
	payload := syncprotocol.RegisterRequest{
		Version: syncprotocol.Version, ClientID: clientID, ClientName: clientName,
		Platform: runtime.GOOS + "/" + runtime.GOARCH, AppVersion: "0.1.0",
	}
	var response syncprotocol.RegisterResponse
	route := "/devices/register"
	accessMode := syncprotocol.DeviceAccessFull
	if enrollmentToken == "" {
		route = "/devices/register-upload"
		accessMode = syncprotocol.DeviceAccessUploadOnly
	}
	if err := m.postJSON(ctx, route, enrollmentToken, payload, &response); err != nil {
		return err
	}
	if response.Version != syncprotocol.Version || response.ClientID != clientID || strings.TrimSpace(response.DeviceToken) == "" || response.AccessMode != accessMode {
		return errors.New("远程同步设备注册响应无效")
	}
	m.mu.Lock()
	m.config.DeviceToken = strings.TrimSpace(response.DeviceToken)
	m.config.DeviceAccess = response.AccessMode
	m.config.EnrollmentToken = ""
	err := m.saveConfigLocked()
	m.mu.Unlock()
	return err
}

func (m *Manager) Configure(ctx context.Context, enabled bool, enrollmentToken string) (Status, error) {
	m.connectMu.Lock()
	defer m.connectMu.Unlock()

	enrollmentToken = strings.TrimSpace(enrollmentToken)
	if enrollmentToken != "" && len(enrollmentToken) < 32 {
		return m.Status(), errors.New("服务器注册令牌至少需要 32 个字符")
	}
	m.mu.Lock()
	previous := m.config
	previousActiveEndpoint := m.activeEndpoint
	previousActiveSince := m.activeSince
	if enrollmentToken != "" {
		m.config.EnrollmentToken = enrollmentToken
		m.config.EnrollmentTokenMasked = maskToken(enrollmentToken)
		m.config.DeviceToken = ""
		m.config.DeviceAccess = ""
		m.activeEndpoint = ""
		m.activeSince = time.Time{}
		m.config.PullCursor = 0
		m.config.RoomPullCursor = 0
		m.config.PacketPullCursor = 0
	}
	if enabled && m.config.EnrollmentToken == "" && m.deviceAccessLocked() != syncprotocol.DeviceAccessFull {
		m.mu.Unlock()
		return m.Status(), errors.New("请输入服务器安装时生成的注册令牌")
	}
	m.config.Enabled = enabled
	if err := m.saveConfigLocked(); err != nil {
		m.config = previous
		m.mu.Unlock()
		return m.Status(), err
	}
	m.mu.Unlock()
	if !enabled {
		return m.Status(), nil
	}
	if err := m.ensureRegisteredLocked(ctx); err != nil {
		m.mu.Lock()
		m.config = previous
		m.activeEndpoint = previousActiveEndpoint
		m.activeSince = previousActiveSince
		restoreErr := m.saveConfigLocked()
		m.mu.Unlock()
		m.recordError(err)
		if restoreErr != nil {
			return m.Status(), fmt.Errorf("%v；恢复原远程同步配置失败: %w", err, restoreErr)
		}
		return m.Status(), err
	}
	m.recordSuccess()
	return m.Status(), nil
}

func (m *Manager) flushOnce(ctx context.Context) (bool, error) {
	m.mu.Lock()
	if len(m.outbox) == 0 || m.config.DeviceToken == "" {
		m.mu.Unlock()
		return false, nil
	}
	limit := len(m.outbox)
	if limit > syncprotocol.MaxBatchItems {
		limit = syncprotocol.MaxBatchItems
	}
	items := append([]syncprotocol.BatchItem(nil), m.outbox[:limit]...)
	deviceToken, clientID := m.config.DeviceToken, m.clientID
	m.mu.Unlock()
	requestID, err := randomID()
	if err != nil {
		return false, err
	}
	payload := syncprotocol.BatchRequest{
		Version: syncprotocol.Version, RequestID: requestID, ClientID: clientID,
		SentAt: time.Now().UTC().Format(time.RFC3339Nano), Items: items,
	}
	var response syncprotocol.BatchResponse
	if err := m.postJSON(ctx, "/sync/batch", deviceToken, payload, &response); err != nil {
		return false, err
	}
	if response.Version != syncprotocol.Version || response.RequestID != requestID {
		return false, errors.New("远程同步确认响应无效")
	}
	acked := make(map[string]struct{}, len(response.Acked))
	for _, key := range response.Acked {
		acked[key] = struct{}{}
	}
	sentHashes := make(map[string]string, len(items))
	for _, item := range items {
		sentHashes[item.IdempotencyKey] = itemHash(item)
	}
	m.mu.Lock()
	remaining := m.outbox[:0]
	for _, item := range m.outbox {
		_, wasAcked := acked[item.IdempotencyKey]
		if wasAcked && sentHashes[item.IdempotencyKey] == itemHash(item) {
			continue
		}
		remaining = append(remaining, item)
	}
	m.outbox = remaining
	err = m.saveOutboxLocked()
	m.mu.Unlock()
	return true, err
}

type remoteStatusError struct {
	status  int
	message string
}

func (e *remoteStatusError) Error() string {
	return fmt.Sprintf("远程同步返回 %d: %s", e.status, e.message)
}

func (m *Manager) postJSON(ctx context.Context, route, token string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	var failures []string
	for _, endpoint := range m.endpointCandidates() {
		if err := m.probeHealth(ctx, endpoint); err != nil {
			failures = append(failures, endpoint+": "+err.Error())
			continue
		}
		err := m.postJSONTo(ctx, endpoint+route, token, body, output)
		if err == nil {
			m.markEndpointActive(endpoint)
			return nil
		}
		var statusErr *remoteStatusError
		if errors.As(err, &statusErr) && statusErr.status >= 400 && statusErr.status < 500 {
			return err
		}
		failures = append(failures, endpoint+": "+err.Error())
	}
	return fmt.Errorf("远程同步主备入口均不可用: %s", strings.Join(failures, "; "))
}

func (m *Manager) postJSONTo(ctx context.Context, target, token string, body []byte, output any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "fubao-engine/0.1")
	response, err := m.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("远程同步连接失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return &remoteStatusError{status: response.StatusCode, message: strings.TrimSpace(string(limited))}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output); err != nil {
		return fmt.Errorf("解析远程同步响应失败: %w", err)
	}
	return nil
}

func (m *Manager) getJSON(ctx context.Context, route, token string, output any) error {
	var failures []string
	for _, endpoint := range m.endpointCandidates() {
		if err := m.probeHealth(ctx, endpoint); err != nil {
			failures = append(failures, endpoint+": "+err.Error())
			continue
		}
		err := m.getJSONFrom(ctx, endpoint+route, token, output)
		if err == nil {
			m.markEndpointActive(endpoint)
			return nil
		}
		var statusErr *remoteStatusError
		if errors.As(err, &statusErr) && statusErr.status >= 400 && statusErr.status < 500 {
			return err
		}
		failures = append(failures, endpoint+": "+err.Error())
	}
	return fmt.Errorf("远程同步主备入口均不可用: %s", strings.Join(failures, "; "))
}

func (m *Manager) getJSONFrom(ctx context.Context, target, token string, output any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "fubao-engine/0.1")
	response, err := m.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("远程同步连接失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return &remoteStatusError{status: response.StatusCode, message: strings.TrimSpace(string(limited))}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output); err != nil {
		return fmt.Errorf("解析中心库增量响应失败: %w", err)
	}
	return nil
}

func (m *Manager) endpointCandidates() []string {
	m.mu.Lock()
	primary, fallback := m.config.Endpoint, m.config.FallbackEndpoint
	active, activeSince := m.activeEndpoint, m.activeSince
	m.mu.Unlock()
	values := make([]string, 0, 2)
	if active != "" && active != primary && time.Since(activeSince) < fallbackHold {
		values = append(values, active)
	}
	values = append(values, primary, fallback)
	result := values[:0]
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimRight(strings.TrimSpace(value), "/")
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (m *Manager) probeHealth(ctx context.Context, endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return errors.New("健康检查地址无效")
	}
	parsed.Path, parsed.RawPath, parsed.RawQuery, parsed.Fragment = "/healthz", "", "", ""
	probeCtx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "fubao-engine/0.1")
	response, err := m.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("健康检查失败: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("健康检查返回 %d", response.StatusCode)
	}
	return nil
}

func (m *Manager) markEndpointActive(endpoint string) {
	m.mu.Lock()
	m.activeEndpoint = endpoint
	m.activeSince = time.Now()
	m.mu.Unlock()
}

func (m *Manager) SyncSnapshot(roomItems []rooms.Room, monitorItems []redpacket.Monitor, events []redpacket.Event) error {
	monitorsByRoom := make(map[string]redpacket.Monitor, len(monitorItems)*2)
	for _, monitor := range monitorItems {
		monitorsByRoom[monitor.RoomID] = monitor
		if monitor.WebRID != "" {
			monitorsByRoom[monitor.WebRID] = monitor
		}
	}
	items := make([]syncprotocol.BatchItem, 0, len(roomItems)+len(events))
	for _, room := range roomItems {
		webRID := strings.TrimSpace(room.WebRID)
		if webRID == "" {
			continue
		}
		monitor := monitorsByRoom[room.ID]
		if monitor.ID == "" {
			monitor = monitorsByRoom[webRID]
		}
		// Center data is learned from other clients and must never be echoed
		// back. A local room only becomes eligible after this client has
		// completed a definitive live probe that observed at least one live
		// session. Imports and unknown/error probes therefore stay local until
		// they have real local live evidence.
		if !roomHasObservedLocalLive(room) {
			continue
		}
		updatedAt := latestTime(room.UpdatedAt, monitor.UpdatedAt)
		payload := syncprotocol.RoomState{
			WebRID: webRID, ActualRoomID: firstNonEmpty(monitor.ActualRoomID, room.ActualRoomID),
			Title: firstNonEmpty(monitor.Name, room.Name), StreamerName: firstNonEmpty(monitor.StreamerName, room.StreamerName),
			MonitorStatus: monitor.Status, ConnectionStatus: monitor.ConnectionStatus,
			LiveStatus: monitor.LiveStatus, LiveStatusSource: monitor.LiveStatusSource,
			LiveStartedAt: monitor.LiveStartedAt, LastSeenLiveAt: room.LastSeenLiveAt,
			LastCheckedAt: monitor.LastCheckedAt, LastRedPacketCheckedAt: monitor.LastRedPacketCheckedAt,
			LastEventAt: monitor.LastEventAt, LiveSessionCount: room.LiveSessionCount, UpdatedAt: updatedAt,
		}
		item, err := makeItem(syncprotocol.ItemRoomState, "room:"+webRID, updatedAt, payload)
		if err != nil {
			return err
		}
		items = append(items, item)
	}
	for _, event := range events {
		item, ok, err := redPacketItem(event)
		if err != nil {
			return err
		}
		if ok {
			items = append(items, item)
		}
	}
	return m.enqueue(items)
}

func roomHasObservedLocalLive(room rooms.Room) bool {
	return room.HasDefinitiveProbe && room.LiveSessionCount > 0
}

func (m *Manager) EnqueueEvent(event redpacket.Event) error {
	if event.DataSource == "center" {
		return nil
	}
	item, ok, err := redPacketItem(event)
	if err != nil || !ok {
		return err
	}
	return m.enqueue([]syncprotocol.BatchItem{item})
}

func redPacketItem(event redpacket.Event) (syncprotocol.BatchItem, bool, error) {
	if event.DataSource == "center" {
		return syncprotocol.BatchItem{}, false, nil
	}
	webRID, packetID := strings.TrimSpace(event.WebRID), strings.TrimSpace(event.PacketID)
	if webRID == "" || packetID == "" {
		return syncprotocol.BatchItem{}, false, nil
	}
	payload := syncprotocol.RedPacket{
		WebRID: webRID, PacketID: packetID,
		ActualRoomID: nativeNumericValue(event.ActualRoomID, 32), JoinBoxID: nativeNumericValue(event.JoinBoxID, 32),
		AnchorID: nativeNumericValue(event.AnchorID, 32), BoxType: nativeNumericValue(event.BoxType, 16),
		SendTime: nativeNumericValue(event.SendTime, 32), DelayTime: nativeNumericValue(event.DelayTime, 32),
		RoomName:     event.RoomName,
		StreamerName: event.StreamerName, Title: event.Title, Prize: event.Prize,
		Condition: event.Condition,
		Source:    event.Source, DetectedAt: event.DetectedAt, DrawAt: event.DrawAt,
		ExpiresAt: event.ExpiresAt, ParticipantCount: event.ParticipantCount,
		TotalDiamonds: event.TotalDiamonds, ShareCount: event.ShareCount,
	}
	item, err := makeItem(syncprotocol.ItemRedPacket, "red_packet:"+webRID+":"+packetID, event.DetectedAt, payload)
	return item, err == nil, err
}

func (m *Manager) PullOnce(ctx context.Context, roomStore *rooms.Store, redPacketStore *redpacket.Store) error {
	return m.PullOnceScoped(ctx, roomStore, redPacketStore, PullAll)
}

func (m *Manager) PullOnceScoped(ctx context.Context, roomStore *rooms.Store, redPacketStore *redpacket.Store, scope PullScope) error {
	if scope == PullNone || roomStore == nil || redPacketStore == nil {
		return nil
	}
	m.mu.Lock()
	pullEnabled := m.config.Enabled && m.deviceAccessLocked() == syncprotocol.DeviceAccessFull
	m.mu.Unlock()
	if !pullEnabled {
		return nil
	}
	if err := m.ensureRegistered(ctx); err != nil {
		m.recordError(err)
		return err
	}
	if scope == PullAll {
		if err := m.SyncCenterExclusions(ctx, roomStore); err != nil {
			return err
		}
		if err := m.pullType(ctx, roomStore, redPacketStore, syncprotocol.ItemRoomState); err != nil {
			return err
		}
	}
	if scope == PullAll || scope == PullRedPackets {
		return m.pullType(ctx, roomStore, redPacketStore, syncprotocol.ItemRedPacket)
	}
	return nil
}

// SyncCenterExclusions uploads only locally-pending tombstones, then replaces
// the local acknowledged cache with the server-authoritative global list.
func (m *Manager) SyncCenterExclusions(ctx context.Context, roomStore *rooms.Store) error {
	if roomStore == nil {
		return nil
	}
	m.mu.Lock()
	allowed := m.config.Enabled && m.deviceAccessLocked() == syncprotocol.DeviceAccessFull
	m.mu.Unlock()
	if !allowed {
		return nil
	}
	if err := m.ensureRegistered(ctx); err != nil {
		m.recordError(err)
		return err
	}
	m.mu.Lock()
	deviceToken := m.config.DeviceToken
	m.mu.Unlock()
	pending := roomStore.PendingCenterExclusions()
	for start := 0; start < len(pending); start += syncprotocol.MaxBatchItems {
		end := start + syncprotocol.MaxBatchItems
		if end > len(pending) {
			end = len(pending)
		}
		requestItems := make([]syncprotocol.CenterRoomExclusion, 0, end-start)
		webRIDs := make([]string, 0, end-start)
		for _, item := range pending[start:end] {
			requestItems = append(requestItems, syncprotocol.CenterRoomExclusion{
				WebRID: item.WebRID, ActualRoomID: item.ActualRoomID, Name: item.Name,
				StreamerName: item.StreamerName, Reason: item.Reason, ExcludedAt: item.ExcludedAt,
			})
			webRIDs = append(webRIDs, item.WebRID)
		}
		var response map[string]any
		if err := m.postJSON(ctx, "/rooms/exclusions", deviceToken, syncprotocol.CenterRoomExclusionsRequest{Items: requestItems}, &response); err != nil {
			m.recordError(err)
			return err
		}
		if err := roomStore.MarkCenterExclusionsSynced(webRIDs); err != nil {
			return err
		}
	}
	var response syncprotocol.CenterRoomExclusionsResponse
	if err := m.getJSON(ctx, "/rooms/exclusions", deviceToken, &response); err != nil {
		m.recordError(err)
		return err
	}
	items := make([]rooms.CenterExclusion, 0, len(response.Items))
	for _, item := range response.Items {
		items = append(items, rooms.CenterExclusion{
			ID: item.WebRID, WebRID: item.WebRID, ActualRoomID: item.ActualRoomID,
			Name: item.Name, StreamerName: item.StreamerName, Reason: item.Reason, ExcludedAt: item.ExcludedAt,
		})
	}
	if err := roomStore.MergeGlobalCenterExclusions(items); err != nil {
		return err
	}
	m.recordSuccess()
	return nil
}

// RestoreCenterExclusion removes the server tombstone first. The caller may
// restore its local room only after this succeeds, avoiding a UI state that
// claims recovery while the center still rejects the room.
func (m *Manager) RestoreCenterExclusion(ctx context.Context, webRID string) error {
	webRID = strings.TrimSpace(webRID)
	if webRID == "" {
		return errors.New("中心库排除的直播间标识为空")
	}
	if err := m.ensureRegistered(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	allowed := m.config.Enabled && m.deviceAccessLocked() == syncprotocol.DeviceAccessFull
	deviceToken := m.config.DeviceToken
	m.mu.Unlock()
	if !allowed {
		return errors.New("当前设备未绑定可管理中心库的同步授权")
	}
	var response map[string]bool
	if err := m.postJSON(ctx, "/rooms/exclusions/restore", deviceToken, syncprotocol.CenterRoomExclusionRestoreRequest{WebRID: webRID}, &response); err != nil {
		m.recordError(err)
		return err
	}
	m.recordSuccess()
	return nil
}

func (m *Manager) pullType(ctx context.Context, roomStore *rooms.Store, redPacketStore *redpacket.Store, itemType syncprotocol.ItemType) error {
	for page := 0; page < 8; page++ {
		m.mu.Lock()
		cursor := m.config.PacketPullCursor
		if itemType == syncprotocol.ItemRoomState {
			cursor = m.config.RoomPullCursor
		}
		clientID, deviceToken := m.clientID, m.config.DeviceToken
		m.mu.Unlock()
		var response syncprotocol.ChangesResponse
		route := fmt.Sprintf("/sync/changes?cursor=%d&limit=%d&type=%s", cursor, syncprotocol.MaxChanges, url.QueryEscape(string(itemType)))
		if err := m.getJSON(ctx, route, deviceToken, &response); err != nil {
			m.recordError(err)
			return err
		}
		if response.Version != syncprotocol.Version || response.NextCursor < cursor {
			err := errors.New("中心库增量响应无效")
			m.recordError(err)
			return err
		}
		centerRooms := make([]rooms.CenterRoom, 0)
		centerEvents := make([]redpacket.CenterEvent, 0)
		for _, change := range response.Changes {
			if change.Cursor <= cursor || change.OriginClientID == clientID || change.Type != itemType {
				continue
			}
			switch change.Type {
			case syncprotocol.ItemRoomState:
				var item syncprotocol.RoomState
				if err := json.Unmarshal(change.Payload, &item); err != nil {
					return fmt.Errorf("解析中心库直播间失败: %w", err)
				}
				centerRooms = append(centerRooms, rooms.CenterRoom{
					WebRID: item.WebRID, ActualRoomID: item.ActualRoomID, Title: item.Title,
					StreamerName: item.StreamerName, LiveStatus: item.LiveStatus,
					LiveStartedAt: item.LiveStartedAt, LastSeenLiveAt: item.LastSeenLiveAt,
					LastEventAt: item.LastEventAt, MetricsVersion: item.MetricsVersion,
					LiveSessionCount: item.LiveSessionCount, RedPacketCount: item.RedPacketCount,
					CenterUpdatedAt: item.UpdatedAt,
				})
			case syncprotocol.ItemRedPacket:
				var item syncprotocol.RedPacket
				if err := json.Unmarshal(change.Payload, &item); err != nil {
					return fmt.Errorf("解析中心库红包失败: %w", err)
				}
				centerEvents = append(centerEvents, redpacket.CenterEvent{
					WebRID: item.WebRID, PacketID: item.PacketID,
					ActualRoomID: item.ActualRoomID, JoinBoxID: item.JoinBoxID,
					AnchorID: item.AnchorID, BoxType: item.BoxType, SendTime: item.SendTime, DelayTime: item.DelayTime,
					RoomName:     item.RoomName,
					StreamerName: item.StreamerName, Title: item.Title, Prize: item.Prize,
					Condition: item.Condition,
					Source:    item.Source, DetectedAt: item.DetectedAt, DrawAt: item.DrawAt,
					ExpiresAt: item.ExpiresAt, ParticipantCount: item.ParticipantCount,
					TotalDiamonds: item.TotalDiamonds, ShareCount: item.ShareCount,
				})
			}
		}
		if _, err := roomStore.MergeCenter(centerRooms); err != nil {
			return err
		}
		if _, err := redPacketStore.MergeCenter(centerEvents); err != nil {
			return err
		}
		if len(centerRooms) > 0 {
			if err := redPacketStore.SyncRooms(roomStore.All()); err != nil {
				return err
			}
		}
		m.mu.Lock()
		changedCursor := false
		if itemType == syncprotocol.ItemRoomState && response.NextCursor > m.config.RoomPullCursor {
			m.config.RoomPullCursor = response.NextCursor
			changedCursor = true
		}
		if itemType == syncprotocol.ItemRedPacket && response.NextCursor > m.config.PacketPullCursor {
			m.config.PacketPullCursor = response.NextCursor
			changedCursor = true
		}
		if changedCursor {
			if err := m.saveConfigLocked(); err != nil {
				m.mu.Unlock()
				return err
			}
		}
		m.mu.Unlock()
		m.recordSuccess()
		if !response.HasMore || len(response.Changes) == 0 {
			return nil
		}
	}
	return nil
}

func (m *Manager) enqueue(items []syncprotocol.BatchItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := make(map[string]int, len(m.outbox))
	roomCount := 0
	for i, item := range m.outbox {
		index[item.IdempotencyKey] = i
		if item.Type == syncprotocol.ItemRoomState {
			roomCount++
		}
	}
	changed := false
	for _, item := range items {
		hash := itemHash(item)
		if m.lastHashes[item.IdempotencyKey] == hash {
			continue
		}
		m.lastHashes[item.IdempotencyKey] = hash
		if existing, ok := index[item.IdempotencyKey]; ok {
			m.outbox[existing] = item
			changed = true
			continue
		}
		if item.Type == syncprotocol.ItemRoomState && roomCount >= maxRoomQueue {
			continue
		}
		index[item.IdempotencyKey] = len(m.outbox)
		m.outbox = append(m.outbox, item)
		if item.Type == syncprotocol.ItemRoomState {
			roomCount++
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return m.saveOutboxLocked()
}

func (m *Manager) deviceAccessLocked() string {
	if m.config.DeviceToken == "" {
		return ""
	}
	if m.config.DeviceAccess == "" {
		return syncprotocol.DeviceAccessFull
	}
	return m.config.DeviceAccess
}

func (m *Manager) tokenMaskedLocked() string {
	if m.config.EnrollmentTokenMasked != "" {
		return m.config.EnrollmentTokenMasked
	}
	if m.deviceAccessLocked() != syncprotocol.DeviceAccessFull {
		return ""
	}
	return maskToken(m.config.DeviceToken)
}

func maskToken(value string) string {
	characters := []rune(strings.TrimSpace(value))
	if len(characters) == 0 {
		return ""
	}
	if len(characters) <= 8 {
		return "••••••"
	}
	return string(characters[:4]) + "…" + string(characters[len(characters)-4:])
}

func (m *Manager) recordError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastError = err.Error()
	m.lastErrorAt = time.Now().UTC().Format(time.RFC3339Nano)
}

func (m *Manager) recordSuccess() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSuccessAt = time.Now().UTC().Format(time.RFC3339Nano)
	m.lastError, m.lastErrorAt = "", ""
}

func (m *Manager) saveConfigLocked() error {
	payload, err := json.MarshalIndent(m.config, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateJSONFile(m.configPath, append(payload, '\n'))
}

func (m *Manager) saveOutboxLocked() error {
	payload, err := json.MarshalIndent(outboxFile{Version: configVersion, Items: m.outbox}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateJSONFile(m.outboxPath, append(payload, '\n'))
}

func writePrivateFile(path string, content []byte) error {
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, content, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

func makeItem(itemType syncprotocol.ItemType, key, occurredAt string, payload any) (syncprotocol.BatchItem, error) {
	content, err := json.Marshal(payload)
	if err != nil {
		return syncprotocol.BatchItem{}, err
	}
	if strings.TrimSpace(occurredAt) == "" {
		occurredAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return syncprotocol.BatchItem{Type: itemType, IdempotencyKey: key, OccurredAt: occurredAt, Payload: content}, nil
}

func itemHash(item syncprotocol.BatchItem) string {
	sum := sha256.Sum256(append([]byte(string(item.Type)+"\x00"+item.IdempotencyKey+"\x00"), item.Payload...))
	return hex.EncodeToString(sum[:])
}

func randomID() (string, error) {
	buffer := make([]byte, 18)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func latestTime(values ...string) string {
	var latest string
	var latestValue time.Time
	for _, value := range values {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err == nil && parsed.After(latestValue) {
			latest, latestValue = value, parsed
		}
	}
	if latest == "" {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return latest
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func nativeNumericValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit {
		return ""
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return ""
		}
	}
	return value
}
