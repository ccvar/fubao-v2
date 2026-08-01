package httpclient

import "strings"

// parseCookie splits a raw Cookie header ("a=1; b=2") into name->value pairs,
// mirroring how the Python poller reads cookie values.
func parseCookie(cookie string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(part[:eq])
		if name != "" {
			out[name] = strings.TrimSpace(part[eq+1:])
		}
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// WebcastQueryTokens mirrors _webcast_query_tokens: the stable page/session
// tokens Douyin expects to be signed into webcast requests, extracted from the
// client's cookie. Without these (msToken/verifyFp/...) the lottery_info
// endpoint serves an anti-bot challenge page instead of JSON. Returns an empty
// map when the cookie lacks them.
func (c *Client) WebcastQueryTokens() map[string]string {
	ck := parseCookie(c.Cookie)
	tokens := map[string]string{}
	if v := ck["msToken"]; v != "" {
		tokens["msToken"] = v
	}
	webID := ck["s_v_web_id"]
	if v := firstNonEmpty(ck["verifyFp"], webID); v != "" {
		tokens["verifyFp"] = v
	}
	if v := firstNonEmpty(ck["fp"], webID); v != "" {
		tokens["fp"] = v
	}
	if webID != "" {
		tokens["s_v_web_id"] = webID
	}
	for _, k := range []string{"device_id", "odin_tt", "sec_user_id", "anchor_id"} {
		if v := ck[k]; v != "" {
			tokens[k] = v
		}
	}
	return tokens
}
