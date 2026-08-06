package httpclient

import (
	"context"
	"io"
	"net/http"
	"testing"
)

func TestNewSignedPostRequestUsesPostSignatureShape(t *testing.T) {
	client := New(WithCookie("sessionid_ss=native-only"), WithRoomURL("https://live.douyin.com/123456"))
	req, err := client.NewSignedRequest(context.Background(), http.MethodPost, "https://live.douyin.com/webcast/luckybox/join/", map[string]string{
		"aid": "6383", "room_id": "7001", "box_id": "box-1", "anchor_id": "anchor-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPost {
		t.Fatalf("expected POST request, got %s", req.Method)
	}
	query := req.URL.Query()
	if query.Get("a_bogus") == "" || query.Get("room_id") != "7001" || query.Get("box_id") != "box-1" {
		t.Fatalf("signed POST query is incomplete: %s", req.URL.RawQuery)
	}
	// Verified live-page join uses JSON body, not an empty form.
	if req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected POST content type: %s", req.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"anchor_id":"anchor-1","box_id":"box-1","room_id":"7001"}` &&
		string(body) != `{"box_id":"box-1","room_id":"7001","anchor_id":"anchor-1"}` {
		// map iteration order is stable for these keys in Go 1.20+ json.Marshal
		// of map[string]string sorts keys alphabetically.
		if string(body) != `{"anchor_id":"anchor-1","box_id":"box-1","room_id":"7001"}` {
			t.Fatalf("unexpected JSON body: %s", body)
		}
	}
	if req.Header.Get("Cookie") != "sessionid_ss=native-only" || req.Header.Get("Origin") != "https://live.douyin.com" {
		t.Fatalf("native credential headers were not attached correctly")
	}
}
