package auth

import (
	"sync"
	"time"
)

// Brute-force damping (RFC-0002). This is damping, not a WAF: it raises the cost
// of online guessing against one device, nothing more. Two modest mechanisms:
//
//   - a fixed small delay on every failed login, so an attacker cannot pipeline
//     high-rate guesses against the argon2id verifier;
//   - a per-source failure counter with backoff: after a threshold of failures a
//     source is temporarily refused, with the lockout growing on repeated abuse.
//
// It is not rate-limiting infrastructure and does not defend against a distributed
// attacker. The primary defense remains that there is no default credential and no
// config surface before claim.
const (
	damperThreshold = 5                // failures before lockout begins
	damperBaseLock  = 5 * time.Second  // first lockout after the threshold
	damperMaxLock   = 15 * time.Minute // cap on the growing lockout
	damperDecay     = 15 * time.Minute // idle time after which a source's count resets
)

// damper tracks per-source login failures. It is safe for concurrent use.
type damper struct {
	mu       sync.Mutex
	sources  map[string]*sourceState
	now      func() time.Time
	failWait time.Duration // fixed per-failure delay
}

type sourceState struct {
	fails     int
	lockUntil time.Time
	lastFail  time.Time
}

func newDamper(now func() time.Time, failWait time.Duration) *damper {
	return &damper{sources: map[string]*sourceState{}, now: now, failWait: failWait}
}

// locked reports whether source is currently in a lockout window, and if so how
// long until it lifts. The gate refuses a login attempt from a locked source
// before ever touching the verifier.
func (d *damper) locked(source string) (bool, time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.sources[source]
	if st == nil {
		return false, 0
	}
	now := d.now()
	// A long-idle source is forgiven: drop stale state so a transient fat-finger
	// yesterday does not compound with a typo today.
	if !st.lastFail.IsZero() && now.Sub(st.lastFail) > damperDecay {
		delete(d.sources, source)
		return false, 0
	}
	if now.Before(st.lockUntil) {
		return true, st.lockUntil.Sub(now)
	}
	return false, 0
}

// fixedDelay is the constant delay applied to every failed attempt.
func (d *damper) fixedDelay() time.Duration { return d.failWait }

// recordFailure bumps a source's failure count and, past the threshold, sets a
// backing-off lockout (doubling per failure over the threshold, capped).
func (d *damper) recordFailure(source string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	st := d.sources[source]
	if st == nil {
		st = &sourceState{}
		d.sources[source] = st
	}
	if !st.lastFail.IsZero() && now.Sub(st.lastFail) > damperDecay {
		st.fails = 0 // idle long enough — start fresh
	}
	st.fails++
	st.lastFail = now
	if st.fails > damperThreshold {
		lock := damperBaseLock << uint(st.fails-damperThreshold-1)
		if lock > damperMaxLock || lock <= 0 {
			lock = damperMaxLock
		}
		st.lockUntil = now.Add(lock)
	}
}

// recordSuccess clears a source's failure state on a successful login.
func (d *damper) recordSuccess(source string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sources, source)
}
