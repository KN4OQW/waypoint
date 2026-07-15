package netconfig

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// DefaultConfirmTimeout is the rollback window when an apply does not request one:
// long enough for an admin to confirm the node is still reachable, short enough
// that a stranded node self-heals quickly.
const DefaultConfirmTimeout = 90 * time.Second

// Guard is the confirm-or-revert engine for network applies — the domain's
// non-negotiable safety property. A network apply can cut the admin's own
// connection, so instead of applying and hoping, the Guard:
//
//  1. Create()s a Checkpoint of the pre-apply state,
//  2. applies the new config (writes keyfiles + reloads NM),
//  3. hands back a confirm token and a deadline, and arms a SERVER-SIDE timer,
//  4. on Confirm(token) before the deadline: Destroy()s the checkpoint (permanent),
//  5. otherwise, when the timer fires: Rollback()s the checkpoint (pre-apply state).
//
// The timer runs on the Guard, not on the HTTP handler, so the rollback does not
// depend on the admin's session — even if the apply severs their connection and
// the HTTP response never arrives, the node reverts itself. Exactly one apply may
// be pending at a time.
type Guard struct {
	cp    Checkpoint
	clock Clock
	// apply performs the actual, dangerous change (keyfile Sync + nmcli reload).
	// Injected so the Guard is pure control-flow and the state machine is testable
	// without touching NetworkManager.
	apply func(Model) error
	// newToken mints confirm tokens; injectable so tests get deterministic ones.
	newToken func() string
	// logf reports lifecycle events (notably an automatic rollback — an operator
	// must see when the node reverted itself). No-op unless set via SetLogger.
	logf func(format string, args ...any)

	mu      sync.Mutex
	pending *pending
}

// SetLogger wires a logger for guard lifecycle events (e.g. log.Printf). The
// auto-rollback path especially should be visible: it fires server-side with no
// HTTP response to carry the news.
func (g *Guard) SetLogger(logf func(format string, args ...any)) { g.logf = logf }

func (g *Guard) log(format string, args ...any) {
	if g.logf != nil {
		g.logf(format, args...)
	}
}

type pending struct {
	token    string
	handle   string
	deadline time.Time
	timer    Timer
	// done is set once the apply is resolved (confirmed or rolled back), so a
	// timer firing after a confirm — or a confirm racing the timer — is a no-op.
	done    bool
	outcome Outcome
}

// Outcome is how a pending apply resolved, for the applies journal / UI.
type Outcome string

const (
	OutcomePending    Outcome = "pending"
	OutcomeConfirmed  Outcome = "confirmed"
	OutcomeRolledBack Outcome = "rolled_back"
)

var (
	// ErrApplyPending is returned by Apply when one apply is already awaiting
	// confirmation — the window must be resolved (confirmed or rolled back) first.
	ErrApplyPending = errors.New("netconfig: an apply is already pending confirmation")
	// ErrNoPendingApply is returned by Confirm when nothing is awaiting confirmation
	// (never applied, or the deadline already rolled it back).
	ErrNoPendingApply = errors.New("netconfig: no apply is pending confirmation")
	// ErrBadToken is returned by Confirm when the token does not match the pending apply.
	ErrBadToken = errors.New("netconfig: confirm token does not match the pending apply")
)

// NewGuard builds a Guard over a checkpoint backend and an apply function, using
// the real clock and a random token generator. apply is the closure that writes
// keyfiles and reloads NM (the server wires model.Sync + nmcli reload here).
func NewGuard(cp Checkpoint, apply func(Model) error) *Guard {
	return &Guard{cp: cp, clock: realClock{}, apply: apply, newToken: randomToken}
}

// randomToken mints an unguessable confirm token. crypto/rand is used (not the
// banned Math.rand / Date.now) so tokens are unpredictable.
func randomToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Apply guards a network change: it checkpoints, applies, and arms the rollback
// timer, returning the confirm token and the absolute deadline. If the apply
// itself fails, the just-created checkpoint is rolled back and destroyed so no
// half-applied state lingers, and the error is returned. Only one apply may be
// pending; a second call returns ErrApplyPending until the first resolves.
func (g *Guard) Apply(m Model, timeout time.Duration) (token string, deadline time.Time, err error) {
	if timeout <= 0 {
		timeout = DefaultConfirmTimeout
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending != nil && !g.pending.done {
		return "", time.Time{}, ErrApplyPending
	}

	handle, err := g.cp.Create(timeout)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := g.apply(m); err != nil {
		// Undo the checkpoint we just took so a failed apply leaves no armed state.
		_ = g.cp.Rollback(handle)
		_ = g.cp.Destroy(handle)
		return "", time.Time{}, err
	}

	p := &pending{
		token:    g.newToken(),
		handle:   handle,
		deadline: g.clock.Now().Add(timeout),
		outcome:  OutcomePending,
	}
	// The timer is the safety net: it runs regardless of what happens to the HTTP
	// caller. onTimeout re-checks p.done under the lock, so it is inert if the apply
	// was already confirmed.
	p.timer = g.clock.AfterFunc(timeout, func() { g.onTimeout(p) })
	g.pending = p
	return p.token, p.deadline, nil
}

// Confirm resolves the pending apply as permanent: it stops the rollback timer and
// destroys the checkpoint. It fails if nothing is pending or the token is wrong.
func (g *Guard) Confirm(token string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending == nil || g.pending.done {
		return ErrNoPendingApply
	}
	if g.pending.token != token {
		return ErrBadToken
	}
	g.pending.timer.Stop()
	g.pending.done = true
	g.pending.outcome = OutcomeConfirmed
	return g.cp.Destroy(g.pending.handle)
}

// onTimeout is the deadline handler: if the apply is still unresolved, roll back to
// the pre-apply state. Guarded by p.done so a confirm that landed first (or a
// double-fire) makes this a no-op.
func (g *Guard) onTimeout(p *pending) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if p.done {
		return
	}
	p.done = true
	p.outcome = OutcomeRolledBack
	if err := g.cp.Rollback(p.handle); err != nil {
		g.log("network apply auto-rollback FAILED after confirm deadline: %v", err)
		return
	}
	g.log("network apply auto-rolled back: no confirm before the deadline; pre-apply state restored")
}

// PendingStatus reports the in-flight apply (if any) so the UI can render the
// "Keep these settings?" countdown after a reload, without holding the token. ok
// is false when no apply is awaiting confirmation.
func (g *Guard) PendingStatus() (deadline time.Time, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending == nil || g.pending.done {
		return time.Time{}, false
	}
	return g.pending.deadline, true
}

// LastOutcome returns how the most recent apply resolved (or OutcomePending while
// one is in flight, "" if none has run). Used by tests and the applies journal.
func (g *Guard) LastOutcome() Outcome {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending == nil {
		return ""
	}
	return g.pending.outcome
}
