package lcd

import (
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
)

// Replaying the design §3 event table yields the documented derived fields, and
// the RF/network direction swap normalizes {lh_call}/{lh_tg} to a callsign+TG in
// both directions.
func TestStateFolding(t *testing.T) {
	at := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	s := &state{}

	s.handle(hub.Event{Type: "mode", Mode: "DMR"})
	if s.activeMode != "DMR" {
		t.Fatalf("mode event: activeMode = %q", s.activeMode)
	}

	s.handle(hub.Event{Type: "link", Network: "BM_3102"})
	if !s.links["BM_3102"] {
		t.Fatalf("link event not folded: %+v", s.links)
	}

	// RF voice: Source=callsign, Dest=talkgroup.
	s.handle(hub.Event{Type: "rf_voice_start", Mode: "YSF", Source: "W1ABC", Dest: "TG5"})
	if !s.active || s.actDir != "RX" || s.actMode != "YSF" || s.actCall != "W1ABC" || s.actTG != "TG5" {
		t.Fatalf("rf_voice_start: %+v", s)
	}
	if s.activeMode != "YSF" {
		t.Fatalf("voice_start did not update activeMode: %q", s.activeMode)
	}
	s.handle(hub.Event{Type: "rf_voice_end", Mode: "YSF", Source: "W1ABC", Dest: "TG5", BER: 0.3, RSSI: -80, Time: at})
	if s.active {
		t.Fatal("rf_voice_end left active set")
	}
	if lh := s.lastHeard; lh == nil || lh.call != "W1ABC" || lh.tg != "TG5" || lh.mode != "YSF" || lh.ber != 0.3 || lh.rssi != -80 || !lh.at.Equal(at) {
		t.Fatalf("rf lastHeard: %+v", s.lastHeard)
	}

	// Network voice: Source=talkgroup/reflector, Dest=callsign (swapped) — the
	// caller callsign must still land in lastHeard.call.
	s.handle(hub.Event{Type: "net_voice_start", Mode: "DMR", Source: "REF", Dest: "N0CALL"})
	if s.actDir != "TX" || s.actCall != "N0CALL" || s.actTG != "REF" {
		t.Fatalf("net_voice_start direction/normalization: %+v", s)
	}
	s.handle(hub.Event{Type: "net_voice_end", Mode: "DMR", Source: "REF", Dest: "N0CALL", Time: at})
	if lh := s.lastHeard; lh.call != "N0CALL" || lh.tg != "REF" {
		t.Fatalf("net lastHeard normalization: %+v", s.lastHeard)
	}
}
