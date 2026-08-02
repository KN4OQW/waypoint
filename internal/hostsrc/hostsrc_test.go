package hostsrc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/verifydl"
)

func TestSplit(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"a", []string{"a"}},
		{"a,b", []string{"a", "b"}},
		{" a , b ,, c ", []string{"a", "b", "c"}},
		{"a\nb\tc", []string{"a", "b", "c"}},
	} {
		got := Split(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("Split(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("Split(%q) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}
}

// A dead primary must cost a retry, not the feature: the next source supplies the
// body, and the status records which one actually answered.
func TestDownloadFallsThroughToALiveSource(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("BM_Test 0 1.2.3.4 pass 62031\n"))
	}))
	defer live.Close()

	Register("t_fallback", "test")
	body, src, err := Download(context.Background(), "t_fallback", []string{dead.URL, live.URL}, verifydl.Verify{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(body) != "BM_Test 0 1.2.3.4 pass 62031\n" {
		t.Errorf("body = %q", body)
	}
	if src != live.URL {
		t.Errorf("source = %q, want the live one (%q)", src, live.URL)
	}
	st, _ := Report("t_fallback")
	if st.Source != live.URL || st.LastSuccess.IsZero() || st.LastError != "" {
		t.Errorf("status after success = %+v", st)
	}
}

// When every source fails the error has to name them all, because "the list is
// empty" is otherwise unanswerable.
func TestDownloadReportsEverySourceOnTotalFailure(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer b.Close()

	Register("t_allfail", "test")
	_, _, err := Download(context.Background(), "t_allfail", []string{a.URL, b.URL}, verifydl.Verify{})
	if err == nil {
		t.Fatal("want an error when every source fails")
	}
	for _, u := range []string{a.URL, b.URL} {
		if !contains(err.Error(), u) {
			t.Errorf("error %q does not name source %q", err, u)
		}
	}
	st, _ := Report("t_allfail")
	if st.LastError == "" {
		t.Error("a total failure should be recorded in the status")
	}
	if st.LastAttempt.IsZero() {
		t.Error("an attempt should be recorded even when it fails")
	}
}

func TestDownloadWithNoSources(t *testing.T) {
	if _, _, err := Download(context.Background(), "t_none", nil, verifydl.Verify{}); err != ErrNoSources {
		t.Errorf("err = %v, want ErrNoSources", err)
	}
}

// Restore lays a floor only where there is no cache: a stale real list must
// always beat the shipped copy.
func TestRestoreOnlyWritesWhenThereIsNoCache(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "DMR_Hosts.txt")
	wrote, err := Restore(DMRHosts, path)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !wrote {
		t.Fatal("want the shipped copy written when no cache exists")
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		t.Fatalf("seed not readable: %v (%d bytes)", err, len(b))
	}
	if st, _ := Report(DMRHosts); !st.FromSeed || st.Source != "seed" {
		t.Errorf("status should record the seed: %+v", st)
	}

	// An existing cache is never overwritten.
	real := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(real, []byte("REAL_LIST\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if wrote, err := Restore(DMRHosts, real); err != nil || wrote {
		t.Errorf("Restore over an existing cache: wrote=%v err=%v, want false/nil", wrote, err)
	}
	if b, _ := os.ReadFile(real); string(b) != "REAL_LIST\n" {
		t.Errorf("Restore clobbered a real cache: %q", b)
	}

	// An empty file counts as no cache — that is the interrupted-first-write case.
	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if wrote, err := Restore(DMRHosts, empty); err != nil || !wrote {
		t.Errorf("Restore over an empty file: wrote=%v err=%v, want true/nil", wrote, err)
	}
}

// A list with no shipped copy is a normal state, not an error.
func TestRestoreWithoutASeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "DMRIds.dat")
	wrote, err := Restore(DMRIds, path)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if wrote {
		t.Error("the DMR ID table ships no copy; nothing should have been written")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("no seed should mean no file")
	}
}

func TestSeedsAreShippedForTheListsThatClaimThem(t *testing.T) {
	// Every picker-backing list now ships a copy: the four that had none are
	// captured from the classic text lists and converted (internal/hostconv).
	for _, name := range []string{DMRHosts, DMRTalkgroups, DStarHosts, YSFHosts, P25Hosts, NXDNHosts, M17Hosts} {
		if !HasSeed(name) {
			t.Errorf("%s should ship a copy", name)
		}
		if b, ok := Seed(name); !ok || len(b) < 1024 {
			t.Errorf("%s seed looks truncated (%d bytes)", name, len(b))
		}
	}
	// DMRIds.dat is deliberately not shipped: 6.6 MB of continuously-ageing data.
	if HasSeed(DMRIds) {
		t.Error("the DMR ID table should not be embedded — it is 6.6 MB and ages continuously")
	}
}

func TestStale(t *testing.T) {
	if !(Status{}).Stale(time.Hour) {
		t.Error("a list that never succeeded is stale")
	}
	if (Status{LastSuccess: time.Now()}).Stale(time.Hour) {
		t.Error("a list that just refreshed is not stale")
	}
	if !(Status{LastSuccess: time.Now().Add(-2 * time.Hour)}).Stale(time.Hour) {
		t.Error("a list older than the window is stale")
	}
}

// A failed refresh must not cost a node its lists until the next daily tick: the
// daemon starts before the network is up by design, so the first fetch of every
// list routinely loses that race and the retry is what recovers it.
func TestNextWaitRetriesFailuresWellInsideTheInterval(t *testing.T) {
	const day = 24 * time.Hour

	wait, next := nextWait(day, firstRetry, true)
	if wait != firstRetry {
		t.Errorf("first retry after a failure = %s, want %s — a boot-order race must not cost a day", wait, firstRetry)
	}
	if next != 2*firstRetry {
		t.Errorf("backoff after one failure = %s, want %s", next, 2*firstRetry)
	}

	// The ladder climbs but is capped, so an upstream that is genuinely gone is
	// retried twice an hour rather than continuously.
	b := firstRetry
	for i := 0; i < 20; i++ {
		_, b = nextWait(day, b, true)
	}
	if b != maxRetry {
		t.Errorf("backoff saturates at %s, got %s", maxRetry, b)
	}
	if wait, _ := nextWait(day, b, true); wait != maxRetry {
		t.Errorf("saturated retry waits %s, want %s", wait, maxRetry)
	}

	// A success returns to the normal cadence and forgets the ladder.
	if wait, next := nextWait(day, maxRetry, false); wait != day || next != firstRetry {
		t.Errorf("after a success: wait=%s next=%s, want %s and %s", wait, next, day, firstRetry)
	}

	// Retrying must never be slower than the configured refresh.
	if wait, _ := nextWait(time.Second, maxRetry, true); wait != time.Second {
		t.Errorf("retry with a 1s interval waited %s, want 1s", wait)
	}
}

// Every keeps calling fetch after failures, and stops when the context is done.
func TestEveryRetriesUntilCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	defer clearRefresher("test_retry")
	calls := make(chan struct{}, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		Every(ctx, "test_retry", time.Millisecond, func(context.Context) error {
			// Non-blocking: fetch must never be what stalls the loop, or a full
			// channel would wedge this test instead of failing it.
			select {
			case calls <- struct{}{}:
			default:
			}
			return os.ErrNotExist // always fails; the loop must keep trying
		})
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-calls:
		case <-time.After(2 * time.Second):
			t.Fatalf("fetch called only %d time(s); a failing refresh must keep retrying", i)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Every did not return after its context was canceled")
	}
}

// The status is in memory and the cache is on disk, so a restart must not make a
// node forget it is still serving the shipped copy.
func TestRestoreRecognisesTheSeedAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "DMR_Hosts.txt")

	// First boot: no cache, so the seed is written and the status says so.
	wrote, err := Restore(DMRHosts, path)
	if err != nil || !wrote {
		t.Fatalf("Restore on a fresh node: wrote=%v err=%v", wrote, err)
	}
	if st, _ := Report(DMRHosts); !st.FromSeed {
		t.Fatal("a freshly seeded list must report from_seed")
	}

	// Restart: wipe the in-memory status the way a process restart does, and run
	// Restore again against the cache the previous boot left behind.
	with(DMRHosts, func(s *Status) { s.FromSeed, s.Source = false, "" })
	wrote, err = Restore(DMRHosts, path)
	if err != nil || wrote {
		t.Fatalf("Restore over an existing cache: wrote=%v err=%v (it must not overwrite)", wrote, err)
	}
	if st, _ := Report(DMRHosts); !st.FromSeed || st.Source != "seed" {
		t.Errorf("after a restart: from_seed=%v source=%q — a seeded node must still say so", st.FromSeed, st.Source)
	}

	// A real download replaces the floor, and is not mistaken for it.
	if err := os.WriteFile(path, []byte("BM_Downloaded 1 1.2.3.4 passw0rd 62031\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	with(DMRHosts, func(s *Status) { s.FromSeed, s.Source = false, "" })
	if _, err := Restore(DMRHosts, path); err != nil {
		t.Fatal(err)
	}
	if st, _ := Report(DMRHosts); st.FromSeed {
		t.Error("a downloaded cache must not be reported as the shipped copy")
	}
}

// Refresh runs the same fetch the scheduler runs, and reports a list it does not
// know about rather than silently doing nothing.
func TestRefreshRunsTheRegisteredFetch(t *testing.T) {
	defer clearRefresher("test_manual")
	var calls int
	setRefresher("test_manual", func(context.Context) error {
		calls++
		return nil
	})
	if err := Refresh(context.Background(), "test_manual"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if calls != 1 {
		t.Errorf("fetch called %d times, want 1", calls)
	}
	if err := Refresh(context.Background(), "no_such_list"); !errors.Is(err, ErrNoRefresher) {
		t.Errorf("Refresh of an unknown list = %v, want ErrNoRefresher", err)
	}
}

// A refresh already in flight is reported as busy, not queued and not run twice
// concurrently — two writers racing on one cache file is exactly what the lock
// exists to prevent.
func TestRefreshIsNotRunConcurrently(t *testing.T) {
	// Cleared on the way out: this fake blocks until the test releases it, and a
	// later RefreshAll picking it up out of the shared registry would hang.
	defer clearRefresher("test_busy")
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	setRefresher("test_busy", func(context.Context) error {
		// Announce without blocking, and park until released. Once release is
		// closed both operations complete immediately, so a later call — the one
		// that checks the lock was handed back — runs straight through.
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return nil
	})

	done := make(chan error, 1)
	go func() { done <- Refresh(context.Background(), "test_busy") }()
	<-entered // the first refresh is now inside the fetch, holding the lock

	if err := Refresh(context.Background(), "test_busy"); !errors.Is(err, ErrBusy) {
		t.Errorf("second concurrent Refresh = %v, want ErrBusy", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Errorf("first Refresh: %v", err)
	}
	// With the first one finished the lock is free again.
	if err := Refresh(context.Background(), "test_busy"); err != nil {
		t.Errorf("Refresh after the previous one finished: %v", err)
	}
}

// RefreshAll reports every list's outcome, and one failure never suppresses
// another list's success.
func TestRefreshAllReportsEachListIndependently(t *testing.T) {
	defer clearRefresher("test_all_ok")
	defer clearRefresher("test_all_bad")
	Register("test_all_ok", "A list that works")
	setRefresher("test_all_ok", func(context.Context) error { return nil })
	setRefresher("test_all_bad", func(context.Context) error { return os.ErrPermission })

	got := map[string]RefreshResult{}
	for _, res := range RefreshAll(context.Background()) {
		got[res.Name] = res
	}
	if ok := got["test_all_ok"]; !ok.OK || ok.Error != "" {
		t.Errorf("working list: %+v, want OK", ok)
	}
	if ok := got["test_all_ok"]; ok.Label != "A list that works" {
		t.Errorf("label = %q, want the registered label", ok.Label)
	}
	if bad := got["test_all_bad"]; bad.OK || bad.Error == "" {
		t.Errorf("failing list: %+v, want a reported error", bad)
	}
	if !Refreshable() {
		t.Error("Refreshable() is false with refreshers registered")
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (hay == needle || indexOf(hay, needle) >= 0)
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
