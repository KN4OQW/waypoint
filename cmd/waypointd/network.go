package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os/exec"
	"time"

	"github.com/KN4OQW/waypoint/internal/netconfig"
)

// writeJSON encodes v as the JSON response body, matching the existing handlers'
// idiom (Content-Type + streaming encode).
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// readBody reads a request body with the same 1 MiB cap the config handlers use.
func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, 1<<20))
}

// The host/OS networking domain (docs/config-coverage.md §4): the second renderer
// family. This file wires the read-only status endpoint, the netconfig view, and
// the confirm-or-revert apply engine. A network apply can strand the node, so —
// unlike the radio apply — it is guarded: apply checkpoints and arms a server-side
// rollback timer, and the change only sticks if the admin confirms before the
// deadline. See internal/netconfig.

// newNetGuard builds the confirm-or-revert Guard for network applies. The apply
// closure is the dangerous part it guards: render the store's netconfig model to
// NetworkManager keyfiles (Sync writes 0600 and prunes only waypoint-* profiles),
// then reload NM so it re-reads them. The checkpoint backend is the portable
// keyfile snapshot (KeyfileCheckpoint) — robust and unit-tested; the NM-native
// D-Bus checkpoint (NMCheckpoint) is the preferred backstop once validated on the
// bench NM version, and drops in behind the same interface without touching the
// Guard.
func (s *server) newNetGuard() *netconfig.Guard {
	cp := netconfig.NewKeyfileCheckpoint(s.netKeyfileDir, netRun)
	apply := func(m netconfig.Model) error {
		changed, err := m.Sync(s.netKeyfileDir)
		if err != nil {
			return err
		}
		if changed {
			if _, err := netRun("nmcli", "connection", "reload"); err != nil {
				return err
			}
		}
		return nil
	}
	return netconfig.NewGuard(cp, apply)
}

// netRun is the production status/apply command runner: it executes the command
// and returns combined output. Shelling out to nmcli/timedatectl/busctl matches how
// the rest of Waypoint drives the host (systemctl restarts, i2cdetect).
func netRun(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// networkStatus serves GET /api/network/status: the live host-network state
// (interfaces, IPv4, DNS, Wi-Fi, NTP), parsed from nmcli + timedatectl. Read-only
// and independent of the store — it is what the box is actually doing. This is the
// value the Network tab ships immediately.
func (s *server) networkStatus(w http.ResponseWriter, _ *http.Request) {
	st, err := netconfig.Collect(netconfig.ExecRunner)
	if err != nil {
		http.Error(w, "network status: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, st)
}

// networkConfig serves the netconfig read/write surface:
//
//	GET  /api/network/config  → the redacted View (PSKs never serialized) plus any
//	                            in-flight confirm deadline, so the UI can resume a
//	                            "Keep these settings?" countdown after a reload.
//	PUT  /api/network/config  → merge a partial body into the stored model (Set),
//	                            preserving secrets on blank. This is the store
//	                            write; it does NOT apply — POST /api/network/apply
//	                            renders and guards.
//
// The Wi-Fi/VLAN edit UI that drives PUT lands in the next slice; the plumbing is
// here now so secrets are handled correctly from the first surface.
func (s *server) networkConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		m, err := netconfig.Load(s.store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp := struct {
			netconfig.View
			PendingConfirm *pendingConfirm `json:"pending_confirm,omitempty"`
		}{View: m.View()}
		if deadline, ok := s.netGuard.PendingStatus(); ok {
			resp.PendingConfirm = &pendingConfirm{Deadline: deadline.UTC().Format(time.RFC3339)}
		}
		writeJSON(w, resp)
	case http.MethodPut:
		body, err := readBody(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := netconfig.Set(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// pendingConfirm is the in-flight apply's deadline, surfaced to the UI so it can
// render the countdown without holding the confirm token (the token is returned
// only to the caller that applied).
type pendingConfirm struct {
	Deadline string `json:"deadline"` // RFC3339
}

// networkApply serves POST /api/network/apply: checkpoint the current network
// state, render+apply the stored netconfig model, and hand back a confirm token
// and deadline. If the admin does not POST /api/network/confirm before the
// deadline, a server-side timer rolls the change back — so a mistake that severs
// the admin's own connection self-heals without any surviving HTTP session.
func (s *server) networkApply(w http.ResponseWriter, _ *http.Request) {
	m, err := netconfig.Load(s.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := m.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token, deadline, err := s.netGuard.Apply(m, s.netConfirmTimeout)
	if err != nil {
		// A pending apply is a conflict, not a server error.
		code := http.StatusInternalServerError
		if err == netconfig.ErrApplyPending {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	_ = s.store.RecordApply("api", map[string]any{"domain": "network", "confirm_deadline": deadline.UTC().Format(time.RFC3339)})
	log.Printf("network apply staged; awaiting confirm by %s", deadline.UTC().Format(time.RFC3339))
	writeJSON(w, map[string]any{
		"applied":  true,
		"token":    token,
		"deadline": deadline.UTC().Format(time.RFC3339),
	})
}

// networkConfirm serves POST /api/network/confirm: the admin confirms the node is
// still reachable, so the checkpoint is destroyed and the change is made
// permanent. Body: {"token":"…"}.
func (s *server) networkConfirm(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.netGuard.Confirm(req.Token); err != nil {
		code := http.StatusBadRequest
		if err == netconfig.ErrNoPendingApply {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	log.Printf("network apply confirmed; change is permanent")
	writeJSON(w, map[string]any{"confirmed": true})
}
