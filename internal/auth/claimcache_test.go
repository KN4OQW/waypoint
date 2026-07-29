package auth

import (
	"testing"
	"time"
)

// The claim cache used to live forever, and that produced the worst kind of
// bug: `waypointd reset-claim` — a SEPARATE PROCESS on the same SQLite file —
// wiped the credential, printed success, and the running daemon carried on
// serving the login page to an operator whose password no longer existed
// anywhere. Nothing was broken enough to look broken.
//
// These pin the expiry that fixes it, with an injected clock so nothing sleeps.

func newTestAuth(t *testing.T, now func() time.Time) (*Auth, *Store) {
	t.Helper()
	st := newAuthStore(t)
	a := New(st, Options{Now: now})
	a.claimedTTL = time.Second
	return a, st
}

func claimIt(t *testing.T, st *Store, at time.Time) {
	t.Helper()
	rec, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Claim("bench", rec, at); err != nil {
		t.Fatal(err)
	}
}

func TestClaimCacheNoticesAnotherProcessResetting(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	a, st := newTestAuth(t, func() time.Time { return clock })

	claimIt(t, st, clock)
	if !a.Claimed() {
		t.Fatal("a freshly claimed device reports unclaimed")
	}

	// Another process — `waypointd reset-claim` — wipes the credential. It has no
	// way to reach into this process's memory, which is the whole point.
	if err := st.ResetClaim(); err != nil {
		t.Fatal(err)
	}
	if !a.Claimed() {
		t.Fatal("the cache should still be warm immediately after an external reset")
	}

	clock = clock.Add(2 * time.Second)
	if a.Claimed() {
		t.Fatal("the daemon is still refusing logins for a credential that no longer exists — the cache never expired")
	}
}

// TestClaimCacheStillCachesWithinItsWindow keeps the fix honest: the gate
// consults this on every request, so it must not become a query per request.
func TestClaimCacheStillCachesWithinItsWindow(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	a, st := newTestAuth(t, func() time.Time { return clock })
	claimIt(t, st, clock)
	if !a.Claimed() {
		t.Fatal("claimed device reports unclaimed")
	}
	if err := st.ResetClaim(); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(500 * time.Millisecond)
	if !a.Claimed() {
		t.Fatal("the cache expired inside its own window")
	}
}
