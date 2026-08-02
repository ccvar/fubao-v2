package syncserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fubao.ccvar.com/engine/internal/syncprotocol"
	_ "modernc.org/sqlite"
)

type Store struct {
	db  *sql.DB
	now func() time.Time
}

type Stats struct {
	Devices   int `json:"devices"`
	Rooms     int `json:"rooms"`
	RedPacket int `json:"red_packets"`
}

func OpenStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("同步数据库路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("创建同步数据目录失败: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开同步数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, now: time.Now}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE IF NOT EXISTS devices (
			client_id TEXT PRIMARY KEY,
			client_name TEXT NOT NULL DEFAULT '',
			platform TEXT NOT NULL DEFAULT '',
			app_version TEXT NOT NULL DEFAULT '',
			token_hash TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS rooms (
			web_rid TEXT PRIMARY KEY,
			actual_room_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			streamer_name TEXT NOT NULL DEFAULT '',
			monitor_status TEXT NOT NULL DEFAULT '',
			connection_status TEXT NOT NULL DEFAULT '',
			live_status TEXT NOT NULL DEFAULT '',
			live_status_source TEXT NOT NULL DEFAULT '',
			live_started_at TEXT NOT NULL DEFAULT '',
			last_seen_live_at TEXT NOT NULL DEFAULT '',
			last_checked_at TEXT NOT NULL DEFAULT '',
			last_red_packet_checked_at TEXT NOT NULL DEFAULT '',
			last_event_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL,
			last_client_id TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS red_packet_events (
			web_rid TEXT NOT NULL,
			packet_id TEXT NOT NULL,
			room_name TEXT NOT NULL DEFAULT '',
			streamer_name TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			prize TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			detected_at TEXT NOT NULL,
			draw_at TEXT NOT NULL DEFAULT '',
			expires_at TEXT NOT NULL DEFAULT '',
			participant_count INTEGER NOT NULL DEFAULT 0,
			total_diamonds REAL NOT NULL DEFAULT 0,
			share_count INTEGER NOT NULL DEFAULT 0,
			last_seen_at TEXT NOT NULL,
			last_client_id TEXT NOT NULL,
			PRIMARY KEY (web_rid, packet_id)
		)`,
		`CREATE TABLE IF NOT EXISTS sync_requests (
			client_id TEXT NOT NULL,
			request_id TEXT NOT NULL,
			received_at TEXT NOT NULL,
			item_count INTEGER NOT NULL,
			PRIMARY KEY (client_id, request_id)
		)`,
		`CREATE INDEX IF NOT EXISTS red_packet_detected_at_idx ON red_packet_events(detected_at DESC)`,
		`CREATE INDEX IF NOT EXISTS rooms_live_status_idx ON rooms(live_status, updated_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("初始化同步数据库失败: %w", err)
		}
	}
	return nil
}

func (s *Store) RegisterDevice(ctx context.Context, req syncprotocol.RegisterRequest) (syncprotocol.RegisterResponse, error) {
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" || len(clientID) > 128 {
		return syncprotocol.RegisterResponse{}, errors.New("客户端标识无效")
	}
	token, err := randomToken()
	if err != nil {
		return syncprotocol.RegisterResponse{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO devices(client_id, client_name, platform, app_version, token_hash, created_at, last_seen_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(client_id) DO UPDATE SET
			client_name=excluded.client_name,
			platform=excluded.platform,
			app_version=excluded.app_version,
			token_hash=excluded.token_hash,
			last_seen_at=excluded.last_seen_at`,
		clientID, safeText(req.ClientName, 160), safeText(req.Platform, 80), safeText(req.AppVersion, 40), tokenHash(token), now, now)
	if err != nil {
		return syncprotocol.RegisterResponse{}, fmt.Errorf("注册同步设备失败: %w", err)
	}
	return syncprotocol.RegisterResponse{Version: syncprotocol.Version, ClientID: clientID, DeviceToken: token}, nil
}

func (s *Store) AuthorizeDevice(ctx context.Context, token string) (string, error) {
	var clientID string
	err := s.db.QueryRowContext(ctx, `SELECT client_id FROM devices WHERE token_hash = ?`, tokenHash(token)).Scan(&clientID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("同步设备令牌无效")
	}
	if err != nil {
		return "", fmt.Errorf("读取同步设备失败: %w", err)
	}
	return clientID, nil
}

func (s *Store) ApplyBatch(ctx context.Context, clientID string, req syncprotocol.BatchRequest) (syncprotocol.BatchResponse, error) {
	if err := syncprotocol.ValidateBatch(req); err != nil {
		return syncprotocol.BatchResponse{}, err
	}
	if req.ClientID != clientID {
		return syncprotocol.BatchResponse{}, errors.New("同步设备与请求不匹配")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return syncprotocol.BatchResponse{}, err
	}
	defer tx.Rollback()
	acked := itemKeys(req.Items)
	var duplicate int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM sync_requests WHERE client_id = ? AND request_id = ?`, clientID, req.RequestID).Scan(&duplicate)
	if err == nil {
		return syncprotocol.BatchResponse{Version: syncprotocol.Version, RequestID: req.RequestID, Accepted: len(req.Items), Acked: acked, Duplicate: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return syncprotocol.BatchResponse{}, err
	}
	for _, item := range req.Items {
		switch item.Type {
		case syncprotocol.ItemRoomState:
			var payload syncprotocol.RoomState
			if err := json.Unmarshal(item.Payload, &payload); err != nil {
				return syncprotocol.BatchResponse{}, errors.New("直播间同步数据无效")
			}
			if err := upsertRoom(ctx, tx, clientID, payload); err != nil {
				return syncprotocol.BatchResponse{}, err
			}
		case syncprotocol.ItemRedPacket:
			var payload syncprotocol.RedPacket
			if err := json.Unmarshal(item.Payload, &payload); err != nil {
				return syncprotocol.BatchResponse{}, errors.New("红包同步数据无效")
			}
			if err := upsertRedPacket(ctx, tx, clientID, payload, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
				return syncprotocol.BatchResponse{}, err
			}
		}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO sync_requests(client_id, request_id, received_at, item_count) VALUES(?, ?, ?, ?)`, clientID, req.RequestID, now, len(req.Items)); err != nil {
		return syncprotocol.BatchResponse{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE devices SET last_seen_at = ? WHERE client_id = ?`, now, clientID); err != nil {
		return syncprotocol.BatchResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return syncprotocol.BatchResponse{}, err
	}
	return syncprotocol.BatchResponse{Version: syncprotocol.Version, RequestID: req.RequestID, Accepted: len(req.Items), Acked: acked}, nil
}

func upsertRoom(ctx context.Context, tx *sql.Tx, clientID string, room syncprotocol.RoomState) error {
	room.WebRID = strings.TrimSpace(room.WebRID)
	if room.WebRID == "" || len(room.WebRID) > 32 {
		return errors.New("直播间标识无效")
	}
	if strings.TrimSpace(room.UpdatedAt) == "" {
		return errors.New("直播间更新时间无效")
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO rooms(web_rid, actual_room_id, title, streamer_name, monitor_status, connection_status, live_status, live_status_source, live_started_at, last_seen_live_at, last_checked_at, last_red_packet_checked_at, last_event_at, updated_at, last_client_id)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(web_rid) DO UPDATE SET
			actual_room_id=COALESCE(NULLIF(excluded.actual_room_id, ''), rooms.actual_room_id),
			title=COALESCE(NULLIF(excluded.title, ''), rooms.title),
			streamer_name=COALESCE(NULLIF(excluded.streamer_name, ''), rooms.streamer_name),
			monitor_status=excluded.monitor_status,
			connection_status=excluded.connection_status,
			live_status=excluded.live_status,
			live_status_source=excluded.live_status_source,
			live_started_at=COALESCE(NULLIF(excluded.live_started_at, ''), rooms.live_started_at),
			last_seen_live_at=COALESCE(NULLIF(excluded.last_seen_live_at, ''), rooms.last_seen_live_at),
			last_checked_at=COALESCE(NULLIF(excluded.last_checked_at, ''), rooms.last_checked_at),
			last_red_packet_checked_at=COALESCE(NULLIF(excluded.last_red_packet_checked_at, ''), rooms.last_red_packet_checked_at),
			last_event_at=COALESCE(NULLIF(excluded.last_event_at, ''), rooms.last_event_at),
			updated_at=excluded.updated_at,
			last_client_id=excluded.last_client_id`,
		room.WebRID, safeText(room.ActualRoomID, 64), safeText(room.Title, 500), safeText(room.StreamerName, 200), safeText(room.MonitorStatus, 40), safeText(room.ConnectionStatus, 40), safeText(room.LiveStatus, 40), safeText(room.LiveStatusSource, 80), room.LiveStartedAt, room.LastSeenLiveAt, room.LastCheckedAt, room.LastRedPacketCheckedAt, room.LastEventAt, room.UpdatedAt, clientID)
	return err
}

func upsertRedPacket(ctx context.Context, tx *sql.Tx, clientID string, packet syncprotocol.RedPacket, seenAt string) error {
	packet.WebRID = strings.TrimSpace(packet.WebRID)
	packet.PacketID = strings.TrimSpace(packet.PacketID)
	if packet.WebRID == "" || packet.PacketID == "" || len(packet.WebRID) > 32 || len(packet.PacketID) > 256 {
		return errors.New("红包标识无效")
	}
	if strings.TrimSpace(packet.DetectedAt) == "" {
		return errors.New("红包发现时间无效")
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO red_packet_events(web_rid, packet_id, room_name, streamer_name, title, prize, source, detected_at, draw_at, expires_at, participant_count, total_diamonds, share_count, last_seen_at, last_client_id)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(web_rid, packet_id) DO UPDATE SET
			room_name=COALESCE(NULLIF(excluded.room_name, ''), red_packet_events.room_name),
			streamer_name=COALESCE(NULLIF(excluded.streamer_name, ''), red_packet_events.streamer_name),
			title=COALESCE(NULLIF(excluded.title, ''), red_packet_events.title),
			prize=COALESCE(NULLIF(excluded.prize, ''), red_packet_events.prize),
			source=COALESCE(NULLIF(excluded.source, ''), red_packet_events.source),
			detected_at=MIN(red_packet_events.detected_at, excluded.detected_at),
			draw_at=COALESCE(NULLIF(excluded.draw_at, ''), red_packet_events.draw_at),
			expires_at=COALESCE(NULLIF(excluded.expires_at, ''), red_packet_events.expires_at),
			participant_count=MAX(red_packet_events.participant_count, excluded.participant_count),
			total_diamonds=MAX(red_packet_events.total_diamonds, excluded.total_diamonds),
			share_count=MAX(red_packet_events.share_count, excluded.share_count),
			last_seen_at=excluded.last_seen_at,
			last_client_id=excluded.last_client_id`,
		packet.WebRID, packet.PacketID, safeText(packet.RoomName, 500), safeText(packet.StreamerName, 200), safeText(packet.Title, 500), safeText(packet.Prize, 500), safeText(packet.Source, 80), packet.DetectedAt, packet.DrawAt, packet.ExpiresAt, packet.ParticipantCount, packet.TotalDiamonds, packet.ShareCount, seenAt, clientID)
	return err
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var result Stats
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM devices`:           &result.Devices,
		`SELECT COUNT(*) FROM rooms`:             &result.Rooms,
		`SELECT COUNT(*) FROM red_packet_events`: &result.RedPacket,
	} {
		if err := s.db.QueryRowContext(ctx, query).Scan(target); err != nil {
			return Stats{}, err
		}
	}
	return result, nil
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("生成同步设备令牌失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func safeText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func itemKeys(items []syncprotocol.BatchItem) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.IdempotencyKey)
	}
	return keys
}
