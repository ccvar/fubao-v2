package httpclient

import (
	"context"
	"net/http"
	"testing"
)

func TestNewSignedPostRequestUsesPostSignatureShape(t *testing.T) {
	client := New(WithCookie("sessionid_ss=native-only"), WithRoomURL("https://live.douyin.com/123456"))
	req, err := client.NewSignedRequest(context.Background(), http.MethodPost, "https://live.douyin.com/webcast/luckybox/join/", map[string]string{
		"aid": "6383", "room_id": "7001", "box_id": "box-1",
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
	if req.Header.Get("Content-Type") != "application/x-www-form-urlencoded; charset=UTF-8" {
		t.Fatalf("unexpected POST content type: %s", req.Header.Get("Content-Type"))
	}
	if req.Header.Get("Cookie") != "sessionid_ss=native-only" || req.Header.Get("Origin") != "https://live.douyin.com" {
		t.Fatalf("native credential headers were not attached correctly")
	}
}
