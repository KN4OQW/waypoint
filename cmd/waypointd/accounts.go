package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KN4OQW/waypoint/internal/auth"
	"github.com/KN4OQW/waypoint/internal/config"
)

// The account-management surface (RFC-0002 Amendment 1), plus the two routes
// every authenticated caller needs whatever their role: who am I, and let me
// change my password.
//
// Everything under /api/accounts is admin-only, enforced by roles.go rather than
// re-checked here — a handler that re-derives its own permission is a second
// place for the two to disagree. What IS checked here is the state the role
// mapping cannot express: the last admin, and the difference between changing
// your own password and being handed one.

// minPasswordLen mirrors the claim-time floor. RFC-0002 leaves the full strength
// policy to the UI layer and fixes only that there is one; an initial password an
// admin sets is held to the same bar as one an operator chooses at claim.
const minPasswordLen = 8

func (s *server) registerAccountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/whoami", s.whoami)
	mux.HandleFunc("/api/password", s.changePassword)
	mux.HandleFunc("/api/accounts", s.accountsCollection)
	mux.HandleFunc("/api/accounts/", s.accountItem)
}

// accountView is what an account looks like over the wire. It is a projection,
// not the record: the hash and its parameter block have no field here, so a
// handler cannot leak them by forgetting to clear one.
type accountView struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	PhonebookID int64  `json:"phonebook_id,omitempty"`
	MustRotate  bool   `json:"must_rotate"`
	CreatedAt   string `json:"created_at,omitempty"`
}

func toAccountView(a auth.Account) accountView {
	return accountView{
		ID: a.ID, Username: a.Username, Role: string(a.Role),
		PhonebookID: a.PhonebookID, MustRotate: a.MustRotate,
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// whoamiView is the whoami response, as a named type rather than a map so the
// amendment's field audit has a declaration to walk. Growing a field here is the
// thing that test is watching for: this is the one route a viewer reaches, and its
// shape is what holds the GET /api/config denial up.
type whoamiView struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Callsign string `json:"callsign"`
}

// whoami answers "who am I and what may I do", for every authenticated account.
//
// It exists so a viewer never needs GET /api/config to paint the sidebar's
// callsign chip. Three fields, all of which the caller is already entitled to:
// their own name, their own role, and the station identity this node transmits in
// the clear on every transmission anyway.
//
// The role here REPORTS what the server will enforce; it does not grant anything.
// A client that lies to itself about its role still gets 403s from roles.go.
func (s *server) whoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	acct, ok := s.auth.Caller(r)
	if !ok {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	// The station callsign, and nothing else out of the config. Reading the whole
	// model to project one field is deliberate: the alternative is a second copy
	// of the callsign somewhere, and two places to keep in step.
	callsign := ""
	if m, err := config.Load(s.store); err == nil {
		callsign = m.General.Callsign
	}
	// Three fields, and the amendment's test asserts there is no fourth. The
	// rotation flag is deliberately NOT among them: a must-rotate account cannot
	// reach this route at all (roles.go refuses everything but /api/password), so
	// a field reporting the flag could never be read by the client that needs it.
	// The client learns it from the gate instead — the rotation screen at "/" and
	// the 403 mode:"rotate" — which is the same way it learns "claim" and "login".
	writeJSON(w, whoamiView{
		Username: acct.Username,
		Role:     string(acct.Role),
		Callsign: callsign,
	})
}

// changePassword is the caller changing their OWN password.
//
// It is the one route a must-rotate account may reach, so it cannot require the
// rotation to be finished before it will run. It also requires the current
// password even then: a session left open on an unattended screen must not be
// enough to take the account over, and the rotation flag does not change that.
func (s *server) changePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	acct, ok := s.auth.Caller(r)
	if !ok {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	var body struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if okPass, err := acct.Record.Verify(body.Current); err != nil || !okPass {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "current password is incorrect"})
		return
	}
	if utf8.RuneCountInString(body.New) < minPasswordLen {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "new password must be at least " + strconv.Itoa(minPasswordLen) + " characters",
		})
		return
	}
	if body.New == body.Current {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "the new password must be different from the current one",
		})
		return
	}
	rec, err := auth.HashPassword(body.New)
	if err != nil {
		log.Printf("accounts: hashing a new password failed: %v", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not set the password"})
		return
	}
	// Rotating clears the flag: the password is now one only its owner knows.
	if err := s.authStore.SetPassword(acct.ID, rec, false, time.Now()); err != nil {
		log.Printf("accounts: setting a password failed: %v", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not set the password"})
		return
	}
	log.Printf("SECURITY: account %q changed its own password", acct.Username)
	w.WriteHeader(http.StatusNoContent)
}

// accountsCollection is GET (list) and POST (create).
func (s *server) accountsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		accts, err := s.authStore.Accounts()
		if err != nil {
			log.Printf("accounts: list: %v", err)
			writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not read accounts"})
			return
		}
		out := make([]accountView, 0, len(accts))
		for _, a := range accts {
			out = append(out, toAccountView(a))
		}
		writeJSON(w, map[string]any{"accounts": out})
	case http.MethodPost:
		s.createAccount(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *server) createAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		Role        string `json:"role"`
		PhonebookID int64  `json:"phonebook_id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
		return
	}
	username := strings.TrimSpace(body.Username)
	if username == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "a username is required", "field": "username"})
		return
	}
	role := auth.Role(body.Role)
	if !role.Valid() {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": `role must be "admin", "operator" or "viewer"`, "field": "role",
		})
		return
	}
	if utf8.RuneCountInString(body.Password) < minPasswordLen {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "initial password must be at least " + strconv.Itoa(minPasswordLen) + " characters",
			"field": "password",
		})
		return
	}
	rec, err := auth.HashPassword(body.Password)
	if err != nil {
		log.Printf("accounts: hashing failed: %v", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not create the account"})
		return
	}
	// must_rotate is always set here. An admin chose this password, so two people
	// know it; the flag is what makes that state brief rather than permanent.
	acct, err := s.authStore.CreateAccount(body.PhonebookID, username, rec, role, true, time.Now())
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrUsernameTaken):
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error(), "field": "username"})
		return
	default:
		// A phonebook_id that does not exist arrives here as a foreign-key failure.
		// Reported as a 400 rather than a 500: the caller named a row that is not
		// there, which is their input rather than our fault.
		log.Printf("accounts: create: %v", err)
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "could not create the account; check the phonebook entry it links to",
		})
		return
	}
	log.Printf("SECURITY: account %q created with role %s", acct.Username, acct.Role)
	writeJSONStatus(w, http.StatusCreated, toAccountView(acct))
}

// accountItem is PATCH (change role or reset the password) and DELETE (revoke).
func (s *server) accountItem(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.URL.Path, "/api/accounts/")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "id must be a positive number"})
		return
	}
	switch r.Method {
	case http.MethodPatch:
		s.patchAccount(w, r, id)
	case http.MethodDelete:
		s.deleteAccount(w, r, id)
	default:
		w.Header().Set("Allow", "PATCH, DELETE")
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *server) patchAccount(w http.ResponseWriter, r *http.Request, id int64) {
	var body struct {
		Role     *string `json:"role"`
		Password *string `json:"password"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
		return
	}
	if body.Role == nil && body.Password == nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "nothing to change"})
		return
	}
	if body.Role != nil {
		role := auth.Role(*body.Role)
		if !role.Valid() {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{
				"error": `role must be "admin", "operator" or "viewer"`, "field": "role",
			})
			return
		}
		switch err := s.authStore.SetRole(id, role, time.Now()); {
		case err == nil:
		case errors.Is(err, auth.ErrLastAdmin):
			writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error(), "field": "role"})
			return
		case errors.Is(err, auth.ErrNoSuchAccount):
			writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		default:
			log.Printf("accounts: set role: %v", err)
			writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not change the role"})
			return
		}
		log.Printf("SECURITY: account %d role changed to %s", id, *body.Role)
	}
	if body.Password != nil {
		if utf8.RuneCountInString(*body.Password) < minPasswordLen {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{
				"error": "password must be at least " + strconv.Itoa(minPasswordLen) + " characters",
				"field": "password",
			})
			return
		}
		rec, err := auth.HashPassword(*body.Password)
		if err != nil {
			log.Printf("accounts: hashing failed: %v", err)
			writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not set the password"})
			return
		}
		// An admin setting somebody else's password re-arms the rotation flag, for
		// the same reason creation does: two people know it now.
		if err := s.authStore.SetPassword(id, rec, true, time.Now()); err != nil {
			if errors.Is(err, auth.ErrNoSuchAccount) {
				writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": err.Error()})
				return
			}
			log.Printf("accounts: set password: %v", err)
			writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not set the password"})
			return
		}
		// A password reset logs the account out everywhere. Whoever knew the old
		// one should not keep a live session on the strength of it.
		if err := s.authStore.RevokeAccountSessions(id); err != nil {
			log.Printf("accounts: revoking sessions after a password reset: %v", err)
		}
		log.Printf("SECURITY: account %d password reset by an admin; its sessions were revoked", id)
	}
	acct, err := s.authStore.AccountByID(id)
	if err != nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "no such account"})
		return
	}
	writeJSON(w, toAccountView(acct))
}

func (s *server) deleteAccount(w http.ResponseWriter, r *http.Request, id int64) {
	acct, err := s.authStore.AccountByID(id)
	if err != nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "no such account"})
		return
	}
	switch err := s.authStore.DeleteAccount(id); {
	case err == nil:
	case errors.Is(err, auth.ErrLastAdmin):
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, auth.ErrNoSuchAccount):
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	default:
		log.Printf("accounts: delete: %v", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not revoke the login"})
		return
	}
	// The phonebook row is deliberately untouched: revoking a login removes trust,
	// not identity. The person stays in the phonebook and keeps appearing on the
	// dashboard; they simply cannot sign in.
	log.Printf("SECURITY: login revoked for account %q; its sessions were revoked and the phonebook entry was kept", acct.Username)
	w.WriteHeader(http.StatusNoContent)
}
