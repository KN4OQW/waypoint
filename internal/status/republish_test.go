package status

import (
	"encoding/json"
	"testing"
)

// Property 6: a status change publishes the specified retained topics/payloads.
func TestRepublish(t *testing.T) {
	got := map[string]string{}
	publish := func(topic string, payload []byte) { got[topic] = string(payload) }

	s := Status{
		Mode: "DMR",
		TX:   &Transmission{Mode: "DMR", Source: "W1ABC", Dest: "TG 91", Direction: "rf", StartedAt: t0},
		Networks: map[string]Link{
			"BM 3103": {Up: true, Detail: "logged in", Since: t0}, // space → topic-safe underscore
		},
		Gateways: map[string]Link{"dmrgateway": {Up: false, Detail: "not running", Since: t0}},
		Feed:     Feed{Connected: true, Since: t0},
	}
	Republish(s, "waypoint/status", publish)

	if got["waypoint/status/mode"] != "DMR" {
		t.Errorf("mode topic = %q", got["waypoint/status/mode"])
	}
	if _, ok := got["waypoint/status/tx"]; !ok || got["waypoint/status/tx"] == "" {
		t.Errorf("tx topic should carry the transmission JSON: %q", got["waypoint/status/tx"])
	}
	// The space in the network name must be made topic-safe.
	if _, ok := got["waypoint/status/network/BM_3103"]; !ok {
		t.Errorf("network topic not published topic-safe: %v", keys(got))
	}
	if _, ok := got["waypoint/status/gateway/dmrgateway"]; !ok {
		t.Errorf("gateway topic missing: %v", keys(got))
	}
	// tx must be valid JSON with no secret-ish field (there are none in the model).
	var tx map[string]any
	if err := json.Unmarshal([]byte(got["waypoint/status/tx"]), &tx); err != nil {
		t.Errorf("tx payload not JSON: %v", err)
	}
}

// An idle status clears the tx topic with an empty retained payload.
func TestRepublishIdleClearsTX(t *testing.T) {
	got := map[string]string{}
	Republish(Status{Mode: "IDLE"}, "waypoint/status", func(topic string, p []byte) { got[topic] = string(p) })
	if v, ok := got["waypoint/status/tx"]; !ok || v != "" {
		t.Errorf("idle tx topic should be empty, got %q (ok=%v)", v, ok)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
