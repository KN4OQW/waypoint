package supervisor

import (
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/backoff"
)

var t0 = time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

func testAttachment() Attachment {
	return Attachment{Name: "BM_3102", Kind: KindDMRMaster, Host: "master.example", Port: "62031", Unit: "waypoint-dmrgateway.service"}
}

// testPolicy is deterministic: no jitter, so a test asserts an exact cooldown.
func testPolicy() Policy {
	return Policy{
		Grace:       90 * time.Second,
		Settle:      60 * time.Second,
		SustainedOK: 5 * time.Minute,
		Backoff:     backoff.Backoff{Initial: 30 * time.Second, Max: 10 * time.Minute},
	}
}

// act steps the monitor and closes the loop the way a caller with one attachment
// on the unit would: a granted restart is reported back through Remediated, which
// is what advances the schedule. Step itself only requests (see policy.go).
func act(m *Monitor, o Observation) Decision {
	d := m.Step(o)
	if d.Action == ActRestart {
		m.Remediated(o.Now)
	}
	return d
}

// healthy is an observation of an attachment that is working.
func healthy(at time.Time) Observation {
	return Observation{Now: at, WANUp: true, Unit: TriYes, Endpoint: TriYes, Login: TriYes}
}

// broken is an observation of an attachment whose master has gone away — the
// daemon is running, the node is online, the endpoint no longer resolves.
func broken(at time.Time) Observation {
	return Observation{Now: at, WANUp: true, Unit: TriYes, Endpoint: TriNo, Login: TriNo}
}

// A healthy attachment is left alone and claimed up.
func TestHealthyIsLeftAlone(t *testing.T) {
	m := NewMonitor(testAttachment(), testPolicy())
	for i := 0; i < 20; i++ {
		d := m.Step(healthy(t0.Add(time.Duration(i) * time.Minute)))
		if d.Action != ActNone {
			t.Fatalf("step %d acted on a healthy attachment: %+v", i, d)
		}
		if !d.Claim.Up {
			t.Fatalf("step %d claimed a healthy attachment down: %+v", i, d)
		}
	}
}

// Nothing reported yet is not bad news: an attachment whose probes have not run
// must not be restarted on the strength of an absence.
func TestUnknownsAreNotFailures(t *testing.T) {
	m := NewMonitor(testAttachment(), testPolicy())
	o := Observation{Now: t0, WANUp: true, Unit: TriYes, Endpoint: TriUnknown, Login: TriUnknown}
	for i := 0; i < 10; i++ {
		o.Now = t0.Add(time.Duration(i) * time.Minute)
		if d := m.Step(o); d.Action != ActNone || !d.Claim.Up {
			t.Fatalf("step %d acted on unknowns: %+v", i, d)
		}
	}
}

// The core recovery: an attachment that stays broken past the grace period is
// restarted — once — and then left alone for the cooldown.
func TestRestartsAfterGrace(t *testing.T) {
	p := testPolicy()
	m := NewMonitor(testAttachment(), p)

	if d := act(m, broken(t0)); d.Action != ActNone {
		t.Fatalf("restarted immediately on the first bad observation: %+v", d)
	}
	if d := act(m, broken(t0.Add(p.Grace-time.Second))); d.Action != ActNone {
		t.Fatal("restarted inside the grace period")
	}
	d := act(m, broken(t0.Add(p.Grace+time.Second)))
	if d.Action != ActRestart {
		t.Fatalf("did not restart after the grace period: %+v", d)
	}
	if d.Claim.Up {
		t.Error("a broken attachment must not be claimed up")
	}
	// The settle window is the floor on the first cooldown, so the daemon is not
	// judged before it has had time to log back in.
	at := t0.Add(p.Grace + time.Second)
	for _, dt := range []time.Duration{time.Second, 30 * time.Second, p.Settle - time.Second} {
		if d := act(m, broken(at.Add(dt))); d.Action != ActNone {
			t.Fatalf("restarted again %v later, inside the settle window: %+v", dt, d)
		}
	}
}

// Repeated failure backs off: each remediation waits longer than the last, so a
// master that is gone for the weekend is knocked on periodically, not hammered.
func TestRepeatedFailureBacksOff(t *testing.T) {
	p := testPolicy()
	m := NewMonitor(testAttachment(), p)

	now := t0
	var gaps []time.Duration
	var lastRestart time.Time
	for i := 0; i < 400 && len(gaps) < 5; i++ {
		d := act(m, broken(now))
		if d.Action == ActRestart {
			if !lastRestart.IsZero() {
				gaps = append(gaps, now.Sub(lastRestart))
			}
			lastRestart = now
		}
		now = now.Add(15 * time.Second)
	}
	if len(gaps) < 4 {
		t.Fatalf("expected several remediations, got gaps %v", gaps)
	}
	for i := 1; i < len(gaps); i++ {
		if gaps[i] < gaps[i-1] {
			t.Errorf("gap %d (%v) is shorter than the previous (%v) — not backing off", i, gaps[i], gaps[i-1])
		}
	}
	if m.Attempt() < 3 {
		t.Errorf("backoff barely advanced after repeated failures: attempt %d", m.Attempt())
	}
}

// With no route out the node reports the truth and remediates nothing. Restarting
// a daemon during an outage cannot help and can hurt.
func TestNoRemediationWhileOffline(t *testing.T) {
	p := testPolicy()
	m := NewMonitor(testAttachment(), p)

	now := t0
	for i := 0; i < 60; i++ { // ten minutes of outage
		d := m.Step(Observation{Now: now, WANUp: false, Unit: TriYes, Endpoint: TriNo, Login: TriNo})
		if d.Action != ActNone {
			t.Fatalf("remediated during a WAN outage at +%v: %+v", now.Sub(t0), d)
		}
		if d.Claim.Up {
			t.Fatalf("claimed a link up while the node had no route out: %+v", d)
		}
		now = now.Add(10 * time.Second)
	}
	if m.Attempt() != 0 {
		t.Errorf("an outage should not consume the backoff schedule, attempt = %d", m.Attempt())
	}
}

// The WAN returning must not fire a restart instantly on grace banked during the
// outage — the daemon gets a fresh chance to recover on its own first. This is the
// difference between one restart at the right moment and a restart storm the
// moment an ISP comes back for a whole town.
func TestWANRestoreGivesAFreshGrace(t *testing.T) {
	p := testPolicy()
	m := NewMonitor(testAttachment(), p)

	now := t0
	for i := 0; i < 60; i++ {
		m.Step(Observation{Now: now, WANUp: false, Unit: TriYes, Endpoint: TriNo, Login: TriNo})
		now = now.Add(10 * time.Second)
	}
	// Route back, attachment still broken: no instant restart.
	if d := m.Step(broken(now)); d.Action != ActNone {
		t.Fatalf("restarted the instant the WAN returned: %+v", d)
	}
	if d := m.Step(broken(now.Add(p.Grace - time.Second))); d.Action != ActNone {
		t.Fatal("restarted inside the post-restore grace period")
	}
	// Still broken a grace period later: now it is genuinely the daemon's problem.
	if d := m.Step(broken(now.Add(p.Grace + time.Second))); d.Action != ActRestart {
		t.Fatal("never remediated after the grace period following WAN restore")
	}
}

// A daemon that recovers on its own during the post-restore grace is never
// restarted at all — the common case for the gateways that do reconnect properly.
func TestSelfRecoveryNeedsNoRestart(t *testing.T) {
	p := testPolicy()
	m := NewMonitor(testAttachment(), p)

	now := t0
	for i := 0; i < 30; i++ {
		m.Step(Observation{Now: now, WANUp: false, Unit: TriYes, Endpoint: TriNo, Login: TriNo})
		now = now.Add(10 * time.Second)
	}
	m.Step(broken(now)) // route back, not yet logged in
	now = now.Add(20 * time.Second)
	for i := 0; i < 20; i++ { // it logs back in by itself
		if d := m.Step(healthy(now)); d.Action != ActNone {
			t.Fatalf("restarted a daemon that recovered on its own: %+v", d)
		}
		now = now.Add(30 * time.Second)
	}
}

// A restart is postponed while a transmission is on the air, and fires on the next
// step after the release — postponed, not cancelled, and not charged to the backoff.
func TestTransmissionDefersRestart(t *testing.T) {
	p := testPolicy()
	m := NewMonitor(testAttachment(), p)

	m.Step(broken(t0))
	at := t0.Add(p.Grace + time.Second)

	keyed := broken(at)
	keyed.TXActive = true
	if d := m.Step(keyed); d.Action != ActNone {
		t.Fatalf("restarted mid-transmission: %+v", d)
	}
	if m.Attempt() != 0 {
		t.Errorf("a deferred restart should not consume the backoff, attempt = %d", m.Attempt())
	}
	if d := m.Step(broken(at.Add(time.Second))); d.Action != ActRestart {
		t.Fatal("the deferred restart never fired after the transmission ended")
	}
}

// The backoff resets only after a SUSTAINED recovery. A connection that is
// accepted and immediately dropped looks like success at the instant it is made;
// resetting there is what lets a duplicate-login flap run hot forever
// (YSFClients#155).
func TestBackoffResetsOnlyWhenSustained(t *testing.T) {
	p := testPolicy()
	m := NewMonitor(testAttachment(), p)

	// Fail enough to walk the schedule along.
	now := t0
	for i := 0; i < 200 && m.Attempt() < 3; i++ {
		act(m, broken(now))
		now = now.Add(15 * time.Second)
	}
	advanced := m.Attempt()
	if advanced < 3 {
		t.Fatalf("setup failed to advance the backoff: %d", advanced)
	}

	// A brief flash of health — the flap — must not reset it.
	act(m, healthy(now))
	act(m, broken(now.Add(time.Second)))
	if m.Attempt() != advanced {
		t.Errorf("a momentary success reset the backoff: %d → %d", advanced, m.Attempt())
	}

	// Health that holds past SustainedOK does reset it.
	base := now.Add(time.Minute)
	act(m, healthy(base))
	act(m, healthy(base.Add(p.SustainedOK-time.Second)))
	if m.Attempt() != advanced {
		t.Errorf("reset before the sustained window elapsed: %d", m.Attempt())
	}
	act(m, healthy(base.Add(p.SustainedOK+time.Second)))
	if m.Attempt() != 0 {
		t.Errorf("a sustained recovery should reset the backoff, attempt = %d", m.Attempt())
	}
}

// Each failing signal is reported in its own words, so the event log says what
// was actually wrong rather than "unhealthy".
func TestAssessReasons(t *testing.T) {
	cases := []struct {
		name    string
		obs     Observation
		healthy bool
		reason  string
	}{
		{"unit down", Observation{Unit: TriNo, Endpoint: TriYes, Login: TriYes}, false, "the gateway is not running"},
		{"endpoint gone", Observation{Unit: TriYes, Endpoint: TriNo, Login: TriYes}, false, "the endpoint is unreachable"},
		{"login refused", Observation{Unit: TriYes, Endpoint: TriYes, Login: TriNo}, false, "not logged in"},
		{"logged in", Observation{Unit: TriYes, Endpoint: TriYes, Login: TriYes}, true, "logged in"},
		{"reachable only", Observation{Unit: TriYes, Endpoint: TriYes, Login: TriUnknown}, true, "reachable"},
		{"nothing known", Observation{Unit: TriYes, Endpoint: TriUnknown, Login: TriUnknown}, true, "running"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, reason := assess(c.obs)
			if ok != c.healthy || reason != c.reason {
				t.Errorf("assess = (%v, %q), want (%v, %q)", ok, reason, c.healthy, c.reason)
			}
		})
	}
}

// A unit the liveness probe has not reached yet is not a dead one. This is the
// difference a bool cannot carry: at startup the probe has filed nothing, and
// reading that emptiness as "not running" would have the supervisor restarting
// every gateway on the node the moment it came up.
func TestUnknownUnitIsNotADeadUnit(t *testing.T) {
	p := testPolicy()
	m := NewMonitor(testAttachment(), p)
	o := Observation{Now: t0, WANUp: true, Unit: TriUnknown, Endpoint: TriUnknown, Login: TriUnknown}
	for i := 0; i < 20; i++ {
		o.Now = t0.Add(time.Duration(i) * time.Minute)
		if d := m.Step(o); d.Action != ActNone {
			t.Fatalf("restarted a unit nothing had reported on yet: %+v", d)
		}
	}
}

// A dead unit is a failure the supervisor acts on: it is the signal that survives
// when a daemon crashes outright rather than merely losing its link.
func TestDeadUnitIsRemediated(t *testing.T) {
	p := testPolicy()
	m := NewMonitor(testAttachment(), p)
	dead := Observation{Now: t0, WANUp: true, Unit: TriNo, Endpoint: TriYes, Login: TriUnknown}

	m.Step(dead)
	dead.Now = t0.Add(p.Grace + time.Second)
	d := m.Step(dead)
	if d.Action != ActRestart {
		t.Fatalf("a unit that stayed dead past the grace period was not restarted: %+v", d)
	}
	if d.Reason != "the gateway is not running" {
		t.Errorf("reason = %q", d.Reason)
	}
}
