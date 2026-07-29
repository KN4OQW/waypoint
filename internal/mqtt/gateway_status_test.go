package mqtt

import (
	"testing"

	"github.com/KN4OQW/waypoint/internal/status"
)

// A DMRGateway status envelope becomes a hub event naming the network it is about,
// so the supervisor can attribute it and the event log can show it.
func TestTranslateGatewayStatus(t *testing.T) {
	payload := []byte(`{"status":{"timestamp":"2026-07-29T12:00:00.000Z","message":"Logged into DMR Network: BM_3102"}}`)
	e, ok := TranslateGatewayStatus(payload)
	if !ok {
		t.Fatal("a well-formed status envelope was not translated")
	}
	if e.Type != status.TypeGatewayStatus {
		t.Errorf("type = %q", e.Type)
	}
	if e.Network != "BM_3102" {
		t.Errorf("network = %q", e.Network)
	}
	if e.Detail != "Logged into DMR Network: BM_3102" {
		t.Errorf("detail should carry the message verbatim: %q", e.Detail)
	}
	if e.Time.IsZero() {
		t.Error("the daemon's timestamp was dropped")
	}
}

func TestTranslateGatewayStatusFailure(t *testing.T) {
	e, ok := TranslateGatewayStatus([]byte(`{"status":{"message":"Failed login into DMR Network: TGIF"}}`))
	if !ok || e.Network != "TGIF" {
		t.Fatalf("failure message not translated: %+v ok=%v", e, ok)
	}
}

// Everything that is not a status envelope naming a network passes by without
// inventing an event — including the mode traffic MMDVM-Host publishes on its own
// <name>/json, in case the two are ever pointed at one topic.
func TestTranslateGatewayStatusIgnoresOtherPayloads(t *testing.T) {
	for _, payload := range []string{
		`{"DMR":{"action":"start","slot":2,"src_id":1234}}`,
		`{"status":{"message":"DMRGateway is starting"}}`,
		`{"status":{"message":""}}`,
		`{"MMDVM":{"mode":"DMR"}}`,
		`not json at all`,
		``,
		`{}`,
		`{"status":null}`,
	} {
		if e, ok := TranslateGatewayStatus([]byte(payload)); ok {
			t.Errorf("payload %q should not have produced an event, got %+v", payload, e)
		}
	}
}
