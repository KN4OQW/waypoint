package wxfeed

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// testPolicy is a Policy that says yes to warnings and to NEW, which is the
// shipped default shape.
type testPolicy struct {
	announceActions map[string]bool
	routedClasses   map[string]bool
}

func newTestPolicy() *testPolicy {
	return &testPolicy{
		announceActions: map[string]bool{"NEW": true},
		routedClasses:   map[string]bool{"W": true, "A": true},
	}
}

func (p *testPolicy) ShouldAnnounce(a string) bool { return p.announceActions[strings.ToUpper(a)] }
func (p *testPolicy) Announces(_, sig string) bool { return p.routedClasses[strings.ToUpper(sig)] }

func alertJSON(t *testing.T, a Alert) []byte {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func liveWarning() Alert {
	return Alert{
		ID: 1234, VTEC: "KMOB.TO.W.0012.2026", Event: "Tornado Warning",
		Office: "KMOB", Phenomena: "TO", Significance: "W", ETN: 12,
		Action: "NEW", Status: "active", Severity: "Extreme",
		Headline: "TORNADO WARNING IN EFFECT UNTIL 345 PM CDT",
		SAME:     []string{"012113", "001003"},
		Ends:     time.Date(2026, 8, 11, 20, 45, 0, 0, time.UTC),
	}
}

// The property the whole feature's safety rests on. A retained message is state,
// not an event, and announcing the retained burst would transmit for every
// hazard already in effect on every reconnect.
func TestRetainedNeverAnnounces(t *testing.T) {
	a := liveWarning()
	_, v := Decide("wxalerts/nws/v1/same/012113/12", alertJSON(t, a), true, nil, newTestPolicy())
	if v.Announce {
		t.Fatal("a retained alert announced; this transmits the whole backlog on every reconnect")
	}
	if v.Clear {
		t.Error("a retained alert cleared state; it IS the state")
	}
	if !strings.Contains(v.Reason, "retained") {
		t.Errorf("reason = %q, want it to name retention", v.Reason)
	}
}

// Retained is checked BEFORE the action, because a retained message legitimately
// carries action NEW for a hazard issued hours ago.
func TestRetainedBeatsEveryOtherTest(t *testing.T) {
	a := liveWarning() // action NEW, class W, unseen: passes every other gate
	_, v := Decide("t", alertJSON(t, a), true, func(string, string) bool { return false }, newTestPolicy())
	if v.Announce {
		t.Error("retained NEW announced; the retain check must come first")
	}
}

func TestLiveNewAnnounces(t *testing.T) {
	a := liveWarning()
	got, v := Decide("t", alertJSON(t, a), false, nil, newTestPolicy())
	if !v.Announce {
		t.Fatalf("a live NEW warning did not announce: %s", v.Reason)
	}
	if got.Event != "Tornado Warning" {
		t.Errorf("event = %q", got.Event)
	}
}

func TestTombstoneClearsAndNeverAnnounces(t *testing.T) {
	_, v := Decide("wxalerts/nws/v1/same/012113/12", nil, true, nil, newTestPolicy())
	if v.Announce {
		t.Fatal("a tombstone announced")
	}
	if !v.Clear {
		t.Error("a tombstone did not clear state")
	}
	// A zero-length payload arrives retained by definition, but the check must
	// not depend on that: an empty live publish is still nothing to announce.
	if _, v := Decide("t", []byte{}, false, nil, newTestPolicy()); v.Announce || !v.Clear {
		t.Errorf("an empty live payload = %+v, want clear and silent", v)
	}
}

func TestDedupAcrossCounties(t *testing.T) {
	a := liveWarning()
	// One alert covering two monitored counties arrives twice, identically.
	seenKeys := map[string]bool{}
	seen := func(key, action string) bool {
		k := key + "|" + action
		if seenKeys[k] {
			return true
		}
		seenKeys[k] = true
		return false
	}
	_, first := Decide("wxalerts/nws/v1/same/012113/12", alertJSON(t, a), false, seen, newTestPolicy())
	_, second := Decide("wxalerts/nws/v1/same/001003/12", alertJSON(t, a), false, seen, newTestPolicy())

	if !first.Announce {
		t.Fatalf("first county did not announce: %s", first.Reason)
	}
	if second.Announce {
		t.Error("the same hazard announced twice because it covers two monitored counties")
	}
}

func TestDedupIsPerActionSoUpdatesStillAnnounce(t *testing.T) {
	p := newTestPolicy()
	p.announceActions["CAN"] = true
	seenKeys := map[string]bool{}
	seen := func(key, action string) bool {
		k := key + "|" + action
		if seenKeys[k] {
			return true
		}
		seenKeys[k] = true
		return false
	}
	a := liveWarning()
	if _, v := Decide("t", alertJSON(t, a), false, seen, p); !v.Announce {
		t.Fatal("NEW did not announce")
	}
	a.Action = "CAN"
	if _, v := Decide("t", alertJSON(t, a), false, seen, p); !v.Announce {
		t.Error("a cancellation of an already-announced hazard was deduped away")
	}
}

func TestUnroutedAndUnannouncedActions(t *testing.T) {
	p := newTestPolicy()
	a := liveWarning()

	a.Significance = "Y" // advisory: not routed by this policy
	if _, v := Decide("t", alertJSON(t, a), false, nil, p); v.Announce {
		t.Error("an advisory announced under a policy that does not route advisories")
	}

	a = liveWarning()
	a.Action = "CON" // a continuation, reissued every few minutes
	if _, v := Decide("t", alertJSON(t, a), false, nil, p); v.Announce {
		t.Error("CON announced; that re-transmits the same hazard all afternoon")
	}
}

func TestExpiredAndCancelledClear(t *testing.T) {
	for _, st := range []string{"expired", "cancelled", "EXPIRED"} {
		a := liveWarning()
		a.Status = st
		_, v := Decide("t", alertJSON(t, a), false, nil, newTestPolicy())
		if v.Announce {
			t.Errorf("status %q announced", st)
		}
		if !v.Clear {
			t.Errorf("status %q did not clear", st)
		}
	}
}

func TestUnparseablePayloadIsSilent(t *testing.T) {
	_, v := Decide("t", []byte("{not json"), false, nil, newTestPolicy())
	if v.Announce || v.Clear {
		t.Errorf("garbage produced %+v, want silence", v)
	}
}

func TestDedupKeyFallsBackToID(t *testing.T) {
	a := Alert{ID: 316}
	if got := a.DedupKey(); got != "id316" {
		t.Errorf("DedupKey without VTEC = %q, want id316", got)
	}
	a.VTEC = "KMOB.TO.W.0012.2026"
	if got := a.DedupKey(); got != a.VTEC {
		t.Errorf("DedupKey with VTEC = %q", got)
	}
}

func TestSMSText(t *testing.T) {
	loc := time.FixedZone("CDT", -5*3600)
	now := time.Date(2026, 8, 11, 20, 15, 0, 0, time.UTC)
	a := liveWarning()

	got := SMSText(a, now, loc, 120)
	if !strings.HasPrefix(got, "Tornado Warning") {
		t.Errorf("text = %q, want the hazard first", got)
	}
	if !strings.Contains(got, "3:45 PM") {
		t.Errorf("text = %q, want the local end time", got)
	}

	// The budget must not split a word.
	short := SMSText(a, now, loc, 30)
	if len([]rune(short)) > 31 {
		t.Errorf("truncated text is %d runes: %q", len([]rune(short)), short)
	}
	if strings.Contains(short, "Warnin…") {
		t.Errorf("truncation split a word: %q", short)
	}

	// An alert whose end time has passed should not claim a future deadline.
	past := a
	past.Ends = now.Add(-time.Hour)
	if strings.Contains(SMSText(past, now, loc, 120), "until") {
		t.Errorf("an already-ended alert rendered an 'until': %q", SMSText(past, now, loc, 120))
	}
}
