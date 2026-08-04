package syncprotocol

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

const (
	Version                 = 1
	DefaultEndpoint         = "https://fbv2.ccvar.com/api/v1"
	DefaultFallbackEndpoint = "https://fbv2.ccvar.com:8087/api/v1"
	MaxBatchItems           = 200
	MaxChanges              = 200
	DeviceAccessUploadOnly  = "upload_only"
	DeviceAccessFull        = "full"
)

type ItemType string

const (
	ItemRoomState ItemType = "room_state"
	ItemRedPacket ItemType = "red_packet"
)

type BatchItem struct {
	Type           ItemType        `json:"type"`
	IdempotencyKey string          `json:"idempotency_key"`
	OccurredAt     string          `json:"occurred_at"`
	Payload        json.RawMessage `json:"payload"`
}

type BatchRequest struct {
	Version   int         `json:"version"`
	RequestID string      `json:"request_id"`
	ClientID  string      `json:"client_id"`
	SentAt    string      `json:"sent_at"`
	Items     []BatchItem `json:"items"`
}

type BatchResponse struct {
	Version   int      `json:"version"`
	RequestID string   `json:"request_id"`
	Accepted  int      `json:"accepted"`
	Acked     []string `json:"acked"`
	Duplicate bool     `json:"duplicate,omitempty"`
}

type RegisterRequest struct {
	Version    int    `json:"version"`
	ClientID   string `json:"client_id"`
	ClientName string `json:"client_name,omitempty"`
	Platform   string `json:"platform,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
}

type RegisterResponse struct {
	Version     int    `json:"version"`
	ClientID    string `json:"client_id"`
	DeviceToken string `json:"device_token"`
	AccessMode  string `json:"access_mode"`
}

type Change struct {
	Cursor         int64           `json:"cursor"`
	Type           ItemType        `json:"type"`
	OriginClientID string          `json:"origin_client_id"`
	ChangedAt      string          `json:"changed_at"`
	Payload        json.RawMessage `json:"payload"`
}

type ChangesResponse struct {
	Version    int      `json:"version"`
	NextCursor int64    `json:"next_cursor"`
	HasMore    bool     `json:"has_more"`
	Changes    []Change `json:"changes"`
}

type RoomState struct {
	WebRID                 string `json:"web_rid"`
	ActualRoomID           string `json:"actual_room_id,omitempty"`
	Title                  string `json:"title,omitempty"`
	StreamerName           string `json:"streamer_name,omitempty"`
	MonitorStatus          string `json:"monitor_status,omitempty"`
	ConnectionStatus       string `json:"connection_status,omitempty"`
	LiveStatus             string `json:"live_status,omitempty"`
	LiveStatusSource       string `json:"live_status_source,omitempty"`
	LiveStartedAt          string `json:"live_started_at,omitempty"`
	LastSeenLiveAt         string `json:"last_seen_live_at,omitempty"`
	LastCheckedAt          string `json:"last_checked_at,omitempty"`
	LastRedPacketCheckedAt string `json:"last_red_packet_checked_at,omitempty"`
	LastEventAt            string `json:"last_event_at,omitempty"`
	MetricsVersion         int    `json:"metrics_version,omitempty"`
	LiveSessionCount       int    `json:"live_session_count,omitempty"`
	RedPacketCount         int    `json:"red_packet_count,omitempty"`
	UpdatedAt              string `json:"updated_at"`
}

type RedPacket struct {
	WebRID           string  `json:"web_rid"`
	PacketID         string  `json:"packet_id"`
	ActualRoomID     string  `json:"actual_room_id,omitempty"`
	JoinBoxID        string  `json:"join_box_id,omitempty"`
	AnchorID         string  `json:"anchor_id,omitempty"`
	BoxType          string  `json:"box_type,omitempty"`
	SendTime         string  `json:"send_time,omitempty"`
	DelayTime        string  `json:"delay_time,omitempty"`
	RoomName         string  `json:"room_name,omitempty"`
	StreamerName     string  `json:"streamer_name,omitempty"`
	Title            string  `json:"title,omitempty"`
	Prize            string  `json:"prize,omitempty"`
	Source           string  `json:"source,omitempty"`
	DetectedAt       string  `json:"detected_at"`
	DrawAt           string  `json:"draw_at,omitempty"`
	ExpiresAt        string  `json:"expires_at,omitempty"`
	ParticipantCount int     `json:"participant_count,omitempty"`
	TotalDiamonds    float64 `json:"total_diamonds,omitempty"`
	ShareCount       int     `json:"share_count,omitempty"`
}

// CenterRoomExclusion is a server-authoritative tombstone for a room that must
// not be accepted into the shared center library again. Display metadata is
// intentionally limited to safe room fields.
type CenterRoomExclusion struct {
	WebRID       string `json:"web_rid"`
	ActualRoomID string `json:"actual_room_id,omitempty"`
	Name         string `json:"name,omitempty"`
	StreamerName string `json:"streamer_name,omitempty"`
	Reason       string `json:"reason,omitempty"`
	ExcludedAt   string `json:"excluded_at"`
}

type CenterRoomExclusionsRequest struct {
	Items []CenterRoomExclusion `json:"items"`
}

type CenterRoomExclusionsResponse struct {
	Items []CenterRoomExclusion `json:"items"`
}

type CenterRoomExclusionRestoreRequest struct {
	WebRID string `json:"web_rid"`
}

func NormalizeEndpoint(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		value = DefaultEndpoint
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("远程同步地址必须是有效的 HTTPS 地址")
	}
	return value, nil
}

func ValidateBatch(req BatchRequest) error {
	if req.Version != Version {
		return errors.New("同步协议版本不兼容")
	}
	if strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.ClientID) == "" {
		return errors.New("同步请求标识不完整")
	}
	if len(req.Items) == 0 || len(req.Items) > MaxBatchItems {
		return errors.New("同步批次数量无效")
	}
	if timestamp, err := time.Parse(time.RFC3339Nano, req.SentAt); err != nil || time.Since(timestamp) > 24*time.Hour || time.Until(timestamp) > 10*time.Minute {
		return errors.New("同步请求时间无效")
	}
	for _, item := range req.Items {
		if item.Type != ItemRoomState && item.Type != ItemRedPacket {
			return errors.New("同步数据类型无效")
		}
		if strings.TrimSpace(item.IdempotencyKey) == "" || len(item.Payload) == 0 || !json.Valid(item.Payload) {
			return errors.New("同步数据内容无效")
		}
	}
	return nil
}
