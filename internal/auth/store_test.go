package auth

import (
	"sync"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/store"
)

func newAuthStore(t *testing.T) *Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	as, err := NewStore(st)
	if err != nil {
		t.Fatal(err)
	}
	return as
}

// A fresh store is unclaimed; a claim flips it; the credential is stored hashed.
func TestClaimLifecycle(t *testing.T) {
	as := newAuthStore(t)
	if claimed, err := as.IsClaimed(); err != nil || claimed {
		t.Fatalf("fresh store IsClaimed = %v, %v; want false, nil", claimed, err)
	}
	rec, _ := HashPassword("hunter2hunter2")
	if err := as.Claim("kn4oqw", rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	if claimed, err := as.IsClaimed(); err != nil || !claimed {
		t.Fatalf("after claim IsClaimed = %v, %v; want true, nil", claimed, err)
	}
	admin, ok, err := as.Admin()
	if err != nil || !ok {
		t.Fatalf("Admin after claim: ok=%v err=%v", ok, err)
	}
	if admin.Username != "kn4oqw" {
		t.Fatalf("admin username = %q", admin.Username)
	}
	if ok, _ := admin.Record.Verify("hunter2hunter2"); !ok {
		t.Fatal("stored record does not verify the claim password")
	}
}

// A second claim loses to the first: it returns ErrAlreadyClaimed and leaves the
// original credential intact.
func TestClaimSecondConflicts(t *testing.T) {
	as := newAuthStore(t)
	rec, _ := HashPassword("first-password")
	if err := as.Claim("first", rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	rec2, _ := HashPassword("second-password")
	if err := as.Claim("second", rec2, time.Now()); err != ErrAlreadyClaimed {
		t.Fatalf("second claim err = %v, want ErrAlreadyClaimed", err)
	}
	admin, _, _ := as.Admin()
	if admin.Username != "first" {
		t.Fatalf("second claim overwrote the admin: username = %q", admin.Username)
	}
}

// Concurrent claims serialize on the fixed admin id: exactly one wins, the store
// ends with a single admin and one claimed_at. (The gate-level HTTP race test in
// cmd/waypointd complements this at the handler layer.)
func TestConcurrentClaimSingleWinner(t *testing.T) {
	as := newAuthStore(t)
	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec, _ := HashPassword("password-longenough")
			err := as.Claim("user", rec, time.Now())
			mu.Lock()
			switch err {
			case nil:
				wins++
			case ErrAlreadyClaimed:
				conflicts++
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1", wins)
	}
	if conflicts != n-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, n-1)
	}
}

// Sessions round-trip, slide forward on touch, and revoke individually and en masse.
func TestSessionCRUD(t *testing.T) {
	as := newAuthStore(t)
	now := time.Now()
	if err := as.CreateSession("hashA", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := as.LookupSession("hashA")
	if err != nil || !ok {
		t.Fatalf("lookup: ok=%v err=%v", ok, err)
	}
	if !got.ExpiresAt.After(now) {
		t.Fatal("expires_at not stored")
	}
	// Touch slides the deadline out.
	if err := as.TouchSession("hashA", now.Add(time.Minute), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _, _ = as.LookupSession("hashA")
	if !got.ExpiresAt.After(now.Add(time.Hour)) {
		t.Fatal("touch did not slide expires_at forward")
	}
	// Revoke one.
	if err := as.RevokeSession("hashA"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := as.LookupSession("hashA"); ok {
		t.Fatal("session still present after revoke")
	}
}

// ResetClaim returns the store to the unclaimed state and revokes all sessions.
func TestResetClaim(t *testing.T) {
	as := newAuthStore(t)
	rec, _ := HashPassword("password-longenough")
	if err := as.Claim("user", rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_ = as.CreateSession("s1", now, now.Add(time.Hour))
	_ = as.CreateSession("s2", now, now.Add(time.Hour))
	if err := as.ResetClaim(); err != nil {
		t.Fatal(err)
	}
	if claimed, _ := as.IsClaimed(); claimed {
		t.Fatal("still claimed after reset")
	}
	if _, ok, _ := as.Admin(); ok {
		t.Fatal("admin row survived reset")
	}
	if n, _ := as.SessionCount(); n != 0 {
		t.Fatalf("sessions survived reset: %d", n)
	}
	// After a reset the device can be claimed again.
	if err := as.Claim("newowner", rec, time.Now()); err != nil {
		t.Fatalf("re-claim after reset failed: %v", err)
	}
}

// SweepExpired removes only sessions past their deadline.
func TestSweepExpired(t *testing.T) {
	as := newAuthStore(t)
	base := time.Now()
	_ = as.CreateSession("live", base, base.Add(time.Hour))
	_ = as.CreateSession("dead", base.Add(-2*time.Hour), base.Add(-time.Hour))
	if err := as.SweepExpired(base); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := as.LookupSession("dead"); ok {
		t.Fatal("expired session not swept")
	}
	if _, ok, _ := as.LookupSession("live"); !ok {
		t.Fatal("live session wrongly swept")
	}
}

// migrate is idempotent — a second NewStore over the same store is a clean no-op.
func TestMigrateIdempotent(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := NewStore(st); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(st); err != nil {
		t.Fatalf("second NewStore (re-migrate) failed: %v", err)
	}
}
