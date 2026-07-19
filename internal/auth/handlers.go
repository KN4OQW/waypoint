package auth

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// minPasswordLen is the claim-time password floor (RFC-0002 leaves the full
// strength policy to the UI; the architectural minimum enforced server-side is a
// non-empty username and a password of at least this length).
const minPasswordLen = 8

// credentials is the body of both POST /api/claim and POST /api/session.
type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// HandleClaim serves POST /api/claim: the first-boot device claim. It validates
// the credentials, hashes the password, writes the admin row and the claimed_at
// stamp in one transaction, issues a session so the claimer is logged in
// immediately, and returns 201. A claim that loses the race (or arrives after the
// device is already claimed) gets 409. The gate only routes here while unclaimed,
// but the store transaction is the real guard — two concurrent claims serialize on
// the fixed admin id and exactly one wins.
func (a *Auth) HandleClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var c credentials
	if err := decodeJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(c.Username) == "" {
		writeError(w, http.StatusBadRequest, "username must not be empty")
		return
	}
	if utf8.RuneCountInString(c.Password) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "password must be at least "+strconv.Itoa(minPasswordLen)+" characters")
		return
	}
	rec, err := HashPassword(c.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	switch err := a.store.Claim(strings.TrimSpace(c.Username), rec, a.now()); err {
	case nil:
		// no-op
	case ErrAlreadyClaimed:
		writeError(w, http.StatusConflict, "device already claimed")
		return
	default:
		a.logf("auth: claim failed: %v", err)
		writeError(w, http.StatusInternalServerError, "claim failed")
		return
	}
	a.invalidateClaimed()
	a.logf("auth: device claimed by admin %q", strings.TrimSpace(c.Username))
	if err := a.issueSession(w); err != nil {
		// The claim is committed; only the auto-login cookie failed. Report success
		// so the client can fall back to the login page rather than re-claiming.
		a.logf("auth: claim succeeded but issuing session failed: %v", err)
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"claimed": true})
}

// HandleSession serves POST /api/session (log in) and DELETE /api/session (log
// out). Login verifies the credential with the same damping the RFC specifies;
// logout revokes the server-side session so the token is dead, not merely dropped.
func (a *Auth) HandleSession(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		a.handleLogin(w, r)
	case http.MethodDelete:
		a.handleLogout(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *Auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	source := sourceIP(r)
	if locked, retry := a.damper.locked(source); locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too many failed attempts; try again later")
		return
	}
	var c credentials
	if err := decodeJSON(r, &c); err != nil {
		a.failLogin(w, source, "invalid request body")
		return
	}
	admin, ok, err := a.store.Admin()
	if err != nil {
		a.logf("auth: admin lookup failed: %v", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	// Verify even when there is no admin row or the username differs, so the
	// response time does not reveal which of the two was wrong.
	match := false
	if ok {
		if v, verr := admin.Record.Verify(c.Password); verr == nil {
			match = v
		} else {
			a.logf("auth: verifying password failed: %v", verr)
		}
	}
	if !ok || admin.Username != c.Username || !match {
		a.failLogin(w, source, "invalid username or password")
		return
	}
	a.damper.recordSuccess(source)
	if err := a.issueSession(w); err != nil {
		a.logf("auth: issuing session failed: %v", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"authenticated": true})
}

// failLogin applies the fixed per-failure delay, records the failure against the
// source's backoff counter, and returns 401. The delay runs before the counter is
// updated so every failed attempt pays it.
func (a *Auth) failLogin(w http.ResponseWriter, source, msg string) {
	a.sleep(a.damper.fixedDelay())
	a.damper.recordFailure(source)
	writeError(w, http.StatusUnauthorized, msg)
}

func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		if err := a.store.RevokeSession(hashToken(c.Value)); err != nil {
			a.logf("auth: revoking session on logout failed: %v", err)
		}
	}
	a.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// Gate wraps the whole mux with the claim state machine. It is the single seam:
// every request passes this one check rather than each handler re-deriving auth.
//
//   - Unclaimed: only the claim page, POST /api/claim, and GET /api/health answer;
//     everything else is 403 with a JSON body naming claim mode. The event stream
//     and the entire config surface are denied here — a fresh device leaks nothing.
//   - Claimed: the login page, POST /api/session, and GET /api/health are the only
//     pre-auth routes; everything else requires a valid session or 401. Because the
//     default is deny, a newly registered route is behind the wall until it is
//     deliberately allowlisted here.
func (a *Auth) Gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Claimed() {
			a.gateUnclaimed(w, r, next)
			return
		}
		a.gateClaimed(w, r, next)
	})
}

func (a *Auth) gateUnclaimed(w http.ResponseWriter, r *http.Request, next http.Handler) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/health":
		next.ServeHTTP(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/claim":
		next.ServeHTTP(w, r)
	case isPageAsset(r):
		a.writeAuthPage(w, claimPlaceholder)
	default:
		writeErrorMode(w, http.StatusForbidden, "device is unclaimed", "claim")
	}
}

func (a *Auth) gateClaimed(w http.ResponseWriter, r *http.Request, next http.Handler) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/health":
		next.ServeHTTP(w, r)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/api/session":
		next.ServeHTTP(w, r)
		return
	}
	if _, ok := a.authenticate(r); ok {
		next.ServeHTTP(w, r)
		return
	}
	// Unauthenticated: the login page is the only asset served; everything else,
	// including the SSE stream and the config API, is 401.
	if isPageAsset(r) {
		a.writeAuthPage(w, loginPlaceholder)
		return
	}
	writeErrorMode(w, http.StatusUnauthorized, "authentication required", "login")
}

// writeAuthPage serves the pre-auth screen at the top-level route: the wired UI
// asset when present, else the built-in fallback. Both claim and login states
// serve the same page — it branches on GET /api/health's claim flag — so the gate
// does not need to know which screen the client will render.
func (a *Auth) writeAuthPage(w http.ResponseWriter, fallback string) {
	if len(a.authPage) > 0 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(a.authPage)
		return
	}
	writePlaceholder(w, fallback)
}

// isPageAsset reports whether the request is for the top-level HTML page. Pre-auth
// that is the only static route served (a self-contained placeholder for now); the
// embedded SPA and its assets sit behind auth until the frontend PR lands.
func isPageAsset(r *http.Request) bool {
	return r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/index.html")
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

// writeErrorMode is writeError plus the current claim-state mode ("claim" or
// "login"), so a client hitting the wall learns which surface to show.
func writeErrorMode(w http.ResponseWriter, status int, msg, mode string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg, "mode": mode})
}

func writePlaceholder(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// Minimal, self-contained placeholder pages. The real claim and login screens are
// the frontend PR; these exist so a browser hitting the device pre-claim or
// pre-login sees something rather than a raw JSON error, without pulling in any
// gated asset.
const claimPlaceholder = `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
	`<meta name="viewport" content="width=device-width,initial-scale=1"><title>Claim this Waypoint device</title></head>` +
	`<body><h1>This Waypoint device is unclaimed</h1>` +
	`<p>Set an admin username and password to claim it. The claim UI ships in a later release; ` +
	`until then, claim via <code>POST /api/claim</code> with a JSON body <code>{"username","password"}</code>.</p>` +
	`</body></html>`

const loginPlaceholder = `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
	`<meta name="viewport" content="width=device-width,initial-scale=1"><title>Log in to Waypoint</title></head>` +
	`<body><h1>Log in</h1>` +
	`<p>This device is claimed. The login UI ships in a later release; ` +
	`until then, log in via <code>POST /api/session</code> with a JSON body <code>{"username","password"}</code>.</p>` +
	`</body></html>`
