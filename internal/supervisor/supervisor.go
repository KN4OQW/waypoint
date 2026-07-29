package supervisor

import (
	"context"
	"log"
	"sync"
	"time"

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

	// Logf is injectable for tests; nil logs through the standard logger.
	Logf func(format string, args ...any)
	// Now is injectable for tests.
	Now func() time.Time

	mu       sync.Mutex
	monitors map[string]*Monitor // by attachment name
	login    map[string]Tri      // last daemon-reported login, by network name
	claimed  map[string]Claim    // last claim published, to avoid re-announcing
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

	for _, a := range attachments {
		s.stepOne(ctx, a, now, wan, tx)
	}
}

func (s *Supervisor) stepOne(ctx context.Context, a Attachment, now time.Time, wan, tx bool) {
	s.mu.Lock()
	m := s.monitors[a.Name]
	if m == nil {
		m = NewMonitor(a, s.Policy)
		s.monitors[a.Name] = m
	}
	m.Attachment = a // an edited address must be probed at its new value
	login := s.login[a.Name]
	s.mu.Unlock()

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

	if d.Action != ActRestart {
		return
	}
	if !s.Remediate || s.Signals.Restart == nil {
		s.logf("supervisor: %s would be restarted (%s) — remediation is off", a.Unit, d.Reason)
		return
	}
	s.logf("supervisor: restarting %s: %s (%s)", a.Unit, d.Reason, a.Name)
	if err := s.Signals.Restart(a.Unit); err != nil {
		s.logf("supervisor: restarting %s failed: %v", a.Unit, err)
	}
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
