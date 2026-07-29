package supervisor

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/backoff"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/status"
)

// supervisor.go is the runner: it assembles an Observation per attachment from
// the signals available, steps each Monitor, and publishes the resulting claim
// onto the event hub. All the judgement lives in policy.go; this file is wiring
// and I/O, deliberately thin enough to read in one sitting.

// DefaultInterval is how often the supervisor re-evaluates. Slow on purpose: the
// grace period is measured in minutes, nothing here is a latency-sensitive
// decision, and every tick is a name lookup against somebody else's DNS.
const DefaultInterval = 30 * time.Second

// The global rate cap: a backstop across every unit, on top of the per-unit
// backoff. Nothing on a hotspot should come close to it — a node runs two
// supervised units at most — so tripping it means something is wrong with the
// supervisor's own reasoning rather than with the network. Better to stop
// restarting daemons and say so than to be a well-intentioned loop.
const (
	DefaultMaxRestarts   = 6
	DefaultRestartWindow = 15 * time.Minute
)

// unitGate rate-limits one systemd unit. It exists because the unit, not the
// attachment, is the thing being restarted: with three DMR masters on one
// DMRGateway, three monitors each politely backing off can still add up to a
// restart every few seconds if nothing is counting at the unit.
type unitGate struct {
	bo         backoff.Backoff
	coolUntil  time.Time
	restarts   int
	lastReason string
}

// Signals is what the runner needs from the rest of the daemon. Each is a
// function so the supervisor owns no subsystem and tests inject all of it.
type Signals struct {
	// Attachments derives the current supervised set. Called every tick so a
	// config apply that adds or removes a network is picked up without a restart.
	Attachments func() []Attachment
	// WANUp reports whether the node has a route out — netwatch's question, asked
	// of the kernel rather than of any third party's server.
	WANUp func() bool
	// TXActive reports whether a transmission is on the air.
	TXActive func() bool
	// UnitActive reports a systemd unit's state. TriUnknown when the liveness
	// probe has not reached it yet, which must not read as a dead daemon.
	UnitActive func(unit string) Tri
	// LinkState asks the gateway daemons where their upstream links actually
	// stand, keyed by network name. Nil, or a name absent from the result, means
	// nothing was asked or nothing answered — not a failure.
	//
	// This is the signal that closes the gap the bench run exposed: a daemon
	// retrying a lost connection publishes "Opening DMR Network" over and over
	// and never says the link is down, so the announcements alone leave an
	// unreachable master looking merely unconfirmed. Asked directly, the same
	// daemon reports "disc" the whole time.
	LinkState func() map[string]Tri
	// Restart performs the remediation. Nil (or Remediate false) means decisions
	// are observed and logged but never acted on.
	Restart func(unit string) error
}

// Supervisor watches every upstream attachment and keeps the node honest about
// them.
type Supervisor struct {
	Signals  Signals
	Prober   *Prober
	Policy   Policy
	Hub      *hub.Hub
	Interval time.Duration

	// Remediate arms restarts. Off means the supervisor observes, publishes honest
	// link state, and logs what it would have done — the whole detection path
	// exercised on a real node with nothing acted upon.
	Remediate bool

	// MaxRestarts and RestartWindow are the global backstop across all units.
	// Zero uses the defaults.
	MaxRestarts   int
	RestartWindow time.Duration

	// Logf is injectable for tests; nil logs through the standard logger.
	Logf func(format string, args ...any)
	// Now is injectable for tests.
	Now func() time.Time

	mu       sync.Mutex
	monitors map[string]*Monitor  // by attachment name
	login    map[string]Tri       // last daemon-reported login, by network name
	claimed  map[string]Claim     // last claim published, to avoid re-announcing
	units    map[string]*unitGate // by systemd unit
	recent   []time.Time          // restart times inside the global window
}

func (s *Supervisor) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Supervisor) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (s *Supervisor) interval() time.Duration {
	if s.Interval > 0 {
		return s.Interval
	}
	return DefaultInterval
}

// ObserveEvent folds one hub event into the supervisor's view. It picks up the
// gateway daemons' own link reports; everything else passes through untouched.
func (s *Supervisor) ObserveEvent(e hub.Event) {
	if e.Type != status.TypeGatewayStatus || e.Network == "" {
		return
	}
	_, login, ok := DMRGatewayStatus(e.Detail)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.login == nil {
		s.login = map[string]Tri{}
	}
	// "Opening DMR Network: X" reports TriUnknown — an attempt in progress. It
	// voids a previous success, because a link being re-established is not a link
	// that is up. It must NOT erase a previous failure, though, and that
	// distinction is load-bearing: a daemon stuck in a reconnect loop alternates
	// "Failed connection" and "Opening" forever, and treating each "Opening" as
	// no-news-so-presumably-fine resets the health clock every few seconds, so the
	// grace period never elapses and the supervisor watches it fail indefinitely
	// without ever acting. Found by the tier-2 harness against the real daemon.
	if login == TriUnknown && s.login[e.Network] == TriNo {
		return
	}
	s.login[e.Network] = login
}

// Run steps the supervisor on its interval, and folds hub events in between, until
// ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context) {
	ch, _, cancel := s.Hub.Subscribe()
	defer cancel()

	t := time.NewTicker(s.interval())
	defer t.Stop()

	s.Step(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-ch:
			s.ObserveEvent(e)
		case <-t.C:
			s.Step(ctx)
		}
	}
}

// Step runs one evaluation over every attachment. Separate from Run so the whole
// cycle is testable without a clock or a goroutine — the same shape netwatch uses.
func (s *Supervisor) Step(ctx context.Context) {
	if s.Signals.Attachments == nil {
		return
	}
	attachments := s.Signals.Attachments()
	now := s.now()

	wan := true
	if s.Signals.WANUp != nil {
		wan = s.Signals.WANUp()
	}
	tx := false
	if s.Signals.TXActive != nil {
		tx = s.Signals.TXActive()
	}
	// Asked once per cycle rather than once per attachment: one query returns
	// every link the daemon carries.
	var polled map[string]Tri
	if s.Signals.LinkState != nil && wan {
		polled = s.Signals.LinkState()
	}

	s.mu.Lock()
	if s.monitors == nil {
		s.monitors = map[string]*Monitor{}
	}
	// Forget monitors for attachments the operator has removed, so a deleted
	// network stops being reported on and its state does not linger if it returns.
	live := make(map[string]bool, len(attachments))
	for _, a := range attachments {
		live[a.Name] = true
	}
	for name := range s.monitors {
		if !live[name] {
			delete(s.monitors, name)
			delete(s.claimed, name)
		}
	}
	s.mu.Unlock()

	// Two passes. The first asks every attachment what it wants; the second decides
	// what the node actually does about it. They are separate because a restart is
	// a unit-wide event and several attachments commonly share a unit: acting
	// inside the first pass would restart DMRGateway once per unhappy master.
	requests := map[string][]*Monitor{} // unit → the monitors asking for it
	reasons := map[string]string{}      // unit → why, from the first to ask
	for _, a := range attachments {
		m, d := s.stepOne(ctx, a, now, wan, tx, polled[a.Name])
		if d.Action != ActRestart {
			continue
		}
		requests[a.Unit] = append(requests[a.Unit], m)
		if _, ok := reasons[a.Unit]; !ok {
			reasons[a.Unit] = a.Name + ": " + d.Reason
		}
	}
	if len(requests) == 0 {
		return
	}

	// Deterministic order, so a node with two units restarts them in the same
	// sequence every time and a test can assert it.
	units := make([]string, 0, len(requests))
	for u := range requests {
		units = append(units, u)
	}
	sort.Strings(units)
	for _, u := range units {
		s.remediateUnit(u, reasons[u], requests[u], attachments, now)
	}
}

func (s *Supervisor) stepOne(ctx context.Context, a Attachment, now time.Time, wan, tx bool, polled Tri) (*Monitor, Decision) {
	s.mu.Lock()
	m := s.monitors[a.Name]
	if m == nil {
		m = NewMonitor(a, s.Policy)
		s.monitors[a.Name] = m
	}
	m.Attachment = a // an edited address must be probed at its new value
	login := s.login[a.Name]
	s.mu.Unlock()

	// A direct answer beats a remembered announcement. The daemon's own state
	// machine is what the poll reads, so when it says anything at all it is more
	// current than the last message we happened to catch — and it is the only
	// thing that reports a link down while the daemon quietly retries.
	if polled != TriUnknown {
		login = polled
	}

	unit := TriUnknown
	if s.Signals.UnitActive != nil {
		unit = s.Signals.UnitActive(a.Unit)
	}

	// Probing during an outage answers a question we already know the answer to,
	// and would report every attachment unreachable for reasons that are not theirs.
	endpoint := TriUnknown
	if wan {
		// The deep probe is reserved for an attachment something already looks
		// wrong about — see Probe's contract for why this is not done every tick.
		deep := unit == TriNo || login == TriNo
		endpoint, _ = s.Prober.Probe(ctx, a, deep)
	}

	d := m.Step(Observation{
		Now: now, WANUp: wan, TXActive: tx,
		Unit: unit, Endpoint: endpoint, Login: login,
	})

	s.publishClaim(a, d, now)
	return m, d
}

// remediateUnit restarts one unit on behalf of the attachments asking for it, if
// the unit's own pacing and the global cap both allow.
func (s *Supervisor) remediateUnit(unit, reason string, asking []*Monitor, all []Attachment, now time.Time) {
	s.mu.Lock()
	if s.units == nil {
		s.units = map[string]*unitGate{}
	}
	g := s.units[unit]
	if g == nil {
		g = &unitGate{bo: s.Policy.Backoff}
		s.units[unit] = g
	}

	switch {
	case now.Before(g.coolUntil):
		s.mu.Unlock()
		// Received, standing by. Telling the askers to settle rather than leaving
		// them to re-request every tick keeps the log quiet and costs them nothing:
		// Settled does not advance anyone's backoff.
		for _, m := range asking {
			m.Settled(now)
		}
		return
	case !s.allowGloballyLocked(now):
		s.mu.Unlock()
		s.logf("supervisor: not restarting %s (%s) — %d restarts already in the last %s, which is the cap",
			unit, reason, s.maxRestarts(), s.restartWindow())
		// Declining is as much a part of the story as acting: an operator reading
		// back a night of trouble needs to see where the supervisor stopped, or the
		// log just goes quiet and looks like the problem fixed itself.
		s.announce(unit, "declined to restart "+unit+" — "+reason+"; already at the restart cap", now)
		for _, m := range asking {
			m.Settled(now)
		}
		return
	}

	wait := g.bo.Next()
	if wait < s.Policy.Settle {
		wait = s.Policy.Settle
	}
	g.coolUntil = now.Add(wait)
	g.restarts++
	g.lastReason = reason
	s.recent = append(s.recent, now)
	s.mu.Unlock()

	if !s.Remediate || s.Signals.Restart == nil {
		s.logf("supervisor: %s would be restarted (%s) — remediation is off", unit, reason)
		s.announce(unit, "would restart "+unit+" — "+reason+"; remediation is off", now)
	} else {
		s.logf("supervisor: restarting %s: %s", unit, reason)
		if err := s.Signals.Restart(unit); err != nil {
			s.logf("supervisor: restarting %s failed: %v", unit, err)
			s.announce(unit, "could not restart "+unit+" — "+err.Error(), now)
		} else {
			s.announce(unit, "restarted "+unit+" — "+reason, now)
		}
	}

	// The askers are charged for it; every other attachment on the same unit was
	// restarted underneath them and has to stand by without being charged. Missing
	// this second group is what would make a healthy master on a shared gateway
	// look like it had just failed.
	charged := make(map[*Monitor]bool, len(asking))
	for _, m := range asking {
		m.Remediated(now)
		charged[m] = true
	}
	s.mu.Lock()
	for _, a := range all {
		if a.Unit != unit {
			continue
		}
		if m := s.monitors[a.Name]; m != nil && !charged[m] {
			m.Settled(now)
		}
	}
	s.mu.Unlock()
}

// announce puts a supervisor action on the event hub, where it persists to
// events.db and shows in the dashboard's event log — so unattended recovery can be
// read back by whoever was asleep when it happened.
func (s *Supervisor) announce(unit, detail string, now time.Time) {
	if s.Hub == nil {
		return
	}
	s.Hub.Publish(hub.Event{
		Time:   now.UTC(),
		Type:   status.TypeSupervisorAction,
		Source: unit,
		Detail: detail,
	})
}

func (s *Supervisor) maxRestarts() int {
	if s.MaxRestarts > 0 {
		return s.MaxRestarts
	}
	return DefaultMaxRestarts
}

func (s *Supervisor) restartWindow() time.Duration {
	if s.RestartWindow > 0 {
		return s.RestartWindow
	}
	return DefaultRestartWindow
}

// allowGloballyLocked reports whether another restart fits inside the global cap,
// dropping the record of any that have aged out. Caller holds mu.
func (s *Supervisor) allowGloballyLocked(now time.Time) bool {
	cutoff := now.Add(-s.restartWindow())
	kept := s.recent[:0]
	for _, t := range s.recent {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.recent = kept
	return len(s.recent) < s.maxRestarts()
}

// publishClaim emits a link event when the verdict about an attachment changes.
// Only on change: the supervisor evaluates every attachment on every tick, and an
// unchanged verdict is not news — it would bury the event log and churn the
// retained topics.
func (s *Supervisor) publishClaim(a Attachment, d Decision, now time.Time) {
	s.mu.Lock()
	if s.claimed == nil {
		s.claimed = map[string]Claim{}
	}
	prev, seen := s.claimed[a.Name]
	changed := !seen || prev != d.Claim
	if changed {
		s.claimed[a.Name] = d.Claim
	}
	s.mu.Unlock()

	if !changed || s.Hub == nil {
		return
	}
	t := status.TypeLinkUp
	if !d.Claim.Up {
		t = status.TypeLinkDown
	}
	s.Hub.Publish(hub.Event{
		Time:    now.UTC(),
		Type:    t,
		Network: a.Name,
		Detail:  d.Claim.Detail,
	})
}
