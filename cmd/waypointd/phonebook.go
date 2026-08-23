package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/phonebook"
)

// The authenticated phonebook API: who this node knows, and how to reach them.
//
// Like the public-view panel's endpoints, and unlike everything under
// /api/config/, none of this is part of the config store's section tree. A
// phonebook entry renders to no INI key, restarts no daemon, and takes effect on
// the next request — so there is no Apply here, and routing it through the apply
// pipeline would make an operator restart their repeater to correct a spelling.
// internal/config carries the test that holds that line.
//
// Every route sits behind s.auth.Gate with the rest of /api. The gate is
// default-deny, so registering a route is what makes it authenticated and there is
// nothing to opt in to. That default is doing real work on this surface: the
// entries carry email addresses, which are PII (D4), and the session wall is what
// keeps them off an anonymous response. No public-view type carries a phonebook
// field, and nothing here is reachable from the anonymous allowlist.

// registerPhonebookRoutes mounts the collection and item endpoints.
func (s *server) registerPhonebookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/phonebook", s.phonebookCollection)
	mux.HandleFunc("/api/phonebook/", s.phonebookItem)
}

// phonebookEntry is the wire shape of an entry.
//
// It is a separate type from phonebook.Entry for one field: DMRID is decoded as a
// signed 64-bit number so an out-of-range value can be REPORTED rather than
// rejected by the decoder. Decoding straight into a uint32 answers 5000000000 with
// "json: cannot unmarshal number 5000000000 into Go struct field ... of type
// uint32", which tells an operator nothing about what they may type instead.
//
// The nil check on DMRID also separates "no DMR ID" from "DMR ID zero" on the
// wire, so a client that omits the field and one that sends null mean the same
// thing, and neither is confused with a value that has to be validated.
type phonebookEntry struct {
	ID       int64  `json:"id,omitempty"`
	Callsign string `json:"callsign"`
	DMRID    *int64 `json:"dmr_id"`
	FullName string `json:"full_name"`
	Email    string `json:"email"`
}

// toEntry converts a decoded body into the store's type, range-checking the DMR ID
// on the way through.
func (b phonebookEntry) toEntry(id int64) (phonebook.Entry, error) {
	e := phonebook.Entry{ID: id, Callsign: b.Callsign, FullName: b.FullName, Email: b.Email}
	if b.DMRID == nil {
		return e, nil
	}
	n, err := phonebook.ValidateDMRID(*b.DMRID)
	if err != nil {
		return phonebook.Entry{}, err
	}
	e.DMRID = n
	return e, nil
}

// pbDecode reads a request body into a phonebookEntry.
//
// DisallowUnknownFields, like the config handlers: a client sending "e-mail" gets
// told, rather than silently storing an entry with no address. The cap is small
// because an entry is four short strings — a request that needs more than 8 KiB is
// not one of ours.
func pbDecode(w http.ResponseWriter, r *http.Request) (phonebookEntry, bool) {
	var body phonebookEntry
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
		return phonebookEntry{}, false
	}
	return body, true
}

// pbError maps the store's errors onto status codes.
//
// The two uniqueness conflicts are 409 and stay DISTINCT all the way to the
// client, because the panel shows the operator which field to change. A validation
// failure is 400 and its message is written to be read by whoever typed it.
//
// The default branch logs and answers a generic 500. What it deliberately does not
// do is put the request in the log: the body carries an email address, and a
// failed write is exactly the moment a well-meaning debug line would disclose one.
func pbError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, phonebook.ErrNotFound):
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, phonebook.ErrCallsignTaken):
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error(), "field": "callsign"})
	case errors.Is(err, phonebook.ErrDMRIDTaken):
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error(), "field": "dmr_id"})
	case errors.Is(err, phonebook.ErrBadCallsign):
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "callsign"})
	case errors.Is(err, phonebook.ErrBadDMRID):
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "dmr_id"})
	case errors.Is(err, phonebook.ErrBadEmail):
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "email"})
	default:
		log.Printf("phonebook: %s: %v", op, err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not read or write the phonebook"})
	}
}

// phonebookCollection serves GET (list) and POST (create) on /api/phonebook.
func (s *server) phonebookCollection(w http.ResponseWriter, r *http.Request) {
	if s.phonebook == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "no phonebook on this node"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.phonebook.List()
		if err != nil {
			pbError(w, "list", err)
			return
		}
		// Never null: a client rendering a table should get an empty list, not a
		// type error on the first node whose phonebook nobody has filled in.
		if list == nil {
			list = []phonebook.Entry{}
		}
		writeJSON(w, map[string]any{"entries": list})
	case http.MethodPost:
		body, ok := pbDecode(w, r)
		if !ok {
			return
		}
		e, err := body.toEntry(0)
		if err != nil {
			pbError(w, "create", err)
			return
		}
		made, err := s.phonebook.Create(e)
		if err != nil {
			pbError(w, "create", err)
			return
		}
		// 201 with the stored row, not the request: the callsign comes back
		// uppercased and the id and timestamps are the server's to assign.
		writeJSONStatus(w, http.StatusCreated, made)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// phonebookItem serves PUT (replace) and DELETE on /api/phonebook/{id}.
//
// GET on an item is deliberately absent: the panel reads the whole list, and an
// endpoint nothing calls is a surface to keep correct for nothing. The store has
// Get, so adding the route later is one case in this switch.
func (s *server) phonebookItem(w http.ResponseWriter, r *http.Request) {
	if s.phonebook == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "no phonebook on this node"})
		return
	}
	raw := strings.TrimPrefix(r.URL.Path, "/api/phonebook/")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "id must be a positive number"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		body, ok := pbDecode(w, r)
		if !ok {
			return
		}
		// The path wins over any id in the body, so a request cannot edit one row
		// while addressing another.
		e, err := body.toEntry(id)
		if err != nil {
			pbError(w, "update", err)
			return
		}
		got, err := s.phonebook.Update(e)
		if err != nil {
			pbError(w, "update", err)
			return
		}
		writeJSON(w, got)
	case http.MethodDelete:
		if err := s.phonebook.Delete(id); err != nil {
			pbError(w, "delete", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}
