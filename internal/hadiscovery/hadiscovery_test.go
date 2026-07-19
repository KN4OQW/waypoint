package hadiscovery

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
)

func TestNode(t *testing.T) {
	cases := map[string]string{
		"KN4OQW":   "kn4oqw",
		"W1AW/3":   "w1aw3",
		"  m0abc ": "m0abc",
		"!!!":      "waypoint", // fully stripped falls back
		"":         "waypoint",
	}
	for in, want := range cases {
		if got := Node(in); got != want {
			t.Errorf("Node(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTopicsFor(t *testing.T) {
	got := TopicsFor("kn4oqw", "")
	if got.State != "waypoint/status/kn4oqw/state" {
		t.Errorf("state topic = %q", got.State)
	}
	if got.Availability != "waypoint/status/kn4oqw/availability" {
		t.Errorf("availability topic = %q", got.Availability)
	}
	// Blank prefix defaults to homeassistant.
	if got.Discovery != "homeassistant/device/kn4oqw/config" {
		t.Errorf("discovery topic = %q", got.Discovery)
	}
	// A custom prefix is honored.
	if c := TopicsFor("kn4oqw", "ha").Discovery; c != "ha/device/kn4oqw/config" {
		t.Errorf("custom prefix discovery = %q", c)
	}
}

// The discovery bundle must carry the mandatory device + origin blocks, a shared
// availability + state topic, and one component per entity — each with a platform
// and a stable unique_id tied to the node.
func TestDiscoveryPayload(t *testing.T) {
	topic, payload, err := DiscoveryPayload(DeviceInfo{Callsign: "KN4OQW", DMRID: "3180202", Version: "1.2.3", ConfigURL: "http://host/"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if topic != "homeassistant/device/kn4oqw/config" {
		t.Fatalf("topic = %q", topic)
	}
	var b map[string]json.RawMessage
	if err := json.Unmarshal(payload, &b); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	for _, k := range []string{"dev", "o", "avty_t", "stat_t", "cmps"} {
		if _, ok := b[k]; !ok {
			t.Errorf("discovery bundle missing %q", k)
		}
	}

	var dev map[string]any
	_ = json.Unmarshal(b["dev"], &dev)
	if dev["ids"] != "kn4oqw" || dev["sw"] != "1.2.3" || dev["sn"] != "3180202" || dev["cu"] != "http://host/" {
		t.Errorf("device block wrong: %+v", dev)
	}

	var cmps map[string]component
	_ = json.Unmarshal(b["cmps"], &cmps)
	if len(cmps) != EntityCount() || len(cmps) == 0 {
		t.Fatalf("want %d components, got %d", EntityCount(), len(cmps))
	}
	for key, c := range cmps {
		if c.Platform == "" {
			t.Errorf("component %q missing platform", key)
		}
		if c.UniqueID == "" {
			t.Errorf("component %q missing unique_id", key)
		}
	}
	// Availability + state topics are shared at the root, not per component.
	var avty, stat string
	_ = json.Unmarshal(b["avty_t"], &avty)
	_ = json.Unmarshal(b["stat_t"], &stat)
	if avty != "waypoint/status/kn4oqw/availability" || stat != "waypoint/status/kn4oqw/state" {
		t.Errorf("shared topics wrong: avty=%q stat=%q", avty, stat)
	}
}

// A blank callsign still produces a well-formed bundle under the fallback node.
func TestDiscoveryPayloadBlankCallsign(t *testing.T) {
	topic, _, err := DiscoveryPayload(DeviceInfo{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if topic != "homeassistant/device/waypoint/config" {
		t.Errorf("blank-callsign topic = %q", topic)
	}
}

// The state reducer mirrors the dashboard: keyup marks activity and sets the
// current transmitter; keydown clears activity and records the metrics; link and
// mode events update their fields.
func TestStateApply(t *testing.T) {
	at := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	s := NewState()
	if s.Active != "OFF" {
		t.Fatalf("initial active = %q, want OFF", s.Active)
	}

	s.Apply(hub.Event{Type: "mode", Mode: "DMR"})
	s.Apply(hub.Event{Type: "link", Network: "BM_3102", Detail: "linked"})
	s.Apply(hub.Event{Time: at, Type: "rf_voice_start", Mode: "DMR", Source: "KN4OQW", Dest: "TG 91"})

	if s.Active != "ON" || s.LastHeard != "KN4OQW" || s.LastTarget != "TG 91" || s.LastMode != "DMR" {
		t.Fatalf("after keyup: %+v", s)
	}
	if s.CurrentMode != "DMR" || s.Network != "linked" {
		t.Errorf("mode/link not applied: %+v", s)
	}
	if s.LastTime != at.Format(time.RFC3339) {
		t.Errorf("last_time = %q", s.LastTime)
	}

	s.Apply(hub.Event{Time: at.Add(3 * time.Second), Type: "rf_voice_end", Mode: "DMR", Source: "KN4OQW", Dest: "TG 91", Seconds: 3.4, BER: 0.5, RSSI: -70})
	if s.Active != "OFF" {
		t.Errorf("keydown should clear active: %q", s.Active)
	}
	if s.LastBER != 0.5 || s.LastRSSI != -70 || s.LastSeconds != 3.4 {
		t.Errorf("metrics not recorded: %+v", s)
	}
	// Last-heard persists after keydown (it's the most recent transmitter).
	if s.LastHeard != "KN4OQW" {
		t.Errorf("last_heard should persist: %q", s.LastHeard)
	}

	// State encodes with the field names the value_templates read.
	b, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"last_heard", "last_target", "last_mode", "last_time", "active", "current_mode", "network", "last_ber", "last_rssi", "last_duration"} {
		if _, ok := m[k]; !ok {
			t.Errorf("encoded state missing %q", k)
		}
	}
}
