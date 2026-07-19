package status

import (
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
)

var t0 = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

// Property 1: the fold maps each event type to the right transition.
func TestFold(t *testing.T) {
	a := New(DefaultTxTTL)

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
	a := New(10 * time.Second)
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
	a := New(10 * time.Second)
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
	a := New(DefaultTxTTL)
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

// OnChange delivers snapshots and unregisters cleanly.
func TestOnChange(t *testing.T) {
	a := New(DefaultTxTTL)
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
