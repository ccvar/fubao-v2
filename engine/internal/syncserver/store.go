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
	"strings"
	"time"

	"fubao.ccvar.com/engine/internal/syncprotocol"
)

type Store struct {
	db     *sql.DB
	driver string
	now    func() time.Time
}

type Stats struct {
	Devices        int `json:"devices"`
	Rooms          int `json:"rooms"`
	RedPacket      int `json:"red_packets"`
	RoomExclusions int `json:"room_exclusions"`
}

type DeviceAuthorization struct {
	ClientID   string
	AccessMode string
}

const centerServerOriginClientID = "center-server"

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	statements := s.migrationStatements()
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("初始化同步数据库失败: %w", err)
		}
	}
	if err := s.ensureColumn(ctx, "devices", "access_mode", s.textColumnDefinition(20, "full")); err != nil {
		return err
	}
	for column, size := range map[string]int{"actual_room_id": 32, "join_box_id": 32, "anchor_id": 32, "box_type": 16, "send_time": 32, "delay_time": 32, "participation_condition": 200} {
		if err := s.ensureColumn(ctx, "red_packet_events", column, s.textColumnDefinition(size, "")); err != nil {
			return err
		}
	}
	for column, definition := range map[string]string{
		"metrics_version":    s.integerColumnDefinition(1),
		"live_session_count": s.integerColumnDefinition(0),
		"red_packet_count":   s.integerColumnDefinition(0),
	} {
		if err := s.ensureColumn(ctx, "rooms", column, definition); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE rooms SET red_packet_count = (
			SELECT COUNT(*) FROM red_packet_events WHERE red_packet_events.web_rid = rooms.web_rid
		), metrics_version = 1`); err != nil {
		return fmt.Errorf("回填中心库直播间统计失败: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, s.backfillLiveSessionsQuery()); err != nil {
		return fmt.Errorf("回填中心库开播场次失败: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, s.backfillLiveSessionCountsQuery()); err != nil {
		return fmt.Errorf("回填中心库开播次数失败: %w", err)
	}
	if err := s.backfillChanges(ctx); err != nil {
		return err
	}
	return s.refreshRoomMetricChanges(ctx)
}

func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	if s.driver == StorageMySQL {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, column).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
	} else {
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
	query := registerDeviceQuery(s.driver, replace)
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
			accepted, err := upsertRoom(ctx, tx, s.driver, clientID, payload)
			if err != nil {
				return syncprotocol.BatchResponse{}, err
			}
			if !accepted {
				continue
			}
			canonical, err := readRoomTx(ctx, tx, payload.WebRID)
			if err != nil {
				return syncprotocol.BatchResponse{}, err
			}
			if err := appendChangeTx(ctx, tx, s.driver, syncprotocol.ItemRoomState, canonical.WebRID, centerServerOriginClientID, canonical.UpdatedAt, canonical); err != nil {
				return syncprotocol.BatchResponse{}, err
			}
		case syncprotocol.ItemRedPacket:
			var payload syncprotocol.RedPacket
			if err := json.Unmarshal(item.Payload, &payload); err != nil {
				return syncprotocol.BatchResponse{}, errors.New("红包同步数据无效")
			}
			accepted, err := upsertRedPacket(ctx, tx, s.driver, clientID, payload, s.now().UTC().Format(time.RFC3339Nano))
			if err != nil {
				return syncprotocol.BatchResponse{}, err
			}
			if !accepted {
				continue
			}
			canonical, err := readRedPacketTx(ctx, tx, payload.WebRID, payload.PacketID)
			if err != nil {
				return syncprotocol.BatchResponse{}, err
			}
			if err := appendChangeTx(ctx, tx, s.driver, syncprotocol.ItemRedPacket, canonical.WebRID+"\x00"+canonical.PacketID, clientID, s.now().UTC().Format(time.RFC3339Nano), canonical); err != nil {
				return syncprotocol.BatchResponse{}, err
			}
			room, err := readRoomTx(ctx, tx, canonical.WebRID)
			if err == nil {
				if err := appendChangeTx(ctx, tx, s.driver, syncprotocol.ItemRoomState, room.WebRID, centerServerOriginClientID, s.now().UTC().Format(time.RFC3339Nano), room); err != nil {
					return syncprotocol.BatchResponse{}, err
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
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

func upsertRoom(ctx context.Context, tx *sql.Tx, driver, clientID string, room syncprotocol.RoomState) (bool, error) {
	room.WebRID = strings.TrimSpace(room.WebRID)
	if room.WebRID == "" || len(room.WebRID) > 32 {
		return false, errors.New("直播间标识无效")
	}
	if strings.TrimSpace(room.UpdatedAt) == "" {
		return false, errors.New("直播间更新时间无效")
	}
	excluded, err := centerRoomExcludedTx(ctx, tx, room.WebRID, room.ActualRoomID)
	if err != nil || excluded {
		return false, err
	}
	var priorLiveStatus, priorLiveStartedAt, priorUpdatedAt string
	var priorLiveSessionCount int
	priorErr := tx.QueryRowContext(ctx, `SELECT live_status, live_started_at, live_session_count, updated_at FROM rooms WHERE web_rid = ?`, room.WebRID).
		Scan(&priorLiveStatus, &priorLiveStartedAt, &priorLiveSessionCount, &priorUpdatedAt)
	if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
		return false, priorErr
	}
	stateAccepted := errors.Is(priorErr, sql.ErrNoRows) || room.UpdatedAt >= priorUpdatedAt
	if !stateAccepted {
		return false, nil
	}
	seedLiveSessionCount := max(0, room.LiveSessionCount)
	_, err = tx.ExecContext(ctx, upsertRoomQuery(driver),
		room.WebRID, safeText(room.ActualRoomID, 64), safeText(room.Title, 500), safeText(room.StreamerName, 200), safeText(room.MonitorStatus, 40), safeText(room.ConnectionStatus, 40), safeText(room.LiveStatus, 40), safeText(room.LiveStatusSource, 80), room.LiveStartedAt, room.LastSeenLiveAt, room.LastCheckedAt, room.LastRedPacketCheckedAt, room.LastEventAt, seedLiveSessionCount, room.WebRID, room.UpdatedAt, clientID)
	if err != nil || strings.ToLower(strings.TrimSpace(room.LiveStatus)) != "live" {
		return err == nil, err
	}
	newSession := false
	startedAt := strings.TrimSpace(room.LiveStartedAt)
	if startedAt != "" {
		result, insertErr := tx.ExecContext(ctx, insertLiveSessionQuery(driver), room.WebRID, "started:"+startedAt, room.UpdatedAt)
		if insertErr != nil {
			return false, insertErr
		}
		inserted, insertErr := result.RowsAffected()
		if insertErr != nil {
			return false, insertErr
		}
		newSession = inserted > 0 && (priorLiveStatus != "live" || priorLiveStartedAt != startedAt || priorLiveSessionCount == 0)
	} else {
		newSession = priorLiveStatus != "live"
	}
	if newSession && seedLiveSessionCount <= priorLiveSessionCount {
		if _, err := tx.ExecContext(ctx, `UPDATE rooms SET live_session_count = ? WHERE web_rid = ?`, priorLiveSessionCount+1, room.WebRID); err != nil {
			return false, err
		}
	}
	return true, nil
}

func appendChangeTx(ctx context.Context, tx *sql.Tx, driver string, itemType syncprotocol.ItemType, entityKey, clientID, changedAt string, payload any) error {
	content, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var previous string
	err = tx.QueryRowContext(ctx, previousChangeQuery(driver), itemType, entityKey).Scan(&previous)
	if err == nil && previous == string(content) {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if driver == StorageMySQL {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sync_changes WHERE item_type = ? AND entity_key = ?`, itemType, entityKey); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sync_changes(item_type, entity_key, origin_client_id, changed_at, payload_json) VALUES(?, ?, ?, ?, ?)`, itemType, entityKey, clientID, changedAt, string(content))
		return err
	}
	_, err = tx.ExecContext(ctx, appendChangeSQLiteQuery(), itemType, entityKey, clientID, changedAt, string(content))
	return err
}

func readRoomTx(ctx context.Context, tx *sql.Tx, webRID string) (syncprotocol.RoomState, error) {
	var room syncprotocol.RoomState
	err := tx.QueryRowContext(ctx, `
		SELECT web_rid, actual_room_id, title, streamer_name, monitor_status, connection_status,
			live_status, live_status_source, live_started_at, last_seen_live_at, last_checked_at,
			last_red_packet_checked_at, last_event_at, metrics_version, live_session_count, red_packet_count, updated_at
		FROM rooms WHERE web_rid = ?`, strings.TrimSpace(webRID)).Scan(
		&room.WebRID, &room.ActualRoomID, &room.Title, &room.StreamerName, &room.MonitorStatus,
		&room.ConnectionStatus, &room.LiveStatus, &room.LiveStatusSource, &room.LiveStartedAt,
		&room.LastSeenLiveAt, &room.LastCheckedAt, &room.LastRedPacketCheckedAt, &room.LastEventAt,
		&room.MetricsVersion, &room.LiveSessionCount, &room.RedPacketCount, &room.UpdatedAt,
	)
	return room, err
}

func readRedPacketTx(ctx context.Context, tx *sql.Tx, webRID, packetID string) (syncprotocol.RedPacket, error) {
	var packet syncprotocol.RedPacket
	err := tx.QueryRowContext(ctx, `
		SELECT web_rid, packet_id, actual_room_id, join_box_id, anchor_id, box_type, send_time, delay_time,
			room_name, streamer_name, title, prize, participation_condition, source, detected_at,
			draw_at, expires_at, participant_count, total_diamonds, share_count
		FROM red_packet_events WHERE web_rid = ? AND packet_id = ?`, strings.TrimSpace(webRID), strings.TrimSpace(packetID)).Scan(
		&packet.WebRID, &packet.PacketID, &packet.ActualRoomID, &packet.JoinBoxID, &packet.AnchorID,
		&packet.BoxType, &packet.SendTime, &packet.DelayTime, &packet.RoomName, &packet.StreamerName, &packet.Title,
		&packet.Prize, &packet.Condition, &packet.Source, &packet.DetectedAt, &packet.DrawAt, &packet.ExpiresAt,
		&packet.ParticipantCount, &packet.TotalDiamonds, &packet.ShareCount,
	)
	return packet, err
}

func upsertRedPacket(ctx context.Context, tx *sql.Tx, driver, clientID string, packet syncprotocol.RedPacket, seenAt string) (bool, error) {
	packet.WebRID = strings.TrimSpace(packet.WebRID)
	packet.PacketID = strings.TrimSpace(packet.PacketID)
	if packet.WebRID == "" || packet.PacketID == "" || len(packet.WebRID) > 32 || len(packet.PacketID) > 256 {
		return false, errors.New("红包标识无效")
	}
	if strings.TrimSpace(packet.DetectedAt) == "" {
		return false, errors.New("红包发现时间无效")
	}
	excluded, err := centerRoomExcludedTx(ctx, tx, packet.WebRID, packet.ActualRoomID)
	if err != nil || excluded {
		return false, err
	}
	_, err = tx.ExecContext(ctx, upsertRedPacketQuery(driver),
		packet.WebRID, packet.PacketID,
		safeNumericText(packet.ActualRoomID, 32), safeNumericText(packet.JoinBoxID, 32), safeNumericText(packet.AnchorID, 32),
		safeNumericText(packet.BoxType, 16), safeNumericText(packet.SendTime, 32), safeNumericText(packet.DelayTime, 32),
		safeText(packet.RoomName, 500), safeText(packet.StreamerName, 200), safeText(packet.Title, 500), safeText(packet.Prize, 500), safeText(packet.Condition, 200), safeText(packet.Source, 80), packet.DetectedAt, packet.DrawAt, packet.ExpiresAt, packet.ParticipantCount, packet.TotalDiamonds, packet.ShareCount, seenAt, clientID)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE rooms SET metrics_version = 1,
			red_packet_count = (SELECT COUNT(*) FROM red_packet_events WHERE red_packet_events.web_rid = rooms.web_rid),
			last_event_at = CASE WHEN last_event_at = '' OR last_event_at < ? THEN ? ELSE last_event_at END
		WHERE web_rid = ?`, packet.DetectedAt, packet.DetectedAt, packet.WebRID); err != nil {
		return false, err
	}
	return true, nil
}

func centerRoomExcludedTx(ctx context.Context, tx *sql.Tx, webRID, actualRoomID string) (bool, error) {
	webRID = strings.TrimSpace(webRID)
	actualRoomID = strings.TrimSpace(actualRoomID)
	var found int
	err := tx.QueryRowContext(ctx, `
		SELECT 1 FROM room_exclusions
		WHERE web_rid = ? OR (? <> '' AND actual_room_id = ?)
		LIMIT 1`, webRID, actualRoomID, actualRoomID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) CenterRoomExclusions(ctx context.Context) ([]syncprotocol.CenterRoomExclusion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT web_rid, actual_room_id, name, streamer_name, reason, excluded_at
		FROM room_exclusions ORDER BY excluded_at DESC, web_rid ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]syncprotocol.CenterRoomExclusion, 0)
	for rows.Next() {
		var item syncprotocol.CenterRoomExclusion
		if err := rows.Scan(&item.WebRID, &item.ActualRoomID, &item.Name, &item.StreamerName, &item.Reason, &item.ExcludedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ExcludeCenterRooms(ctx context.Context, clientID string, items []syncprotocol.CenterRoomExclusion) error {
	if len(items) == 0 || len(items) > syncprotocol.MaxBatchItems {
		return errors.New("中心库排除记录数量无效")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		item.WebRID = strings.TrimSpace(item.WebRID)
		if item.WebRID == "" || len(item.WebRID) > 32 {
			return errors.New("中心库排除的直播间标识无效")
		}
		if strings.TrimSpace(item.ExcludedAt) == "" {
			item.ExcludedAt = s.now().UTC().Format(time.RFC3339Nano)
		}
		if _, err := time.Parse(time.RFC3339Nano, item.ExcludedAt); err != nil {
			return errors.New("中心库排除时间无效")
		}
		if _, err := tx.ExecContext(ctx, upsertRoomExclusionQuery(s.driver),
			item.WebRID, safeNumericText(item.ActualRoomID, 32), safeText(item.Name, 500), safeText(item.StreamerName, 200), safeText(item.Reason, 500), item.ExcludedAt, clientID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM rooms WHERE web_rid = ? OR (? <> '' AND actual_room_id = ?)`, item.WebRID, item.ActualRoomID, item.ActualRoomID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM red_packet_events WHERE web_rid = ? OR (? <> '' AND actual_room_id = ?)`, item.WebRID, item.ActualRoomID, item.ActualRoomID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, deleteRoomChangesQuery(s.driver), syncprotocol.ItemRoomState, item.WebRID, syncprotocol.ItemRedPacket,
			item.WebRID, item.ActualRoomID, item.ActualRoomID); err != nil {
			return err
		}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE devices SET last_seen_at = ? WHERE client_id = ?`, now, clientID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RestoreCenterRoomExclusion(ctx context.Context, clientID, webRID string) error {
	webRID = strings.TrimSpace(webRID)
	if webRID == "" || len(webRID) > 32 {
		return errors.New("中心库排除的直播间标识无效")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM room_exclusions WHERE web_rid = ?`, webRID)
	if err != nil {
		return err
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if removed == 0 {
		return errors.New("中心库排除记录不存在")
	}
	return s.TouchDevice(ctx, clientID)
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
			last_red_packet_checked_at, last_event_at, metrics_version, live_session_count, red_packet_count,
			updated_at, last_client_id FROM rooms ORDER BY web_rid`)
	if err != nil {
		return err
	}
	for roomRows.Next() {
		var room syncprotocol.RoomState
		var clientID string
		if err := roomRows.Scan(&room.WebRID, &room.ActualRoomID, &room.Title, &room.StreamerName, &room.MonitorStatus,
			&room.ConnectionStatus, &room.LiveStatus, &room.LiveStatusSource, &room.LiveStartedAt, &room.LastSeenLiveAt,
			&room.LastCheckedAt, &room.LastRedPacketCheckedAt, &room.LastEventAt, &room.MetricsVersion,
			&room.LiveSessionCount, &room.RedPacketCount, &room.UpdatedAt, &clientID); err != nil {
			roomRows.Close()
			return err
		}
		seeds = append(seeds, seed{itemType: syncprotocol.ItemRoomState, entityKey: room.WebRID, clientID: centerServerOriginClientID, at: room.UpdatedAt, payload: room})
	}
	if err := roomRows.Close(); err != nil {
		return err
	}
	packetRows, err := s.db.QueryContext(ctx, `
		SELECT web_rid, packet_id, actual_room_id, join_box_id, anchor_id, box_type, send_time, delay_time,
			room_name, streamer_name, title, prize, participation_condition, source, detected_at,
			draw_at, expires_at, participant_count, total_diamonds, share_count, last_seen_at, last_client_id
		FROM red_packet_events ORDER BY detected_at, web_rid, packet_id`)
	if err != nil {
		return err
	}
	for packetRows.Next() {
		var packet syncprotocol.RedPacket
		var changedAt, clientID string
		if err := packetRows.Scan(&packet.WebRID, &packet.PacketID, &packet.ActualRoomID, &packet.JoinBoxID, &packet.AnchorID,
			&packet.BoxType, &packet.SendTime, &packet.DelayTime, &packet.RoomName, &packet.StreamerName, &packet.Title,
			&packet.Prize, &packet.Condition, &packet.Source, &packet.DetectedAt, &packet.DrawAt, &packet.ExpiresAt,
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
		if err := appendChangeTx(ctx, tx, s.driver, item.itemType, item.entityKey, item.clientID, item.at, item.payload); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// refreshRoomMetricChanges republishes canonical room payloads after schema
// upgrades and aggregate changes. appendChangeTx is content-aware, so normal
// restarts do not advance cursors unless the safe room payload actually
// changed.
func (s *Store) refreshRoomMetricChanges(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT web_rid, actual_room_id, title, streamer_name, monitor_status, connection_status,
			live_status, live_status_source, live_started_at, last_seen_live_at, last_checked_at,
			last_red_packet_checked_at, last_event_at, metrics_version, live_session_count, red_packet_count,
			updated_at, last_client_id FROM rooms ORDER BY web_rid`)
	if err != nil {
		return err
	}
	type roomChange struct {
		room     syncprotocol.RoomState
		clientID string
	}
	changes := make([]roomChange, 0)
	for rows.Next() {
		var change roomChange
		room := &change.room
		if err := rows.Scan(&room.WebRID, &room.ActualRoomID, &room.Title, &room.StreamerName, &room.MonitorStatus,
			&room.ConnectionStatus, &room.LiveStatus, &room.LiveStatusSource, &room.LiveStartedAt, &room.LastSeenLiveAt,
			&room.LastCheckedAt, &room.LastRedPacketCheckedAt, &room.LastEventAt, &room.MetricsVersion,
			&room.LiveSessionCount, &room.RedPacketCount, &room.UpdatedAt, &change.clientID); err != nil {
			rows.Close()
			return err
		}
		changes = append(changes, change)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changedAt := s.now().UTC().Format(time.RFC3339Nano)
	for _, change := range changes {
		if err := appendChangeTx(ctx, tx, s.driver, syncprotocol.ItemRoomState, change.room.WebRID, centerServerOriginClientID, changedAt, change.room); err != nil {
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
		`SELECT COUNT(*) FROM room_exclusions`:   &result.RoomExclusions,
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

func safeNumericText(value string, limit int) string {
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

func itemKeys(items []syncprotocol.BatchItem) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.IdempotencyKey)
	}
	return keys
}
