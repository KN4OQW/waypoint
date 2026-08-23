package auth

import (
	"encoding/json"
	"errors"
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
	// Look the account up rather than having Claim return it: the claim is already
	// committed, and a failure here costs the auto-login cookie, not the claim.
	acct, ok, lerr := a.store.AccountByUsername(strings.TrimSpace(c.Username))
	if lerr != nil || !ok {
		a.logf("auth: claim succeeded but the new account could not be read back: %v", lerr)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"claimed": true})
		return
	}
	if err := a.issueSession(w, acct.ID); err != nil {
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
	acct, found, err := a.store.AccountByUsername(strings.TrimSpace(c.Username))
	if err != nil {
		a.logf("auth: account lookup failed: %v", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	// Verify against SOMETHING every time, even when the username does not exist.
	//
	// With one fixed admin this mattered little — there was only ever one username
	// and an attacker already knew it. With several accounts, skipping the argon2
	// work on an unknown username makes the response measurably faster for names
	// that do not exist, which is username enumeration: an attacker learns who has
	// a login on the node before guessing a single password. So a miss verifies
	// against a decoy record with the same cost parameters and discards the result.
	record := acct.Record
	if !found {
		record = decoyRecord()
	}
	match := false
	if v, verr := record.Verify(c.Password); verr == nil {
		match = v
	} else if found {
		a.logf("auth: verifying password failed: %v", verr)
	}
	if !found || !match {
		a.failLogin(w, source, "invalid username or password")
		return
	}
	a.damper.recordSuccess(source)
	if err := a.issueSession(w, acct.ID); err != nil {
		a.logf("auth: issuing session failed: %v", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// must_rotate is reported so the client can go straight to the change-password
	// screen. It is not a permission — the server refuses every other route while
	// the flag is set, whatever the client does with this.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"authenticated": true,
		"role":          string(acct.Role),
		"must_rotate":   acct.MustRotate,
	})
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
		// The opt-in public surface is the one route group that is anonymous by
		// design, so it is let through here rather than being taught to satisfy the
		// wall. This is not a hole: what it passes to still decides for itself
		// whether to answer at all, and answers 404 unless the operator turned the
		// feature on. Letting it past a gate that would otherwise 403 an unclaimed
		// node also keeps the two states consistent — a public page must not appear
		// and disappear depending on whether the owner has claimed the box.
		if a.allowAnon != nil && a.allowAnon(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !a.Claimed() {
			a.gateUnclaimed(w, r, next)
			return
		}
		a.gateClaimed(w, r, next)
	})
}

// AllowAnonymous registers the predicate that names the routes exempt from the
// session wall. It exists so the exemption is declared by the subsystem that owns
// those routes rather than hardcoded here — this package should not have to know
// what a public dashboard is — while still being wired in exactly one place.
//
// Passing nil, or never calling this, leaves the gate default-deny as before.
func (a *Auth) AllowAnonymous(fn func(*http.Request) bool) { a.allowAnon = fn }

// HasSession reports whether a request carries a valid session.
//
// It exists for the one route whose content depends on being signed in rather
// than on being allowed through: with the public view enabled, "/" serves the
// public page to a stranger and the dashboard to a keeper (D7). Everything else
// is decided by the gate, and no handler should be re-deriving auth — this is a
// read of the same session the gate reads, not a second way to be authorized.
func (a *Auth) HasSession(r *http.Request) bool {
	_, ok := a.authenticate(r)
	return ok
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
	if sess, ok := a.authenticate(r); ok {
		// Authenticated, but the account may still be carrying an admin-chosen
		// password. Forced rotation refuses every route but the password change,
		// and roles.go does that for the API; what it cannot do is decide what the
		// PAGE is, because "/" is not an /api/ path. So the page is decided here,
		// beside the claim and login screens it is a third case of: a caller who is
		// past the wall but not yet free of it gets a self-contained screen rather
		// than the dashboard shell they cannot use.
		if a.mustRotate(sess) && isPageAsset(r) {
			writePlaceholder(w, rotatePlaceholder)
			return
		}
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

// mustRotate reports whether the session's owner still owes a password rotation.
//
// A session with no owner (a pre-amendment row the migration could not attribute,
// or an account deleted mid-flight) is NOT treated as owing one: the rotation
// screen would be a dead end for a caller who has nothing to rotate. Those cases
// are already handled downstream — roles.go answers 401 when it cannot resolve the
// caller — and duplicating that judgement here would give two answers to one
// question.
func (a *Auth) mustRotate(sess Session) bool {
	if sess.AccountID == 0 {
		return false
	}
	acct, err := a.store.AccountByID(sess.AccountID)
	if err != nil {
		if !errors.Is(err, ErrNoSuchAccount) {
			a.logf("auth: resolving session owner for rotation check failed: %v", err)
		}
		return false
	}
	return acct.MustRotate
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

// The login screen carries the lockout instructions, and it is the only place
// they are any use: a forgotten password is discovered here, by someone who
// cannot reach the settings, the docs directory or anything else this node
// serves. A link to a wiki they cannot open on a node with no internet is not
// an answer either, so the two routes are spelled out inline.
//
// Publishing them costs nothing. Both require something an attacker on the
// network does not have — a shell on the box, or the SD card in their hand — so
// the only person this helps is the one holding the hardware (RFC-0002).
var loginPlaceholder = authScreen(
	"login", "Log in",
	"Enter your admin credentials to manage this Waypoint node.",
	"/api/session", "Log in", false)

// The forced-rotation screen (RFC-0002 Amendment 1).
//
// An admin who creates an account chooses its first password, so for a moment two
// people know it. must_rotate is what makes that moment brief: until the account
// rotates, the gate serves this instead of the dashboard and roles.go refuses
// every route but /api/password.
//
// It asks for the current password even though the caller is already
// authenticated. That is not belt-and-braces — a session left open on an
// unattended screen must not be enough to take an account over, and the rotation
// flag does not change that.
//
// There is no username field: the account is whoever the session says it is, and
// offering to change somebody else's password from here would be a different
// route with a different permission.
var rotatePlaceholder = rotateScreen()

func rotateScreen() string {
	return authHead("Set a new password") + `
<body>
  <form id="f" class="card" autocomplete="on">
    <div class="brand"><svg width="24" height="24" viewBox="0 0 512 512" fill="var(--accent)" aria-hidden="true"><path d="M256 474 C201 410 138 362 138 284 A118 118 0 0 1 374 284 C374 362 311 410 256 474 Z"></path></svg><b>WAYPOINT</b></div>
    <h1>Set a new password</h1>
    <p class="sub">This account was created with a password somebody else chose. Set your own to finish signing in — nothing else on this node will open until you do.</p>
    <p class="err" id="err" role="alert" hidden></p>
    <label>Current password<input id="current" type="password" autocomplete="current-password" required autofocus></label>
    <label>New password<input id="password" type="password" autocomplete="new-password" required minlength="8"></label>
    <label>Confirm new password<input id="confirm" type="password" autocomplete="new-password" required minlength="8"></label>
    <button type="submit">Set password</button>
  </form>
<script>
  var f=document.getElementById("f"),err=document.getElementById("err");
  function show(m){err.textContent=m;err.hidden=false;return false;}
  f.addEventListener("submit",function(e){
    e.preventDefault();err.hidden=true;
    var c=document.getElementById("current").value,p=document.getElementById("password").value;
    if(p!==document.getElementById("confirm").value){return show("Passwords do not match");}
    if(p.length<8){return show("Password must be at least 8 characters");}
    if(p===c){return show("The new password must be different from the current one");}
    fetch("/api/password",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({current_password:c,new_password:p})})
      .then(function(r){if(r.ok){location.href="/";return;}return r.json().then(function(j){show((j&&j.error)||"Request failed");});})
      .catch(function(){show("Network error — check the connection and try again");});
  });
</script>
</body></html>`
}

// lockoutHelp is the "forgotten it?" disclosure, on the login screen only. The
// claim screen does not need it: there is no password to have forgotten yet.
func lockoutHelp(kind string) string {
	if kind != "login" {
		return ""
	}
	return `<details class="help">
      <summary>Forgotten your password?</summary>
      <p>It cannot be recovered — it is stored only as a hash. You can take the node
      back if you have the hardware:</p>
      <p><b>With an SSH shell on this node:</b></p>
      <pre>sudo waypointd reset-claim</pre>
      <p>The dashboard returns to its claim screen within a few seconds and you set a
      new username and password. Nothing else changes — the node stays on the air.</p>
      <p><b>With only the SD card:</b> power the node down, put the card in a reader,
      and create an empty file named <code>waypoint-reset</code> on the small boot
      partition (the one holding <code>config.txt</code>). On the next boot the node
      resets fully and runs first-boot setup again.</p>
      <p>Full instructions: <code>docs/recovery.md</code> in the Waypoint repository,
      or the <b>Regaining access</b> page on the project wiki.</p>
    </details>`
}

// authScreenHead is the shared <head> of every pre-auth screen: the theme-restore
// script and the whole stylesheet. It is one constant rather than three copies so
// the claim, login and rotation screens cannot drift apart visually — they are the
// same surface seen at three moments, and an operator who meets two of them in one
// sitting should not be able to tell they were built separately.
//
// The literal {{TITLE}} is the page title, substituted by authHead. It is a token
// rather than a Sprintf verb because this stylesheet is full of percent signs
// (gradients, widths, viewport units) and every one of them would be a format
// verb — a class of bug that shows up as a mangled page, not a compile error.
const authScreenHead = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Waypoint · {{TITLE}}</title>
<script>(function(){try{var m=localStorage.getItem("wp-mode");if(m===null&&window.matchMedia&&matchMedia("(prefers-color-scheme: light)").matches)m="light";if(m==="light")document.documentElement.setAttribute("data-mode","light");var t=localStorage.getItem("wp-theme");if(t&&t!=="phosphor")document.documentElement.setAttribute("data-theme",t);}catch(e){}})();</script>
<style>
  :root{--accent:#35d07f;--accent-soft:rgba(53,208,127,.13);--bg:#06070a;--panel:#0f1218;--panel-line:#1c222c;--field:#0a0d12;--field-line:#262c38;--ink:#e4ebf4;--ink-head:#eef2f7;--muted:#8a94a6;--bad:#ff6b6b;--mono:ui-monospace,"SF Mono",Consolas,Menlo,monospace;--sans:system-ui,-apple-system,"Segoe UI",Arial,sans-serif;}
  :root[data-theme="amber"]{--accent:#f0a935;--accent-soft:rgba(240,169,53,.13);}
  :root[data-theme="ice"]{--accent:#4db8ff;--accent-soft:rgba(77,184,255,.13);}
  /* Light mode. These accents are the CORRECTED ones from ui/static/settings.html,
     not the values this file shipped with: #12a35a was 3.27:1 on white and
     #1f77c9 was 4.09:1 on --bg, both below AA. The app fixed them (#121); these
     screens kept the old palette because nothing measured them — the axe harness
     authenticates before it scans, so it never saw the claim or login page. The
     rotation screen is the first pre-auth screen it does see, and it failed on
     exactly this. Keep the three in step with settings.html. */
  :root[data-mode="light"]{--accent:#0e7c45;--accent-soft:rgba(14,124,69,.12);--bg:#eef1f6;--panel:#fff;--panel-line:#dde3ec;--field:#f5f7fb;--field-line:#ccd4e0;--ink:#1a2130;--ink-head:#0e1420;--muted:#566072;--bad:#cc3333;}
  :root[data-mode="light"][data-theme="amber"]{--accent:#9a5d05;--accent-soft:rgba(154,93,5,.12);}
  :root[data-mode="light"][data-theme="ice"]{--accent:#1d6eba;--accent-soft:rgba(29,110,186,.12);}
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
  /* color:--bg, not a fixed near-black. The accent is bright in dark mode and dark
     in light mode, so a single hardcoded ink can only pass in one of them; --bg is
     the surface the accent is already sized against, which is what .btn.primary in
     the app does and for the same reason. */
  button{width:100%;margin-top:8px;background:var(--accent);color:var(--bg);border:0;border-radius:8px;padding:13px;font-size:14px;font-weight:600;font-family:var(--mono);letter-spacing:.5px;cursor:pointer;min-height:48px;}
  button:focus-visible{outline:2px solid var(--ink-head);outline-offset:2px;}
  .err{margin:0 0 14px;color:var(--bad);font-size:13px;font-family:var(--mono);}
  a{color:var(--accent);}
  .help{margin-top:20px;border-top:1px solid var(--panel-line);padding-top:14px;font-size:13px;line-height:1.55;color:var(--muted);}
  .help summary{cursor:pointer;font-family:var(--mono);font-size:11px;letter-spacing:1.2px;text-transform:uppercase;color:var(--accent);min-height:32px;display:flex;align-items:center;}
  .help summary:focus-visible{outline:2px solid var(--ink-head);outline-offset:2px;}
  .help p{margin:10px 0;}
  .help b{color:var(--ink);font-weight:600;}
  .help pre,.help code{font-family:var(--mono);font-size:12px;color:var(--ink);background:var(--field);border:1px solid var(--field-line);border-radius:6px;}
  .help pre{padding:9px 10px;margin:8px 0;overflow-x:auto;}
  .help code{padding:1px 5px;}
</style></head>
`

// authHead renders the shared head with a page title.
func authHead(title string) string {
	return strings.Replace(authScreenHead, "{{TITLE}}", title, 1)
}

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
	return authHead(heading) + `
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
    ` + lockoutHelp(kind) + `
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
