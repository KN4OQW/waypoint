package mqtt

import (
	"testing"
	"time"
)

func TestTranslateDMRGroupCallStartEnd(t *testing.T) {
	b := NewBridge()

	// RF group-call start: MMDVM-Host emits IDs + a resolved src_info.
	start := b.Translate([]byte(`{"DMR":{"timestamp":"2026-07-13T02:41:33.123Z","source":"rf","action":"start","slot":2,"src_id":3180202,"dst_id":9,"group":"yes","src_info":"KN4OQW Clint"}}`))
	if len(start) != 1 {
		t.Fatalf("start: want 1 event, got %d", len(start))
	}
	if got := start[0]; got.Type != "rf_voice_start" || got.Mode != "DMR" || got.Slot != 2 ||
		got.Source != "KN4OQW Clint" || got.Dest != "TG 9" {
		t.Fatalf("start event wrong: %+v", got)
	}
	if !start[0].Time.Equal(mustTime(t, "2026-07-13T02:41:33.123Z")) {
		t.Fatalf("start timestamp not parsed: %v", start[0].Time)
	}

	// End on the same slot: no identity or source in the payload — must be
	// recovered from the paired start, with duration/ber/rssi attached.
	end := b.Translate([]byte(`{"DMR":{"timestamp":"2026-07-13T02:41:38.500Z","action":"end","slot":2,"duration":5.1,"ber":0.4,"rssi":{"min":-70,"max":-60,"ave":-64}}}`))
	if len(end) != 1 {
		t.Fatalf("end: want 1 event, got %d", len(end))
	}
	e := end[0]
	if e.Type != "rf_voice_end" || e.Source != "KN4OQW Clint" || e.Dest != "TG 9" ||
		e.Seconds != 5.1 || e.BER != 0.4 || e.RSSI != -64 {
		t.Fatalf("end event wrong: %+v", e)
	}
}

func TestTranslateNetworkPairingBySlot(t *testing.T) {
	b := NewBridge()
	// Two slots active at once: end events must pair to the right slot/direction.
	b.Translate([]byte(`{"DMR":{"source":"network","action":"start","slot":1,"src_id":3112,"dst_id":31121,"group":"yes","src_info":"W4ABC"}}`))
	b.Translate([]byte(`{"DMR":{"source":"rf","action":"start","slot":2,"src_id":3180202,"dst_id":9,"group":"yes","src_info":"KN4OQW"}}`))

	end1 := b.Translate([]byte(`{"DMR":{"action":"end","slot":1,"duration":2.0,"ber":0.0}}`))
	if end1[0].Type != "net_voice_end" || end1[0].Source != "W4ABC" || end1[0].Dest != "TG 31121" {
		t.Fatalf("slot-1 end mispaired: %+v", end1[0])
	}
	end2 := b.Translate([]byte(`{"DMR":{"action":"end","slot":2,"duration":3.0,"ber":0.1}}`))
	if end2[0].Type != "rf_voice_end" || end2[0].Source != "KN4OQW" || end2[0].Dest != "TG 9" {
		t.Fatalf("slot-2 end mispaired: %+v", end2[0])
	}
}

func TestTranslateDStarCallsigns(t *testing.T) {
	b := NewBridge()
	ev := b.Translate([]byte(`{"D-Star":{"timestamp":"2026-07-13T02:50:00.000Z","source":"rf","action":"start","src_callsign":"KN4OQW  ","src_ext":"MOBI","dst_callsign":"CQCQCQ  ","reflector":"REF001 C"}}`))
	if len(ev) != 1 {
		t.Fatalf("want 1 event, got %d", len(ev))
	}
	if ev[0].Mode != "D-Star" || ev[0].Source != "KN4OQW  " || ev[0].Dest != "CQCQCQ  " || ev[0].Network != "REF001 C" {
		t.Fatalf("d-star event wrong: %+v", ev[0])
	}
}

func TestTranslateLateEntryAndLost(t *testing.T) {
	b := NewBridge()
	le := b.Translate([]byte(`{"YSF":{"source":"network","action":"late_entry","src_callsign":"KM4SSB","dst_callsign":"FL-TREASURE"}}`))
	if le[0].Type != "net_voice_start" {
		t.Fatalf("late_entry should map to start: %+v", le[0])
	}
	lost := b.Translate([]byte(`{"YSF":{"action":"lost","duration":1.2,"ber":2.0}}`))
	if lost[0].Type != "net_voice_end" || lost[0].Detail != "signal lost" || lost[0].Source != "KM4SSB" {
		t.Fatalf("lost event wrong: %+v", lost[0])
	}
}

func TestTranslateIgnoresNonVoice(t *testing.T) {
	b := NewBridge()
	for _, p := range []string{
		`{"RSSI":{"mode":"DMR","value":-64}}`,
		`{"BER":{"mode":"DMR","value":0.3}}`,
		`{"Text":{"mode":"DMR","value":"hello"}}`,
		`{"DMR":{"action":"csbk","csbk_desc":"Radio Check","src_id":1,"dst_id":2}}`,
		`{"DMR":{"action":"rejected","src_id":1,"dst_id":2}}`,
		`not json`,
		`{}`,
	} {
		if ev := b.Translate([]byte(p)); len(ev) != 0 {
			t.Fatalf("payload %q should yield no events, got %+v", p, ev)
		}
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	out, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad test timestamp %q: %v", s, err)
	}
	return out.UTC()
}
