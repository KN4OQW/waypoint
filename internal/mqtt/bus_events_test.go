package mqtt

import "testing"

// TestTranslateBusEvent is the D4 consumer mapping, table-driven over every bus
// event type plus the drop cases (empty=retained clear, invalid, foreign).
func TestTranslateBusEvent(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantOK  bool
		type_   string
	}{
		{"bus_busy", `{"type":"bus_busy","mode":"DMR","source":"YSF","detail":"busy: via YSF"}`, true, "bus_busy"},
		{"bus_voice_start", `{"type":"bus_voice_start","mode":"YSF","source":"KN4OQW","dest":"ALL"}`, true, "bus_voice_start"},
		{"bus_voice_end", `{"type":"bus_voice_end","mode":"YSF","source":"KN4OQW","seconds":3.6}`, true, "bus_voice_end"},
		{"bus_down", `{"type":"bus_down","network":"busA","detail":"owner offline"}`, true, "bus_down"},
		{"bus_up", `{"type":"bus_up","network":"busA","detail":"owner online"}`, true, "bus_up"},
		{"peer_connected", `{"type":"peer_connected","network":"busA","source":"garage"}`, true, "peer_connected"},
		{"peer_disconnected", `{"type":"peer_disconnected","network":"busA","source":"garage"}`, true, "peer_disconnected"},

		{"empty is a retained clear", ``, false, ""},
		{"whitespace is a retained clear", "  \n", false, ""},
		{"invalid json dropped", `{not json`, false, ""},
		{"foreign type dropped", `{"type":"rf_voice_start","mode":"DMR"}`, false, ""},
		{"typeless dropped", `{"mode":"DMR"}`, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, ok := TranslateBusEvent([]byte(c.payload))
			if ok != c.wantOK {
				t.Fatalf("ok=%v, want %v (event=%+v)", ok, c.wantOK, e)
			}
			if ok && e.Type != c.type_ {
				t.Fatalf("type=%q, want %q", e.Type, c.type_)
			}
		})
	}
}

// TestTranslateBusEventPreservesFields: the map is 1:1 — every hub.Event field a
// bus publishes survives (no translation layer).
func TestTranslateBusEventPreservesFields(t *testing.T) {
	e, ok := TranslateBusEvent([]byte(`{"type":"bus_voice_end","mode":"DMR","slot":2,"source":"3180202","dest":"9990","network":"Bus A","seconds":0.4,"detail":"bus Bus A"}`))
	if !ok {
		t.Fatal("should translate")
	}
	if e.Mode != "DMR" || e.Slot != 2 || e.Source != "3180202" || e.Dest != "9990" || e.Network != "Bus A" || e.Seconds != 0.4 {
		t.Fatalf("fields not preserved 1:1: %+v", e)
	}
}
