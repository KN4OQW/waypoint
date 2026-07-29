package supervisor

import (
	"time"

	"github.com/KN4OQW/waypoint/internal/backoff"
)

// policy.go is the whole decision. It is a pure state machine over an injected
// clock — no sockets, no systemd, no goroutines — so every rule that keeps this
// from becoming a restart loop against somebody else's server is a table test
// that runs in microseconds rather than a property nobody can check.

// Tri is a signal that can be good, bad, or not yet known.
//
// The third case is the one that matters and the reason this is not a bool. Every
// signal here can legitimately be missing — a probe that has not run, a unit the
// liveness poll has not reached, a daemon that reports nothing at all — and the
// difference between "reported bad" and "not reported" is the difference between
// restarting a daemon and leaving a working one alone. Collapsing them into a
// bool makes every gap in the node's knowledge read as a fault.
type Tri int

const (
	TriUnknown Tri = iota
	TriYes
	TriNo
)

// Observation is everything known about one attachment at one instant. The
// caller assembles it; the policy never looks anything up for itself.
type Observation struct {
	Now time.Time
	// WANUp is whether the node has a route out at all — the same question
	// netwatch asks the kernel. Always known (the kernel always answers), and it
	// gates remediation entirely.
	WANUp bool
	// TXActive is whether a transmission is on the air right now. Restarting a
	// gateway mid-transmission drops somebody's audio to fix a link that has
	// already been broken for minutes; it can wait for the release.
	TXActive bool
	// Unit is the attachment's systemd unit state, from the liveness probe.
	Unit Tri
	// Endpoint is what an address probe found.
	Endpoint Tri
	// Login is what the daemon itself last said about this attachment on its MQTT
	// status plane — a hint, never the sole authority (see hint.go).
	Login Tri
}

// Action is what the supervisor should do about an attachment.
type Action int

const (
	// ActNone: nothing to do, or nothing that may be done yet.
	ActNone Action = iota
	// ActRestart: restart the attachment's unit.
	ActRestart
)

// Claim is what the status pipeline should now say about this attachment. It is
// separate from Action because honesty and remediation are independent: during a
// WAN outage the node reports every attachment down and restarts nothing.
type Claim struct {
	Up     bool
	Detail string
}

// Decision is one step's output.
type Decision struct {
	Action Action
	Claim  Claim
	// Reason is the human-readable why, for the event log. It explains the
	// Action when there is one and the Claim otherwise.
	Reason string
}

// Policy is the timing that governs remediation. The zero value is unusable;
// DefaultPolicy is the tuned one.
type Policy struct {
	// Grace is how long an attachment must look unhealthy before it is restarted.
	// It absorbs the ordinary: a master's brief hiccup, a re-login in progress, a
	// config apply cycling the unit.
	Grace time.Duration
	// Settle is the floor on how long to wait after a restart before judging the
	// result. A daemon that has just been restarted is not yet logged in, and
	// reading that as failure would restart it again immediately.
	Settle time.Duration
	// SustainedOK is how long an attachment must stay healthy before the backoff
	// resets. This is the YSFClients#155 lesson: a duplicate-login flap looks
	// exactly like success at the instant the connection is accepted, so resetting
	// on first success is what lets a hot loop run forever.
	SustainedOK time.Duration
	// Backoff is the retry schedule between remediations of the same attachment.
	Backoff backoff.Backoff
}

// DefaultPolicy is tuned for a hotspot on a domestic connection.
//
// Grace of 90 s is set against a router reboot, which is the common cause of a
// short outage and is back well inside it; below about a minute the supervisor
// starts restarting daemons that were going to recover on their own. Settle of
// 60 s is a DMR login sequence with room to spare. Backoff runs 30 s to 10 min:
// the first retry is quick because most failures are transient, and the ceiling is
// low enough to recover an hour-long outage promptly but high enough that a node
// whose master is gone for a weekend is knocking twice an hour, not constantly.
func DefaultPolicy(rand func() float64) Policy {
	return Policy{
		Grace:       90 * time.Second,
		Settle:      60 * time.Second,
		SustainedOK: 5 * time.Minute,
		Backoff:     backoff.Backoff{Initial: 30 * time.Second, Max: 10 * time.Minute, Rand: rand},
	}
}

// Monitor is the per-attachment state machine. One exists per supervised
// attachment and is stepped on every observation.
type Monitor struct {
	Attachment Attachment

	policy  Policy
	bo      backoff.Backoff
	offline bool // the last step saw no route out

	unhealthySince time.Time // when the current unhealthy run began; zero when healthy
	healthySince   time.Time // when the current healthy run began; zero when unhealthy
	cooldownUntil  time.Time // no remediation before this
}

// NewMonitor returns a monitor for one attachment. A fresh monitor has seen
// nothing, so its first steps report unknown-but-not-failing until the
// observations say otherwise.
func NewMonitor(a Attachment, p Policy) *Monitor {
	return &Monitor{Attachment: a, policy: p, bo: p.Backoff}
}

// Attempt reports how far down the backoff schedule this attachment has walked —
// zero when it is healthy or has never been remediated.
func (m *Monitor) Attempt() int { return m.bo.Attempt() }

// Step folds one observation and returns what to do about it.
func (m *Monitor) Step(o Observation) Decision {
	// No route out: report the truth, remediate nothing.
	//
	// This is the single most important rule in the package. During an outage the
	// daemon is not at fault and restarting it cannot help — but it can hurt, by
	// burning a login attempt and, on a master that kicks duplicate logins, by
	// making the node fight its own previous session on the way back. So the whole
	// ladder is frozen and the timers are held, not accumulated: an outage must not
	// bank grace time that fires a restart the instant connectivity returns.
	if !o.WANUp {
		m.offline = true
		m.unhealthySince, m.healthySince = time.Time{}, time.Time{}
		return Decision{
			Action: ActNone,
			Claim:  Claim{Up: false, Detail: "unconfirmed — the node has no route out"},
			Reason: "node offline; remediation suspended",
		}
	}

	// The route just came back. Give the daemon a fresh grace period to recover on
	// its own before concluding it cannot — some do. The backoff is deliberately
	// NOT reset here: a connection that flaps up and down must keep backing off,
	// and a WAN that flaps is exactly that.
	if m.offline {
		m.offline = false
		m.unhealthySince, m.healthySince = time.Time{}, time.Time{}
	}

	healthy, reason := assess(o)

	if healthy {
		m.unhealthySince = time.Time{}
		if m.healthySince.IsZero() {
			m.healthySince = o.Now
		}
		if o.Now.Sub(m.healthySince) >= m.policy.SustainedOK {
			m.bo.Reset()
		}
		return Decision{Action: ActNone, Claim: Claim{Up: true, Detail: reason}, Reason: reason}
	}

	m.healthySince = time.Time{}
	if m.unhealthySince.IsZero() {
		m.unhealthySince = o.Now
	}
	claim := Claim{Up: false, Detail: reason}

	switch {
	case o.Now.Before(m.cooldownUntil):
		// Either the backoff between remediations or the settle window after one.
		return Decision{Action: ActNone, Claim: claim, Reason: reason + "; waiting before another restart"}
	case o.Now.Sub(m.unhealthySince) < m.policy.Grace:
		return Decision{Action: ActNone, Claim: claim, Reason: reason + "; within the grace period"}
	case o.TXActive:
		// Deliberately does not consume grace or advance the backoff — the restart
		// is postponed, not spent, and fires on the next step after the release.
		return Decision{Action: ActNone, Claim: claim, Reason: reason + "; deferred, a transmission is on the air"}
	}

	// A restart is warranted and this attachment's own pacing allows it. Note that
	// nothing is mutated here: Step is a request, not the act.
	//
	// The caller decides, because the caller is the only thing that knows what a
	// restart actually costs. Several attachments commonly share one unit — every
	// DMR master rides DMRGateway — so restarting "for" one of them restarts all of
	// them, and a monitor that charged itself for a restart it did not cause, or
	// that kept its own schedule while the unit was cycled on somebody else's
	// behalf, would be reasoning about a machine it does not control. The caller
	// reports back through Remediated or Settled.
	return Decision{Action: ActRestart, Claim: claim, Reason: reason}
}

// Remediated tells the monitor that its unit was restarted on this attachment's
// behalf. It advances the backoff and holds the attachment off for at least the
// settle window, so a short early backoff can never mean judging a daemon before
// it has had time to log back in. Clearing unhealthySince restarts the grace clock,
// putting the next possible restart a cooldown plus a grace away rather than
// immediately after.
func (m *Monitor) Remediated(now time.Time) {
	wait := m.bo.Next()
	if wait < m.policy.Settle {
		wait = m.policy.Settle
	}
	m.cooldownUntil = now.Add(wait)
	m.unhealthySince = time.Time{}
}

// Settled tells the monitor to stand by without charging it for a restart: either
// its unit was cycled on another attachment's behalf, or its request was held off
// by a rate limit. Either way this attachment did not cause anything, so its
// backoff must not advance — but it does have to wait, because judging a daemon
// that was just restarted underneath it would read the restart as a fault.
func (m *Monitor) Settled(now time.Time) {
	m.cooldownUntil = now.Add(m.policy.Settle)
	m.unhealthySince = time.Time{}
}

// assess is the health verdict for one observation: unhealthy on positive bad
// news only, so an attachment nothing has reported on yet is left alone rather
// than restarted on the strength of a probe that has not run.
func assess(o Observation) (bool, string) {
	switch {
	case o.Unit == TriNo:
		return false, "the gateway is not running"
	case o.Endpoint == TriNo:
		return false, "the endpoint is unreachable"
	case o.Login == TriNo:
		return false, "not logged in"
	case o.Login == TriYes:
		return true, "logged in"
	case o.Endpoint == TriYes:
		return true, "reachable"
	default:
		return true, "running"
	}
}
