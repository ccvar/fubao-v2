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

type DeviceAuthorization struct {
	ClientID   string
	AccessMode string
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
			access_mode TEXT NOT NULL DEFAULT 'full',
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
		`CREATE TABLE IF NOT EXISTS sync_changes (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			item_type TEXT NOT NULL,
			entity_key TEXT NOT NULL,
			origin_client_id TEXT NOT NULL,
			changed_at TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			UNIQUE(item_type, entity_key)
		)`,
		`CREATE INDEX IF NOT EXISTS red_packet_detected_at_idx ON red_packet_events(detected_at DESC)`,
		`CREATE INDEX IF NOT EXISTS rooms_live_status_idx ON rooms(live_status, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS sync_changes_seq_idx ON sync_changes(seq)`,
		`CREATE INDEX IF NOT EXISTS sync_changes_entity_idx ON sync_changes(item_type, entity_key, seq DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("初始化同步数据库失败: %w", err)
		}
	}
	if err := s.ensureColumn(ctx, "devices", "access_mode", "TEXT NOT NULL DEFAULT 'full'"); err != nil {
		return err
	}
	return s.backfillChanges(ctx)
}

func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition); err != nil {
		return fmt.Errorf("升级同步数据库字段失败: %w", err)
	}
	return nil
}

func (s *Store) RegisterDevice(ctx context.Context, req syncprotocol.RegisterRequest) (syncprotocol.RegisterResponse, error) {
	return s.registerDevice(ctx, req, syncprotocol.DeviceAccessFull, true)
}

func (s *Store) RegisterUploadDevice(ctx context.Context, req syncprotocol.RegisterRequest) (syncprotocol.RegisterResponse, error) {
	if !strings.HasPrefix(strings.TrimSpace(req.ClientID), "desktop_") {
		return syncprotocol.RegisterResponse{}, errors.New("自动上传客户端标识无效")
	}
	return s.registerDevice(ctx, req, syncprotocol.DeviceAccessUploadOnly, false)
}

func (s *Store) registerDevice(ctx context.Context, req syncprotocol.RegisterRequest, accessMode string, replace bool) (syncprotocol.RegisterResponse, error) {
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" || len(clientID) > 128 {
		return syncprotocol.RegisterResponse{}, errors.New("客户端标识无效")
	}
	token, err := randomToken()
	if err != nil {
		return syncprotocol.RegisterResponse{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	query := `
		INSERT INTO devices(client_id, client_name, platform, app_version, token_hash, access_mode, created_at, last_seen_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(client_id) DO NOTHING`
	if replace {
		query = `
			INSERT INTO devices(client_id, client_name, platform, app_version, token_hash, access_mode, created_at, last_seen_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(client_id) DO UPDATE SET
				client_name=excluded.client_name,
				platform=excluded.platform,
				app_version=excluded.app_version,
				token_hash=excluded.token_hash,
				access_mode=excluded.access_mode,
				last_seen_at=excluded.last_seen_at`
	}
	result, err := s.db.ExecContext(ctx, query,
		clientID, safeText(req.ClientName, 160), safeText(req.Platform, 80), safeText(req.AppVersion, 40), tokenHash(token), accessMode, now, now)
	if err != nil {
		return syncprotocol.RegisterResponse{}, fmt.Errorf("注册同步设备失败: %w", err)
	}
	if !replace {
		inserted, err := result.RowsAffected()
		if err != nil {
			return syncprotocol.RegisterResponse{}, err
		}
		if inserted == 0 {
			return syncprotocol.RegisterResponse{}, errors.New("上传设备已经注册，请绑定同步 KEY 恢复完整设备身份")
		}
	}
	return syncprotocol.RegisterResponse{Version: syncprotocol.Version, ClientID: clientID, DeviceToken: token, AccessMode: accessMode}, nil
}

func (s *Store) AuthorizeDevice(ctx context.Context, token string) (DeviceAuthorization, error) {
	var result DeviceAuthorization
	err := s.db.QueryRowContext(ctx, `SELECT client_id, access_mode FROM devices WHERE token_hash = ?`, tokenHash(token)).Scan(&result.ClientID, &result.AccessMode)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceAuthorization{}, errors.New("同步设备令牌无效")
	}
	if err != nil {
		return DeviceAuthorization{}, fmt.Errorf("读取同步设备失败: %w", err)
	}
	if result.AccessMode == "" {
		result.AccessMode = syncprotocol.DeviceAccessFull
	}
	return result, nil
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
			canonical, err := readRoomTx(ctx, tx, payload.WebRID)
			if err != nil {
				return syncprotocol.BatchResponse{}, err
			}
			if err := appendChangeTx(ctx, tx, syncprotocol.ItemRoomState, canonical.WebRID, clientID, canonical.UpdatedAt, canonical); err != nil {
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
			canonical, err := readRedPacketTx(ctx, tx, payload.WebRID, payload.PacketID)
			if err != nil {
				return syncprotocol.BatchResponse{}, err
			}
			if err := appendChangeTx(ctx, tx, syncprotocol.ItemRedPacket, canonical.WebRID+"\x00"+canonical.PacketID, clientID, s.now().UTC().Format(time.RFC3339Nano), canonical); err != nil {
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
			last_client_id=excluded.last_client_id
		WHERE excluded.updated_at >= rooms.updated_at`,
		room.WebRID, safeText(room.ActualRoomID, 64), safeText(room.Title, 500), safeText(room.StreamerName, 200), safeText(room.MonitorStatus, 40), safeText(room.ConnectionStatus, 40), safeText(room.LiveStatus, 40), safeText(room.LiveStatusSource, 80), room.LiveStartedAt, room.LastSeenLiveAt, room.LastCheckedAt, room.LastRedPacketCheckedAt, room.LastEventAt, room.UpdatedAt, clientID)
	return err
}

func appendChangeTx(ctx context.Context, tx *sql.Tx, itemType syncprotocol.ItemType, entityKey, clientID, changedAt string, payload any) error {
	content, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT payload_json FROM sync_changes WHERE item_type = ? AND entity_key = ? ORDER BY seq DESC LIMIT 1`, itemType, entityKey).Scan(&previous)
	if err == nil && previous == string(content) {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO sync_changes(item_type, entity_key, origin_client_id, changed_at, payload_json)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(item_type, entity_key) DO UPDATE SET
			seq=excluded.seq,
			origin_client_id=excluded.origin_client_id,
			changed_at=excluded.changed_at,
			payload_json=excluded.payload_json`, itemType, entityKey, clientID, changedAt, string(content))
	return err
}

func readRoomTx(ctx context.Context, tx *sql.Tx, webRID string) (syncprotocol.RoomState, error) {
	var room syncprotocol.RoomState
	err := tx.QueryRowContext(ctx, `
		SELECT web_rid, actual_room_id, title, streamer_name, monitor_status, connection_status,
			live_status, live_status_source, live_started_at, last_seen_live_at, last_checked_at,
			last_red_packet_checked_at, last_event_at, updated_at
		FROM rooms WHERE web_rid = ?`, strings.TrimSpace(webRID)).Scan(
		&room.WebRID, &room.ActualRoomID, &room.Title, &room.StreamerName, &room.MonitorStatus,
		&room.ConnectionStatus, &room.LiveStatus, &room.LiveStatusSource, &room.LiveStartedAt,
		&room.LastSeenLiveAt, &room.LastCheckedAt, &room.LastRedPacketCheckedAt, &room.LastEventAt, &room.UpdatedAt,
	)
	return room, err
}

func readRedPacketTx(ctx context.Context, tx *sql.Tx, webRID, packetID string) (syncprotocol.RedPacket, error) {
	var packet syncprotocol.RedPacket
	err := tx.QueryRowContext(ctx, `
		SELECT web_rid, packet_id, room_name, streamer_name, title, prize, source, detected_at,
			draw_at, expires_at, participant_count, total_diamonds, share_count
		FROM red_packet_events WHERE web_rid = ? AND packet_id = ?`, strings.TrimSpace(webRID), strings.TrimSpace(packetID)).Scan(
		&packet.WebRID, &packet.PacketID, &packet.RoomName, &packet.StreamerName, &packet.Title,
		&packet.Prize, &packet.Source, &packet.DetectedAt, &packet.DrawAt, &packet.ExpiresAt,
		&packet.ParticipantCount, &packet.TotalDiamonds, &packet.ShareCount,
	)
	return packet, err
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

func (s *Store) GetChanges(ctx context.Context, cursor int64, limit int) (syncprotocol.ChangesResponse, error) {
	return s.GetChangesByType(ctx, cursor, limit, "")
}

func (s *Store) GetChangesByType(ctx context.Context, cursor int64, limit int, itemType syncprotocol.ItemType) (syncprotocol.ChangesResponse, error) {
	if cursor < 0 {
		return syncprotocol.ChangesResponse{}, errors.New("同步游标无效")
	}
	if limit <= 0 || limit > syncprotocol.MaxChanges {
		limit = syncprotocol.MaxChanges
	}
	query := `SELECT seq, item_type, origin_client_id, changed_at, payload_json
		FROM sync_changes WHERE seq > ? ORDER BY seq ASC LIMIT ?`
	args := []any{cursor, limit + 1}
	if itemType != "" {
		if itemType != syncprotocol.ItemRoomState && itemType != syncprotocol.ItemRedPacket {
			return syncprotocol.ChangesResponse{}, errors.New("中心库增量类型无效")
		}
		query = `SELECT seq, item_type, origin_client_id, changed_at, payload_json
			FROM sync_changes WHERE seq > ? AND item_type = ? ORDER BY seq ASC LIMIT ?`
		args = []any{cursor, itemType, limit + 1}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return syncprotocol.ChangesResponse{}, fmt.Errorf("读取中心库增量失败: %w", err)
	}
	defer rows.Close()
	result := syncprotocol.ChangesResponse{Version: syncprotocol.Version, NextCursor: cursor, Changes: make([]syncprotocol.Change, 0, limit)}
	for rows.Next() {
		var change syncprotocol.Change
		var payload []byte
		if err := rows.Scan(&change.Cursor, &change.Type, &change.OriginClientID, &change.ChangedAt, &payload); err != nil {
			return syncprotocol.ChangesResponse{}, err
		}
		if len(result.Changes) == limit {
			result.HasMore = true
			break
		}
		if !json.Valid(payload) {
			return syncprotocol.ChangesResponse{}, errors.New("中心库增量数据损坏")
		}
		change.Payload = append(json.RawMessage(nil), payload...)
		result.Changes = append(result.Changes, change)
		result.NextCursor = change.Cursor
	}
	if err := rows.Err(); err != nil {
		return syncprotocol.ChangesResponse{}, err
	}
	return result, nil
}

func (s *Store) TouchDevice(ctx context.Context, clientID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET last_seen_at = ? WHERE client_id = ?`, s.now().UTC().Format(time.RFC3339Nano), clientID)
	return err
}

func (s *Store) backfillChanges(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_changes`).Scan(&count); err != nil || count > 0 {
		return err
	}
	type seed struct {
		itemType  syncprotocol.ItemType
		entityKey string
		clientID  string
		at        string
		payload   any
	}
	seeds := make([]seed, 0)
	roomRows, err := s.db.QueryContext(ctx, `
		SELECT web_rid, actual_room_id, title, streamer_name, monitor_status, connection_status,
			live_status, live_status_source, live_started_at, last_seen_live_at, last_checked_at,
			last_red_packet_checked_at, last_event_at, updated_at, last_client_id FROM rooms ORDER BY web_rid`)
	if err != nil {
		return err
	}
	for roomRows.Next() {
		var room syncprotocol.RoomState
		var clientID string
		if err := roomRows.Scan(&room.WebRID, &room.ActualRoomID, &room.Title, &room.StreamerName, &room.MonitorStatus,
			&room.ConnectionStatus, &room.LiveStatus, &room.LiveStatusSource, &room.LiveStartedAt, &room.LastSeenLiveAt,
			&room.LastCheckedAt, &room.LastRedPacketCheckedAt, &room.LastEventAt, &room.UpdatedAt, &clientID); err != nil {
			roomRows.Close()
			return err
		}
		seeds = append(seeds, seed{itemType: syncprotocol.ItemRoomState, entityKey: room.WebRID, clientID: clientID, at: room.UpdatedAt, payload: room})
	}
	if err := roomRows.Close(); err != nil {
		return err
	}
	packetRows, err := s.db.QueryContext(ctx, `
		SELECT web_rid, packet_id, room_name, streamer_name, title, prize, source, detected_at,
			draw_at, expires_at, participant_count, total_diamonds, share_count, last_seen_at, last_client_id
		FROM red_packet_events ORDER BY detected_at, web_rid, packet_id`)
	if err != nil {
		return err
	}
	for packetRows.Next() {
		var packet syncprotocol.RedPacket
		var changedAt, clientID string
		if err := packetRows.Scan(&packet.WebRID, &packet.PacketID, &packet.RoomName, &packet.StreamerName, &packet.Title,
			&packet.Prize, &packet.Source, &packet.DetectedAt, &packet.DrawAt, &packet.ExpiresAt,
			&packet.ParticipantCount, &packet.TotalDiamonds, &packet.ShareCount, &changedAt, &clientID); err != nil {
			packetRows.Close()
			return err
		}
		seeds = append(seeds, seed{itemType: syncprotocol.ItemRedPacket, entityKey: packet.WebRID + "\x00" + packet.PacketID, clientID: clientID, at: changedAt, payload: packet})
	}
	if err := packetRows.Close(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range seeds {
		if err := appendChangeTx(ctx, tx, item.itemType, item.entityKey, item.clientID, item.at, item.payload); err != nil {
			return err
		}
	}
	return tx.Commit()
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
