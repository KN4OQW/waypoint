package auth

import (
	"testing"
	"time"
)

// The damper locks a source only after the threshold of failures, then backs off,
// and a success clears the state.
func TestDamperLockoutAndReset(t *testing.T) {
	clock := time.Unix(0, 0)
	d := newDamper(func() time.Time { return clock }, 500*time.Millisecond)

	// Below the threshold: no lockout yet.
	for i := 0; i < damperThreshold; i++ {
		if locked, _ := d.locked("1.2.3.4"); locked {
			t.Fatalf("locked after %d failures, before threshold %d", i, damperThreshold)
		}
		d.recordFailure("1.2.3.4")
	}
	// One past the threshold: locked out.
	d.recordFailure("1.2.3.4")
	locked, retry := d.locked("1.2.3.4")
	if !locked {
		t.Fatal("not locked after exceeding the threshold")
	}
	if retry <= 0 {
		t.Fatalf("retry-after = %v, want > 0", retry)
	}

	// A different source is unaffected — damping is per-source.
	if locked, _ := d.locked("5.6.7.8"); locked {
		t.Fatal("an unrelated source was locked")
	}

	// A success clears the lockout.
	d.recordSuccess("1.2.3.4")
	if locked, _ := d.locked("1.2.3.4"); locked {
		t.Fatal("still locked after a successful login")
	}
}

// The lockout lifts once its window passes.
func TestDamperLockoutExpires(t *testing.T) {
	clock := time.Unix(0, 0)
	d := newDamper(func() time.Time { return clock }, 0)
	for i := 0; i <= damperThreshold; i++ {
		d.recordFailure("host")
	}
	if locked, _ := d.locked("host"); !locked {
		t.Fatal("expected lockout")
	}
	// Advance beyond the maximum lock window; the source is forgiven.
	clock = clock.Add(damperMaxLock + damperDecay + time.Second)
	if locked, _ := d.locked("host"); locked {
		t.Fatal("lockout did not lift after its window passed")
	}
}
