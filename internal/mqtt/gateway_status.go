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
// The same topic also carries {"link":{...}} from DMRGateway 2a3306d onward, which
// is the structured form of the same news; both shapes are decoded from one
// payload because a daemon publishes them on one topic.
type gatewayStatusEnvelope struct {
	Status *struct {
		Timestamp string `json:"timestamp"`
		Message   string `json:"message"`
	} `json:"status"`
	Link *struct {
		Timestamp string `json:"timestamp"`
		Action    string `json:"action"`
		Reason    string `json:"reason"`
		Network   string `json:"network"`
	} `json:"link"`
}

// TranslateGatewayStatus decodes one gateway status payload into a hub event.
// ok is false for anything that is not a status envelope naming a network, so the
// gateways' other json messages pass by without inventing events.
func TranslateGatewayStatus(payload []byte) (hub.Event, bool) {
	var env gatewayStatusEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return hub.Event{}, false
	}
	// The structured event first: where a daemon publishes both, it is the one
	// whose meaning does not depend on upstream's wording.
	if e, ok := translateGatewayLink(env); ok {
		return e, true
	}
	if env.Status == nil {
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

// translateGatewayLink decodes the structured {"link":{...}} form.
//
// State carries the verdict so the supervisor does not have to re-read prose, and
// Detail carries the daemon's own action and reason tokens — "failed: auth" — and
// nothing else. That is not a formatting choice made lightly: a sentence composed
// here would be a user-facing English string generated in Go, which this project
// cannot translate (CLAUDE.md). The daemon's vocabulary is data, the same way the
// prose path's message already is. Turning "auth" into "the password was rejected"
// belongs in the UI catalogs, where a translator can reach it.
func translateGatewayLink(env gatewayStatusEnvelope) (hub.Event, bool) {
	if env.Link == nil || env.Link.Network == "" {
		return hub.Event{}, false
	}
	login, ok := supervisor.DMRGatewayLink(env.Link.Action)
	if !ok {
		return hub.Event{}, false // an action this build does not know is no news
	}
	detail := env.Link.Action
	if env.Link.Reason != "" {
		detail += ": " + env.Link.Reason
	}
	return hub.Event{
		Time:    parseTimestamp(env.Link.Timestamp),
		Type:    status.TypeGatewayStatus,
		Network: env.Link.Network,
		State:   supervisor.LinkStateString(login),
		Detail:  detail,
	}, true
}
