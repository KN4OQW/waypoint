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
		writePlaceholder(w, claimPlaceholder)
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
		writePlaceholder(w, loginPlaceholder)
		return
	}
	writeErrorMode(w, http.StatusUnauthorized, "authentication required", "login")
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

// The first-run claim and login screens (RFC-0009). Served pre-auth by the gate,
// so they are fully self-contained — no gated asset, no CDN — and phone-first
// (a device is set up next to the radio, usually on a phone). They post JSON to
// the existing /api/claim and /api/session and redirect to / on success. Dark is
// the default; they honour the operator's saved theme/mode via localStorage so a
// returning login matches the app.
var claimPlaceholder = authScreen(
	"claim", "Claim this device",
	"Set an admin username and password. This is the only account until you add more — there are no default credentials.",
	"/api/claim", "Claim device", true)

var loginPlaceholder = authScreen(
	"login", "Log in",
	"Enter your admin credentials to manage this Waypoint node.",
	"/api/session", "Log in", false)

// authScreen builds a self-contained first-run page. withConfirm adds a
// confirm-password field and the 8-char client-side check (the API enforces the
// floor too); endpoint is where the form POSTs.
func authScreen(kind, heading, sub, endpoint, submit string, withConfirm bool) string {
	confirmField := ""
	confirmJS := ""
	if withConfirm {
		confirmField = `<label>Confirm password<input id="confirm" type="password" autocomplete="new-password" required minlength="8"></label>`
		confirmJS = `if(p!==document.getElementById('confirm').value){return show('Passwords do not match');}
      if(p.length<8){return show('Password must be at least 8 characters');}`
	}
	pwAutocomplete := "current-password"
	if withConfirm {
		pwAutocomplete = "new-password"
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Waypoint · ` + heading + `</title>
<script>(function(){try{var m=localStorage.getItem("wp-mode");if(m===null&&window.matchMedia&&matchMedia("(prefers-color-scheme: light)").matches)m="light";if(m==="light")document.documentElement.setAttribute("data-mode","light");var t=localStorage.getItem("wp-theme");if(t&&t!=="phosphor")document.documentElement.setAttribute("data-theme",t);}catch(e){}})();</script>
<style>
  :root{--accent:#35d07f;--accent-soft:rgba(53,208,127,.13);--bg:#06070a;--panel:#0f1218;--panel-line:#1c222c;--field:#0a0d12;--field-line:#262c38;--ink:#e4ebf4;--ink-head:#eef2f7;--muted:#8a94a6;--bad:#ff6b6b;--mono:ui-monospace,"SF Mono",Consolas,Menlo,monospace;--sans:system-ui,-apple-system,"Segoe UI",Arial,sans-serif;}
  :root[data-theme="amber"]{--accent:#f0a935;--accent-soft:rgba(240,169,53,.13);}
  :root[data-theme="ice"]{--accent:#4db8ff;--accent-soft:rgba(77,184,255,.13);}
  :root[data-mode="light"]{--accent:#12a35a;--accent-soft:rgba(18,163,90,.12);--bg:#eef1f6;--panel:#fff;--panel-line:#dde3ec;--field:#f5f7fb;--field-line:#ccd4e0;--ink:#1a2130;--ink-head:#0e1420;--muted:#566072;--bad:#cc3333;}
  :root[data-mode="light"][data-theme="amber"]{--accent:#9a5d05;--accent-soft:rgba(154,93,5,.12);}
  :root[data-mode="light"][data-theme="ice"]{--accent:#1f77c9;--accent-soft:rgba(31,119,201,.12);}
  *{box-sizing:border-box;}html,body{margin:0;padding:0;}
  body{background:radial-gradient(ellipse 90% 60% at 78% -10%,var(--accent-soft),transparent 55%),var(--bg);color:var(--ink);font-family:var(--sans);min-height:100vh;display:flex;align-items:center;justify-content:center;padding:20px;}
  .card{width:100%;max-width:380px;background:var(--panel);border:1px solid var(--panel-line);border-radius:14px;padding:26px 24px 28px;}
  .brand{display:flex;align-items:center;gap:11px;margin-bottom:20px;}
  .brand svg{flex:none;}
  .brand b{font-size:15px;font-weight:700;letter-spacing:2.5px;color:var(--ink-head);}
  h1{margin:0 0 6px;font-size:22px;font-weight:600;color:var(--ink-head);}
  .sub{margin:0 0 20px;font-size:13.5px;line-height:1.5;color:var(--muted);}
  label{display:block;font-family:var(--mono);font-size:10px;letter-spacing:1.5px;color:var(--muted);text-transform:uppercase;margin-bottom:14px;}
  input{display:block;width:100%;margin-top:6px;background:var(--field);border:1px solid var(--field-line);border-radius:8px;padding:12px;color:var(--ink);font-size:15px;font-family:var(--mono);min-height:44px;}
  input:focus{outline:none;border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-soft);}
  button{width:100%;margin-top:8px;background:var(--accent);color:#04120a;border:0;border-radius:8px;padding:13px;font-size:14px;font-weight:600;font-family:var(--mono);letter-spacing:.5px;cursor:pointer;min-height:48px;}
  button:focus-visible{outline:2px solid var(--ink-head);outline-offset:2px;}
  .err{margin:0 0 14px;color:var(--bad);font-size:13px;font-family:var(--mono);}
  a{color:var(--accent);}
</style></head>
<body>
  <form id="f" class="card" autocomplete="on">
    <div class="brand"><svg width="24" height="24" viewBox="0 0 512 512" fill="var(--accent)" aria-hidden="true"><path d="M256 474 C201 410 138 362 138 284 A118 118 0 0 1 374 284 C374 362 311 410 256 474 Z"></path></svg><b>WAYPOINT</b></div>
    <h1>` + heading + `</h1>
    <p class="sub">` + sub + `</p>
    <p class="err" id="err" role="alert" hidden></p>
    <label>Username<input id="username" name="username" autocomplete="username" required autofocus></label>
    <label>Password<input id="password" name="password" type="password" autocomplete="` + pwAutocomplete + `" required></label>
    ` + confirmField + `
    <button type="submit">` + submit + `</button>
  </form>
<script>
  var f=document.getElementById("f"),err=document.getElementById("err");
  function show(m){err.textContent=m;err.hidden=false;return false;}
  f.addEventListener("submit",function(e){
    e.preventDefault();err.hidden=true;
    var u=document.getElementById("username").value.trim(),p=document.getElementById("password").value;
    if(!u)return show("Enter a username");
    ` + confirmJS + `
    fetch("` + endpoint + `",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({username:u,password:p})})
      .then(function(r){if(r.ok){location.href="/";return;}return r.json().then(function(j){show((j&&j.error)||"Request failed");});})
      .catch(function(){show("Network error — check the connection and try again");});
  });
</script>
</body></html>`
}
