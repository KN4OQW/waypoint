package wizard

import (
	_ "embed"
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
	mux.HandleFunc(Prefix+"prior-admins", w.handlePriorAdmins)
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

// handlePriorAdmins lists prior administrator accounts (GET) and removes the ones
// the operator ticked (POST).
func (w *Wizard) handlePriorAdmins(rw http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		admins, err := w.PriorAdmins(r.Context())
		if err != nil {
			status, msg := statusFor(err)
			writeJSON(rw, status, map[string]any{"error": msg, "mode": "setup"})
			return
		}
		writeJSON(rw, http.StatusOK, map[string]any{
			"admins": admins,
			// The copy lives with the data so a UI cannot render the list without
			// the warning that gives it meaning.
			"notice": priorAdminNotice,
		})
	case http.MethodPost:
		var req RemovePriorAdminsRequest
		if !decode(rw, r, &req) {
			return
		}
		outcomes, err := w.RemovePriorAdmins(r.Context(), req)
		if err != nil {
			status, msg := statusFor(err)
			writeJSON(rw, status, map[string]any{"error": msg, "mode": "setup"})
			return
		}
		// Always 200: individual failures are in the outcomes, and a non-2xx would
		// invite a client to treat one stubborn account as a failed setup step.
		writeJSON(rw, http.StatusOK, map[string]any{"outcomes": outcomes})
	default:
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// priorAdminNotice is what the operator is told above the list. It says what
// these accounts are and what they can do, because "previous-owner" on its own
// reads like a stale entry rather than root-equivalent access to their node.
const priorAdminNotice = "These accounts already had administrator (sudo) access to this node before you " +
	"set it up. On second-hand hardware they are the previous owner's, and any of them with an SSH key " +
	"can log in without a password. Removing them is optional and nothing is removed unless you tick it. " +
	"If this board came from someone else, reflashing the card is the only way to be sure of what is on it."

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
			writeSetupPage(rw)
		case r.Method == http.MethodGet && (r.URL.Path == "/logo.svg" || r.URL.Path == "/logo-mono.svg"):
			// Served from the dashboard's assets behind the gate: a page that
			// renders with a broken image looks like a broken node.
			next.ServeHTTP(rw, r)
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

// isPageAsset reports whether the request is for the setup page itself or an
// asset it references. The logo is included because a page that renders with a
// broken image looks like a broken node.
func isPageAsset(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/", "/index.html", "/setup", "/setup.html":
		return true
	}
	return false
}

//go:embed setup.html
var setupPage []byte

// writeSetupPage serves the wizard.
//
// It is one self-contained file with no build step and no external request. This
// page is delivered over the setup access point to a phone with no route to the
// internet, frequently inside a captive-portal sheet — so a CDN font or a
// bundled framework is not a style preference, it is a page that does not load.
func writeSetupPage(rw http.ResponseWriter) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write(setupPage)
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
