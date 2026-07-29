package mqtt

import (
	"encoding/json"

	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/status"
	"github.com/KN4OQW/waypoint/internal/supervisor"
)

// gateway_status.go consumes the gateway daemons' own status plane.
//
// MMDVM-Host's <name>/json carries per-mode voice traffic (bridge.go). The
// gateways publish a different shape on their own <name>/json: a status envelope
// announcing what happened to their upstream links — DMRGateway says "Logged into
// DMR Network: BM_3102" the moment a master accepts it, and "Failed login into DMR
// Network: BM_3102" the moment one refuses. That is the fastest available notice
// of a link change, and no external probe can see it at all.
//
// It becomes an ordinary hub event, which means it persists to events.db and shows
// in the event log like everything else. It is NOT folded into link state here:
// the supervisor weighs it against the systemd unit state and Waypoint's own
// endpoint probe before claiming anything about a link (see supervisor/hint.go for
// why this source is a hint rather than an authority).

// gatewayStatusEnvelope is what a gateway daemon publishes: {"status":{...}}.
type gatewayStatusEnvelope struct {
	Status *struct {
		Timestamp string `json:"timestamp"`
		Message   string `json:"message"`
	} `json:"status"`
}

// TranslateGatewayStatus decodes one gateway status payload into a hub event.
// ok is false for anything that is not a status envelope naming a network, so the
// gateways' other json messages pass by without inventing events.
func TranslateGatewayStatus(payload []byte) (hub.Event, bool) {
	var env gatewayStatusEnvelope
	if err := json.Unmarshal(payload, &env); err != nil || env.Status == nil {
		return hub.Event{}, false
	}
	network, _, ok := supervisor.DMRGatewayStatus(env.Status.Message)
	if !ok {
		return hub.Event{}, false
	}
	return hub.Event{
		Time:    parseTimestamp(env.Status.Timestamp),
		Type:    status.TypeGatewayStatus,
		Network: network,
		Detail:  env.Status.Message,
	}, true
}
