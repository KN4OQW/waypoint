package supervisor

import (
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/status"
)

// structuredLink builds the event shape internal/mqtt produces from DMRGateway
// 2a3306d's {"link":{...}} payload: a verdict in State, the daemon's tokens in
// Detail, and no prose to parse.
func structuredLink(network, state, detail string) hub.Event {
	return hub.Event{
		Type:    status.TypeGatewayStatus,
		Network: network,
		State:   state,
		Detail:  detail,
	}
}

func TestDMRGatewayLinkActions(t *testing.T) {
	for _, tc := range []struct {
		action string
		want   Tri
		ok     bool
	}{
		{"linking", TriYes, true},
		{"unlinked", TriNo, true},
		{"failed", TriNo, true},
		// Not ours to interpret. Reporting TriNo here would let a word upstream
		// added restart a node that is perfectly healthy.
		{"reconnecting", TriUnknown, false},
		{"", TriUnknown, false},
	} {
		got, ok := DMRGatewayLink(tc.action)
		if got != tc.want || ok != tc.ok {
			t.Errorf("DMRGatewayLink(%q) = %v,%v want %v,%v", tc.action, got, ok, tc.want, tc.ok)
		}
	}
}

// The structured verdict is read straight from State rather than re-parsed out of
// Detail. Detail here is deliberately NOT anything DMRGatewayStatus could match,
// so a pass proves the structured path carried it.
func TestObserveStructuredLinkUsesState(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.sup.ObserveEvent(structuredLink("BM_3102", status.StateDown, "failed: auth"))
	if got := h.sup.login["BM_3102"]; got != TriNo {
		t.Errorf("login = %v, want TriNo; the structured verdict was not read", got)
	}
	h.sup.ObserveEvent(structuredLink("BM_3102", status.StateUp, "linking: network"))
	if got := h.sup.login["BM_3102"]; got != TriYes {
		t.Errorf("login = %v, want TriYes", got)
	}
}

// A prose event still works, unchanged, and reaches the same verdict as the
// structured one for the same news. Both paths live side by side because a node on
// an older DMRGateway has only the prose.
func TestObserveProseStillWorksAlongsideStructured(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.sup.ObserveEvent(hub.Event{
		Type: status.TypeGatewayStatus, Network: "BM_3102",
		Detail: "Logged into DMR Network: BM_3102",
	})
	if got := h.sup.login["BM_3102"]; got != TriYes {
		t.Fatalf("the prose path stopped working: login = %v", got)
	}
	h.sup.ObserveEvent(structuredLink("BM_3102", status.StateDown, "failed: timeout"))
	if got := h.sup.login["BM_3102"]; got != TriNo {
		t.Errorf("a structured failure did not override an earlier prose success: %v", got)
	}
}

// The latch the tier-2 harness found, now driven through the structured path.
//
// A daemon in a reconnect loop alternates a failure and an in-progress attempt
// forever. An "unknown" arriving on top of a known failure must NOT clear it: if
// it did, the health clock would reset every few seconds, the grace period would
// never elapse, and the supervisor would watch a node fail indefinitely without
// ever acting. This is the same rule the prose path has, asserted for the shape
// that now carries the news.
func TestObserveStructuredUnknownDoesNotClearAFailure(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.sup.ObserveEvent(structuredLink("BM_3102", status.StateDown, "failed: timeout"))
	h.sup.ObserveEvent(structuredLink("BM_3102", status.StateUnknown, "opening"))
	if got := h.sup.login["BM_3102"]; got != TriNo {
		t.Errorf("login = %v, want TriNo; an in-progress attempt erased a known failure", got)
	}
	// A real success still clears it, or the latch would be a different bug.
	h.sup.ObserveEvent(structuredLink("BM_3102", status.StateUp, "linking: network"))
	if got := h.sup.login["BM_3102"]; got != TriYes {
		t.Errorf("login = %v, want TriYes; the latch never released", got)
	}
}

// An event with neither a usable State nor a parseable Detail changes nothing. The
// clean case matters as much as the failure cases: a supervisor that treated
// "I did not understand that" as "the link is down" would restart on noise.
func TestObserveUnreadableEventIsNoNews(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.sup.ObserveEvent(structuredLink("BM_3102", status.StateUp, "linking: network"))
	h.sup.ObserveEvent(structuredLink("BM_3102", "sideways", "who knows"))
	if got := h.sup.login["BM_3102"]; got != TriYes {
		t.Errorf("login = %v, want TriYes; an unreadable event moved the verdict", got)
	}
}

// End to end through the runner: a structured failure on a node whose unit is up
// and whose endpoint resolves is still enough to drive a restart, exactly as the
// prose failure is. This is the assertion that the new path is actually wired to
// the decision and not just to a map.
func TestStructuredFailureDrivesRemediation(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.step(0)
	h.wan, h.resolves = true, true
	h.sup.Signals.UnitActive = func(string) Tri { return TriYes }
	h.sup.ObserveEvent(structuredLink("BM_3102", status.StateDown, "failed: auth"))
	for i := 0; i < 4; i++ { // past the 90s grace
		h.step(30 * time.Second)
	}
	if len(h.restarts) != 1 {
		t.Fatalf("expected one restart from a structured failure, got %v", h.restarts)
	}
}
