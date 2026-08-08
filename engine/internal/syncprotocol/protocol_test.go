package syncprotocol

import (
	"testing"
	"time"
)

func TestRedPacketExpiredAtUsesEarliestAuthoritativeDeadline(t *testing.T) {
	now := time.Date(2026, 8, 8, 14, 0, 0, 0, time.UTC)
	packet := RedPacket{
		SendTime:  "1786197590000",
		DelayTime: "5",
		ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
	}
	if !RedPacketExpiredAt(packet, now) {
		t.Fatal("native send/delay deadline should prevent a stale future expires_at from keeping the packet open")
	}
	packet.SendTime = "invalid"
	if RedPacketExpiredAt(packet, now) {
		t.Fatal("future expires_at should keep the packet open when native timing is unavailable")
	}
	packet.ExpiresAt = ""
	if RedPacketExpiredAt(packet, now) {
		t.Fatal("missing deadlines must remain unknown rather than being treated as expired")
	}
}
