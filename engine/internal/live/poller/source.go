package poller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"fubao.ccvar.com/engine/internal/live/httpclient"
)

// reDiamonds / reFubaoCount mirror the Chinese-text heuristics in
// _has_lottery_business_data.
var (
	reDiamonds   = regexp.MustCompile(`(?:总|共)?\s*\d+\s*(?:钻|抖币)`)
	reFubaoCount = regexp.MustCompile(`\d+\s*个\s*福袋`)
)

// LotteryInfoEndpoint is the direct-HTTP lottery_info API, matching the Python
// _fetch_lottery_info_snapshots_unthrottled endpoint.
const LotteryInfoEndpoint = "https://live.douyin.com/webcast/lottery/melon/lottery_info/"

// queryFromValues mirror the two query_from probes the Python performs: "1"
// (first room entry) and "3" (polling/refresh).
var queryFromValues = []string{"1", "3"}

// Snapshot is a single lottery_info snapshot, matching the dict the Python
// _fetch_lottery_info_snapshots_unthrottled appends: source + room ids + the
// business `data` map + the raw payload. Its Data map is what gets converted to
// a FubaoEvent.
type Snapshot struct {
	Source       string
	RoomID       string
	ActualRoomID string
	Data         map[string]any
	Raw          map[string]any
}

// SnapshotSource is the pluggable fetch strategy. The default HTTPSource hits
// the signed lottery_info API directly. Phase 2 browser/CDP strategies
// (no-bridge, silent-browser, visible-browser) implement this same interface;
// they are stubbed here.
type SnapshotSource interface {
	// Fetch returns the active-fubao snapshots for the configured room.
	Fetch(ctx context.Context) ([]Snapshot, error)
}

// signer is the subset of *httpclient.Client the HTTPSource needs; it lets tests
// substitute a lightweight fake instead of constructing the full signed client.
type signer interface {
	GetSigned(ctx context.Context, baseURL string, params map[string]string) (*http.Response, error)
}

// compile-time check that the real client satisfies signer.
var _ signer = (*httpclient.Client)(nil)

// HTTPSource is the direct-HTTP lottery_info snapshot source — the REAL
// detection path. It ports _fetch_lottery_info_snapshots_unthrottled: for each
// query_from value it issues a signed GET, parses the JSON, keeps only payloads
// with real lottery business data that pass the active-fubao predicate, and
// dedupes within the batch by activity id.
type HTTPSource struct {
	client       signer
	roomID       string // web_rid (public room id)
	actualRoomID string // numeric room_id used in the API params
	now          func() time.Time
}

// HTTPOption configures an HTTPSource.
type HTTPOption func(*HTTPSource)

// WithNow overrides the clock (tests).
func WithNow(now func() time.Time) HTTPOption {
	return func(s *HTTPSource) { s.now = now }
}

// NewHTTPSource builds the direct-HTTP source. roomID is the public web_rid used
// for reporting; actualRoomID is the numeric room id sent as the room_id API
// param (in the Python it is resolved via the room page; here it is supplied).
func NewHTTPSource(client signer, roomID, actualRoomID string, opts ...HTTPOption) *HTTPSource {
	s := &HTTPSource{
		client:       client,
		roomID:       roomID,
		actualRoomID: actualRoomID,
		now:          time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// commonAPIParams mirrors _common_api_params.
func commonAPIParams() map[string]string {
	return map[string]string{
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
	}
}

// Fetch ports _fetch_lottery_info_snapshots_unthrottled. It returns an empty
// slice (not an error) when no active fubao is found, matching the Python
// behaviour where an empty snapshot list simply means "no welfare this round".
func (s *HTTPSource) Fetch(ctx context.Context) ([]Snapshot, error) {
	if s.actualRoomID == "" {
		return nil, nil
	}

	var snapshots []Snapshot
	seenIDs := map[string]struct{}{}

	for _, queryFrom := range queryFromValues {
		params := commonAPIParams()
		params["room_id"] = s.actualRoomID
		params["query_from"] = queryFrom

		resp, err := s.client.GetSigned(ctx, LotteryInfoEndpoint, params)
		if err != nil {
			// A transport error on one probe should not abort the other; record
			// and continue, mirroring the per-request try/except in Python.
			continue
		}

		snap, ok := s.parseResponse(resp)
		if !ok {
			continue
		}

		fubaoID := anyToStr(extractFirst(snap.Data, activityIDKeys))
		dedupeKey := fubaoID
		if dedupeKey == "" {
			// Fall back to a stable JSON encoding of the data map.
			b, _ := json.Marshal(snap.Data)
			dedupeKey = string(b)
		}
		if _, dup := seenIDs[dedupeKey]; dup {
			continue
		}
		seenIDs[dedupeKey] = struct{}{}
		snapshots = append(snapshots, snap)
	}

	return snapshots, nil
}

// parseResponse ports the per-request body handling in the Python loop: skip
// HTTP >= 400, skip empty bodies, skip invalid JSON, require a dict `data`,
// merge extra, require real business data, and require the active predicate.
func (s *HTTPSource) parseResponse(resp *http.Response) (Snapshot, bool) {
	if resp == nil {
		return Snapshot{}, false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return Snapshot{}, false
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Snapshot{}, false
	}
	if strings.TrimSpace(string(body)) == "" {
		return Snapshot{}, false
	}

	var payload map[string]any
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return Snapshot{}, false
	}
	normalizeNumbers(payload)

	data, ok := payload["data"].(map[string]any)
	if !ok {
		return Snapshot{}, false
	}

	// Copy data and fold in extra (setdefault semantics).
	snapshotData := make(map[string]any, len(data)+1)
	for k, v := range data {
		snapshotData[k] = v
	}
	if extra, ok := payload["extra"].(map[string]any); ok {
		if _, exists := snapshotData["extra"]; !exists {
			snapshotData["extra"] = extra
		}
	}

	if !hasLotteryBusinessData(snapshotData) {
		return Snapshot{}, false
	}
	if !isActiveLotteryInfo(snapshotData, s.now) {
		return Snapshot{}, false
	}

	return Snapshot{
		Source:       "lottery_info_api",
		RoomID:       s.roomID,
		ActualRoomID: s.actualRoomID,
		Data:         snapshotData,
		Raw:          payload,
	}, true
}

// normalizeNumbers converts json.Number leaves to float64 (matching the numeric
// handling elsewhere in this port and in the eventhandler). This keeps the
// active/business predicates and the eventhandler consistent.
func normalizeNumbers(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			if n, ok := item.(json.Number); ok {
				x[k] = numberToAny(n)
			} else {
				normalizeNumbers(item)
			}
		}
	case []any:
		for i, item := range x {
			if n, ok := item.(json.Number); ok {
				x[i] = numberToAny(n)
			} else {
				normalizeNumbers(item)
			}
		}
	}
}

func numberToAny(n json.Number) any {
	if f, err := n.Float64(); err == nil {
		return f
	}
	return n.String()
}

// --- Phase 2 (browser / CDP) stubs ---

// ErrSourceUnavailable is returned by the stub browser sources. Phase 2 replaces
// them with real CDP-backed implementations.
var ErrSourceUnavailable = errors.New("poller: browser snapshot source not available (Phase 2 / CDP)")

// noBridgeSource stubs _fetch_no_bridge_lottery_info_snapshots /
// _fetch_no_bridge_template_lottery_snapshots.
type noBridgeSource struct{}

// silentBrowserSource stubs _fetch_silent_browser_lottery_info_snapshots.
type silentBrowserSource struct{}

// visibleBrowserSource stubs _fetch_browser_lottery_info_snapshots
// (ChromeLotteryBridge visible-browser bridge).
type visibleBrowserSource struct{}

// NewNoBridgeSource returns the Phase-1b stub for the no-bridge experiment
// strategy. It always reports ErrSourceUnavailable.
func NewNoBridgeSource() SnapshotSource { return noBridgeSource{} }

// NewSilentBrowserSource returns the Phase-1b stub for the silent-browser
// strategy. It always reports ErrSourceUnavailable.
func NewSilentBrowserSource() SnapshotSource { return silentBrowserSource{} }

// NewVisibleBrowserSource returns the Phase-1b stub for the visible Chrome
// bridge strategy. It always reports ErrSourceUnavailable.
func NewVisibleBrowserSource() SnapshotSource { return visibleBrowserSource{} }

func (noBridgeSource) Fetch(context.Context) ([]Snapshot, error) {
	return nil, ErrSourceUnavailable
}
func (silentBrowserSource) Fetch(context.Context) ([]Snapshot, error) {
	return nil, ErrSourceUnavailable
}
func (visibleBrowserSource) Fetch(context.Context) ([]Snapshot, error) {
	return nil, ErrSourceUnavailable
}

var (
	_ SnapshotSource = (*HTTPSource)(nil)
	_ SnapshotSource = noBridgeSource{}
	_ SnapshotSource = silentBrowserSource{}
	_ SnapshotSource = visibleBrowserSource{}
)
