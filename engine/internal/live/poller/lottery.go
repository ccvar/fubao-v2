// Package poller implements the HTTP lottery_info poller — the real fubao
// detection path in this app. It is a faithful port of the CORE of the Python
// reference LiveFubaoPoller (src/network/live_fubao_poller.py): the deep nested
// lottery_info key search + "is this an active fubao" predicate, the
// snapshot->FubaoEvent conversion (delegated to the eventhandler package), the
// dedupe by (fubao_id + state signature), and the adaptive poll interval /
// backoff loop, all driving the direct-HTTP lottery_info fetch against
// https://live.douyin.com/webcast/lottery/melon/lottery_info/ .
//
// The browser/CDP-dependent snapshot strategies (_fetch_no_bridge_*,
// _fetch_silent_browser_*, _fetch_browser_*) are NOT ported here (Phase 2). They
// are represented by the SnapshotSource interface; the default HTTP-only source
// is provided, and browser sources plug in later.
package poller

import (
	"strconv"
	"strings"
	"time"
)

// lotteryPayloadKeys are the nested container keys searched for a lottery
// payload, mirroring the Python _extract_reflow_lottery_data list.
var lotteryPayloadKeys = []string{
	"lottery_info", "lotteryInfo", "lotteryData", "lottery", "melonLottery", "luckyBag", "luckyBagInfo",
}

// activityIDKeys mirror the Python id-key list used across the poller.
var activityIDKeys = []string{
	"lottery_id_str", "lotteryIdStr", "lottery_id", "lotteryId", "activity_id", "activityId",
}

// businessSignalKeys mirrors the Python _has_lottery_business_data signal_keys set.
var businessSignalKeys = map[string]struct{}{}

func init() {
	for _, k := range []string{
		"activity_id", "activityId",
		"lottery_id", "lotteryId", "lottery_id_str", "lotteryIdStr",
		"box_id_str", "boxIdStr",
		"draw_time", "drawTime", "lottery_draw_time", "lotteryDrawTime",
		"end_time", "endTime", "expire_time", "expireTime",
		"lottery_finish_time", "lotteryFinishTime",
		"count_down_time", "countDownTime", "countdown_time", "countdownTime",
		"remaining_time", "remainingTime", "remain_time", "remainTime",
		"left_time", "leftTime",
		"prize_info", "prizeInfo", "prize", "prize_count", "prizeCount",
		"lucky_count", "luckyCount",
		"candidate_user_num", "candidateUserNum",
		"participant_count", "participantCount",
		"condition", "participation_condition", "participationCondition",
		"activity_state", "activityState",
		"event_type", "eventType",
		"lottery_result", "lotteryResult",
		"dom_state",
	} {
		businessSignalKeys[k] = struct{}{}
	}
}

// nestedLotteryContainerKeys mirrors the recursive descent keys in
// _has_lottery_business_data.
var nestedLotteryContainerKeys = map[string]struct{}{
	"lottery_info": {}, "lotteryInfo": {}, "lotteryData": {},
	"lottery": {}, "melonLottery": {}, "luckyBag": {}, "luckyBagInfo": {},
}

// hasLotteryData mirrors _has_lottery_data: a value is "present" when it is not
// None/empty; dicts are truthy when any value is present; lists when any element
// has data.
func hasLotteryData(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case map[string]any:
		for _, item := range v {
			if !isEmptyValue(item) {
				return true
			}
		}
		return false
	case []any:
		for _, item := range v {
			if hasLotteryData(item) {
				return true
			}
		}
		return false
	case string:
		return v != ""
	case bool:
		return v
	default:
		// numbers etc: truthy unless zero. Python bool(0)==False.
		return !isZeroNumber(value)
	}
}

// isEmptyValue mirrors Python's `v not in (None, "", [], {})` membership test.
func isEmptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	default:
		return false
	}
}

func isZeroNumber(v any) bool {
	switch x := v.(type) {
	case float64:
		return x == 0
	case int:
		return x == 0
	case int64:
		return x == 0
	}
	return false
}

// collectTextValues mirrors _collect_text_values: gather every string leaf.
func collectTextValues(data any, out *[]string) {
	switch v := data.(type) {
	case map[string]any:
		for _, item := range v {
			collectTextValues(item, out)
		}
	case []any:
		for _, item := range v {
			collectTextValues(item, out)
		}
	case string:
		*out = append(*out, v)
	}
}

// hasLotteryBusinessData mirrors _has_lottery_business_data: true only when a
// payload has real lottery fields (signal keys or nested lottery containers with
// business data, or the Chinese text heuristics), not just metadata.
func hasLotteryBusinessData(value any) bool {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if hasLotteryBusinessData(item) {
				return true
			}
		}
		return false
	case map[string]any:
		for key, item := range v {
			if _, isSignal := businessSignalKeys[key]; isSignal && hasLotteryData(item) {
				return true
			}
			if _, isContainer := nestedLotteryContainerKeys[key]; isContainer {
				if hasLotteryBusinessData(item) {
					return true
				}
			}
		}
		var texts []string
		collectTextValues(v, &texts)
		text := strings.Join(texts, " ")
		if reDiamonds.MatchString(text) && reFubaoCount.MatchString(text) {
			return true
		}
		if strings.Contains(text, "倒计时") {
			for _, token := range []string{"参与条件", "一键发评论参与福袋", "已参与"} {
				if strings.Contains(text, token) {
					return true
				}
			}
		}
		return false
	default:
		return false
	}
}

// extractLotteryDirectField mirrors _extract_lottery_direct_field: look at the
// top-level data, then nested lottery_info/lotteryInfo, then extra.
func extractLotteryDirectField(data map[string]any, keys []string) any {
	for _, k := range keys {
		if v, ok := data[k]; ok {
			return v
		}
	}
	if li := firstDict(data["lottery_info"], data["lotteryInfo"]); li != nil {
		for _, k := range keys {
			if v, ok := li[k]; ok {
				return v
			}
		}
	}
	if extra, ok := data["extra"].(map[string]any); ok {
		for _, k := range keys {
			if v, ok := extra[k]; ok {
				return v
			}
		}
	}
	return nil
}

// firstDict returns the first argument that is a non-nil map (mirrors the Python
// `a or b` idiom where empty dicts are falsy — but here we only need the first
// dict present).
func firstDict(vals ...any) map[string]any {
	for _, v := range vals {
		if m, ok := v.(map[string]any); ok && len(m) > 0 {
			return m
		}
	}
	return nil
}

// isActiveLotteryInfo is a faithful port of _is_active_lottery_info: the "is
// this an active fubao?" predicate. now is injected for testability
// (datetime.now() in Python).
func isActiveLotteryInfo(value any, now func() time.Time) bool {
	if !hasLotteryData(value) {
		return false
	}
	if list, ok := value.([]any); ok {
		for _, item := range list {
			if isActiveLotteryInfo(item, now) {
				return true
			}
		}
		return false
	}
	m, ok := value.(map[string]any)
	if !ok {
		return false
	}

	_, hasLI := m["lottery_info"]
	_, hasLI2 := m["lotteryInfo"]
	if hasLI || hasLI2 {
		li := m["lottery_info"]
		if li == nil {
			li = m["lotteryInfo"]
		}
		if !hasLotteryData(li) {
			return false
		}
	}

	if !hasLotteryBusinessData(m) {
		return false
	}

	statusValue := extractLotteryDirectField(m, []string{"status", "lottery_status", "lotteryStatus"})
	if statusValue != nil {
		normalized := strings.ToLower(strings.TrimSpace(anyToStr(statusValue)))
		switch normalized {
		case "1", "inprogress", "in_progress", "ongoing", "active":
		default:
			return false
		}
	}

	drawSeconds, hasDraw := parseTimestampSeconds(
		extractLotteryDirectField(m, []string{"draw_time", "drawTime", "lottery_draw_time", "lotteryDrawTime"}),
	)
	currentSeconds, hasCurrent := parseTimestampSeconds(
		extractLotteryDirectField(m, []string{"current_time", "currentTime", "lottery_current_time", "lotteryCurrentTime", "now"}),
	)
	if hasDraw && hasCurrent && drawSeconds <= currentSeconds {
		return false
	}
	if hasDraw {
		if drawSeconds <= float64(now().Unix()) {
			return false
		}
	}
	return true
}

// extractFirst mirrors _extract_first: recursive first-key search (returns the
// value even if empty, as long as the key exists).
func extractFirst(data any, keys []string) any {
	switch v := data.(type) {
	case map[string]any:
		for _, k := range keys {
			if val, ok := v[k]; ok {
				return val
			}
		}
		for _, val := range v {
			if found := extractFirst(val, keys); found != nil {
				return found
			}
		}
	case []any:
		for _, item := range v {
			if found := extractFirst(item, keys); found != nil {
				return found
			}
		}
	}
	return nil
}

// parseTimestampSeconds mirrors _parse_timestamp_seconds for the value shapes
// that appear in lottery payloads: unix seconds or milliseconds (numbers or
// numeric strings). Millisecond values (> 1e12) are scaled down.
func parseTimestampSeconds(value any) (float64, bool) {
	var f float64
	switch x := value.(type) {
	case nil:
		return 0, false
	case float64:
		f = x
	case int:
		f = float64(x)
	case int64:
		f = float64(x)
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if f <= 0 {
		return 0, false
	}
	// Milliseconds -> seconds (Python treats > 1e12 as ms).
	if f > 1e12 {
		f = f / 1000.0
	}
	return f, true
}

func anyToStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case nil:
		return ""
	default:
		return ""
	}
}
