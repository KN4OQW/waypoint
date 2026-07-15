package netconfig

import (
	"errors"
	"testing"
	"time"
)

// --- fakes ----------------------------------------------------------------

// fakeClock is a manually-advanced Clock: timers do not fire until the test calls
// Fire(), so the confirm-or-revert deadline is exercised without real sleeps.
type fakeClock struct {
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c       *fakeClock
	f       func()
	stopped bool
	fired   bool
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) AfterFunc(_ time.Duration, f func()) Timer {
	t := &fakeTimer{c: c, f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

// fireAll fires every armed (not stopped, not already fired) timer — the test's
// stand-in for the deadline elapsing.
func (c *fakeClock) fireAll() {
	for _, t := range c.timers {
		if !t.stopped && !t.fired {
			t.fired = true
			t.f()
		}
	}
}

// fakeCheckpoint records Create/Destroy/Rollback calls so a test can assert
// exactly which resolution path ran.
type fakeCheckpoint struct {
	created   int
	destroyed []string
	rolled    []string
	createErr error
}

func (f *fakeCheckpoint) Create(time.Duration) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	f.created++
	return "cp-1", nil
}

func (f *fakeCheckpoint) Destroy(h string) error  { f.destroyed = append(f.destroyed, h); return nil }
func (f *fakeCheckpoint) Rollback(h string) error { f.rolled = append(f.rolled, h); return nil }

// newTestGuard wires a Guard over the fakes with a deterministic token.
func newTestGuard(cp Checkpoint, clk Clock, applied *int) *Guard {
	g := &Guard{
		cp:       cp,
		clock:    clk,
		apply:    func(Model) error { *applied++; return nil },
		newToken: func() string { return "tok" },
	}
	return g
}

// --- tests ----------------------------------------------------------------

// A confirmed apply destroys the checkpoint (permanent) and never rolls back —
// even after the deadline would have fired.
func TestGuardConfirmMakesPermanent(t *testing.T) {
	cp := &fakeCheckpoint{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	applied := 0
	g := newTestGuard(cp, clk, &applied)

	tok, deadline, err := g.Apply(Model{}, 90*time.Second)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if tok != "tok" || applied != 1 || cp.created != 1 {
		t.Fatalf("apply state: tok=%q applied=%d created=%d", tok, applied, cp.created)
	}
	if want := clk.now.Add(90 * time.Second); !deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", deadline, want)
	}

	if err := g.Confirm("tok"); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if len(cp.destroyed) != 1 {
		t.Fatalf("confirm should destroy exactly one checkpoint, got %v", cp.destroyed)
	}

	// The deadline elapsing after a confirm must not roll back.
	clk.fireAll()
	if len(cp.rolled) != 0 {
		t.Fatalf("rollback ran after confirm: %v", cp.rolled)
	}
	if g.LastOutcome() != OutcomeConfirmed {
		t.Fatalf("outcome = %q, want confirmed", g.LastOutcome())
	}
}

// No confirmation before the deadline rolls the change back — driven entirely by
// the server-side timer, with no HTTP session involved.
func TestGuardTimeoutRollsBack(t *testing.T) {
	cp := &fakeCheckpoint{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	applied := 0
	g := newTestGuard(cp, clk, &applied)

	var logged int
	g.SetLogger(func(string, ...any) { logged++ })
	if _, _, err := g.Apply(Model{}, 90*time.Second); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Admin never confirms; the deadline fires.
	clk.fireAll()

	if logged == 0 {
		t.Error("an auto-rollback should be logged (operator must see the node reverted)")
	}
	if len(cp.rolled) != 1 || cp.rolled[0] != "cp-1" {
		t.Fatalf("timeout should roll back the checkpoint, got %v", cp.rolled)
	}
	if len(cp.destroyed) != 0 {
		t.Fatalf("timeout must not destroy (that would make it permanent): %v", cp.destroyed)
	}
	if g.LastOutcome() != OutcomeRolledBack {
		t.Fatalf("outcome = %q, want rolled_back", g.LastOutcome())
	}
	// A confirm after the deadline is refused — nothing is pending.
	if err := g.Confirm("tok"); !errors.Is(err, ErrNoPendingApply) {
		t.Fatalf("post-deadline Confirm = %v, want ErrNoPendingApply", err)
	}
}

// Only one apply may be pending; a second is refused until the first resolves,
// then allowed.
func TestGuardOnePendingAtATime(t *testing.T) {
	cp := &fakeCheckpoint{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	applied := 0
	g := newTestGuard(cp, clk, &applied)

	if _, _, err := g.Apply(Model{}, time.Minute); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if _, _, err := g.Apply(Model{}, time.Minute); !errors.Is(err, ErrApplyPending) {
		t.Fatalf("second Apply = %v, want ErrApplyPending", err)
	}
	if err := g.Confirm("tok"); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	// After resolution a new apply is allowed.
	if _, _, err := g.Apply(Model{}, time.Minute); err != nil {
		t.Fatalf("post-confirm Apply: %v", err)
	}
}

// A wrong confirm token is rejected and leaves the apply pending (so the real
// admin can still confirm).
func TestGuardBadToken(t *testing.T) {
	cp := &fakeCheckpoint{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	applied := 0
	g := newTestGuard(cp, clk, &applied)

	if _, _, err := g.Apply(Model{}, time.Minute); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := g.Confirm("wrong"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("Confirm(wrong) = %v, want ErrBadToken", err)
	}
	if _, ok := g.PendingStatus(); !ok {
		t.Fatalf("apply should still be pending after a bad token")
	}
	if err := g.Confirm("tok"); err != nil {
		t.Fatalf("Confirm(correct): %v", err)
	}
}

// A failing apply rolls back and destroys the checkpoint it just took, and leaves
// nothing pending.
func TestGuardApplyFailureUnwinds(t *testing.T) {
	cp := &fakeCheckpoint{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	g := &Guard{
		cp:       cp,
		clock:    clk,
		apply:    func(Model) error { return errors.New("nmcli reload failed") },
		newToken: func() string { return "tok" },
	}
	_, _, err := g.Apply(Model{}, time.Minute)
	if err == nil {
		t.Fatalf("Apply should surface the apply failure")
	}
	if len(cp.rolled) != 1 || len(cp.destroyed) != 1 {
		t.Fatalf("failed apply should roll back + destroy, rolled=%v destroyed=%v", cp.rolled, cp.destroyed)
	}
	if _, ok := g.PendingStatus(); ok {
		t.Fatalf("no apply should be pending after a failed apply")
	}
}

// A Create failure surfaces without applying anything.
func TestGuardCheckpointCreateFailure(t *testing.T) {
	cp := &fakeCheckpoint{createErr: errors.New("NM has no checkpoint support")}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	applied := 0
	g := newTestGuard(cp, clk, &applied)

	if _, _, err := g.Apply(Model{}, time.Minute); err == nil {
		t.Fatalf("Apply should fail when the checkpoint cannot be created")
	}
	if applied != 0 {
		t.Fatalf("apply must not run when the checkpoint failed (applied=%d)", applied)
	}
}
