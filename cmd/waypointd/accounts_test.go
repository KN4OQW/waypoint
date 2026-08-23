package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/auth"
	"github.com/KN4OQW/waypoint/internal/phonebook"
	"github.com/KN4OQW/waypoint/internal/store"
)

// The account-management surface (RFC-0002 Amendment 1), covering the parts of the
// amendment's test contract that are not about the route mapping itself:
//
//	7  last-admin protection
//	10 forced rotation
//	11 whoami discloses only its three fields
//	12 referential integrity, on an ON-DISK store
//
// plus the property revocation exists for: deleting an account signs its sessions
// out in the same transaction.

// grant creates an account through the API as an admin, the way the panel does,
// and returns its id. Going through the route rather than the store is deliberate
// for the rotation tests: must_rotate is set by the handler, and a test that
// called the store directly could set it to whatever it wanted and prove nothing.
func grant(t *testing.T, e *authEnv, admin *http.Cookie, username, password string, role auth.Role, pbID int64) int64 {
	t.Helper()
	body := map[string]any{"username": username, "password": password, "role": string(role)}
	if pbID != 0 {
		body["phonebook_id"] = pbID
	}
	rec := e.do(t, "POST", "/api/accounts", body, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/accounts = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		ID         int64 `json:"id"`
		MustRotate bool  `json:"must_rotate"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// The flag is the whole point of an admin-chosen password, so assert it here
	// rather than trusting every caller to remember.
	if !got.MustRotate {
		t.Fatalf("an admin-created account came back with must_rotate=false")
	}
	return got.ID
}

// login signs in and returns the cookie, failing the test if it cannot.
func login(t *testing.T, e *authEnv, username, password string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, jsonReq("POST", "/api/session", map[string]string{
		"username": username, "password": password,
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("login as %s = %d (%s)", username, rec.Code, rec.Body.String())
	}
	return sessionCookie(t, rec.Result())
}

// ---------------------------------------------------------------------------
// 10. Forced rotation
// ---------------------------------------------------------------------------

// TestForcedRotationRefusesEverythingButThePasswordRoute is the amendment's tenth
// test. An admin-created account authenticates — the session is issued normally —
// and then reaches nothing but the rotation route until it has rotated.
//
// It runs for all three roles because the rotation gate is checked BEFORE the
// role, and a viewer created with must_rotate=1 is the case that would break if
// the password route were left to the default-deny: it would be refused the only
// route it is allowed, and the account could never be used at all.
func TestForcedRotationRefusesEverythingButThePasswordRoute(t *testing.T) {
	for _, role := range []auth.Role{auth.RoleAdmin, auth.RoleOperator, auth.RoleViewer} {
		t.Run(string(role), func(t *testing.T) {
			e := newAuthEnv(t, ":memory:")
			admin := e.claim(t, "theadmin", "goodpassword")
			const initial = "admin-chose-this"
			grant(t, e, admin, "rotate-"+string(role), initial, role, 0)
			cookie := login(t, e, "rotate-"+string(role), initial)

			// Every route the role would otherwise reach is refused, and the refusal
			// names the mode so the client knows to show the rotation screen rather
			// than the login one.
			for _, path := range []string{"/api/whoami", "/api/status", "/api/config", "/api/accounts"} {
				req := httptest.NewRequest("GET", path, nil)
				req.AddCookie(cookie)
				rec := httptest.NewRecorder()
				e.handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusForbidden {
					t.Errorf("%s before rotation = %d, want 403", path, rec.Code)
					continue
				}
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["mode"] != "rotate" {
					t.Errorf("%s refusal carried mode=%v, want \"rotate\"", path, body["mode"])
				}
			}

			// Rotating is allowed, and clears the flag.
			rec := e.do(t, "POST", "/api/password", map[string]string{
				"current_password": initial, "new_password": "the-one-only-i-know",
			}, cookie)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("POST /api/password = %d, want 204 (%s)", rec.Code, rec.Body.String())
			}

			// And now the mapping's routes open, on the SAME session: rotating is not
			// a re-authentication, and forcing one would log the operator out at the
			// moment they had just proved who they were.
			if ok, code := probe(t, e, "GET", "/api/whoami", cookie); !ok {
				t.Errorf("whoami still refused after rotation (%d)", code)
			}
			wantConfig := role != auth.RoleViewer
			if ok, code := probe(t, e, "GET", "/api/config", cookie); ok != wantConfig {
				t.Errorf("after rotation /api/config allowed=%v (%d), want %v", ok, code, wantConfig)
			}
		})
	}
}

// TestRotationScreenIsServedInsteadOfTheDashboard: the API half of forced rotation
// is a 403, but "/" is not an /api/ path and a caller who cannot use the dashboard
// must not be handed its shell. The gate serves a self-contained screen, the same
// way it serves the claim and login screens.
func TestRotationScreenIsServedInsteadOfTheDashboard(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	admin := e.claim(t, "theadmin", "goodpassword")
	const initial = "admin-chose-this"
	grant(t, e, admin, "needsrotate", initial, auth.RoleOperator, 0)
	cookie := login(t, e, "needsrotate", initial)

	page := func(c *http.Cookie) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET / = %d, want 200", rec.Code)
		}
		return rec.Body.String()
	}

	body := page(cookie)
	if !strings.Contains(body, "Set a new password") {
		t.Errorf("a must-rotate account was not served the rotation screen; got:\n%s", truncate(body))
	}
	// It posts to the one route the account may reach, and asks for the current
	// password: an unattended open session must not be enough to take the account
	// over, and the rotation flag does not change that.
	if !strings.Contains(body, "/api/password") {
		t.Error("the rotation screen does not post to /api/password")
	}
	if !strings.Contains(body, `id="current"`) {
		t.Error("the rotation screen does not ask for the current password")
	}

	// An account with nothing to rotate gets the dashboard, not the screen. The
	// admin claimed their own password, so must_rotate is 0 for them.
	if got := page(admin); strings.Contains(got, "Set a new password") {
		t.Error("an account with no rotation owing was served the rotation screen")
	}
}

func truncate(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// 11. whoami discloses only its three fields
// ---------------------------------------------------------------------------

// TestWhoamiDisclosesExactlyThreeFields is the amendment's eleventh test, and it
// is the thing holding the viewer /api/config denial up: the only reason a viewer
// does not need the config view is that this route gives it the callsign. If it
// ever grows a fourth field, that reasoning has to be redone.
//
// Asserted twice over — on the wire, and over the response type by reflection —
// because a field could be added to the struct with a json tag the wire test's
// fixture did not think to look for.
func TestWhoamiDisclosesExactlyThreeFields(t *testing.T) {
	want := []string{"username", "role", "callsign"}

	rt := reflect.TypeOf(whoamiView{})
	var tags []string
	for i := range rt.NumField() {
		tag := rt.Field(i).Tag.Get("json")
		tags = append(tags, strings.Split(tag, ",")[0])
	}
	if !reflect.DeepEqual(tags, want) {
		t.Errorf("whoamiView has fields %v, want exactly %v — see the amendment's test 11", tags, want)
	}

	e := newAuthEnv(t, ":memory:")
	_ = e.claim(t, "theadmin", "goodpassword")
	for _, role := range []auth.Role{auth.RoleAdmin, auth.RoleOperator, auth.RoleViewer} {
		cookie := loginAs(t, e, "whoami-"+string(role), role)
		rec := e.do(t, "GET", "/api/whoami", nil, cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/whoami as %s = %d", role, rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body) != len(want) {
			t.Errorf("whoami as %s returned %d fields (%v), want exactly %v", role, len(body), keysOf(body), want)
		}
		for _, k := range want {
			if _, ok := body[k]; !ok {
				t.Errorf("whoami as %s is missing %q", role, k)
			}
		}
		if body["role"] != string(role) {
			t.Errorf("whoami as %s reported role %v", role, body["role"])
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------------------
// 7. Last-admin protection
// ---------------------------------------------------------------------------

// TestLastAdminCannotBeDeletedOrDemoted is the amendment's seventh test. Without
// this a claimed device can be locked out of itself by an ordinary mistake, with
// no path back but a physical reset.
//
// The second half is the one worth writing: with TWO admins either may go, so the
// guard is "the last one" and not "an admin".
func TestLastAdminCannotBeDeletedOrDemoted(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	admin := e.claim(t, "theadmin", "goodpassword")

	first, ok, err := e.as.AccountByUsername("theadmin")
	if err != nil || !ok {
		t.Fatal(err)
	}
	id := idPath(first.ID)

	// Demoting the only admin: 409, and the account keeps its role.
	rec := e.do(t, "PATCH", "/api/accounts/"+id, map[string]string{"role": "operator"}, admin)
	if rec.Code != http.StatusConflict {
		t.Errorf("demoting the only admin = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if got, _, _ := e.as.AccountByUsername("theadmin"); got.Role != auth.RoleAdmin {
		t.Errorf("the only admin was demoted anyway, role is now %q", got.Role)
	}

	// Deleting the only admin: 409, and the account is still there.
	rec = e.do(t, "DELETE", "/api/accounts/"+id, nil, admin)
	if rec.Code != http.StatusConflict {
		t.Errorf("deleting the only admin = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if _, present, _ := e.as.AccountByUsername("theadmin"); !present {
		t.Error("the only admin was deleted anyway")
	}

	// With a second admin, either may be removed — the guard is "the last one",
	// not "an admin". The second is the one deleted here rather than the first,
	// because the first is the account this cookie belongs to and deleting it
	// would answer 401 (its session goes with it) rather than exercising the
	// guard. That behaviour is pinned separately below.
	secondID := grant(t, e, admin, "coadmin", "another-good-password", auth.RoleAdmin, 0)
	if rec := e.do(t, "DELETE", "/api/accounts/"+idPath(secondID), nil, admin); rec.Code != http.StatusNoContent {
		t.Errorf("deleting one of two admins = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	// And the guard closes behind it: the survivor is the last admin again.
	if rec := e.do(t, "DELETE", "/api/accounts/"+id, nil, admin); rec.Code != http.StatusConflict {
		t.Errorf("deleting the last remaining admin = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

// TestAnAdminMayDeleteItsOwnAccountAndIsSignedOut. Discovered by writing the test
// above the wrong way round and being answered 401 rather than 409: an admin who
// deletes the account they are signed in as succeeds, and the CASCADE takes their
// own session with it on the way out.
//
// That is the right behaviour and worth pinning rather than leaving as a surprise
// — it is how somebody hands a node over and steps back — but it means "delete an
// admin" and "delete THIS admin" are different acts, and the last-admin guard is
// the only thing between the second one and a locked-out device.
func TestAnAdminMayDeleteItsOwnAccountAndIsSignedOut(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	admin := e.claim(t, "theadmin", "goodpassword")
	self, ok, err := e.as.AccountByUsername("theadmin")
	if err != nil || !ok {
		t.Fatal(err)
	}
	// A second admin, so the last-admin guard is not what answers.
	grant(t, e, admin, "successor", "another-good-password", auth.RoleAdmin, 0)

	if rec := e.do(t, "DELETE", "/api/accounts/"+idPath(self.ID), nil, admin); rec.Code != http.StatusNoContent {
		t.Fatalf("an admin deleting its own account = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	if rec := e.do(t, "GET", "/api/accounts", nil, admin); rec.Code != http.StatusUnauthorized {
		t.Errorf("the deleted admin's session still worked: %d, want 401", rec.Code)
	}
}

// idPath renders an account or entry id for a URL.
func idPath(i int64) string { return strconv.FormatInt(i, 10) }

// ---------------------------------------------------------------------------
// Revocation signs the account out
// ---------------------------------------------------------------------------

// TestRevokingAnAccountKillsItsLiveSessions: sessions.account_id is ON DELETE
// CASCADE, which makes "revoking a login signs that person out" true by
// construction rather than by a handler remembering to do it.
//
// The session is exercised through the real stack before and after, because the
// row being gone is not the claim — the claim is that the cookie stops working.
func TestRevokingAnAccountKillsItsLiveSessions(t *testing.T) {
	// On disk, not :memory:. store.Open applies _pragma=foreign_keys(1) only to a
	// file path, so the CASCADE this test is about does not exist in an in-memory
	// store and the test would pass without proving anything.
	dir := t.TempDir()
	e := newAuthEnv(t, filepath.Join(dir, "config.db"))
	admin := e.claim(t, "theadmin", "goodpassword")

	const pw = "operator-first-password"
	id := grant(t, e, admin, "revokeme", pw, auth.RoleOperator, 0)
	cookie := login(t, e, "revokeme", pw)
	// Clear the rotation flag so the session is a normal working one; the thing
	// under test is revocation, not the rotation gate.
	if rec := e.do(t, "POST", "/api/password", map[string]string{
		"current_password": pw, "new_password": "operator-own-password",
	}, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("rotating = %d (%s)", rec.Code, rec.Body.String())
	}
	if ok, code := probe(t, e, "GET", "/api/config", cookie); !ok {
		t.Fatalf("the operator could not reach /api/config to begin with (%d)", code)
	}

	if rec := e.do(t, "DELETE", "/api/accounts/"+idPath(id), nil, admin); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/accounts/%d = %d (%s)", id, rec.Code, rec.Body.String())
	}

	// The cookie is now worthless. It is a 401 and not a 403: the account behind
	// the session is gone, so the caller is not authenticated rather than not
	// permitted, and sending them to a login screen is the honest answer.
	req := httptest.NewRequest("GET", "/api/config", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a revoked account's session got %d, want 401", rec.Code)
	}

	// And the session row itself is gone, not merely unusable — otherwise the
	// table would fill with the sessions of deleted accounts.
	var n int
	if err := e.s.store.DB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE account_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d session row(s) survived the account they belonged to", n)
	}
}

// ---------------------------------------------------------------------------
// 12. Referential integrity
// ---------------------------------------------------------------------------

// TestDeletingAPhonebookRowWithALoginIsRefused is the amendment's twelfth test.
//
// accounts.phonebook_id is ON DELETE RESTRICT rather than CASCADE so that removing
// somebody from the phonebook cannot silently delete their login. The refusal is a
// 409 carrying a machine-readable reason, because the panel has to say "revoke
// this entry's login first" in the operator's own language and a sentence built in
// Go could not be translated.
//
// ON DISK, for the reason the amendment spells out: store.Open applies
// _pragma=foreign_keys(1) only when the path is not ":memory:", so an in-memory
// store does not enforce foreign keys and this test would pass vacuously.
func TestDeletingAPhonebookRowWithALoginIsRefused(t *testing.T) {
	dir := t.TempDir()
	e := newAuthEnv(t, filepath.Join(dir, "config.db"))
	e.s.phonebook = phonebook.New(e.s.store)
	admin := e.claim(t, "theadmin", "goodpassword")

	rec := e.do(t, "POST", "/api/phonebook", map[string]any{"callsign": "W1AW"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating the phonebook entry = %d (%s)", rec.Code, rec.Body.String())
	}
	var entry struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	acctID := grant(t, e, admin, "w1aw", "a-good-first-password", auth.RoleOperator, entry.ID)

	// The delete is refused, and NOTHING is deleted.
	rec = e.do(t, "DELETE", "/api/phonebook/"+idPath(entry.ID), nil, admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("deleting a phonebook row an account depends on = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["reason"] != "has_account" {
		t.Errorf("the 409 carried reason=%q, want \"has_account\" — the panel keys its copy off this", body["reason"])
	}
	if _, err := e.s.phonebook.Get(entry.ID); err != nil {
		t.Errorf("the phonebook row was deleted despite the refusal: %v", err)
	}
	if _, err := e.as.AccountByID(acctID); err != nil {
		t.Errorf("the account was deleted despite the refusal: %v", err)
	}

	// Revoking the login first is the cure the panel tells the operator about, so
	// assert it actually works — a message naming a remedy that does not work is
	// worse than no message.
	if rec := e.do(t, "DELETE", "/api/accounts/"+idPath(acctID), nil, admin); rec.Code != http.StatusNoContent {
		t.Fatalf("revoking the login = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := e.do(t, "DELETE", "/api/phonebook/"+idPath(entry.ID), nil, admin); rec.Code != http.StatusNoContent {
		t.Errorf("deleting the entry after revoking its login = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
}

// TestForeignKeysAreOnForAFileStore guards the premise every test above rests on.
// If _pragma=foreign_keys(1) ever stops being applied, the RESTRICT and CASCADE
// tests would go green while enforcing nothing, and this is the one assertion that
// would notice.
func TestForeignKeysAreOnForAFileStore(t *testing.T) {
	dir := t.TempDir()
	e := newAuthEnv(t, filepath.Join(dir, "config.db"))
	var on int
	if err := e.s.store.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on != 1 {
		t.Fatal("foreign keys are OFF for an on-disk store; the RESTRICT and CASCADE tests above prove nothing")
	}
}

// ---------------------------------------------------------------------------
// 8. Migration fidelity, end to end
// ---------------------------------------------------------------------------

// TestPreAmendmentPasswordStillAuthenticates is the half of the amendment's
// eighth test that internal/store cannot make.
//
// The store's own migration tests assert the hash and its parameter block arrive
// byte-identical, which is the mechanism; this asserts the consequence, which is
// what the operator experiences: the password they chose at claim time still logs
// them in afterwards. The store fixture deliberately uses a placeholder hash
// (nothing in that package verifies one), so the claim has to be made here, where
// the real verifier and the real HTTP stack are both in reach.
//
// The pre-amendment database is made by taking a REAL claimed store back to v5
// rather than by hand-writing one. The point is that the credential is genuine —
// a hand-written fixture can only prove the bytes moved, not that they still mean
// what they meant.
func TestPreAmendmentPasswordStillAuthenticates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.db")
	const user, pass = "kn4oqw", "the-password-they-chose"

	// A real claimed node at head, with a live session.
	e := newAuthEnv(t, path)
	cookie := e.claim(t, user, pass)
	if ok, _ := probe(t, e, "GET", "/api/config", cookie); !ok {
		t.Fatal("the freshly claimed admin could not reach /api/config")
	}
	if err := e.s.store.Close(); err != nil {
		t.Fatal(err)
	}

	// Take it back to v5: accounts becomes admin again, sessions loses its owner
	// column, and the version says 5. The password hash and its parameter block
	// are carried across untouched, which is what makes this a real credential
	// rather than a fixture string.
	downgradeToV5(t, path)
	// Prove the downgrade actually happened before trusting what follows. A
	// downgrade that silently no-opped would leave the store at head, the reopen
	// would migrate nothing, and every assertion below would pass while testing
	// the absence of a migration.
	assertAtV5(t, path)

	// Reopening runs the ladder, which is the migration under test.
	e2 := newAuthEnv(t, path)
	if v, err := e2.s.store.Version(); err != nil || v != store.SchemaVersion {
		t.Fatalf("after reopening, schema version = %d (%v), want %d", v, err, store.SchemaVersion)
	}

	// The claim: the same password still works.
	migrated := login(t, e2, user, pass)
	if ok, code := probe(t, e2, "GET", "/api/config", migrated); !ok {
		t.Errorf("the migrated admin was refused /api/config (%d)", code)
	}
	// And it is an admin with no rotation owing — they chose that password
	// themselves, so demanding a change would be a rotation with no event behind
	// it. A must_rotate=1 here would show up as the rotation screen, not as a
	// failed login, which is why it is asserted rather than assumed.
	acct, ok, err := e2.as.AccountByUsername(user)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if acct.Role != auth.RoleAdmin {
		t.Errorf("the migrated account has role %q, want admin", acct.Role)
	}
	if acct.MustRotate {
		t.Error("the migrated admin was flagged for rotation; nothing prompted one")
	}
	// The session that was live before the migration still authenticates: RFC-0002
	// guarantees sessions survive a restart, and a migration happens during one.
	if ok, code := probe(t, e2, "GET", "/api/config", cookie); !ok {
		t.Errorf("a session live before the migration stopped working after it (%d)", code)
	}
}

// downgradeToV5 rewrites a head store into the shape a pre-amendment node had:
// the fixed-id admin row instead of accounts, and sessions with no owner.
//
// It is the inverse of migrateAccounts and exists only to produce a realistic
// starting point. It opens the file directly rather than through store.Open,
// because Open would migrate it straight back up.
func downgradeToV5(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test cleanup
	for _, stmt := range []string{
		`CREATE TABLE admin (
			id            INTEGER PRIMARY KEY CHECK (id = 1),
			username      TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			params        TEXT NOT NULL,
			created_at    TEXT NOT NULL,
			role          TEXT NOT NULL DEFAULT 'admin'
		)`,
		`INSERT INTO admin (id, username, password_hash, params, created_at, role)
			SELECT 1, username, password_hash, params, created_at, 'admin' FROM accounts ORDER BY id LIMIT 1`,
		`DROP TABLE accounts`,
		`ALTER TABLE sessions DROP COLUMN account_id`,
		`UPDATE meta SET schema_version = 5 WHERE id = 1`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("downgrade step %q: %v", stmt, err)
		}
	}
}

// assertAtV5 checks the file really is in the pre-amendment shape: version 5, no
// accounts table, and a sessions table with no owner column.
func assertAtV5(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test cleanup
	var v int
	if err := db.QueryRow(`SELECT schema_version FROM meta WHERE id = 1`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 5 {
		t.Fatalf("the downgraded store reports schema_version %d, want 5", v)
	}
	if err := db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='accounts'`).Scan(new(int)); err == nil {
		t.Fatal("the downgraded store still has an accounts table")
	}
	if err := db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='admin'`).Scan(new(int)); err != nil {
		t.Fatalf("the downgraded store has no admin table: %v", err)
	}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('sessions')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck // test cleanup
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name == "account_id" {
			t.Fatal("the downgraded store's sessions table still has account_id")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
