package supervisor

import (
	"strconv"
	"strings"
)

// hint.go reads what a gateway daemon says about its own upstream links.
//
// DMRGateway publishes these on its MQTT status plane, not to a log file, so
// consuming them does not breach RFC-0008's ban on log scraping — nothing here
// opens /var/log, and disabling every log file on the node changes nothing. But it
// is worth being honest about what this is: the payload is
// {"status":{"message":"Logged into DMR Network: BM_3102"}}, and the message is
// English prose assembled with string concatenation upstream. A wording change in
// a future DMRGateway breaks these matches silently.
//
// That is exactly why it is a hint and never the authority. It contributes one
// signal to an Observation alongside the systemd unit state and Waypoint's own
// endpoint probe, and the policy treats an unrecognised message as no news rather
// than bad news. If upstream rewords every one of these tomorrow, the supervisor
// loses its fastest detection path and keeps working on the other two signals —
// slower, not wrong. What it buys meanwhile is real: DMRGateway announces a failed
// login the instant the master says no, which no external probe can see at all.
//
// Verified against the pinned DMRGateway (79edbc4, DMRNetwork.cpp/DMRGateway.cpp).

// ParseDMRGatewayStatusReply reads DMRGateway's answer to the "status" remote
// command — "xlx:n/a net1:conn net2:disc" — into per-slot login state, keyed by
// the zero-based index that names map onto (net1 is index 0).
//
// This is the better of the two daemon-side signals and the reason [Remote
// Commands] is enabled in the rendered config. The announcements in the rest of
// this file only arrive when something changes, so a supervisor that has just
// started, or that missed a message, holds a stale view indefinitely; this can be
// asked at any time and answers from CDMRNetwork::isConnected — the login state
// machine itself, not a recollection of what it last said.
//
// "n/a" means the slot is disabled or absent, which is not a failure: unknown.
// Anything unrecognised is likewise unknown rather than assumed bad.
func ParseDMRGatewayStatusReply(reply string) map[int]Tri {
	out := map[int]Tri{}
	for _, field := range strings.Fields(reply) {
		name, state, ok := strings.Cut(field, ":")
		if !ok {
			continue
		}
		idx, ok := strings.CutPrefix(name, "net")
		if !ok {
			continue // "xlx" has its own section and no netN slot
		}
		n, err := strconv.Atoi(idx)
		if err != nil || n < 1 {
			continue
		}
		switch state {
		case "conn":
			out[n-1] = TriYes
		case "disc":
			out[n-1] = TriNo
		default: // "n/a" — disabled or not configured
			out[n-1] = TriUnknown
		}
	}
	return out
}

const (
	dmrLoggedIn      = "Logged into DMR Network: "
	dmrFailedLogin   = "Failed login into DMR Network: "
	dmrFailedConnect = "Failed connection into DMR Network: "
	dmrClosing       = "Connection closing into DMR Network: "
	dmrOpening       = "Opening DMR Network: "
)

// DMRGatewayStatus reads one DMRGateway status message into a per-network login
// verdict. ok is false for the messages that name no network ("DMRGateway is
// starting") and for anything unrecognised.
//
// "Opening" reports TriUnknown rather than a failure, and does so with ok true:
// the connection is being established, so any previous "logged in" is void, but
// nothing has gone wrong yet. Clearing a stale success matters — a gateway that
// re-opens a socket and never completes the login would otherwise keep the last
// good verdict forever, which is the latch this whole area of the code is about.
func DMRGatewayStatus(message string) (network string, login Tri, ok bool) {
	message = strings.TrimSpace(message)
	for _, c := range []struct {
		prefix string
		login  Tri
	}{
		{dmrLoggedIn, TriYes},
		{dmrFailedLogin, TriNo},
		{dmrFailedConnect, TriNo},
		{dmrClosing, TriNo},
		{dmrOpening, TriUnknown},
	} {
		if name, found := strings.CutPrefix(message, c.prefix); found {
			name = strings.TrimSpace(name)
			if name == "" {
				return "", TriUnknown, false
			}
			return name, c.login, true
		}
	}
	return "", TriUnknown, false
}
