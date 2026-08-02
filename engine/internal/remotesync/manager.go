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
	configVersion = 1
	flushInterval = 3 * time.Second
	maxRoomQueue  = 20_000
)

type Config struct {
	Version         int    `json:"version"`
	Enabled         bool   `json:"enabled"`
	Endpoint        string `json:"endpoint"`
	EnrollmentToken string `json:"enrollment_token,omitempty"`
	DeviceToken     string `json:"device_token,omitempty"`
}

type Status struct {
	Enabled       bool   `json:"enabled"`
	Configured    bool   `json:"configured"`
	Endpoint      string `json:"endpoint"`
	Pending       int    `json:"pending"`
	ClientID      string `json:"client_id"`
	LastSuccessAt string `json:"last_success_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	LastErrorAt   string `json:"last_error_at,omitempty"`
}

type outboxFile struct {
	Version int                      `json:"version"`
	Items   []syncprotocol.BatchItem `json:"items"`
}

type Manager struct {
	mu            sync.Mutex
	dataDir       string
	configPath    string
	outboxPath    string
	clientIDPath  string
	config        Config
	clientID      string
	clientName    string
	httpClient    *http.Client
	outbox        []syncprotocol.BatchItem
	lastHashes    map[string]string
	lastSuccessAt string
	lastError     string
	lastErrorAt   string
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
	m.config = Config{Version: configVersion, Endpoint: syncprotocol.DefaultEndpoint}
	content, err := os.ReadFile(m.configPath)
	if err == nil {
		if err := json.Unmarshal(content, &m.config); err != nil {
			return fmt.Errorf("解析远程同步配置失败: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取远程同步配置失败: %w", err)
	}
	if endpoint := strings.TrimSpace(os.Getenv("FUBAO_SYNC_ENDPOINT")); endpoint != "" {
		m.config.Endpoint = endpoint
	}
	if token := strings.TrimSpace(os.Getenv("FUBAO_SYNC_ENROLLMENT_TOKEN")); token != "" {
		m.config.EnrollmentToken = token
		m.config.Enabled = true
	}
	if token := strings.TrimSpace(os.Getenv("FUBAO_SYNC_DEVICE_TOKEN")); token != "" {
		m.config.DeviceToken = token
		m.config.Enabled = true
	}
	endpoint, err := syncprotocol.NormalizeEndpoint(m.config.Endpoint)
	if err != nil {
		return err
	}
	m.config.Version = configVersion
	m.config.Endpoint = endpoint
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
	content, err := os.ReadFile(m.outboxPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取远程同步队列失败: %w", err)
	}
	var payload outboxFile
	if err := json.Unmarshal(content, &payload); err != nil {
		return fmt.Errorf("解析远程同步队列失败: %w", err)
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
	return Status{
		Enabled:       m.config.Enabled,
		Configured:    m.config.DeviceToken != "" || m.config.EnrollmentToken != "",
		Endpoint:      m.config.Endpoint,
		Pending:       len(m.outbox),
		ClientID:      m.clientID,
		LastSuccessAt: m.lastSuccessAt,
		LastError:     m.lastError,
		LastErrorAt:   m.lastErrorAt,
	}
}

func (m *Manager) Run(ctx context.Context) {
	if !m.enabled() {
		return
	}
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
	m.mu.Lock()
	if m.config.DeviceToken != "" {
		m.mu.Unlock()
		return nil
	}
	endpoint := m.config.Endpoint
	enrollmentToken := m.config.EnrollmentToken
	clientID := m.clientID
	clientName := m.clientName
	m.mu.Unlock()
	if enrollmentToken == "" {
		return errors.New("远程同步尚未配置设备注册令牌")
	}
	payload := syncprotocol.RegisterRequest{
		Version: syncprotocol.Version, ClientID: clientID, ClientName: clientName,
		Platform: runtime.GOOS + "/" + runtime.GOARCH, AppVersion: "0.1.0",
	}
	var response syncprotocol.RegisterResponse
	if err := m.postJSON(ctx, endpoint+"/devices/register", enrollmentToken, payload, &response); err != nil {
		return err
	}
	if response.Version != syncprotocol.Version || response.ClientID != clientID || strings.TrimSpace(response.DeviceToken) == "" {
		return errors.New("远程同步设备注册响应无效")
	}
	m.mu.Lock()
	m.config.DeviceToken = strings.TrimSpace(response.DeviceToken)
	m.config.EnrollmentToken = ""
	err := m.saveConfigLocked()
	m.mu.Unlock()
	return err
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
	endpoint, deviceToken, clientID := m.config.Endpoint, m.config.DeviceToken, m.clientID
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
	if err := m.postJSON(ctx, endpoint+"/sync/batch", deviceToken, payload, &response); err != nil {
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

func (m *Manager) postJSON(ctx context.Context, target, token string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "fubao-engine/0.1")
	response, err := m.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("远程同步连接失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("远程同步返回 %d: %s", response.StatusCode, strings.TrimSpace(string(limited)))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output); err != nil {
		return fmt.Errorf("解析远程同步响应失败: %w", err)
	}
	return nil
}

func (m *Manager) SyncSnapshot(roomItems []rooms.Room, monitorItems []redpacket.Monitor, events []redpacket.Event) error {
	if !m.enabled() {
		return nil
	}
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
		updatedAt := latestTime(room.UpdatedAt, monitor.UpdatedAt)
		payload := syncprotocol.RoomState{
			WebRID: webRID, ActualRoomID: firstNonEmpty(monitor.ActualRoomID, room.ActualRoomID),
			Title: firstNonEmpty(monitor.Name, room.Name), StreamerName: firstNonEmpty(monitor.StreamerName, room.StreamerName),
			MonitorStatus: monitor.Status, ConnectionStatus: monitor.ConnectionStatus,
			LiveStatus: monitor.LiveStatus, LiveStatusSource: monitor.LiveStatusSource,
			LiveStartedAt: monitor.LiveStartedAt, LastSeenLiveAt: room.LastSeenLiveAt,
			LastCheckedAt: monitor.LastCheckedAt, LastRedPacketCheckedAt: monitor.LastRedPacketCheckedAt,
			LastEventAt: monitor.LastEventAt, UpdatedAt: updatedAt,
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

func (m *Manager) EnqueueEvent(event redpacket.Event) error {
	if !m.enabled() {
		return nil
	}
	item, ok, err := redPacketItem(event)
	if err != nil || !ok {
		return err
	}
	return m.enqueue([]syncprotocol.BatchItem{item})
}

func redPacketItem(event redpacket.Event) (syncprotocol.BatchItem, bool, error) {
	webRID, packetID := strings.TrimSpace(event.WebRID), strings.TrimSpace(event.PacketID)
	if webRID == "" || packetID == "" {
		return syncprotocol.BatchItem{}, false, nil
	}
	payload := syncprotocol.RedPacket{
		WebRID: webRID, PacketID: packetID, RoomName: event.RoomName,
		StreamerName: event.StreamerName, Title: event.Title, Prize: event.Prize,
		Source: event.Source, DetectedAt: event.DetectedAt, DrawAt: event.DrawAt,
		ExpiresAt: event.ExpiresAt, ParticipantCount: event.ParticipantCount,
		TotalDiamonds: event.TotalDiamonds, ShareCount: event.ShareCount,
	}
	item, err := makeItem(syncprotocol.ItemRedPacket, "red_packet:"+webRID+":"+packetID, event.DetectedAt, payload)
	return item, err == nil, err
}

func (m *Manager) enqueue(items []syncprotocol.BatchItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.config.Enabled {
		return nil
	}
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

func (m *Manager) enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config.Enabled
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
	return writePrivateFile(m.configPath, append(payload, '\n'))
}

func (m *Manager) saveOutboxLocked() error {
	payload, err := json.MarshalIndent(outboxFile{Version: configVersion, Items: m.outbox}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(m.outboxPath, append(payload, '\n'))
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
