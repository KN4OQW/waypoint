package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/publicview"
)

// The authenticated write surface behind the Public View settings panel (D1–D8).
//
// It is deliberately NOT part of the config store's section tree. Everything the
// radio does goes through /api/config/{section} and then a render-and-restart
// apply; none of that is true here. Turning the public page on changes no INI,
// restarts no gateway, and takes effect on the next request — so routing it
// through the apply pipeline would make an operator restart their repeater to
// publish a net schedule.
//
// That difference is worth stating because the panel deliberately looks the same
// as the others. Same controls, same layout, no Apply button.

// registerPublicPanelRoutes mounts the settings API. All of it is behind the
// session wall — these endpoints decide what the world can see, so the gate's
// default-deny is doing the important work.
func (s *server) registerPublicPanelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/public-view/settings", s.pvSettings)
	mux.HandleFunc("/api/public-view/suppress", s.pvSuppress)
	mux.HandleFunc("/api/public-view/links", s.pvLinks)
	mux.HandleFunc("/api/public-view/links/", s.pvLinkItem)
	mux.HandleFunc("/api/public-view/nets", s.pvNets)
	mux.HandleFunc("/api/public-view/nets/", s.pvNetItem)
}

func pvJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// pvError maps the model layer's validation errors onto status codes.
//
// The distinction matters to the panel: a 400 is something the operator typed and
// can fix, and the message is written to be shown to them. A 500 is not their
// problem and the panel says so differently.
func pvError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, publicview.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, publicview.ErrBadURL),
		errors.Is(err, publicview.ErrBadCallsign),
		errors.Is(err, publicview.ErrBadGrid),
		errors.Is(err, publicview.ErrUnknownTag),
		errors.Is(err, publicview.ErrRequired),
		errors.Is(err, publicview.ErrBadPosition),
		errors.Is(err, publicview.ErrBadSource):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// pvSettings reads and writes the settings row.
//
// GET also returns the closed set of purpose tags, so the panel renders the
// options the server will actually accept rather than a list duplicated in
// JavaScript that can drift out of step with the validator.
func (s *server) pvSettings(w http.ResponseWriter, r *http.Request) {
	if s.publicStore == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		set, err := s.publicStore.Settings()
		if err != nil {
			pvError(w, err)
			return
		}
		pvJSON(w, struct {
			publicview.Settings
			AvailableTags []string `json:"available_tags"`
			MinRetention  int      `json:"min_retention_hours"`
			MaxRetention  int      `json:"max_retention_hours"`
		}{set, publicview.PurposeTags, publicview.MinRetentionHours, publicview.MaxRetentionHours})
	case http.MethodPut:
		var body publicview.Settings
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.publicStore.SaveSettings(body); err != nil {
			pvError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// pvSuppress is the D8 list: add, remove, read.
//
// DELETE takes the callsign as a query parameter rather than a path segment
// because a callsign can carry a slash (W1AW/4), and an operator will paste
// whatever they saw in the log.
func (s *server) pvSuppress(w http.ResponseWriter, r *http.Request) {
	if s.publicStore == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.publicStore.Suppressed()
		if err != nil {
			pvError(w, err)
			return
		}
		pvJSON(w, map[string]any{"callsigns": list})
	case http.MethodPost:
		var body struct {
			Callsign string `json:"callsign"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		stored, err := s.publicStore.AddSuppressed(body.Callsign)
		if err != nil {
			pvError(w, err)
			return
		}
		// Return what was actually stored: the operator typed "n4abc-7" and the
		// list holds "N4ABC", and showing them the normalised form is how they
		// learn the SSID does not matter.
		pvJSON(w, map[string]string{"callsign": stored})
	case http.MethodDelete:
		if err := s.publicStore.RemoveSuppressed(r.URL.Query().Get("callsign")); err != nil {
			pvError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) pvLinks(w http.ResponseWriter, r *http.Request) {
	if s.publicStore == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.publicStore.Links()
		if err != nil {
			pvError(w, err)
			return
		}
		pvJSON(w, map[string]any{"links": list})
	case http.MethodPost:
		var l publicview.Link
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&l); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		id, err := s.publicStore.AddLink(l)
		if err != nil {
			pvError(w, err)
			return
		}
		pvJSON(w, map[string]any{"id": id})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) pvLinkItem(w http.ResponseWriter, r *http.Request) {
	if s.publicStore == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	id, ok := pvID(w, r, "/api/public-view/links/")
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodPut:
		var l publicview.Link
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&l); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		l.ID = id
		pvErrOrNoContent(w, s.publicStore.UpdateLink(l))
	case http.MethodDelete:
		pvErrOrNoContent(w, s.publicStore.DeleteLink(id))
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) pvNets(w http.ResponseWriter, r *http.Request) {
	if s.publicStore == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.publicStore.Nets()
		if err != nil {
			pvError(w, err)
			return
		}
		pvJSON(w, map[string]any{"nets": list})
	case http.MethodPost:
		var n publicview.Net
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&n); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		id, err := s.publicStore.AddNet(n)
		if err != nil {
			pvError(w, err)
			return
		}
		pvJSON(w, map[string]any{"id": id})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) pvNetItem(w http.ResponseWriter, r *http.Request) {
	if s.publicStore == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	id, ok := pvID(w, r, "/api/public-view/nets/")
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodPut:
		var n publicview.Net
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&n); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		n.ID = id
		pvErrOrNoContent(w, s.publicStore.UpdateNet(n))
	case http.MethodDelete:
		pvErrOrNoContent(w, s.publicStore.DeleteNet(id))
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func pvErrOrNoContent(w http.ResponseWriter, err error) {
	if err != nil {
		pvError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pvID parses the trailing row id.
func pvID(w http.ResponseWriter, r *http.Request, prefix string) (int64, bool) {
	raw := strings.TrimPrefix(r.URL.Path, prefix)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}
