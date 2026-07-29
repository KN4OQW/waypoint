package supervisor

import (
	"testing"
	"time"
)

// The reply is positional and unlabelled, so parsing it wrong means attributing a
// dead link to the wrong network. net1 is index 0.
func TestParseDMRGatewayStatusReply(t *testing.T) {
	got := ParseDMRGatewayStatusReply("xlx:n/a net1:conn net2:disc net3:n/a")
	want := map[int]Tri{0: TriYes, 1: TriNo, 2: TriUnknown}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("slot net%d = %v, want %v", i+1, got[i], w)
		}
	}
}

// The real reply from the bench node, verbatim.
func TestParseDMRGatewayStatusReplyFromHardware(t *testing.T) {
	got := ParseDMRGatewayStatusReply("xlx:n/a net1:conn")
	if len(got) != 1 || got[0] != TriYes {
		t.Errorf("got %v, want one connected slot", got)
	}
}

// Nothing in a malformed or empty reply may be read as a failure — a daemon that
// answers strangely is not a daemon reporting a dead link.
func TestParseDMRGatewayStatusReplyJunk(t *testing.T) {
	for _, reply := range []string{"", "KO", "garbage", "net:conn", "netX:conn", "net0:conn", "xlx:conn"} {
		for slot, v := range ParseDMRGatewayStatusReply(reply) {
			if v == TriNo {
				t.Errorf("reply %q produced a failure verdict at slot %d", reply, slot)
			}
		}
	}
}

// The poll outranks a remembered announcement: it reads the daemon's live state
// machine, and it is the only signal that reports a link down while the daemon
// quietly retries — the gap the bench WAN pull exposed.
func TestPollOutranksTheAnnouncement(t *testing.T) {
	h := newHarness(t, testAttachment())
	// The daemon last announced an attempt in progress, which on its own reads as
	// no-news-so-healthy...
	h.sup.ObserveEvent(hubGatewayStatus("BM_3102", "Opening DMR Network: BM_3102"))
	// ...but asked directly, it says the link is down.
	h.sup.Signals.LinkState = func() map[string]Tri { return map[string]Tri{"BM_3102": TriNo} }

	h.step(0)
	if e := h.lastClaim(t); e.Type != "link_down" {
		t.Fatalf("the poll should have taken the link down, got %s", e.Type)
	}
	for i := 0; i < 6; i++ {
		h.step(30 * time.Second)
	}
	if len(h.restarts) == 0 {
		t.Error("a link the daemon reports as disconnected was never remediated")
	}
}

// A poll that answers nothing must not be read as a failure.
func TestPollSilenceIsNotFailure(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.sup.Signals.LinkState = func() map[string]Tri { return nil }
	for i := 0; i < 10; i++ {
		h.step(30 * time.Second)
	}
	if len(h.restarts) != 0 {
		t.Errorf("an unanswered poll caused %d restarts", len(h.restarts))
	}
	if e := h.lastClaim(t); e.Type != "link_up" {
		t.Errorf("an unanswered poll should leave the link claimed up, got %s", e.Type)
	}
}

// The poll is not consulted while the node itself is offline: every link would
// report down for a reason that is not theirs, and remediation is frozen anyway.
func TestPollSkippedWhileOffline(t *testing.T) {
	h := newHarness(t, testAttachment())
	asked := 0
	h.sup.Signals.LinkState = func() map[string]Tri { asked++; return nil }
	h.wan = false
	for i := 0; i < 5; i++ {
		h.step(30 * time.Second)
	}
	if asked != 0 {
		t.Errorf("polled the daemon %d times while the node had no route out", asked)
	}
}
