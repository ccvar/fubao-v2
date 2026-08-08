package redpacket

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Part of a live-room red packet carries a participation threshold: the most
// common ones are "send a popularity ticket first" (a diamond cost per join)
// and "light up the fan-club badge". luckybox/box/list never normalizes those
// requirements into a single field, so 福宝 derives them from the box row's own
// text and diamond keys. This is a port of that extraction; the result is
// compact display text such as "需1钻、需灯牌".
var (
	conditionDiamondText = regexp.MustCompile(`(?:需|需要|消耗|支付|扣除)\s*(\d+)\s*个?钻`)
	conditionBadgeText   = regexp.MustCompile(`(?:需|需要|要求|必须|请先|先|点亮|加入|开通)\s*(?:粉丝团)?灯牌|粉丝团点亮|加入粉丝团`)
	conditionDiamondSkip = regexp.MustCompile(`需\s*(\d+)\s*钻`)
	conditionKeyNoise    = regexp.MustCompile(`[^a-z0-9_]`)
)

// conditionDiamondKeys hold the per-join diamond cost as a bare number, so the
// text patterns above cannot see them.
var conditionDiamondKeys = map[string]struct{}{
	"need_diamond": {}, "needdiamond": {},
	"need_diamond_count": {}, "needdiamondcount": {},
	"diamond_cost": {}, "diamondcost": {},
	"cost_diamond": {}, "costdiamond": {},
	"consume_diamond": {}, "consumediamond": {},
}

const conditionMaxParts = 4

// extractRedPacketCondition returns the visible participation threshold of one
// luckybox activity payload, or "" when the payload shows no threshold.
func extractRedPacketCondition(payload any) string {
	parts := make([]string, 0, conditionMaxParts)
	seen := make(map[string]struct{}, conditionMaxParts)
	add := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" || len(parts) >= conditionMaxParts {
			return
		}
		if _, exists := seen[text]; exists {
			return
		}
		seen[text] = struct{}{}
		parts = append(parts, text)
	}
	walkRedPacketCondition(payload, false, add)
	// The walk visits Go maps in random order, so the same payload would
	// otherwise render as "需1钻、需灯牌" or "需灯牌、需1钻" between runs. Sort into a
	// fixed category order (diamond cost first, cheapest first) so the badge text
	// and its tooltip stay stable for the same packet.
	sort.SliceStable(parts, func(first, second int) bool {
		firstCost, firstIsDiamond := conditionDiamondCost(parts[first])
		secondCost, secondIsDiamond := conditionDiamondCost(parts[second])
		if firstIsDiamond != secondIsDiamond {
			return firstIsDiamond
		}
		if firstIsDiamond && firstCost != secondCost {
			return firstCost < secondCost
		}
		return false
	})
	return strings.Join(parts, "、")
}

// conditionDiamondCost reads back the numeric cost of a "需N钻" fragment.
func conditionDiamondCost(part string) (int, bool) {
	match := conditionDiamondSkip.FindStringSubmatch(part)
	if match == nil {
		return 0, false
	}
	cost, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return cost, true
}

// redPacketConditionSkipReason reports why a threshold makes the packet
// unreachable for an automated join. An empty string means "no threshold".
func redPacketConditionSkipReason(condition string) string {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return ""
	}
	hasDiamond := conditionDiamondSkip.MatchString(condition)
	hasBadge := strings.Contains(condition, "灯牌")
	switch {
	case hasDiamond && hasBadge:
		return "红包需要钻石/灯牌条件"
	case hasDiamond:
		return "红包需要钻石条件"
	case hasBadge:
		return "红包需要灯牌条件"
	}
	return ""
}

// redPacketConditionFromMessage recognizes a threshold stated by the join
// response itself. box/list frequently stays silent about the requirement, so a
// rejected join is what reveals it; learning it there keeps later rounds from
// spending the account's single unresolved slot on the same packet.
func redPacketConditionFromMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	if condition := extractRedPacketCondition(message); condition != "" {
		return condition
	}
	if containsAny(message, "粉丝团", "灯牌") {
		return "需灯牌"
	}
	return ""
}

// walkRedPacketCondition scans every scalar leaf for requirement text. The
// popularity-ticket gift cost is nested under its own object and carries no
// text, so its diamond count is read positionally once inside that subtree.
func walkRedPacketCondition(value any, inPopularityTicket bool, add func(string)) {
	switch item := value.(type) {
	case map[string]any:
		if inPopularityTicket {
			for _, key := range []string{"diamond_count", "diamondCount"} {
				if cost := conditionPositiveInt(item[key]); cost > 0 {
					add(fmt.Sprintf("需%d钻", cost))
					break
				}
			}
		}
		for key, child := range item {
			if _, direct := conditionDiamondKeys[normalizeConditionKey(key)]; direct {
				if cost := conditionPositiveInt(child); cost > 0 {
					add(fmt.Sprintf("需%d钻", cost))
				}
			}
			nested := inPopularityTicket || strings.EqualFold(key, "popularity_ticket_gift")
			walkRedPacketCondition(child, nested, add)
		}
	case []any:
		for _, child := range item {
			walkRedPacketCondition(child, inPopularityTicket, add)
		}
	default:
		addRedPacketConditionText(scalarString(value), add)
	}
}

func addRedPacketConditionText(text string, add func(string)) {
	if strings.TrimSpace(text) == "" {
		return
	}
	for _, match := range conditionDiamondText.FindAllStringSubmatch(text, -1) {
		if cost, err := strconv.Atoi(match[1]); err == nil && cost > 0 {
			add(fmt.Sprintf("需%d钻", cost))
		}
	}
	if conditionBadgeText.MatchString(text) {
		add("需灯牌")
	}
}

func normalizeConditionKey(key string) string {
	return conditionKeyNoise.ReplaceAllString(strings.ToLower(strings.TrimSpace(key)), "")
}

func conditionPositiveInt(value any) int {
	text := strings.TrimSpace(scalarString(value))
	if text == "" {
		return 0
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || number <= 0 {
		return 0
	}
	return int(number)
}
