package auth

import (
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// cookieName is the session cookie. The raw 256-bit token lives only here; the
// store keeps its hash.
const cookieName = "waypoint_session"

// defaultIdleWindow is the session idle expiry (RFC-0002): a session untouched for
// this long is invalid and swept. Activity slides it forward.
const defaultIdleWindow = 7 * 24 * time.Hour

// defaultFailDelay is the fixed delay applied to every failed login.
const defaultFailDelay = 500 * time.Millisecond

// Auth is the security subsystem: it owns the claim-state cache, the session
// cookie contract, and the login damper, and it produces the HTTP gate that
// fronts the whole mux. One Auth is created per daemon.
type Auth struct {
	store  *Store
	logf   func(string, ...any)
	now    func() time.Time
	sleep  func(time.Duration)
	damper *damper

	idleWindow time.Duration

	// secureCookie sets the cookie Secure flag. It is false until the TLS PR
	// lands and flips it — a pre-TLS build must not set Secure or the cookie would
	// be unusable over plain HTTP during bring-up (RFC-0002).
	secureCookie bool

	// authPage is the pre-auth HTML the gate serves at the top-level route while
	// unclaimed (the claim screen) or claimed-without-a-session (the login screen).
	// It is one self-contained page that branches on GET /api/health's claim flag,
	// so the same bytes serve both states. The daemon wires the embedded UI asset
	// here; when it is nil (auth's own unit tests, which don't pull in the UI) the
	// gate falls back to the minimal built-in placeholders below.
	authPage []byte

	// claimed is the cached claim state. The gate consults it on every request, so
	// it is read far more than it changes; it is loaded once from the store and
	// invalidated on claim and on reset.
	claimedMu sync.RWMutex
	claimed   bool
	claimedOK bool // whether the cache has been populated
}

// Options configures an Auth. The zero value is usable: Now/Sleep/Logf default to
// the real clock, sleep, and the standard logger, and the windows to their
// RFC-0002 defaults. Tests override the clock, sleep, and delays to run instantly.
type Options struct {
	Now          func() time.Time
	Sleep        func(time.Duration)
	Logf         func(string, ...any)
	IdleWindow   time.Duration
	FailDelay    time.Duration
	SecureCookie bool

	// AuthPage is the pre-auth HTML served at the top-level route (the claim/login
	// screen). The daemon passes the embedded UI asset; leave it nil to fall back to
	// the built-in placeholders.
	AuthPage []byte
}

// New builds an Auth over the given store, migrating the auth tables if needed.
func New(s *Store, opts Options) *Auth {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	logf := opts.Logf
	if logf == nil {
		logf = log.Printf
	}
	idle := opts.IdleWindow
	if idle <= 0 {
		idle = defaultIdleWindow
	}
	failDelay := opts.FailDelay
	if failDelay < 0 {
		failDelay = 0
	} else if failDelay == 0 {
		failDelay = defaultFailDelay
	}
	return &Auth{
		store:        s,
		logf:         logf,
		now:          now,
		sleep:        sleep,
		damper:       newDamper(now, failDelay),
		idleWindow:   idle,
		secureCookie: opts.SecureCookie,
		authPage:     opts.AuthPage,
	}
}

// Claimed reports the device's claim state from the cache, loading it from the
// store on first use. A store error is treated as "unclaimed" and logged: failing
// closed here means an unreadable store serves only the claim allowlist rather
// than exposing config surfaces on a boolean default.
func (a *Auth) Claimed() bool {
	a.claimedMu.RLock()
	if a.claimedOK {
		v := a.claimed
		a.claimedMu.RUnlock()
		return v
	}
	a.claimedMu.RUnlock()

	a.claimedMu.Lock()
	defer a.claimedMu.Unlock()
	if a.claimedOK { // filled while we waited for the write lock
		return a.claimed
	}
	claimed, err := a.store.IsClaimed()
	if err != nil {
		a.logf("auth: claim-state query failed, treating device as unclaimed: %v", err)
		return false // fail closed; leave the cache unpopulated so we retry next time
	}
	a.claimed, a.claimedOK = claimed, true
	return a.claimed
}

// invalidateClaimed drops the cached claim state so the next Claimed reloads it
// from the store. Called after a claim and after a reset.
func (a *Auth) invalidateClaimed() {
	a.claimedMu.Lock()
	a.claimed, a.claimedOK = false, false
	a.claimedMu.Unlock()
}

// authenticate resolves the request's session cookie to a live session, sliding
// its idle window forward. It returns false when there is no cookie, the session
// is unknown, or it has passed idle expiry (in which case the stale row is swept).
func (a *Auth) authenticate(r *http.Request) (Session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return Session{}, false
	}
	hash := hashToken(c.Value)
	sess, ok, err := a.store.LookupSession(hash)
	if err != nil {
		a.logf("auth: session lookup failed: %v", err)
		return Session{}, false
	}
	if !ok {
		return Session{}, false
	}
	now := a.now()
	if !now.Before(sess.ExpiresAt) {
		// Past idle expiry: sweep the row so it cannot be revived, and reject.
		if err := a.store.RevokeSession(hash); err != nil {
			a.logf("auth: sweeping expired session failed: %v", err)
		}
		return Session{}, false
	}
	if err := a.store.TouchSession(hash, now, now.Add(a.idleWindow)); err != nil {
		a.logf("auth: touching session failed: %v", err)
	}
	return sess, true
}

// issueSession mints a session and sets the cookie. The raw token is returned to
// the client only here, once; the store keeps only its hash.
func (a *Auth) issueSession(w http.ResponseWriter) error {
	raw, hash, err := newSessionToken()
	if err != nil {
		return err
	}
	now := a.now()
	if err := a.store.CreateSession(hash, now, now.Add(a.idleWindow)); err != nil {
		return err
	}
	a.setCookie(w, raw)
	return nil
}

// setCookie writes the session cookie: HttpOnly (out of reach of injected script),
// SameSite=Lax (blunts CSRF against state-changing routes), Path=/, and Secure
// once TLS lands. MaxAge tracks the idle window.
func (a *Auth) setCookie(w http.ResponseWriter, raw string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.idleWindow / time.Second),
	})
}

// clearCookie expires the session cookie in the client (logout / rejected reset).
func (a *Auth) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// sourceIP is the damper key: the request's remote IP, without the port. Behind a
// reverse proxy this is the proxy's address; per-source damping upstream is the
// proxy operator's concern, out of scope for this modest device-local mechanism.
func sourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
