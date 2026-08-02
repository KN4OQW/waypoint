package publicview

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Rate limits for the public surface. Constants rather than configuration, on
// purpose: these routes are reachable by anyone who finds the node, so the ceiling
// is a property of the software rather than something an operator can raise into a
// problem they will not notice until it is being used against them.
//
// The numbers are sized for the page they serve. A visitor loading the public
// dashboard makes four requests and then polls status every five seconds; a club
// embedding the widget makes one request per viewer per refresh. Ten a second
// sustained, thirty in a burst, is far above either and far below what it costs to
// make the node's own event queries matter.
const (
	RateLimitPerSecond = 10
	RateLimitBurst     = 30

	// rateLimitIdle is how long an unused bucket is kept before the sweeper
	// discards it. Without this the limiter is a memory leak keyed by remote
	// address — which is to say, keyed by whatever an attacker chooses.
	rateLimitIdle = 10 * time.Minute
)

// RateLimiter is a per-IP token bucket.
//
// Per-IP rather than global because a global limit lets one noisy client deny the
// page to everyone else, which converts a rate limit into the outage it exists to
// prevent.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   float64
	// now is injectable so the refill behavior can be tested without sleeping.
	now func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter returns a limiter at the package's constants.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		buckets: map[string]*bucket{},
		rate:    RateLimitPerSecond,
		burst:   RateLimitBurst,
		now:     time.Now,
	}
}

// Allow reports whether a request from key may proceed, consuming a token if so.
func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		// A new client starts with a full bucket, so the first page load is never
		// the request that gets refused.
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += elapsed.Seconds() * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	l.sweepLocked(now)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweepLocked discards buckets nothing has touched recently. It runs on the
// request path rather than on a timer because the limiter has no lifecycle of its
// own to hang a goroutine off, and the work is proportional to the table it is
// already holding the lock on.
func (l *RateLimiter) sweepLocked(now time.Time) {
	if len(l.buckets) < 1024 {
		return
	}
	for k, b := range l.buckets {
		if now.Sub(b.last) > rateLimitIdle {
			delete(l.buckets, k)
		}
	}
}

// Middleware applies the limiter to a handler, keyed by client IP.
//
// Loopback is exempt. The dashboard, the health check, and anything else running
// on the node itself reach these routes over 127.0.0.1, and a limit that could
// throttle the node's own UI would be a self-inflicted outage with no security
// benefit — a local attacker has better options than the public JSON API.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		if !isLoopback(ip) && !l.Allow(ip) {
			// Retry-After is advisory but costs nothing and is what a well-behaved
			// embedding client will honor.
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ClientIP is the address a request came from.
//
// It reads RemoteAddr and nothing else. X-Forwarded-For is deliberately ignored:
// it is a header the client sets, so honoring it would let anyone bypass the limit
// by inventing an address per request. A node genuinely behind a trusted proxy
// needs the proxy's own limiting, which is where the trust actually lives.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isLoopback(ip string) bool {
	addr := net.ParseIP(ip)
	return addr != nil && addr.IsLoopback()
}
