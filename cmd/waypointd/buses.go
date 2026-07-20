package main

import (
	"encoding/json"
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
	}
	writeJSON(w, resp)
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
