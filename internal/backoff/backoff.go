// Package backoff is the shared reconnect schedule: retry quickly at first, then
// exponentially slower up to a ceiling, optionally spread by jitter.
//
// It started as the bus peering dialer's private schedule and was promoted here
// when the resilience supervisor needed the same thing with one addition. Jitter
// is that addition, and it is not cosmetic: the supervisor's retries are triggered
// by conditions that are shared across every node on a network — an ISP outage
// ends for a whole town at once, a DMR master reboots for everyone at once. An
// unjittered schedule turns that shared trigger into a synchronised thundering
// herd on the far end, which is a mild version of the reconnect storm APRS-IS
// server operators had to complain about upstream (YSFClients#155). Spreading each
// delay means a thousand nodes that failed together do not retry together.
package backoff

import "time"

// Backoff is an exponential retry schedule with a cap. The zero value is unusable
// (a zero Initial never grows); construct one with the fields set.
type Backoff struct {
	// Initial is the first delay; each subsequent attempt doubles it.
	Initial time.Duration
	// Max caps the delay. Once reached, the schedule stays there.
	Max time.Duration
	// Rand, when set, jitters each returned delay into [d/2, d) — enough to break
	// up a synchronised herd while keeping a floor, so backing off still means
	// backing off. It must return a value in [0,1). Nil means no jitter, which is
	// the deterministic behaviour the peering dialer and its tests rely on.
	Rand func() float64

	attempt int
}

// Next returns the delay before the next attempt and advances the schedule:
// Initial, 2×, 4× … capped at Max, jittered if Rand is set.
func (b *Backoff) Next() time.Duration {
	d := b.Initial << b.attempt
	if d <= 0 || d > b.Max { // overflow or past the cap
		d = b.Max
	} else {
		b.attempt++
	}
	return b.jitter(d)
}

// jitter spreads d into [d/2, d). The schedule's progression is deliberately
// unaffected — only the delay handed back moves, so Attempt still reports how far
// down the schedule this Backoff has walked.
func (b *Backoff) jitter(d time.Duration) time.Duration {
	if b.Rand == nil || d <= 0 {
		return d
	}
	half := d / 2
	return half + time.Duration(b.Rand()*float64(d-half))
}

// Attempt reports how many times the schedule has advanced. It is what
// distinguishes "this just started failing" from "this has been failing all
// morning" when a caller wants to log or escalate.
func (b *Backoff) Attempt() int { return b.attempt }

// Reset returns the schedule to the start. Callers should do this only after a
// *sustained* success, never on the first one: a connection that is accepted and
// then immediately dropped — the duplicate-login flap behind YSFClients#155 — looks
// exactly like a success at the moment it is established, and resetting there is
// what lets the loop run hot forever.
func (b *Backoff) Reset() { b.attempt = 0 }
