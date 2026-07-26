package wizard

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/KN4OQW/waypoint/internal/privhelper"
)

// Prefix is the wizard's URL space. Everything under it is pre-authentication —
// there is no account to authenticate against yet — which is why the gate below
// serves it and nothing else.
const Prefix = "/api/setup/"

// maxBody bounds a setup request. The largest legitimate one carries an SSH
// public key.
const maxBody = 64 << 10

// Handler serves the wizard's routes.
func (w *Wizard) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(Prefix+"state", w.handleState)
	mux.HandleFunc(Prefix+"hostname", post(w.handleHostname))
	mux.HandleFunc(Prefix+"user", post(w.handleUser))
	mux.HandleFunc(Prefix+"key", post(w.handleKey))
	mux.HandleFunc(Prefix+"lock", post(w.handleLock))
	mux.HandleFunc(Prefix+"network", post(w.handleJoin))
	return mux
}

// post rejects anything but POST, so a link-prefetching browser cannot create an
// account by following a URL.
func post(h http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h(rw, r)
	}
}

func (w *Wizard) handleState(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(rw, http.StatusOK, w.State())
}

func (w *Wizard) handleHostname(rw http.ResponseWriter, r *http.Request) {
	var req HostnameRequest
	if !decode(rw, r, &req) {
		return
	}
	view, err := w.SetHostname(r.Context(), req)
	w.respond(rw, view, err)
}

func (w *Wizard) handleUser(rw http.ResponseWriter, r *http.Request) {
	var req UserRequest
	if !decode(rw, r, &req) {
		return
	}
	view, err := w.CreateUser(r.Context(), req)
	w.respond(rw, view, err)
}

func (w *Wizard) handleKey(rw http.ResponseWriter, r *http.Request) {
	var req KeyRequest
	if !decode(rw, r, &req) {
		return
	}
	view, err := w.InstallKey(r.Context(), req)
	w.respond(rw, view, err)
}

func (w *Wizard) handleLock(rw http.ResponseWriter, r *http.Request) {
	var req LockRequest
	if !decode(rw, r, &req) {
		return
	}
	view, err := w.Lock(r.Context(), req)
	w.respond(rw, view, err)
}

func (w *Wizard) handleJoin(rw http.ResponseWriter, r *http.Request) {
	var req JoinRequest
	if !decode(rw, r, &req) {
		return
	}
	got, err := w.JoinNetwork(r.Context(), req)
	if err != nil {
		status, msg := statusFor(err)
		writeJSON(rw, status, map[string]any{"error": msg, "mode": got.View.Mode, "state": got.View})
		return
	}
	writeJSON(rw, http.StatusOK, got)
}

// respond writes the step's outcome. Success and failure both carry the current
// view, so a client that mis-stepped gets told where it actually is in one round
// trip rather than having to ask again.
func (w *Wizard) respond(rw http.ResponseWriter, view View, err error) {
	if err == nil {
		writeJSON(rw, http.StatusOK, view)
		return
	}
	status, msg := statusFor(err)
	body := map[string]any{
		"error": msg,
		"mode":  view.Mode,
		"state": view,
	}
	var order *ErrOutOfOrder
	if errors.As(err, &order) {
		body["expected_step"] = order.Expected
	}
	writeJSON(rw, status, body)
}

// statusFor maps a helper error onto an HTTP status.
//
// The distinction that matters to an operator is 400 from 503: "what you typed is
// wrong, fix it" against "the helper is not reachable, this is not your fault".
// Flattening the helper's codes into 500 would lose exactly that.
func statusFor(err error) (int, string) {
	msg := err.Error()
	// The privhelper prefix is for logs, not for a person reading a form error.
	msg = strings.TrimPrefix(msg, "privhelper: ")
	if i := strings.Index(msg, ": "); i > 0 && strings.HasPrefix(msg, string(privhelper.CodeOf(err))) {
		msg = msg[i+2:]
	}

	switch privhelper.CodeOf(err) {
	case privhelper.CodeInvalidArgument:
		return http.StatusBadRequest, msg
	case privhelper.CodeConflict:
		return http.StatusConflict, msg
	case privhelper.CodeNotFound:
		return http.StatusNotFound, msg
	case privhelper.CodeDenied:
		return http.StatusForbidden, msg
	case privhelper.CodeUnsupported:
		// The helper is not running, or this node cannot do the thing. Either way
		// it is a service-side condition and retrying the same input may work.
		return http.StatusServiceUnavailable, msg
	}
	var order *ErrOutOfOrder
	if errors.As(err, &order) {
		return http.StatusConflict, msg
	}
	return http.StatusInternalServerError, msg
}

// Gate serves the wizard instead of the dashboard until the node is provisioned.
//
// It sits outside the claim gate: before setup is finished there is no account to
// authenticate and nothing worth showing, so the claim gate never sees a request.
// Once the marker is written this becomes a pass-through, and the claim gate takes
// over — the two never both hold an opinion about the same request.
func (w *Wizard) Gate(next http.Handler) http.Handler {
	handler := w.Handler()
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// The wizard serves its own routes in every state, before the gate below
		// decides anything. They are pre-authentication by definition — there is
		// no account to authenticate against until setup has created one — so
		// routing them through the claim gate would only mean teaching that gate
		// about setup. Once provisioned, State reports the mode and nothing else
		// (see state), so an unauthenticated caller learns no more than which
		// surface to show.
		if strings.HasPrefix(r.URL.Path, Prefix) {
			handler.ServeHTTP(rw, r)
			return
		}
		if w.Provisioned() {
			next.ServeHTTP(rw, r)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/health":
			// Health stays reachable in every state; it is how anything outside
			// the box tells a node that is starting up from one that is wedged.
			next.ServeHTTP(rw, r)
		case isPageAsset(r):
			writePlaceholder(rw, w.State())
		default:
			// Everything else — the config API, the event stream, the claim
			// endpoint — is closed. An unprovisioned node has no hostname the
			// operator chose and an unlocked root; there is nothing here to claim
			// yet.
			writeJSON(rw, http.StatusForbidden, map[string]any{
				"error": "this node has not been set up yet",
				"mode":  "setup",
				"next":  w.Next(),
			})
		}
	})
}

func isPageAsset(r *http.Request) bool {
	return r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/index.html")
}

// writePlaceholder serves a self-contained setup page.
//
// It is deliberately dependency-free: this page is served over the setup access
// point to a phone that has no route to the internet, so every byte it needs has
// to be in the response. The real wizard UI replaces it when the frontend lands;
// until then this is enough to see the flow and to make the gate observable in a
// browser.
func writePlaceholder(rw http.ResponseWriter, v View) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte(`<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Waypoint &mdash; set up this node</title>
<style>
  body{font:16px/1.6 system-ui,sans-serif;max-width:34rem;margin:2.5rem auto;padding:0 1rem}
  h1{font-size:1.4rem}
  h2{font-size:1.05rem;margin-top:2rem}
  code{background:#8881;padding:.1em .35em;border-radius:.25em}
  .warn{border-left:3px solid #c60;background:#c6601a;background:color-mix(in srgb,#c60 12%, transparent);padding:.75rem 1rem;border-radius:.25rem}
  .rec{border-left:3px solid #2a7;background:color-mix(in srgb,#2a7 12%, transparent);padding:.75rem 1rem;border-radius:.25rem}
  ol{padding-left:1.2rem}
</style>
<h1>Set up this Waypoint node</h1>
<p>This node has not been set up yet. The next step is <code>` + string(v.Next) + `</code>.</p>

<div class="warn">
  <strong>This setup network is open.</strong> Anyone within radio range can reach
  this page while it is up, so treat anything you type here as visible to them.
  The access point comes down as soon as this node joins your network, and it is
  only up for the first half hour after boot.
  <br><br>
  To protect it instead, power the node off, put the card in a reader, and create
  <code>waypoint-setup.txt</code> on the boot partition containing
  <code>psk=your-passphrase</code> (8&ndash;63 characters).
</div>

<h2>The recovery account</h2>
<p>Setup creates one non-root account. It is how you get back into this node if
you lose your Waypoint password, so it is worth getting right.</p>

<div class="rec">
  <strong>Recommended: an SSH public key.</strong> Paste the contents of your
  <code>~/.ssh/id_ed25519.pub</code>. A key you already hold cannot be forgotten,
  and &mdash; on this open network &mdash; a public key is not a secret, so
  sending it over the setup access point gives nothing away.
</div>

<p><strong>Fallback: a password.</strong> Use one if you have no key to hand. Be
aware that it travels over this open network in the clear, so choose one you do
not use anywhere else, and change it once the node is on your own Wi-Fi.</p>

<p>You can set both. Whichever you choose, root's password is locked at the end
of setup, and this account is the only way back in.</p>

<h2>What happens next</h2>
<ol>
  <li>Name this node &mdash; the shipped default is refused, so two Waypoints on
      one network do not answer to the same address.</li>
  <li>Create the recovery account.</li>
  <li>Add its SSH key, or skip if you set a password.</li>
  <li>Lock root and finish. The dashboard opens after that, for you to claim.</li>
</ol>
<p>Setup runs over <code>` + Prefix + `</code>; the dashboard is not served until
it is finished.</p>
`))
}

func decode(rw http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(rw, r.Body, maxBody))
	// A field the wizard does not understand means the client is asking for
	// something this build will not do, and creating an account while ignoring
	// half the request is the worse outcome.
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(rw, http.StatusBadRequest, "malformed request: "+err.Error())
		return false
	}
	return true
}

func writeJSON(rw http.ResponseWriter, status int, body any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(body)
}

func writeError(rw http.ResponseWriter, status int, msg string) {
	writeJSON(rw, status, map[string]any{"error": msg, "mode": "setup"})
}
