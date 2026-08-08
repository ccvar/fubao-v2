package syncserver

import "fmt"

func (s *Store) migrationStatements() []string {
	if s.driver == StorageMySQL {
		return mysqlMigrationStatements()
	}
	return sqliteMigrationStatements()
}

func sqliteMigrationStatements() []string {
	return []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE IF NOT EXISTS devices (client_id TEXT PRIMARY KEY, client_name TEXT NOT NULL DEFAULT '', platform TEXT NOT NULL DEFAULT '', app_version TEXT NOT NULL DEFAULT '', token_hash TEXT NOT NULL UNIQUE, access_mode TEXT NOT NULL DEFAULT 'full', created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS rooms (web_rid TEXT PRIMARY KEY, actual_room_id TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', streamer_name TEXT NOT NULL DEFAULT '', monitor_status TEXT NOT NULL DEFAULT '', connection_status TEXT NOT NULL DEFAULT '', live_status TEXT NOT NULL DEFAULT '', live_status_source TEXT NOT NULL DEFAULT '', live_started_at TEXT NOT NULL DEFAULT '', last_seen_live_at TEXT NOT NULL DEFAULT '', last_checked_at TEXT NOT NULL DEFAULT '', last_red_packet_checked_at TEXT NOT NULL DEFAULT '', last_event_at TEXT NOT NULL DEFAULT '', metrics_version INTEGER NOT NULL DEFAULT 1, live_session_count INTEGER NOT NULL DEFAULT 0, red_packet_count INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL, last_client_id TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS room_live_sessions (web_rid TEXT NOT NULL, session_key TEXT NOT NULL, first_seen_at TEXT NOT NULL, PRIMARY KEY (web_rid, session_key), FOREIGN KEY (web_rid) REFERENCES rooms(web_rid) ON DELETE CASCADE)`,
		`CREATE TABLE IF NOT EXISTS red_packet_events (web_rid TEXT NOT NULL, packet_id TEXT NOT NULL, actual_room_id TEXT NOT NULL DEFAULT '', join_box_id TEXT NOT NULL DEFAULT '', anchor_id TEXT NOT NULL DEFAULT '', box_type TEXT NOT NULL DEFAULT '', send_time TEXT NOT NULL DEFAULT '', delay_time TEXT NOT NULL DEFAULT '', room_name TEXT NOT NULL DEFAULT '', streamer_name TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', prize TEXT NOT NULL DEFAULT '', participation_condition TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '', detected_at TEXT NOT NULL, draw_at TEXT NOT NULL DEFAULT '', expires_at TEXT NOT NULL DEFAULT '', participant_count INTEGER NOT NULL DEFAULT 0, total_diamonds REAL NOT NULL DEFAULT 0, share_count INTEGER NOT NULL DEFAULT 0, last_seen_at TEXT NOT NULL, last_client_id TEXT NOT NULL, PRIMARY KEY (web_rid, packet_id))`,
		`CREATE TABLE IF NOT EXISTS sync_requests (client_id TEXT NOT NULL, request_id TEXT NOT NULL, received_at TEXT NOT NULL, item_count INTEGER NOT NULL, PRIMARY KEY (client_id, request_id))`,
		`CREATE TABLE IF NOT EXISTS sync_changes (seq INTEGER PRIMARY KEY AUTOINCREMENT, item_type TEXT NOT NULL, entity_key TEXT NOT NULL, origin_client_id TEXT NOT NULL, changed_at TEXT NOT NULL, payload_json TEXT NOT NULL, UNIQUE(item_type, entity_key))`,
		`CREATE TABLE IF NOT EXISTS room_exclusions (web_rid TEXT PRIMARY KEY, actual_room_id TEXT NOT NULL DEFAULT '', name TEXT NOT NULL DEFAULT '', streamer_name TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '', excluded_at TEXT NOT NULL, excluded_by_client_id TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS red_packet_detected_at_idx ON red_packet_events(detected_at DESC)`,
		`CREATE INDEX IF NOT EXISTS rooms_live_status_idx ON rooms(live_status, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS sync_changes_seq_idx ON sync_changes(seq)`,
		`CREATE INDEX IF NOT EXISTS sync_changes_entity_idx ON sync_changes(item_type, entity_key, seq DESC)`,
		`CREATE INDEX IF NOT EXISTS room_exclusions_actual_room_idx ON room_exclusions(actual_room_id)`,
		`CREATE INDEX IF NOT EXISTS room_live_sessions_room_idx ON room_live_sessions(web_rid)`,
	}
}

func mysqlMigrationStatements() []string {
	const suffix = ` ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`
	return []string{
		`CREATE TABLE IF NOT EXISTS devices (client_id VARCHAR(128) PRIMARY KEY, client_name VARCHAR(160) NOT NULL DEFAULT '', platform VARCHAR(80) NOT NULL DEFAULT '', app_version VARCHAR(40) NOT NULL DEFAULT '', token_hash CHAR(64) NOT NULL UNIQUE, access_mode VARCHAR(20) NOT NULL DEFAULT 'full', created_at VARCHAR(40) NOT NULL, last_seen_at VARCHAR(40) NOT NULL)` + suffix,
		`CREATE TABLE IF NOT EXISTS rooms (web_rid VARCHAR(32) PRIMARY KEY, actual_room_id VARCHAR(64) NOT NULL DEFAULT '', title VARCHAR(500) NOT NULL DEFAULT '', streamer_name VARCHAR(200) NOT NULL DEFAULT '', monitor_status VARCHAR(40) NOT NULL DEFAULT '', connection_status VARCHAR(40) NOT NULL DEFAULT '', live_status VARCHAR(40) NOT NULL DEFAULT '', live_status_source VARCHAR(80) NOT NULL DEFAULT '', live_started_at VARCHAR(40) NOT NULL DEFAULT '', last_seen_live_at VARCHAR(40) NOT NULL DEFAULT '', last_checked_at VARCHAR(40) NOT NULL DEFAULT '', last_red_packet_checked_at VARCHAR(40) NOT NULL DEFAULT '', last_event_at VARCHAR(40) NOT NULL DEFAULT '', metrics_version INT NOT NULL DEFAULT 1, live_session_count BIGINT NOT NULL DEFAULT 0, red_packet_count BIGINT NOT NULL DEFAULT 0, updated_at VARCHAR(40) NOT NULL, last_client_id VARCHAR(128) NOT NULL, KEY rooms_live_status_idx (live_status, updated_at DESC))` + suffix,
		`CREATE TABLE IF NOT EXISTS room_live_sessions (web_rid VARCHAR(32) NOT NULL, session_key VARCHAR(128) NOT NULL, first_seen_at VARCHAR(40) NOT NULL, PRIMARY KEY (web_rid, session_key), KEY room_live_sessions_room_idx (web_rid), CONSTRAINT room_live_sessions_room_fk FOREIGN KEY (web_rid) REFERENCES rooms(web_rid) ON DELETE CASCADE)` + suffix,
		`CREATE TABLE IF NOT EXISTS red_packet_events (web_rid VARCHAR(32) NOT NULL, packet_id VARCHAR(256) NOT NULL, actual_room_id VARCHAR(32) NOT NULL DEFAULT '', join_box_id VARCHAR(32) NOT NULL DEFAULT '', anchor_id VARCHAR(32) NOT NULL DEFAULT '', box_type VARCHAR(16) NOT NULL DEFAULT '', send_time VARCHAR(32) NOT NULL DEFAULT '', delay_time VARCHAR(32) NOT NULL DEFAULT '', room_name VARCHAR(500) NOT NULL DEFAULT '', streamer_name VARCHAR(200) NOT NULL DEFAULT '', title VARCHAR(500) NOT NULL DEFAULT '', prize VARCHAR(500) NOT NULL DEFAULT '', participation_condition VARCHAR(200) NOT NULL DEFAULT '', source VARCHAR(80) NOT NULL DEFAULT '', detected_at VARCHAR(40) NOT NULL, draw_at VARCHAR(40) NOT NULL DEFAULT '', expires_at VARCHAR(40) NOT NULL DEFAULT '', participant_count BIGINT NOT NULL DEFAULT 0, total_diamonds DOUBLE NOT NULL DEFAULT 0, share_count BIGINT NOT NULL DEFAULT 0, last_seen_at VARCHAR(40) NOT NULL, last_client_id VARCHAR(128) NOT NULL, PRIMARY KEY (web_rid, packet_id), KEY red_packet_detected_at_idx (detected_at DESC))` + suffix,
		`CREATE TABLE IF NOT EXISTS sync_requests (client_id VARCHAR(128) NOT NULL, request_id VARCHAR(256) NOT NULL, received_at VARCHAR(40) NOT NULL, item_count INT NOT NULL, PRIMARY KEY (client_id, request_id))` + suffix,
		`CREATE TABLE IF NOT EXISTS sync_changes (seq BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, item_type VARCHAR(32) NOT NULL, entity_key VARCHAR(320) NOT NULL, origin_client_id VARCHAR(128) NOT NULL, changed_at VARCHAR(40) NOT NULL, payload_json LONGTEXT NOT NULL, UNIQUE KEY sync_changes_entity_unique (item_type, entity_key), KEY sync_changes_entity_idx (item_type, entity_key, seq DESC))` + suffix,
		`CREATE TABLE IF NOT EXISTS room_exclusions (web_rid VARCHAR(32) PRIMARY KEY, actual_room_id VARCHAR(32) NOT NULL DEFAULT '', name VARCHAR(500) NOT NULL DEFAULT '', streamer_name VARCHAR(200) NOT NULL DEFAULT '', reason VARCHAR(500) NOT NULL DEFAULT '', excluded_at VARCHAR(40) NOT NULL, excluded_by_client_id VARCHAR(128) NOT NULL, KEY room_exclusions_actual_room_idx (actual_room_id))` + suffix,
	}
}

func (s *Store) textColumnDefinition(size int, defaultValue string) string {
	if s.driver == StorageMySQL {
		return fmt.Sprintf("VARCHAR(%d) NOT NULL DEFAULT '%s'", size, defaultValue)
	}
	return fmt.Sprintf("TEXT NOT NULL DEFAULT '%s'", defaultValue)
}

func (s *Store) integerColumnDefinition(defaultValue int) string {
	if s.driver == StorageMySQL {
		return fmt.Sprintf("BIGINT NOT NULL DEFAULT %d", defaultValue)
	}
	return fmt.Sprintf("INTEGER NOT NULL DEFAULT %d", defaultValue)
}

func (s *Store) backfillLiveSessionsQuery() string {
	if s.driver == StorageMySQL {
		return `INSERT IGNORE INTO room_live_sessions(web_rid, session_key, first_seen_at) SELECT web_rid, CONCAT('started:', live_started_at), COALESCE(NULLIF(last_seen_live_at, ''), updated_at) FROM rooms WHERE live_status = 'live' AND live_started_at <> ''`
	}
	return `INSERT OR IGNORE INTO room_live_sessions(web_rid, session_key, first_seen_at) SELECT web_rid, 'started:' || live_started_at, COALESCE(NULLIF(last_seen_live_at, ''), updated_at) FROM rooms WHERE live_status = 'live' AND live_started_at <> ''`
}

func (s *Store) backfillLiveSessionCountsQuery() string {
	if s.driver == StorageMySQL {
		return `UPDATE rooms SET live_session_count = GREATEST(live_session_count, (SELECT COUNT(*) FROM room_live_sessions WHERE room_live_sessions.web_rid = rooms.web_rid))`
	}
	return `UPDATE rooms SET live_session_count = MAX(live_session_count, (SELECT COUNT(*) FROM room_live_sessions WHERE room_live_sessions.web_rid = rooms.web_rid))`
}

func registerDeviceQuery(driver string, replace bool) string {
	if driver == StorageMySQL {
		if !replace {
			return `INSERT IGNORE INTO devices(client_id, client_name, platform, app_version, token_hash, access_mode, created_at, last_seen_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`
		}
		return `INSERT INTO devices(client_id, client_name, platform, app_version, token_hash, access_mode, created_at, last_seen_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE client_name=VALUES(client_name), platform=VALUES(platform), app_version=VALUES(app_version), token_hash=VALUES(token_hash), access_mode=VALUES(access_mode), last_seen_at=VALUES(last_seen_at)`
	}
	if !replace {
		return `INSERT INTO devices(client_id, client_name, platform, app_version, token_hash, access_mode, created_at, last_seen_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(client_id) DO NOTHING`
	}
	return `INSERT INTO devices(client_id, client_name, platform, app_version, token_hash, access_mode, created_at, last_seen_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(client_id) DO UPDATE SET client_name=excluded.client_name, platform=excluded.platform, app_version=excluded.app_version, token_hash=excluded.token_hash, access_mode=excluded.access_mode, last_seen_at=excluded.last_seen_at`
}

func upsertRoomQuery(driver string) string {
	const insert = `INSERT INTO rooms(web_rid, actual_room_id, title, streamer_name, monitor_status, connection_status, live_status, live_status_source, live_started_at, last_seen_live_at, last_checked_at, last_red_packet_checked_at, last_event_at, metrics_version, live_session_count, red_packet_count, updated_at, last_client_id) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, (SELECT COUNT(*) FROM red_packet_events WHERE web_rid = ?), ?, ?)`
	if driver == StorageMySQL {
		return insert + ` ON DUPLICATE KEY UPDATE actual_room_id=COALESCE(NULLIF(VALUES(actual_room_id), ''), actual_room_id), title=COALESCE(NULLIF(VALUES(title), ''), title), streamer_name=COALESCE(NULLIF(VALUES(streamer_name), ''), streamer_name), monitor_status=VALUES(monitor_status), connection_status=VALUES(connection_status), live_status=VALUES(live_status), live_status_source=VALUES(live_status_source), live_started_at=COALESCE(NULLIF(VALUES(live_started_at), ''), live_started_at), last_seen_live_at=COALESCE(NULLIF(VALUES(last_seen_live_at), ''), last_seen_live_at), last_checked_at=COALESCE(NULLIF(VALUES(last_checked_at), ''), last_checked_at), last_red_packet_checked_at=COALESCE(NULLIF(VALUES(last_red_packet_checked_at), ''), last_red_packet_checked_at), last_event_at=COALESCE(NULLIF(VALUES(last_event_at), ''), last_event_at), metrics_version=1, live_session_count=GREATEST(live_session_count, VALUES(live_session_count)), red_packet_count=GREATEST(red_packet_count, VALUES(red_packet_count)), updated_at=VALUES(updated_at), last_client_id=VALUES(last_client_id)`
	}
	return insert + ` ON CONFLICT(web_rid) DO UPDATE SET actual_room_id=COALESCE(NULLIF(excluded.actual_room_id, ''), rooms.actual_room_id), title=COALESCE(NULLIF(excluded.title, ''), rooms.title), streamer_name=COALESCE(NULLIF(excluded.streamer_name, ''), rooms.streamer_name), monitor_status=excluded.monitor_status, connection_status=excluded.connection_status, live_status=excluded.live_status, live_status_source=excluded.live_status_source, live_started_at=COALESCE(NULLIF(excluded.live_started_at, ''), rooms.live_started_at), last_seen_live_at=COALESCE(NULLIF(excluded.last_seen_live_at, ''), rooms.last_seen_live_at), last_checked_at=COALESCE(NULLIF(excluded.last_checked_at, ''), rooms.last_checked_at), last_red_packet_checked_at=COALESCE(NULLIF(excluded.last_red_packet_checked_at, ''), rooms.last_red_packet_checked_at), last_event_at=COALESCE(NULLIF(excluded.last_event_at, ''), rooms.last_event_at), metrics_version=1, live_session_count=MAX(rooms.live_session_count, excluded.live_session_count), red_packet_count=MAX(rooms.red_packet_count, excluded.red_packet_count), updated_at=excluded.updated_at, last_client_id=excluded.last_client_id`
}

func insertLiveSessionQuery(driver string) string {
	if driver == StorageMySQL {
		return `INSERT IGNORE INTO room_live_sessions(web_rid, session_key, first_seen_at) VALUES(?, ?, ?)`
	}
	return `INSERT OR IGNORE INTO room_live_sessions(web_rid, session_key, first_seen_at) VALUES(?, ?, ?)`
}

func appendChangeSQLiteQuery() string {
	return `INSERT INTO sync_changes(item_type, entity_key, origin_client_id, changed_at, payload_json) VALUES(?, ?, ?, ?, ?) ON CONFLICT(item_type, entity_key) DO UPDATE SET seq=excluded.seq, origin_client_id=excluded.origin_client_id, changed_at=excluded.changed_at, payload_json=excluded.payload_json`
}

func previousChangeQuery(driver string) string {
	if driver == StorageMySQL {
		return `SELECT payload_json FROM sync_changes WHERE item_type = ? AND entity_key = ? ORDER BY seq DESC LIMIT 1 FOR UPDATE`
	}
	return `SELECT payload_json FROM sync_changes WHERE item_type = ? AND entity_key = ? ORDER BY seq DESC LIMIT 1`
}

func upsertRedPacketQuery(driver string) string {
	const insert = `INSERT INTO red_packet_events(web_rid, packet_id, actual_room_id, join_box_id, anchor_id, box_type, send_time, delay_time, room_name, streamer_name, title, prize, participation_condition, source, detected_at, draw_at, expires_at, participant_count, total_diamonds, share_count, last_seen_at, last_client_id) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if driver == StorageMySQL {
		return insert + ` ON DUPLICATE KEY UPDATE actual_room_id=COALESCE(NULLIF(VALUES(actual_room_id), ''), actual_room_id), join_box_id=COALESCE(NULLIF(VALUES(join_box_id), ''), join_box_id), anchor_id=COALESCE(NULLIF(VALUES(anchor_id), ''), anchor_id), box_type=COALESCE(NULLIF(VALUES(box_type), ''), box_type), send_time=COALESCE(NULLIF(VALUES(send_time), ''), send_time), delay_time=COALESCE(NULLIF(VALUES(delay_time), ''), delay_time), room_name=COALESCE(NULLIF(VALUES(room_name), ''), room_name), streamer_name=COALESCE(NULLIF(VALUES(streamer_name), ''), streamer_name), title=COALESCE(NULLIF(VALUES(title), ''), title), prize=COALESCE(NULLIF(VALUES(prize), ''), prize), participation_condition=COALESCE(NULLIF(VALUES(participation_condition), ''), participation_condition), source=COALESCE(NULLIF(VALUES(source), ''), source), detected_at=LEAST(detected_at, VALUES(detected_at)), draw_at=COALESCE(NULLIF(VALUES(draw_at), ''), draw_at), expires_at=COALESCE(NULLIF(VALUES(expires_at), ''), expires_at), participant_count=GREATEST(participant_count, VALUES(participant_count)), total_diamonds=GREATEST(total_diamonds, VALUES(total_diamonds)), share_count=GREATEST(share_count, VALUES(share_count)), last_seen_at=VALUES(last_seen_at), last_client_id=VALUES(last_client_id)`
	}
	return insert + ` ON CONFLICT(web_rid, packet_id) DO UPDATE SET actual_room_id=COALESCE(NULLIF(excluded.actual_room_id, ''), red_packet_events.actual_room_id), join_box_id=COALESCE(NULLIF(excluded.join_box_id, ''), red_packet_events.join_box_id), anchor_id=COALESCE(NULLIF(excluded.anchor_id, ''), red_packet_events.anchor_id), box_type=COALESCE(NULLIF(excluded.box_type, ''), red_packet_events.box_type), send_time=COALESCE(NULLIF(excluded.send_time, ''), red_packet_events.send_time), delay_time=COALESCE(NULLIF(excluded.delay_time, ''), red_packet_events.delay_time), room_name=COALESCE(NULLIF(excluded.room_name, ''), red_packet_events.room_name), streamer_name=COALESCE(NULLIF(excluded.streamer_name, ''), red_packet_events.streamer_name), title=COALESCE(NULLIF(excluded.title, ''), red_packet_events.title), prize=COALESCE(NULLIF(excluded.prize, ''), red_packet_events.prize), participation_condition=COALESCE(NULLIF(excluded.participation_condition, ''), red_packet_events.participation_condition), source=COALESCE(NULLIF(excluded.source, ''), red_packet_events.source), detected_at=MIN(red_packet_events.detected_at, excluded.detected_at), draw_at=COALESCE(NULLIF(excluded.draw_at, ''), red_packet_events.draw_at), expires_at=COALESCE(NULLIF(excluded.expires_at, ''), red_packet_events.expires_at), participant_count=MAX(red_packet_events.participant_count, excluded.participant_count), total_diamonds=MAX(red_packet_events.total_diamonds, excluded.total_diamonds), share_count=MAX(red_packet_events.share_count, excluded.share_count), last_seen_at=excluded.last_seen_at, last_client_id=excluded.last_client_id`
}

func upsertRoomExclusionQuery(driver string) string {
	const insert = `INSERT INTO room_exclusions(web_rid, actual_room_id, name, streamer_name, reason, excluded_at, excluded_by_client_id) VALUES(?, ?, ?, ?, ?, ?, ?)`
	if driver == StorageMySQL {
		return insert + ` ON DUPLICATE KEY UPDATE actual_room_id=COALESCE(NULLIF(VALUES(actual_room_id), ''), actual_room_id), name=COALESCE(NULLIF(VALUES(name), ''), name), streamer_name=COALESCE(NULLIF(VALUES(streamer_name), ''), streamer_name), reason=COALESCE(NULLIF(VALUES(reason), ''), reason), excluded_at=GREATEST(excluded_at, VALUES(excluded_at)), excluded_by_client_id=VALUES(excluded_by_client_id)`
	}
	return insert + ` ON CONFLICT(web_rid) DO UPDATE SET actual_room_id=COALESCE(NULLIF(excluded.actual_room_id, ''), room_exclusions.actual_room_id), name=COALESCE(NULLIF(excluded.name, ''), room_exclusions.name), streamer_name=COALESCE(NULLIF(excluded.streamer_name, ''), room_exclusions.streamer_name), reason=COALESCE(NULLIF(excluded.reason, ''), room_exclusions.reason), excluded_at=MAX(room_exclusions.excluded_at, excluded.excluded_at), excluded_by_client_id=excluded.excluded_by_client_id`
}

func deleteRoomChangesQuery(driver string) string {
	if driver == StorageMySQL {
		return `DELETE FROM sync_changes WHERE (item_type = ? AND entity_key = ?) OR (item_type = ? AND (JSON_UNQUOTE(JSON_EXTRACT(payload_json, '$.web_rid')) = ? OR (? <> '' AND JSON_UNQUOTE(JSON_EXTRACT(payload_json, '$.actual_room_id')) = ?)))`
	}
	return `DELETE FROM sync_changes WHERE (item_type = ? AND entity_key = ?) OR (item_type = ? AND (json_extract(payload_json, '$.web_rid') = ? OR (? <> '' AND json_extract(payload_json, '$.actual_room_id') = ?)))`
}
