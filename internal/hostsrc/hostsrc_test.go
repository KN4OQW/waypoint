package hostsrc

import (
	"context"
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
