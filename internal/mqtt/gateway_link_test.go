package mqtt

import (
	"testing"

	"github.com/KN4OQW/waypoint/internal/status"
)

// The structured link event is DMRGateway 2a3306d's replacement for reading
// English out of a log line. These tests pin the wire shape, because the whole
// value of the structured form is that it does not move when upstream rewords
// something — an assertion that only checks our own decoder proves nothing about
// that. The payloads below are copied from schema.json and from the sprintf in
// DMRGateway.cpp writeJSONLink.

func TestTranslateGatewayLinkConnected(t *testing.T) {
	payload := []byte(`{"link":{"timestamp":"2026-08-23T12:00:00.000Z","action":"linking","reason":"network","network":"BM_3102"}}`)
	e, ok := TranslateGatewayStatus(payload)
	if !ok {
		t.Fatal("a well-formed link event was not translated")
	}
	if e.Type != status.TypeGatewayStatus {
		t.Errorf("type = %q", e.Type)
	}
	if e.Network != "BM_3102" {
		t.Errorf("network = %q; the event names it directly and needs no positional mapping", e.Network)
	}
	if e.State != status.StateUp {
		t.Errorf("state = %q, want %q", e.State, status.StateUp)
	}
	if e.Detail != "linking: network" {
		t.Errorf("detail = %q; it should carry the daemon's own tokens and no composed prose", e.Detail)
	}
	if e.Time.IsZero() {
		t.Error("the daemon's timestamp was dropped")
	}
}

// The wrong-password case, which is the reason this event is worth consuming at
// all: "failed" with reason "auth" is a mistyped password specifically, and was
// previously indistinguishable from any other failed login.
func TestTranslateGatewayLinkWrongPassword(t *testing.T) {
	e, ok := TranslateGatewayStatus([]byte(`{"link":{"action":"failed","reason":"auth","network":"TGIF"}}`))
	if !ok {
		t.Fatal("a failed link event was not translated")
	}
	if e.State != status.StateDown {
		t.Errorf("state = %q, want %q", e.State, status.StateDown)
	}
	if e.Detail != "failed: auth" {
		t.Errorf("detail = %q; an operator has to be able to tell auth from timeout", e.Detail)
	}
}

// Every reason upstream documents must decode, and the ones that mean a lost link
// must all read as down. A table that only carried the happy case would let a new
// reason silently become "no news".
func TestTranslateGatewayLinkEveryDocumentedReason(t *testing.T) {
	for _, tc := range []struct {
		action, reason, want string
	}{
		{"linking", "network", status.StateUp},
		{"unlinked", "network", status.StateDown},
		{"failed", "login", status.StateDown},
		{"failed", "auth", status.StateDown},
		{"failed", "config", status.StateDown},
		{"failed", "session", status.StateDown},
		{"failed", "closed", status.StateDown},
		{"failed", "timeout", status.StateDown},
	} {
		e, ok := TranslateGatewayStatus([]byte(
			`{"link":{"action":"` + tc.action + `","reason":"` + tc.reason + `","network":"N"}}`))
		if !ok {
			t.Errorf("%s/%s was not translated", tc.action, tc.reason)
			continue
		}
		if e.State != tc.want {
			t.Errorf("%s/%s: state = %q, want %q", tc.action, tc.reason, e.State, tc.want)
		}
	}
}

// An action this build does not know is NO NEWS, not bad news. Upstream may add
// one, and a supervisor that read an unfamiliar word as "down" would restart a
// healthy node over vocabulary drift — the exact failure this project's rules
// about ungrounded checks exist to prevent.
func TestTranslateGatewayLinkUnknownActionIsIgnored(t *testing.T) {
	for _, payload := range []string{
		`{"link":{"action":"reconnecting","reason":"network","network":"BM_3102"}}`,
		`{"link":{"action":"","network":"BM_3102"}}`,
		`{"link":{"action":"linking","network":""}}`,
		`{"link":{}}`,
	} {
		if e, ok := TranslateGatewayStatus([]byte(payload)); ok {
			t.Errorf("%s invented an event: %+v", payload, e)
		}
	}
}

// The prose path is not replaced. A node running a DMRGateway older than 2a3306d
// publishes only the status envelope, and it must keep working exactly as before —
// a pin rollback should degrade to the previous behaviour, not to silence.
func TestTranslateGatewayLinkLeavesTheProsePathIntact(t *testing.T) {
	e, ok := TranslateGatewayStatus([]byte(`{"status":{"message":"Logged into DMR Network: BM_3102"}}`))
	if !ok || e.Network != "BM_3102" {
		t.Fatalf("the prose path stopped working: %+v ok=%v", e, ok)
	}
	if e.State != "" {
		t.Errorf("a prose event carries no structured verdict, got state %q", e.State)
	}
}

// Where a daemon publishes both in one payload the structured one wins, because
// its meaning does not depend on a wording upstream is free to change.
func TestTranslateGatewayLinkPreferredOverProse(t *testing.T) {
	e, ok := TranslateGatewayStatus([]byte(
		`{"status":{"message":"Failed login into DMR Network: BM_3102"},` +
			`"link":{"action":"failed","reason":"auth","network":"BM_3102"}}`))
	if !ok {
		t.Fatal("a payload carrying both shapes was not translated")
	}
	if e.Detail != "failed: auth" {
		t.Errorf("detail = %q; the structured event should have won", e.Detail)
	}
}
