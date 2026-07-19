package status

import (
	"encoding/json"
	"strings"
	"testing"
)

func discoveryByTopic(ds []Discovery) map[string]map[string]any {
	m := map[string]map[string]any{}
	for _, d := range ds {
		var p map[string]any
		_ = json.Unmarshal(d.Payload, &p)
		m[d.Topic] = p
	}
	return m
}

// Property 1: the config set covers mode/tx/feed plus one binary sensor per
// gateway and network, each valid HA JSON with the right topics + device block.
func TestDiscoveryConfigs(t *testing.T) {
	s := Status{
		Mode:     "DMR",
		TX:       &Transmission{Source: "W1ABC", Dest: "TG 91"},
		Feed:     Feed{Connected: true},
		Gateways: map[string]Link{"dmrgateway": {Up: true}},
		Networks: map[string]Link{"BM 3103": {Up: true}}, // space → sanitized
	}
	opts := DiscoveryOptions{Prefix: "homeassistant", NodeID: "hs-shack", StatePrefix: "waypoint/status", Version: "1.2.3"}
	got := discoveryByTopic(DiscoveryConfigs(s, opts))

	want := []string{
		"homeassistant/sensor/hs-shack/mode/config",
		"homeassistant/sensor/hs-shack/tx/config",
		"homeassistant/binary_sensor/hs-shack/feed/config",
		"homeassistant/binary_sensor/hs-shack/gateway_dmrgateway/config",
		"homeassistant/binary_sensor/hs-shack/network_BM_3103/config", // space → underscore
	}
	if len(got) != len(want) {
		t.Fatalf("got %d configs, want %d: %v", len(got), len(want), topics(got))
	}
	for _, tp := range want {
		p, ok := got[tp]
		if !ok {
			t.Errorf("missing config topic %s", tp)
			continue
		}
		if p["availability_topic"] != "waypoint/status/availability" {
			t.Errorf("%s: availability_topic = %v", tp, p["availability_topic"])
		}
		if p["state_topic"] == "" || p["state_topic"] == nil {
			t.Errorf("%s: missing state_topic", tp)
		}
		dev, _ := p["device"].(map[string]any)
		if dev == nil {
			t.Errorf("%s: missing device block", tp)
			continue
		}
		ids, _ := dev["identifiers"].([]any)
		if len(ids) != 1 || ids[0] != "waypoint_hs-shack" {
			t.Errorf("%s: device identifiers = %v", tp, dev["identifiers"])
		}
		if dev["sw_version"] != "1.2.3" {
			t.Errorf("%s: device sw_version = %v", tp, dev["sw_version"])
		}
	}

	// The mode sensor points at the mode topic.
	if got["homeassistant/sensor/hs-shack/mode/config"]["state_topic"] != "waypoint/status/mode" {
		t.Errorf("mode state_topic wrong")
	}
	// The gateway binary sensor points at the per-gateway state topic and is a
	// liveness sensor (value_json.up, on/off).
	gw := got["homeassistant/binary_sensor/hs-shack/gateway_dmrgateway/config"]
	if gw["state_topic"] != "waypoint/status/gateway/dmrgateway" {
		t.Errorf("gateway state_topic = %v", gw["state_topic"])
	}
	if gw["value_template"] != "{{ value_json.up }}" || gw["payload_on"] != "True" || gw["payload_off"] != "False" {
		t.Errorf("gateway binary-sensor template/payloads wrong: %+v", gw)
	}
	if gw["device_class"] != "running" {
		t.Errorf("gateway device_class = %v, want running", gw["device_class"])
	}
}

// Property 3: the TX sensor's value_template renders "Idle" on the empty payload
// and source→dest on a real one (branch is explicit in the template).
func TestDiscoveryTXTemplate(t *testing.T) {
	ds := DiscoveryConfigs(Status{Mode: "IDLE"}, DiscoveryOptions{NodeID: "n", StatePrefix: "waypoint/status"})
	tx := discoveryByTopic(ds)["homeassistant/sensor/n/tx/config"]
	tpl, _ := tx["value_template"].(string)
	if !strings.Contains(tpl, "Idle") || !strings.Contains(tpl, "value_json.source") {
		t.Errorf("tx value_template missing Idle/source branches: %q", tpl)
	}
}

// Property 2: no gateway/network entities when the status has none — just the
// three static ones — and defaults fill in when opts are sparse.
func TestDiscoveryDefaultsAndEmpty(t *testing.T) {
	ds := DiscoveryConfigs(Status{Mode: "IDLE"}, DiscoveryOptions{StatePrefix: "waypoint/status"})
	if len(ds) != 3 {
		t.Fatalf("empty status should yield 3 static entities, got %d", len(ds))
	}
	for _, d := range ds {
		if !strings.HasPrefix(d.Topic, "homeassistant/") { // default prefix
			t.Errorf("default HA prefix not applied: %s", d.Topic)
		}
		if !strings.Contains(d.Topic, "/waypoint/") { // default node id
			t.Errorf("default node id not applied: %s", d.Topic)
		}
	}
}

func TestAvailabilityTopic(t *testing.T) {
	if got := AvailabilityTopic("waypoint/status"); got != "waypoint/status/availability" {
		t.Errorf("AvailabilityTopic = %q", got)
	}
	if got := AvailabilityTopic("waypoint/status/"); got != "waypoint/status/availability" {
		t.Errorf("trailing slash not trimmed: %q", got)
	}
}

func topics(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
