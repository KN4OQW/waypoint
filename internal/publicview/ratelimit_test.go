package publicview

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestBucketRefills drives the limiter off a fake clock, so the refill rate is
// asserted exactly rather than by sleeping and hoping.
func TestBucketRefills(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	l := NewRateLimiter()
	l.now = func() time.Time { return now }

	// The burst is spendable immediately: a first page load is never the request
	// that gets refused.
	for i := range RateLimitBurst {
		if !l.Allow("k") {
			t.Fatalf("refused request %d of the burst", i+1)
		}
	}
	if l.Allow("k") {
		t.Fatal("the bucket did not run out after its burst")
	}

	// One second later, RateLimitPerSecond tokens are back — and no more.
	now = now.Add(time.Second)
	for i := range RateLimitPerSecond {
		if !l.Allow("k") {
			t.Fatalf("refused request %d of the refill", i+1)
		}
	}
	if l.Allow("k") {
		t.Error("the bucket refilled past one second's worth of tokens")
	}

	// A long idle period refills to the burst ceiling and no further, so a client
	// that goes quiet for an hour cannot bank an hour of requests.
	now = now.Add(time.Hour)
	for i := range RateLimitBurst {
		if !l.Allow("k") {
			t.Fatalf("refused request %d after a long idle", i+1)
		}
	}
	if l.Allow("k") {
		t.Error("an idle client banked more than the burst")
	}
}

func TestBucketsAreIndependent(t *testing.T) {
	l := NewRateLimiter()
	for range RateLimitBurst * 2 {
		l.Allow("noisy")
	}
	if !l.Allow("quiet") {
		t.Error("one client exhausting its bucket denied another — the limit is global, not per-IP")
	}
}

func TestClientIP(t *testing.T) {
	for _, tc := range []struct{ remote, want string }{
		{"203.0.113.9:1234", "203.0.113.9"},
		{"127.0.0.1:5555", "127.0.0.1"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"no-port", "no-port"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tc.remote
		if got := ClientIP(r); got != tc.want {
			t.Errorf("ClientIP(%q) = %q, want %q", tc.remote, got, tc.want)
		}
	}
}

// TestForwardedForIsIgnored. Honoring a client-set header would let one caller
// bypass the limit entirely by inventing an address per request.
func TestForwardedForIsIgnored(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("X-Forwarded-For", "10.1.2.3")
	r.Header.Set("X-Real-IP", "10.4.5.6")
	if got := ClientIP(r); got != "203.0.113.9" {
		t.Errorf("ClientIP honored a client-set header: %q", got)
	}
}

func TestLoopbackExempt(t *testing.T) {
	l := NewRateLimiter()
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, remote := range []string{"127.0.0.1:1", "[::1]:1"} {
		for i := range RateLimitBurst * 3 {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = remote
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("%s throttled on request %d", remote, i+1)
			}
		}
	}
}

func TestMiddlewareRefusesWithRetryAfter(t *testing.T) {
	l := NewRateLimiter()
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	var last *httptest.ResponseRecorder
	for range RateLimitBurst * 3 {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "203.0.113.9:1234"
		last = httptest.NewRecorder()
		h.ServeHTTP(last, r)
		if last.Code == http.StatusTooManyRequests {
			break
		}
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatal("the middleware never refused")
	}
	if got := last.Header().Get("Retry-After"); got == "" {
		t.Error("a 429 without Retry-After leaves a well-behaved client guessing")
	}
}

// TestSweepBoundsTheTable. The limiter is keyed by remote address, which is to say
// keyed by whatever an attacker chooses, so it has to discard what it no longer
// needs or it is a memory leak with a network interface.
func TestSweepBoundsTheTable(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	l := NewRateLimiter()
	l.now = func() time.Time { return now }

	for i := range 2000 {
		l.Allow(string(rune(i)) + "-spoofed")
	}
	before := len(l.buckets)
	if before < 1024 {
		t.Fatalf("fixture built only %d buckets, too few to trigger the sweep", before)
	}

	// Everything ages out, and one live client keeps its bucket.
	now = now.Add(2 * rateLimitIdle)
	l.Allow("still-here")
	if got := len(l.buckets); got > 2 {
		t.Errorf("the sweep left %d buckets, want the one live client", got)
	}
	if !l.Allow("still-here") {
		t.Error("the sweep discarded the bucket of a client that had just been seen")
	}
}
