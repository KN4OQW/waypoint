package status

import (
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
)

var t0 = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

// Property 1: the fold maps each event type to the right transition.
func TestFold(t *testing.T) {
	a := New(DefaultTxTTL, LinkTTLOff)

	a.Apply(hub.Event{Type: TypeRFStart, Mode: "DMR", Slot: 2, Source: "W1ABC", Dest: "TG 91", Time: t0})
	s := a.Snapshot()
	if s.Mode != "DMR" || s.TX == nil || s.TX.Source != "W1ABC" || s.TX.Direction != "rf" || s.TX.Dest != "TG 91" {
		t.Fatalf("rf start not folded: %+v", s)
	}

	a.Apply(hub.Event{Type: TypeRFEnd, Mode: "DMR", Slot: 2, Time: t0.Add(3 * time.Second)})
	s = a.Snapshot()
	if s.TX != nil || s.Mode != "IDLE" {
		t.Fatalf("rf end did not clear to idle: %+v", s)
	}

	a.Apply(hub.Event{Type: TypeNetStart, Mode: "YSF", Source: "REF", Time: t0})
	if s := a.Snapshot(); s.TX == nil || s.TX.Direction != "network" || s.Mode != "YSF" {
		t.Fatalf("net start not folded: %+v", s)
	}
	a.Apply(hub.Event{Type: TypeNetEnd, Time: t0})

	a.Apply(hub.Event{Type: TypeLink, Network: "BM_3103", Detail: "logged in", Time: t0})
	if s := a.Snapshot(); !s.Networks["BM_3103"].Up {
		t.Errorf("link not folded: %+v", s.Networks)
	}

	a.Apply(hub.Event{Type: TypeGWDown, Network: "DMRGateway", Detail: "inactive", Time: t0})
	if s := a.Snapshot(); s.Gateways["DMRGateway"].Up {
		t.Errorf("gateway_down should set Up=false: %+v", s.Gateways)
	}
	a.Apply(hub.Event{Type: TypeGWUp, Network: "DMRGateway", Time: t0})
	if s := a.Snapshot(); !s.Gateways["DMRGateway"].Up {
		t.Errorf("gateway_up should set Up=true: %+v", s.Gateways)
	}

	a.Apply(hub.Event{Type: TypeFeedDown, Detail: "connection lost", Time: t0})
	if s := a.Snapshot(); s.Feed.Connected {
		t.Errorf("feed_down should set Connected=false: %+v", s.Feed)
	}
	a.Apply(hub.Event{Type: TypeFeedUp, Time: t0})
	if s := a.Snapshot(); !s.Feed.Connected {
		t.Errorf("feed_up should set Connected=true: %+v", s.Feed)
	}
}

// Property 2: a transmission with no closing event self-clears after txTTL — the
// #117 "TX timer counts forever" / #155 "stuck Listening" fix.
func TestSelfHeal(t *testing.T) {
	a := New(10*time.Second, LinkTTLOff)
	a.Apply(hub.Event{Type: TypeRFStart, Mode: "M17", Source: "W1ABC", Time: t0})
	if a.Snapshot().TX == nil {
		t.Fatal("TX should be active after start")
	}
	// Before the deadline: still active.
	a.Expire(t0.Add(5 * time.Second))
	if a.Snapshot().TX == nil {
		t.Fatal("TX expired too early (before txTTL)")
	}
	// Past the deadline: self-cleared to idle, with no end event ever arriving.
	a.Expire(t0.Add(11 * time.Second))
	s := a.Snapshot()
	if s.TX != nil || s.Mode != "IDLE" {
		t.Fatalf("stranded TX did not self-heal: %+v", s)
	}
}

// Property 3: a normal start→end never trips the watchdog, and refreshing before
// the deadline extends it.
func TestNoFalseExpiry(t *testing.T) {
	a := New(10*time.Second, LinkTTLOff)
	a.Apply(hub.Event{Type: TypeRFStart, Mode: "DMR", Source: "W1ABC", Time: t0})
	a.Expire(t0.Add(9 * time.Second)) // within window
	if a.Snapshot().TX == nil {
		t.Fatal("TX wrongly expired within window")
	}
	// A refresh (late_entry-style re-start) pushes the deadline out.
	a.Apply(hub.Event{Type: TypeRFStart, Mode: "DMR", Source: "W1ABC", Time: t0.Add(9 * time.Second)})
	a.Expire(t0.Add(15 * time.Second)) // past the ORIGINAL deadline, within the refreshed one
	if a.Snapshot().TX == nil {
		t.Fatal("refresh did not extend the watchdog")
	}
}

// Property (change detection): an event that changes nothing observable does not
// notify listeners or bump UpdatedAt.
func TestNoChurnOnNoop(t *testing.T) {
	a := New(DefaultTxTTL, LinkTTLOff)
	var notifications int
	a.OnChange(func(Status) { notifications++ })

	a.Apply(hub.Event{Type: TypeMode, Mode: "DMR", Time: t0})
	if notifications != 1 {
		t.Fatalf("first mode change should notify once, got %d", notifications)
	}
	// Same mode again → no change → no notification.
	a.Apply(hub.Event{Type: TypeMode, Mode: "DMR", Time: t0.Add(time.Second)})
	if notifications != 1 {
		t.Errorf("no-op mode should not notify, got %d", notifications)
	}
	// An expire with no active TX is a no-op.
	a.Expire(t0.Add(time.Hour))
	if notifications != 1 {
		t.Errorf("no-op expire should not notify, got %d", notifications)
	}
}

// A network link must be able to come back DOWN. Before #22 the fold hardcoded
// Up:true for every link event, so a network could only ever be raised and never
// lowered — a latch of exactly the class RFC-0008 exists to prevent, and one that
// would have made "status honest during an outage" impossible by construction.
func TestLinkGoesDown(t *testing.T) {
	a := New(DefaultTxTTL, LinkTTLOff)

	a.Apply(hub.Event{Type: TypeLinkUp, Network: "BM_3103", Detail: "logged in", Time: t0})
	if s := a.Snapshot(); !s.Networks["BM_3103"].Up {
		t.Fatalf("link_up should raise the network: %+v", s.Networks)
	}

	a.Apply(hub.Event{Type: TypeLinkDown, Network: "BM_3103", Detail: "master closed the connection", Time: t0.Add(time.Minute)})
	s := a.Snapshot()
	if s.Networks["BM_3103"].Up {
		t.Fatalf("link_down should lower the network: %+v", s.Networks)
	}
	if s.Networks["BM_3103"].Detail != "master closed the connection" {
		t.Errorf("link_down should carry its reason: %q", s.Networks["BM_3103"].Detail)
	}
	if !s.Networks["BM_3103"].Since.Equal(t0.Add(time.Minute)) {
		t.Errorf("a state change should move Since: %v", s.Networks["BM_3103"].Since)
	}

	// A link_down for a network never seen before is still information.
	a.Apply(hub.Event{Type: TypeLinkDown, Network: "TGIF", Detail: "no route", Time: t0})
	if l, ok := a.Snapshot().Networks["TGIF"]; !ok || l.Up {
		t.Errorf("link_down should record an unseen network as down: %+v", l)
	}
}

// Re-confirming a link that is already up is not a state change: Since must hold
// (so "linked since 09:14" survives a supervisor that re-asserts every few
// seconds) and listeners must not be woken.
func TestLinkReconfirmationIsNotAChange(t *testing.T) {
	a := New(DefaultTxTTL, 30*time.Second)
	var notifications int
	a.OnChange(func(Status) { notifications++ })

	a.Apply(hub.Event{Type: TypeLinkUp, Network: "BM_3103", Detail: "logged in", Time: t0})
	if notifications != 1 {
		t.Fatalf("first link should notify once, got %d", notifications)
	}
	a.Apply(hub.Event{Type: TypeLinkUp, Network: "BM_3103", Detail: "logged in", Time: t0.Add(20 * time.Second)})
	if notifications != 1 {
		t.Errorf("re-confirmation should not notify, got %d", notifications)
	}
	if since := a.Snapshot().Networks["BM_3103"].Since; !since.Equal(t0) {
		t.Errorf("re-confirmation must not reset Since: got %v, want %v", since, t0)
	}
}

// Losing the MMDVM-Host feed means nothing can re-assert a network link, so the
// links stop being claimed. Gateway liveness comes from systemd and is untouched.
func TestFeedDownUnconfirmsNetworks(t *testing.T) {
	a := New(DefaultTxTTL, LinkTTLOff)
	a.Apply(hub.Event{Type: TypeLink, Network: "BM_3103", Detail: "logged in", Time: t0})
	a.Apply(hub.Event{Type: TypeGWUp, Network: "dmrgateway", Time: t0})

	a.Apply(hub.Event{Type: TypeFeedDown, Detail: "connection lost", Time: t0.Add(time.Minute)})
	s := a.Snapshot()
	if s.Networks["BM_3103"].Up {
		t.Errorf("a link must not stay claimed once the feed that asserts it is gone: %+v", s.Networks)
	}
	if !s.Gateways["dmrgateway"].Up {
		t.Errorf("feed loss says nothing about systemd liveness: %+v", s.Gateways)
	}

	// It comes back when the link events resume — not merely because the feed did.
	a.Apply(hub.Event{Type: TypeFeedUp, Time: t0.Add(2 * time.Minute)})
	if a.Snapshot().Networks["BM_3103"].Up {
		t.Errorf("feed_up alone is not confirmation of a link: %+v", a.Snapshot().Networks)
	}
	a.Apply(hub.Event{Type: TypeLinkUp, Network: "BM_3103", Detail: "logged in", Time: t0.Add(3 * time.Minute)})
	if !a.Snapshot().Networks["BM_3103"].Up {
		t.Errorf("a fresh link event should restore the link: %+v", a.Snapshot().Networks)
	}
}

// The link watchdog: an armed confirmation window lowers a link nothing has
// re-asserted, and leaves a re-asserted one alone. Same "truth is a function of
// time" rule as the TX watchdog, applied to link state.
func TestLinkWatchdog(t *testing.T) {
	a := New(DefaultTxTTL, 30*time.Second)
	a.Apply(hub.Event{Type: TypeLinkUp, Network: "BM_3103", Detail: "logged in", Time: t0})

	a.Expire(t0.Add(29 * time.Second))
	if !a.Snapshot().Networks["BM_3103"].Up {
		t.Fatal("link expired inside its confirmation window")
	}
	// Re-confirmed in time: the deadline moves out.
	a.Apply(hub.Event{Type: TypeLinkUp, Network: "BM_3103", Detail: "logged in", Time: t0.Add(29 * time.Second)})
	a.Expire(t0.Add(45 * time.Second)) // past the ORIGINAL deadline, inside the refreshed one
	if !a.Snapshot().Networks["BM_3103"].Up {
		t.Fatal("re-confirmation did not extend the watchdog")
	}
	// Nothing re-confirms it again: it stops being claimed, and says why.
	a.Expire(t0.Add(70 * time.Second))
	l := a.Snapshot().Networks["BM_3103"]
	if l.Up {
		t.Fatalf("unconfirmed link stayed claimed: %+v", l)
	}
	if l.Detail == "" {
		t.Error("an expired link should say why it is no longer claimed")
	}
	// Once down it stays down without churning — no deadline left to trip.
	var notifications int
	a.OnChange(func(Status) { notifications++ })
	a.Expire(t0.Add(10 * time.Minute))
	if notifications != 0 {
		t.Errorf("an already-down link should not re-expire, got %d notifications", notifications)
	}
}

// With the watchdog disabled (today's default, because nothing re-confirms links
// yet) a link is never lowered by the clock alone — only by an explicit down.
func TestLinkWatchdogOffNeverExpires(t *testing.T) {
	a := New(DefaultTxTTL, LinkTTLOff)
	a.Apply(hub.Event{Type: TypeLinkUp, Network: "BM_3103", Detail: "logged in", Time: t0})
	a.Expire(t0.Add(24 * time.Hour))
	if !a.Snapshot().Networks["BM_3103"].Up {
		t.Error("link expired with the watchdog disabled")
	}
}

// OnChange delivers snapshots and unregisters cleanly.
func TestOnChange(t *testing.T) {
	a := New(DefaultTxTTL, LinkTTLOff)
	var last Status
	cancel := a.OnChange(func(s Status) { last = s })
	a.Apply(hub.Event{Type: TypeRFStart, Mode: "P25", Source: "W1ABC", Time: t0})
	if last.TX == nil || last.TX.Mode != "P25" {
		t.Fatalf("listener did not receive the change: %+v", last)
	}
	cancel()
	prev := last
	a.Apply(hub.Event{Type: TypeRFEnd, Time: t0})
	if last.TX != prev.TX {
		t.Error("listener fired after cancel")
	}
}
