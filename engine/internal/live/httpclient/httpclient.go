// Package httpclient implements a signed HTTP client for Douyin webcast API
// requests. It wraps the local a_bogus (v1) and x-common-params-v2 signing
// packages, mirroring the request/header construction performed by the Python
// reference (src/network/live_fubao_poller.py).
package httpclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"fubao.ccvar.com/engine/internal/sign/abogus"
	signv2 "fubao.ccvar.com/engine/internal/sign/v2"
)

// SignedUserAgent is the User-Agent bound to the v1 (a_bogus) signature. It must
// match the UA the signature was computed against, otherwise Douyin rejects the
// request.
const SignedUserAgent = abogus.SignedUserAgent

// DefaultTimeout is the default per-request timeout, matching the Python
// request_timeout default.
const DefaultTimeout = 10 * time.Second

// Doer is the minimal HTTP interface the Client depends on. net/http.Client
// satisfies it. Tests may substitute a fake to avoid hitting the network.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client is a signed HTTP client for Douyin webcast requests. It holds a shared
// underlying HTTP doer plus the room context needed to build referer/origin
// headers.
type Client struct {
	// http is the underlying request executor (defaults to a *http.Client).
	http Doer
	// RoomURL is the live room page URL used as the Referer, e.g.
	// "https://live.douyin.com/123456". May be empty.
	RoomURL string
	// Cookie is the raw Cookie header value sent with signed requests. May be
	// empty.
	Cookie string
}

// Option configures a Client.
type Option func(*Client)

// WithDoer overrides the underlying HTTP executor (useful for tests).
func WithDoer(d Doer) Option {
	return func(c *Client) { c.http = d }
}

// WithRoomURL sets the Referer room URL.
func WithRoomURL(u string) Option {
	return func(c *Client) { c.RoomURL = u }
}

// WithCookie sets the Cookie header value.
func WithCookie(cookie string) Option {
	return func(c *Client) { c.Cookie = cookie }
}

// WithTimeout sets the timeout on the default underlying *http.Client. It has no
// effect if WithDoer was also supplied.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if hc, ok := c.http.(*http.Client); ok {
			hc.Timeout = d
		}
	}
}

// New constructs a Client. By default it uses a *http.Client with DefaultTimeout
// pinned to HTTP/1.1: the Python reference uses requests (HTTP/1.1) and Douyin's
// webcast endpoints are validated against that; forcing HTTP/1.1 avoids Go's
// default HTTP/2 upgrade, keeping the wire behaviour identical to the reference.
func New(opts ...Option) *Client {
	c := &Client{
		http: &http.Client{
			Timeout: DefaultTimeout,
			Transport: &http.Transport{
				TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
			},
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// SignAPIParams mirrors _sign_api_params: it augments the given params with the
// fixed browser/engine/os fingerprint fields and then attaches the a_bogus (v1)
// signature via the local signer. The returned map is a fresh copy; the input is
// not mutated.
func (c *Client) SignAPIParams(params map[string]string) map[string]string {
	signed := make(map[string]string, len(params)+24)
	for k, v := range params {
		signed[k] = v
	}
	// Cookie page/session tokens (msToken/verifyFp/...) come before the
	// fingerprint overrides, matching the Python order:
	// params.update(webcast_tokens) then _sign_api_params(params).
	for k, v := range c.WebcastQueryTokens() {
		signed[k] = v
	}
	for k, v := range fingerprintFields {
		signed[k] = v
	}
	return abogus.SignDouyinParams(signed)
}

// fingerprintFields are the fixed browser fingerprint parameters added before
// signing, matching the Python _sign_api_params.
var fingerprintFields = map[string]string{
	"browser_platform": "Win32",
	"browser_name":     "Chrome",
	"browser_version":  "123.0.0.0",
	"browser_online":   "true",
	"engine_name":      "Blink",
	"engine_version":   "123.0.0.0",
	"os_name":          "Windows",
	"os_version":       "10",
	"cpu_core_num":     "16",
	"device_memory":    "8",
	"platform":         "PC",
	"downlink":         "10",
	"effective_type":   "4g",
	"round_trip_time":  "50",
	"screen_width":     "1536",
	"screen_height":    "864",
}

// SignedAPIHeaders mirrors _signed_api_headers (built on top of
// _no_bridge_experiment_headers): the standard webcast headers overridden with
// the signed User-Agent and Windows client hints, plus Referer/Origin and the
// XMLHttpRequest marker.
func (c *Client) SignedAPIHeaders() http.Header {
	h := http.Header{}
	h.Set("User-Agent", SignedUserAgent)
	h.Set("Accept", "application/json, text/plain, */*")
	h.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Pragma", "no-cache")
	h.Set("Origin", "https://live.douyin.com")
	h.Set("X-Requested-With", "XMLHttpRequest")
	h.Set("Sec-Ch-Ua", `"Google Chrome";v="123", "Chromium";v="123", ";Not A Brand";v="99"`)
	h.Set("Sec-Ch-Ua-Mobile", "?0")
	h.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	h.Set("Sec-Fetch-Dest", "empty")
	h.Set("Sec-Fetch-Mode", "cors")
	h.Set("Sec-Fetch-Site", "same-origin")
	if c.RoomURL != "" {
		h.Set("Referer", c.RoomURL)
	}
	if c.Cookie != "" {
		h.Set("Cookie", c.Cookie)
	}
	return h
}

// SignXCommonParamsV2 signs the given full URL with the v2 signer and returns
// the x-common-params-v2 header value, mirroring sign_x_common_params_v2 usage.
func (c *Client) SignXCommonParamsV2(fullURL string) string {
	return signv2.NewSigner(signv2.ABOGUSV2UserAgent, 0, nil).Sign(fullURL, nil)
}

// canonicalParamOrder is the exact browser-natural param order Douyin expects.
// CRITICAL: the webcast API silently drops requests whose query params are in
// any other order (e.g. alphabetically sorted) — it returns HTTP 200 with an
// empty body. The a_bogus signature must also be computed over this same order.
// This mirrors the Python insertion order: _common_api_params, then
// room_id/query_from, then _webcast_query_tokens, then the new keys appended by
// _sign_api_params. Keys already present keep their original position with
// updated values.
var canonicalParamOrder = []string{
	"aid", "app_name", "live_id", "device_platform", "language",
	"browser_language", "browser_platform", "browser_name", "browser_version",
	"cookie_enabled", "screen_width", "screen_height",
	"room_id", "query_from",
	"msToken", "verifyFp", "fp", "s_v_web_id", "device_id", "odin_tt", "sec_user_id", "anchor_id",
	"browser_online", "engine_name", "engine_version", "os_name", "os_version",
	"cpu_core_num", "device_memory", "platform", "downlink", "effective_type", "round_trip_time",
}

// orderedEncode emits params as a query string in canonicalParamOrder (browser
// natural order), appending any unknown keys afterward (sorted, for
// determinism). a_bogus is never included here — it is appended by the caller.
func orderedEncode(params map[string]string) string {
	var b strings.Builder
	seen := map[string]bool{}
	write := func(k string) {
		v, ok := params[k]
		if !ok || k == "a_bogus" {
			return
		}
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(v))
		seen[k] = true
	}
	for _, k := range canonicalParamOrder {
		write(k)
	}
	extra := make([]string, 0)
	for k := range params {
		if !seen[k] && k != "a_bogus" {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		write(k)
	}
	return b.String()
}

// BuildSignedURL builds the final signed request URL. It merges the cookie
// page/session tokens and the fixed fingerprint fields, encodes the query in the
// canonical browser order, signs a_bogus over that exact ordered string, and
// appends a_bogus last (matching how the Python signer returns params with
// a_bogus added at the end). The returned map includes a_bogus.
func (c *Client) BuildSignedURL(baseURL string, params map[string]string) (string, map[string]string) {
	return c.BuildSignedURLForMethod(http.MethodGet, baseURL, params)
}

// BuildSignedURLForMethod is identical to BuildSignedURL but binds a_bogus to
// the actual HTTP method. Douyin's luckybox join/rush actions are POST calls.
func (c *Client) BuildSignedURLForMethod(method, baseURL string, params map[string]string) (string, map[string]string) {
	full := make(map[string]string, len(params)+24)
	for k, v := range params {
		if k != "a_bogus" {
			full[k] = v
		}
	}
	for k, v := range c.WebcastQueryTokens() {
		full[k] = v
	}
	for k, v := range fingerprintFields {
		full[k] = v
	}
	orderedQuery := orderedEncode(full)
	aBogus := abogus.NewSigner("").SignParams(orderedQuery, strings.ToUpper(method))
	full["a_bogus"] = aBogus

	sep := "?"
	if _, _, hasQuery := splitURL(baseURL); hasQuery {
		sep = "&"
	}
	finalURL := baseURL + sep + orderedQuery + "&a_bogus=" + url.QueryEscape(aBogus)
	return finalURL, full
}

// splitURL reports whether the URL already contains a query string.
func splitURL(u string) (base string, query string, hasQuery bool) {
	for i := 0; i < len(u); i++ {
		if u[i] == '?' {
			return u[:i], u[i+1:], true
		}
	}
	return u, "", false
}

// NewSignedRequest builds a signed *http.Request for the given method and base
// URL. Params are signed (a_bogus + fingerprint) and encoded into the query
// string, and the signed API headers are attached.
func (c *Client) NewSignedRequest(ctx context.Context, method, baseURL string, params map[string]string) (*http.Request, error) {
	finalURL, _ := c.BuildSignedURLForMethod(method, baseURL, params)
	var body io.Reader
	contentType := ""
	if strings.EqualFold(method, http.MethodPost) {
		if jsonBody, ok := luckyboxJSONBody(baseURL, params); ok {
			body = strings.NewReader(jsonBody)
			contentType = "application/json"
		} else {
			body = strings.NewReader("")
			contentType = "application/x-www-form-urlencoded; charset=UTF-8"
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, finalURL, body)
	if err != nil {
		return nil, err
	}
	req.Header = c.SignedAPIHeaders()
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

// PostSigned executes a signed same-origin POST. Luckybox join/rush use the
// verified live-page JSON body {{box_id, room_id, anchor_id}}; other endpoints
// keep an empty form body.
func (c *Client) PostSigned(ctx context.Context, baseURL string, params map[string]string) (*http.Response, error) {
	req, err := c.NewSignedRequest(ctx, http.MethodPost, baseURL, params)
	if err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

// luckyboxJSONBody matches the real live.douyin.com XHR captured by DY-KIRO:
// POST Content-Type: application/json with
// {{"box_id":"...","room_id":"...","anchor_id":"..."}} (succeed:true).
func luckyboxJSONBody(baseURL string, params map[string]string) (string, bool) {
	lower := strings.ToLower(baseURL)
	if !strings.Contains(lower, "/webcast/luckybox/join") && !strings.Contains(lower, "/webcast/luckybox/rush") {
		return "", false
	}
	boxID := strings.TrimSpace(params["box_id"])
	roomID := strings.TrimSpace(params["room_id"])
	if boxID == "" || roomID == "" {
		return "", false
	}
	payload := map[string]string{
		"box_id":  boxID,
		"room_id": roomID,
	}
	if anchor := strings.TrimSpace(params["anchor_id"]); anchor != "" {
		payload["anchor_id"] = anchor
	}
	if strings.Contains(lower, "/webcast/luckybox/rush") {
		for _, key := range []string{"box_type", "send_time", "delay_time"} {
			if value := strings.TrimSpace(params[key]); value != "" {
				payload[key] = value
			}
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

// GetSigned builds a signed GET request for the base URL + params and executes
// it through the underlying Doer.
func (c *Client) GetSigned(ctx context.Context, baseURL string, params map[string]string) (*http.Response, error) {
	req, err := c.NewSignedRequest(ctx, http.MethodGet, baseURL, params)
	if err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

// Do executes a prepared request through the client's pinned HTTP/1.1
// transport. Callers use this when an endpoint needs signed headers that are
// added after NewSignedRequest (for example x-common-params-v2).
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	return c.http.Do(req)
}
