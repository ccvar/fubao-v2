package redpacket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"fubao.ccvar.com/engine/internal/live/httpclient"
	"fubao.ccvar.com/engine/internal/live/poller"
)

const (
	luckyboxEndpoint  = "https://live.douyin.com/webcast/luckybox/box/list/"
	roomEnterEndpoint = "https://live.douyin.com/webcast/room/web/enter/"
)

// LiveProbe is the first stage of a room monitor. Red-packet endpoints must
// only be queried after this probe has positively identified an active live.
type LiveProbe struct {
	Status       string
	RawStatus    string
	ActualRoomID string
	Title        string
	StreamerName string
	Source       string
}

type monitorSource interface {
	poller.SnapshotSource
	ProbeLive(context.Context) (LiveProbe, error)
}

// source checks 福宝's dedicated luckybox endpoint first. The endpoint is
// already scoped to红包活动, so its payloads are explicitly marked before the
// common red-packet parser sees them. lottery_info remains a fallback for
// rooms whose luckybox endpoint is temporarily empty, but the parser still
// requires a red marker and therefore cannot surface 福袋.
type source struct {
	client       *httpclient.Client
	webRID       string
	actualRoomID string
}

func newSource(client *httpclient.Client, webRID, actualRoomID string) monitorSource {
	return &source{client: client, webRID: webRID, actualRoomID: actualRoomID}
}

// ProbeLive ports the first layer used by the legacy 福宝 poller: room/web/enter
// is queried first and room.status decides whether the room is live. Douyin
// currently reports 2 for live and 0/4 for ended/offline rooms.
func (s *source) ProbeLive(ctx context.Context) (LiveProbe, error) {
	params := map[string]string{
		"aid":              "6383",
		"app_name":         "douyin_web",
		"live_id":          "1",
		"device_platform":  "web",
		"language":         "zh-CN",
		"browser_language": "zh-CN",
		"browser_platform": "MacIntel",
		"browser_name":     "Chrome",
		"browser_version":  "124.0.0.0",
		"cookie_enabled":   "true",
		"screen_width":     "1440",
		"screen_height":    "900",
		"web_rid":          s.webRID,
		"enter_from":       "web_live",
	}
	resp, err := s.client.GetSigned(ctx, roomEnterEndpoint, params)
	if err != nil {
		return LiveProbe{Status: "error", Source: "room_web_enter"}, err
	}
	if resp == nil {
		return LiveProbe{Status: "error", Source: "room_web_enter"}, errors.New("开播探测接口没有响应")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return LiveProbe{Status: "error", Source: "room_web_enter"}, fmt.Errorf("开播探测接口返回 HTTP %d", resp.StatusCode)
	}
	var payload map[string]any
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return LiveProbe{Status: "error", Source: "room_web_enter"}, fmt.Errorf("解析开播探测响应失败: %w", err)
	}
	data, _ := payload["data"].(map[string]any)
	roomItems, _ := data["data"].([]any)
	if len(roomItems) == 0 {
		return LiveProbe{Status: "unknown", Source: "room_web_enter"}, nil
	}
	room, _ := roomItems[0].(map[string]any)
	if len(room) == 0 {
		return LiveProbe{Status: "unknown", Source: "room_web_enter"}, nil
	}
	rawStatus := scalarString(room["status"])
	probe := LiveProbe{
		Status:       normalizeLiveStatus(rawStatus),
		RawStatus:    rawStatus,
		ActualRoomID: firstString(data["enter_room_id"], room["id_str"], room["id"]),
		Title:        firstString(room["title"]),
		Source:       "room_web_enter",
	}
	if owner, ok := room["owner"].(map[string]any); ok {
		probe.StreamerName = firstString(owner["nickname"])
	}
	if probe.StreamerName == "" {
		if user, ok := data["user"].(map[string]any); ok {
			probe.StreamerName = firstString(user["nickname"])
		}
	}
	if probe.ActualRoomID != "" {
		s.actualRoomID = probe.ActualRoomID
	}
	return probe, nil
}

func normalizeLiveStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "2", "live", "living", "streaming", "online", "on_live", "started":
		return "live"
	case "0", "4", "ended", "end", "finish", "finished", "closed", "offline", "not_live", "not_living", "replay", "disable", "disabled":
		return "offline"
	default:
		return "unknown"
	}
}

func firstString(values ...any) string {
	for _, value := range values {
		if text := strings.TrimSpace(scalarString(value)); text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}

func (s *source) Fetch(ctx context.Context) ([]poller.Snapshot, error) {
	if packets, err := s.fetchLuckybox(ctx); err == nil && len(packets) > 0 {
		return packets, nil
	}
	return poller.NewHTTPSource(s.client, s.webRID, s.actualRoomID).Fetch(ctx)
}

func (s *source) fetchLuckybox(ctx context.Context) ([]poller.Snapshot, error) {
	if strings.TrimSpace(s.actualRoomID) == "" {
		return nil, nil
	}
	params := map[string]string{
		"room_id":         s.actualRoomID,
		"cursor":          "0",
		"aid":             "6383",
		"app_name":        "douyin_web",
		"live_id":         "1",
		"device_platform": "web",
		"language":        "zh-CN",
		"enter_from":      "link_share",
		"web_rid":         s.webRID,
	}
	resp, err := s.client.GetSigned(ctx, luckyboxEndpoint, params)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("红包接口没有响应")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || len(strings.TrimSpace(string(body))) == 0 {
		return nil, err
	}
	var payload map[string]any
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return nil, err
	}
	data, ok := payload["data"]
	if !ok || data == nil {
		return nil, nil
	}
	// The luckybox endpoint returns one row per box/share. 福宝 groups rows
	// belonging to the same activity before creating a snapshot so the prize is
	// the activity total (for example “总99钻，24份红包”), not the first row's
	// placeholder value (often 0).
	items := aggregateLuckyboxItems(data)
	snapshots := make([]poller.Snapshot, 0, len(items))
	for _, item := range items {
		// luckybox/box/list is the red-packet-specific endpoint. Keep this
		// explicit marker local to the engine; it is never returned to IPC.
		item["activity_kind"] = "red_packet"
		item["activity_type"] = "红包"
		item["source"] = "luckybox_api"
		snapshots = append(snapshots, poller.Snapshot{
			Source:       "luckybox_api",
			RoomID:       s.webRID,
			ActualRoomID: s.actualRoomID,
			Data:         item,
			Raw:          payload,
		})
	}
	return snapshots, nil
}

func luckyboxItems(data any) []map[string]any {
	switch value := data.(type) {
	case []any:
		items := make([]map[string]any, 0, len(value))
		for _, item := range value {
			if mapped, ok := item.(map[string]any); ok && len(mapped) > 0 {
				items = append(items, mapped)
			}
		}
		return items
	case map[string]any:
		for _, key := range []string{"box_list", "boxList", "boxes", "list", "items"} {
			if list, ok := value[key].([]any); ok {
				items := make([]map[string]any, 0, len(list))
				for _, item := range list {
					if mapped, ok := item.(map[string]any); ok && len(mapped) > 0 {
						merged := make(map[string]any, len(value)+len(mapped))
						for childKey, child := range value {
							if childKey != key {
								merged[childKey] = child
							}
						}
						for childKey, child := range mapped {
							merged[childKey] = child
						}
						items = append(items, merged)
					}
				}
				return items
			}
		}
		if len(value) > 0 {
			return []map[string]any{value}
		}
	}
	return nil
}

func aggregateLuckyboxItems(data any) []map[string]any {
	items := luckyboxItems(data)
	if len(items) == 0 {
		return nil
	}

	groups := make(map[string][]map[string]any, len(items))
	order := make([]string, 0, len(items))
	for index, item := range items {
		key := firstDirectString(item,
			"activity_id", "activityId", "lottery_id", "lotteryId",
		)
		if key == "" {
			key = firstDirectString(item,
				"box_id_str", "boxIdStr", "box_id", "boxId", "id",
			)
		}
		if key == "" {
			key = fmt.Sprintf("item:%d", index)
		}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], item)
	}

	aggregates := make([]map[string]any, 0, len(order))
	for _, key := range order {
		group := groups[key]
		aggregate := make(map[string]any, len(group[0])+4)
		for field, value := range group[0] {
			aggregate[field] = value
		}
		aggregate["items"] = group

		diamondTotal := firstDirectPositiveInt(aggregate,
			"total_diamond_count", "totalDiamondCount", "total_diamond", "totalDiamond",
			"total_amount", "totalAmount", "total_diamond_num", "totalDiamondNum",
		)
		if diamondTotal <= 0 {
			for _, item := range group {
				diamondTotal += firstDirectPositiveInt(item,
					"diamond_count", "diamondCount", "diamond_num", "diamondNum",
					"diamond_amount", "diamondAmount", "diamond", "diamonds", "amount",
					"content_amount", "contentAmount", "content_diamond_count", "contentDiamondCount",
				)
			}
		}
		if diamondTotal > 0 {
			aggregate["total_diamond_count"] = diamondTotal
		}

		boxCount := firstDirectPositiveInt(aggregate,
			"packet_count", "packetCount", "red_packet_count", "redPacketCount",
			"redpack_count", "redpackCount", "share_count", "shareCount",
			"content_count", "contentCount", "content_num", "contentNum",
			"box_count", "boxCount", "lucky_box_count", "luckyBoxCount",
			"fubao_count", "fubaoCount", "lucky_count", "luckyCount",
			"box_num", "boxNum", "count", "quantity",
		)
		if boxCount <= 0 {
			boxCount = luckyboxPacketCountFromBizExtra(
				aggregate,
				firstDirectString(aggregate, "business_type", "businessType"),
			)
		}
		if boxCount <= 0 {
			boxCount = len(group)
		}
		if boxCount > 0 {
			aggregate["box_count"] = boxCount
			aggregate["packet_count"] = boxCount
			aggregate["share_count"] = boxCount
		}

		participantCount := 0
		for _, item := range group {
			if value := firstDirectPositiveInt(item,
				"participant_count", "participantCount", "candidate_user_num", "candidateUserNum",
				"user_count", "userCount", "joined_user_count", "joinedUserCount",
			); value > participantCount {
				participantCount = value
			}
		}
		if participantCount > 0 {
			aggregate["participant_count"] = participantCount
		}
		aggregates = append(aggregates, aggregate)
	}
	return aggregates
}

func firstDirectString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		for field, value := range item {
			if strings.EqualFold(field, key) {
				if text := strings.TrimSpace(scalarString(value)); text != "" && text != "<nil>" {
					return text
				}
			}
		}
	}
	return ""
}

func firstDirectPositiveInt(item map[string]any, keys ...string) int {
	for _, key := range keys {
		for field, value := range item {
			if !strings.EqualFold(field, key) {
				continue
			}
			numeric, err := strconv.ParseFloat(strings.TrimSpace(scalarString(value)), 64)
			if err == nil && numeric > 0 {
				return int(numeric)
			}
		}
	}
	return 0
}

// Douyin's luckybox endpoint may keep the real red-packet share count inside
// biz_extra.tags instead of a top-level count field. 福包 resolves the tag
// keyed by business_type first (for example tags["1009"] == "15"), then
// falls back to the first positive non-quiz tag. Without this step a single
// activity row is incorrectly displayed as one share.
func luckyboxPacketCountFromBizExtra(data any, businessType string) int {
	switch value := data.(type) {
	case map[string]any:
		for key, raw := range value {
			if strings.EqualFold(key, "biz_extra") || strings.EqualFold(key, "bizExtra") {
				if count := luckyboxPacketCountFromTags(raw, businessType); count > 0 {
					return count
				}
			}
		}
		for _, child := range value {
			if count := luckyboxPacketCountFromBizExtra(child, businessType); count > 0 {
				return count
			}
		}
	case []map[string]any:
		for _, child := range value {
			if count := luckyboxPacketCountFromBizExtra(child, businessType); count > 0 {
				return count
			}
		}
	case []any:
		for _, child := range value {
			if count := luckyboxPacketCountFromBizExtra(child, businessType); count > 0 {
				return count
			}
		}
	}
	return 0
}

func luckyboxPacketCountFromTags(raw any, businessType string) int {
	parsed := raw
	if text, ok := raw.(string); ok {
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return 0
		}
		parsed = decoded
	}
	mapped, ok := parsed.(map[string]any)
	if !ok {
		return 0
	}
	tagsRaw := mapped["tags"]
	tags, ok := tagsRaw.(map[string]any)
	if !ok {
		return 0
	}
	if businessType != "" {
		if count := positiveInt(tags[businessType]); count > 0 {
			return count
		}
	}
	for key, rawCount := range tags {
		if key == "quiz_group_id" || key == "quiz_group_type" {
			continue
		}
		if count := positiveInt(rawCount); count > 0 {
			return count
		}
	}
	return 0
}

func positiveInt(value any) int {
	numeric, err := strconv.ParseFloat(strings.TrimSpace(scalarString(value)), 64)
	if err != nil || numeric <= 0 {
		return 0
	}
	return int(numeric)
}
