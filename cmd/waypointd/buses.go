package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/KN4OQW/waypoint/internal/config"
)

// Bus API (RFC-0003). Two endpoints beyond the generic section writes
// (buses/attachments, handled in configPut): a dry-run validator the attach
// picker consults so the UI never re-implements the validity matrix, and the
// one-click bridge->bus migration.

type busValidateRequest struct {
	Buses       []config.Bus        `json:"buses"`
	Attachments []config.Attachment `json:"attachments"`
	// RemoteAttachments is the candidate set for a "via peer" attach (RFC-0016).
	// When present, the response also runs the peering validator so the UI greys a
	// remote mode with the peering-specific reason strings — never re-derived in JS.
	RemoteAttachments []config.RemoteAttachment `json:"remote_attachments,omitempty"`
}

type busValidateResponse struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// busesValidate runs the attach-time validator (RFC-0003 §2/§5) over a CANDIDATE
// bus topology without persisting it, returning the human-readable reason on
// refusal. The Buses UI posts its working copy plus a hypothetical new attachment
// to decide whether to grey out a mode and what reason to show — it asks this one
// validator rather than duplicating the converter matrix in JS (Task 2).
func (s *server) busesValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req busValidateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m, err := config.Load(s.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := busValidateResponse{OK: true}
	if err := config.ValidateBuses(req.Buses, req.Attachments, m.Networks); err != nil {
		resp.OK, resp.Reason = false, err.Error()
	} else if len(req.RemoteAttachments) > 0 {
		// A "via peer" attach: run the peering validator (structural: union mode-set,
		// per-node uniqueness, node cap, unknown peer/bus) plus the add-time
		// paired-peer gate, so the picker greys with the exact peering reasons.
		if err := config.ValidateRemoteAttachments(req.Buses, req.Attachments, req.RemoteAttachments, m.Peers); err != nil {
			resp.OK, resp.Reason = false, err.Error()
		} else if reason := unpairedPeerReason(req.RemoteAttachments, m.Peers); reason != "" {
			resp.OK, resp.Reason = false, reason
		}
	}
	writeJSON(w, resp)
}

// unpairedPeerReason returns the "peer not paired" refusal (matching
// SetRemoteAttachments' add-time gate) if any candidate references a peer that is
// not in the paired state.
func unpairedPeerReason(ras []config.RemoteAttachment, peers []config.Peer) string {
	state := make(map[string]config.PeerState, len(peers))
	name := make(map[string]string, len(peers))
	for _, p := range peers {
		state[p.ID], name[p.ID] = p.State, p.Name
	}
	for _, ra := range ras {
		if st, ok := state[ra.PeerID]; ok && st != config.PeerPaired {
			n := name[ra.PeerID]
			if n == "" {
				n = ra.PeerID
			}
			return fmt.Sprintf("peer %s is %s (not paired)", n, st)
		}
	}
	return ""
}

type busMigrateResponse struct {
	OK          bool     `json:"ok"`
	Warnings    []string `json:"warnings,omitempty"`
	Buses       int      `json:"buses"`
	Attachments int      `json:"attachments"`
}

// busesMigrate seeds the bus sections from the dormant cross-mode bridge sections
// (RFC-0003 §4) and persists the result through the same validating write paths a
// manual edit uses. It is additive and one-way: the bridge sections are left
// dormant, not deleted. ok=false with warnings means there was nothing to migrate
// or a migrated bus already exists; the warnings are surfaced verbatim in the UI.
func (s *server) busesMigrate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	m, err := config.Load(s.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	buses, attachments, warnings, ok := m.SeedBusesFromBridges()
	if !ok {
		writeJSON(w, busMigrateResponse{OK: false, Warnings: warnings})
		return
	}
	// Persist buses first (so the new bus exists), then attachments (which
	// reference it). Both go through the attach-time validator, so a migration that
	// would produce an invalid topology is refused here rather than persisted.
	bb, _ := json.Marshal(buses)
	if err := config.SetBuses(s.store, bb, "migrate"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ab, _ := json.Marshal(attachments)
	if err := config.SetAttachments(s.store, ab, "migrate"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, busMigrateResponse{OK: true, Warnings: warnings, Buses: len(buses), Attachments: len(attachments)})
}
