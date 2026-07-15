package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
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

// netActivateWait bounds each `nmcli connection up` so a change that severs the
// link cannot hang the apply handler for nmcli's default 90s — the guard's
// rollback is what recovers, not a long block here.
const netActivateWait = "25"

// newNetGuard builds the confirm-or-revert Guard for network applies. The apply
// closure is the dangerous part it guards: render the store's netconfig model to
// NetworkManager keyfiles (Sync writes 0600 and prunes only waypoint-* profiles),
// reload NM, then ACTIVATE each managed profile so the change reaches the live
// device (not just the keyfile on disk).
//
// The checkpoint backend selects how a rollback un-does that:
//   - "composite" (default): NM-native checkpoint (restores live device/connection
//     state — the only thing that un-strands a node whose link was just cut, plus
//     NM's own rollback-timeout backstop if waypointd dies) composed with the
//     keyfile snapshot (keeps on-disk profiles consistent with the reverted state).
//   - "keyfile": the portable keyfile-only snapshot, for environments without NM
//     D-Bus checkpoint support — restores the files + reload but cannot re-activate
//     a live device, so its protection is weaker (documented).
func (s *server) newNetGuard() *netconfig.Guard {
	keyfileCP := netconfig.NewKeyfileCheckpoint(s.netKeyfileDir, netRun)
	var cp netconfig.Checkpoint = keyfileCP
	if s.netBackend != "keyfile" {
		// NM first so a rollback restores the live device before the keyfile layer
		// reconciles disk.
		cp = netconfig.NewCompositeCheckpoint(netconfig.NewNMCheckpoint(netRun), keyfileCP)
	}
	apply := func(m netconfig.Model) error {
		if _, err := m.Sync(s.netKeyfileDir); err != nil {
			return err
		}
		if _, err := netRun("nmcli", "connection", "reload"); err != nil {
			return err
		}
		// Activate each managed profile so the saved change takes effect now. A
		// bounded wait keeps a link-severing change from hanging the handler.
		for _, id := range m.ProfileIDs() {
			if out, err := netRun("nmcli", "-w", netActivateWait, "connection", "up", id); err != nil {
				return fmt.Errorf("activate %s: %v: %s", id, err, strings.TrimSpace(out))
			}
		}
		return nil
	}
	g := netconfig.NewGuard(cp, apply)
	g.SetLogger(log.Printf) // make the server-side auto-rollback visible in the journal
	return g
}

// netRun is the production status/apply command runner: it executes the command
// and returns combined output. Shelling out to nmcli/timedatectl/busctl matches how
// the rest of Waypoint drives the host (systemctl restarts, i2cdetect).
func netRun(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// wifiScanTTL is how long a Wi-Fi scan is cached. A scan takes seconds and the
// picker polls, so serving a recent result avoids hammering the radio; ~10s keeps
// the list fresh enough to reflect the operator walking toward an AP.
const wifiScanTTL = 10 * time.Second

// networkWiFiScan serves GET /api/network/wifi/scan: visible Wi-Fi networks for
// the join picker, cached for wifiScanTTL. The cache is process-wide (one radio),
// guarded by netScanMu.
func (s *server) networkWiFiScan(w http.ResponseWriter, _ *http.Request) {
	s.netScanMu.Lock()
	fresh := s.netScanAt.After(time.Now().Add(-wifiScanTTL)) && s.netScan != nil
	cached := s.netScan
	s.netScanMu.Unlock()
	if fresh {
		writeJSON(w, cached)
		return
	}
	results, err := netconfig.ScanWiFi(netconfig.ExecRunner)
	if err != nil {
		// A scan can fail on a wired-only node (no Wi-Fi device) — return an empty
		// list, not an error, so the picker degrades gracefully.
		results = []netconfig.WiFiScanResult{}
	}
	s.netScanMu.Lock()
	s.netScan = results
	s.netScanAt = time.Now()
	s.netScanMu.Unlock()
	writeJSON(w, results)
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
