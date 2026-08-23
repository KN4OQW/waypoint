package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/dmrids"
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
	// Registered before the subtree so the exact pattern wins: Go's ServeMux picks
	// the longest match, so "/api/phonebook/import" reaches this rather than
	// phonebookItem trying to read "import" as an entry id.
	mux.HandleFunc("/api/phonebook/import", s.phonebookImport)
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
	// A delete refused by the accounts foreign key. It carries a machine-readable
	// reason rather than only prose, because the panel has to say "revoke this
	// entry's login first" in the operator's language and a sentence generated
	// here could not be translated (house rule: user-facing strings live in the
	// catalogs). The English text stays for a caller reading the API directly.
	case errors.Is(err, phonebook.ErrHasAccount):
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error": err.Error(), "reason": "has_account"})
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

// syncPhonebookFromPublic re-reads the phonebook's imported entries against the
// refreshed public table. It is the hook dmrids.RunThen fires after a
// successful download.
//
// The lookup is handed in as a closure so internal/phonebook never imports
// internal/dmrids: RFC-0003 §3 makes that package the single reader of
// DMRIds.dat, and a store that opened the file itself would be a second one.
//
// A node with no phonebook, or one whose entries are all hand-typed, does no work
// beyond one query that matches nothing.
func (s *server) syncPhonebookFromPublic(path string) error {
	if s.phonebook == nil {
		return nil
	}
	res, err := s.phonebook.Sync(func(ids []uint32) (map[uint32]phonebook.Record, error) {
		found, err := dmrids.LookupIDs(path, ids)
		if err != nil {
			return nil, err
		}
		out := make(map[uint32]phonebook.Record, len(found))
		for id, r := range found {
			out[id] = phonebook.Record{ID: r.ID, Callsign: r.Callsign, Name: r.Name}
		}
		return out, nil
	})
	if err != nil {
		return err
	}
	// Logged only when something actually changed. A 24-hour timer that writes a
	// line saying it did nothing is a line that trains an operator to skip the log.
	// The counts carry no email and no name — the phonebook's PII stays out of the
	// journal (D4).
	if res.Updated > 0 || res.Missing > 0 {
		log.Printf("phonebook: public-list refresh: %d imported entr(ies) checked, %d updated, %d no longer listed",
			res.Checked, res.Updated, res.Missing)
	}
	return nil
}

// phonebookImport creates an entry from the public DMRIds.dat table.
//
// The request names an ID and nothing else. The SERVER looks it up and writes what
// the table says, which is the whole point: a client that could post the callsign
// and name alongside the ID could store anything it liked and have it marked as
// coming from the public list, and the refresh would then keep "correcting" a row
// back to a value nobody had ever published. Marking a row imported is a claim
// about provenance, and only the code that reads the file can make it.
//
// It adds no outbound request. The table is the one the ID-database refresher has
// already downloaded for the gateways, under the same operator-visible off switch
// (#138) — a second reader of a file that was already there, exactly as the #140
// callsign lookup is.
//
// The email is not set and cannot be: the export has no address. An operator adds
// one afterwards through the ordinary edit form, and doing so does not stop the
// entry tracking the public list — see phonebook.Store.Update.
func (s *server) phonebookImport(w http.ResponseWriter, r *http.Request) {
	if s.phonebook == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "no phonebook on this node"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var body struct {
		DMRID int64 `json:"dmr_id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	// Range-checked before the scan, the same way phonebookEntry checks it: a DMR
	// ID is a 32-bit field on the air, and a value that cannot be one is a bad
	// request rather than a lookup that will certainly miss.
	if body.DMRID <= 0 || body.DMRID > 0xFFFFFFFF {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "a DMR ID must be a positive number no wider than 32 bits", "field": "dmr_id",
		})
		return
	}
	id := uint32(body.DMRID)
	found, err := dmrids.LookupIDs(s.dmrIDs, []uint32{id})
	if err != nil {
		// The table being unreadable is the node's problem, not the request's, and
		// the path is not named — the same rule the #140 lookup follows.
		log.Printf("phonebook: import: reading the id table failed: %v", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "the DMR ID table could not be read"})
		return
	}
	rec, ok := found[id]
	if !ok {
		// 404, and the message distinguishes the two reasons a lookup finds nothing:
		// there is no table on this node, or there is one and the ID is not in it.
		// The first is fixed from the Updates tab and the second at radioid.net, and
		// telling an operator the wrong one sends them on the wrong errand.
		if !dmrIDTablePresent(s.dmrIDs) {
			writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "no public ID table on this node", "reason": "no_table"})
			return
		}
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "that DMR ID is not in the public list", "reason": "not_listed"})
		return
	}
	got, err := s.phonebook.Create(phonebook.Entry{
		Callsign: rec.Callsign,
		DMRID:    rec.ID,
		FullName: rec.Name,
		Source:   phonebook.SourceDMRIds,
	})
	if err != nil {
		pbError(w, "import", err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, got)
}
