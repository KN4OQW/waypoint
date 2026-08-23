package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// RFC-0001's losslessness contract, property 4: "every historical schema version's
// fixture DB migrates to head losslessly — and a migration interrupted at any step
// (crash-injection) leaves a system that boots on the pre-migration backup."

// v1DDL is the schema exactly as shipped at SchemaVersion 1. It is a frozen
// historical fixture: do not update it when the current schema changes — that is
// the whole point of it.
const v1DDL = `
CREATE TABLE meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  schema_version INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  updated_by TEXT NOT NULL
);
CREATE TABLE applies (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  at         TEXT NOT NULL,
  by         TEXT NOT NULL,
  diff       TEXT NOT NULL
);`

// v2DDL is the schema exactly as shipped at SchemaVersion 2 — v1 plus the meta
// columns the first ladder step added. Like v1DDL it is frozen: it exists so the
// step from v2 can be exercised on its own, rather than only as the tail of a
// v1 -> head run where a bug in it could be masked by the step before.
const v2DDL = `
CREATE TABLE meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  schema_version INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  claimed_at TEXT,
  provisioned INTEGER
);
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  updated_by TEXT NOT NULL
);
CREATE TABLE applies (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  at         TEXT NOT NULL,
  by         TEXT NOT NULL,
  diff       TEXT NOT NULL
);`

// v2AdminDDL is the auth subsystem's admin table as it stood at schema v2, before
// it grew a role column. A claimed node in the field has this; an unclaimed one
// that has still been started has it too, because auth creates its tables at
// startup regardless. Both are fixtures below.
const v2AdminDDL = `
CREATE TABLE admin (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  username      TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  params        TEXT NOT NULL,
  created_at    TEXT NOT NULL
);`

// writeV1Fixture builds a schema-v1 database at path holding operator data, so a
// migration that loses rows is caught rather than a migration that merely runs.
//
// drifted reproduces a v1 node that had already been through the pre-ladder
// startup path, where auth and provision each added their meta column
// opportunistically. That is what every existing bench node actually looks like,
// so it has to migrate as cleanly as a pristine one.
func writeV1Fixture(t *testing.T, path string, drifted bool) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test cleanup
	if _, err := db.Exec(v1DDL); err != nil {
		t.Fatal(err)
	}
	if drifted {
		for _, c := range []string{`ALTER TABLE meta ADD COLUMN claimed_at TEXT`, `ALTER TABLE meta ADD COLUMN provisioned INTEGER`} {
			if _, err := db.Exec(c); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := db.Exec(`INSERT INTO meta(id, schema_version, created_at) VALUES(1, 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for k, v := range fixtureSettings {
		if _, err := db.Exec(`INSERT INTO settings(key, value, updated_at, updated_by) VALUES(?, ?, '2026-01-01T00:00:00Z', 'fixture')`, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO applies(at, by, diff) VALUES('2026-01-01T00:00:00Z', 'fixture', '{"general.callsign":"KN4OQW"}')`); err != nil {
		t.Fatal(err)
	}
}

// fixtureSettings is the operator data a migration must carry across untouched.
var fixtureSettings = map[string]string{
	"general":  `{"callsign":"KN4OQW","dmr_id":3112345}`,
	"modes":    `{"dmr":true,"ysf":true}`,
	"networks": `[{"name":"BrandMeister","enabled":true}]`,
}

// assertFixtureIntact is the losslessness half of property 4.
func assertFixtureIntact(t *testing.T, s *Store) {
	t.Helper()
	for k, want := range fixtureSettings {
		got, ok, err := s.Get(k)
		if err != nil || !ok {
			t.Fatalf("after migration, Get(%q): ok=%v err=%v", k, ok, err)
		}
		if string(got) != want {
			t.Errorf("after migration, %q = %s, want %s", k, got, want)
		}
	}
	var applies int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM applies`).Scan(&applies); err != nil {
		t.Fatal(err)
	}
	if applies != 1 {
		t.Errorf("applies journal has %d rows after migration, want 1", applies)
	}
}

// TestLadderReachesHead catches the authoring mistake of adding a migration and
// forgetting to bump SchemaVersion (or the reverse) at build time, rather than on
// a node that then silently never migrates.
func TestLadderReachesHead(t *testing.T) {
	if got := headVersion(); got != SchemaVersion {
		t.Fatalf("ladder ends at v%d but SchemaVersion is %d — every migration must bump SchemaVersion to match", got, SchemaVersion)
	}
	for i, m := range migrations {
		if want := i + 2; m.to != want {
			t.Errorf("migrations[%d] targets v%d, want v%d — the ladder must be contiguous and start at 2", i, m.to, want)
		}
	}
}

// TestFreshStoreIsAtHead pins the baseline rule: a new database is created at the
// current schema and runs no migrations at all.
func TestFreshStoreIsAtHead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck // test cleanup
	v, err := s.Version()
	if err != nil {
		t.Fatal(err)
	}
	if v != SchemaVersion {
		t.Errorf("fresh store is at v%d, want v%d", v, SchemaVersion)
	}
	// No migration ran, so no pre-migration copy should exist for any version.
	for i := 1; i < SchemaVersion; i++ {
		if b := BackupPath(path, i); fileExists(b) {
			t.Errorf("fresh store left a pre-migration copy at %s", b)
		}
	}
}

func TestMigratesV1ToHead(t *testing.T) {
	for _, tc := range []struct {
		name    string
		drifted bool
	}{
		{"pristine", false},
		{"drifted", true}, // meta columns already added by the pre-ladder ad-hoc path
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.db")
			writeV1Fixture(t, path, tc.drifted)

			s, err := Open(path)
			if err != nil {
				t.Fatalf("Open on a v1 store: %v", err)
			}
			defer s.Close() //nolint:errcheck // test cleanup

			v, err := s.Version()
			if err != nil {
				t.Fatal(err)
			}
			if v != SchemaVersion {
				t.Errorf("after Open, version = %d, want %d", v, SchemaVersion)
			}
			assertFixtureIntact(t, s)

			// The columns the ladder is responsible for are present and usable.
			if _, err := s.db.Exec(`UPDATE meta SET claimed_at = '2026-02-02T00:00:00Z', provisioned = 1 WHERE id = 1`); err != nil {
				t.Errorf("meta claim/provision columns not usable after migration: %v", err)
			}
		})
	}
}

// TestMigratedMatchesFresh is the rule that keeps the baseline DDL and the ladder
// from drifting apart: a database migrated up from v1 must be shaped exactly like
// one created fresh at head. Without this, the two definitions of "the schema"
// diverge silently and only old nodes hit the difference.
//
// Columns are compared as a set, not a sequence: a drifted v1 store got its meta
// columns in whatever order its subsystems happened to initialize, and no code
// depends on column order.
func TestMigratedMatchesFresh(t *testing.T) {
	dir := t.TempDir()

	migPath := filepath.Join(dir, "migrated.db")
	writeV1Fixture(t, migPath, false)
	migrated, err := Open(migPath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close() //nolint:errcheck // test cleanup

	fresh, err := Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close() //nolint:errcheck // test cleanup

	got, want := tableShape(t, migrated), tableShape(t, fresh)
	if got != want {
		t.Errorf("a store migrated to head does not match a fresh one.\nmigrated: %s\nfresh:    %s", got, want)
	}
}

// tableShape renders every table's columns as a stable string, so a mismatch
// reports what actually differs instead of just failing.
func tableShape(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close() //nolint:errcheck // fully drained above

	var out []string
	for _, tbl := range tables {
		cr, err := s.db.Query(`SELECT name, type FROM pragma_table_info(?)`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		var cols []string
		for cr.Next() {
			var name, typ string
			if err := cr.Scan(&name, &typ); err != nil {
				t.Fatal(err)
			}
			cols = append(cols, name+" "+strings.ToUpper(typ))
		}
		if err := cr.Err(); err != nil {
			t.Fatal(err)
		}
		cr.Close() //nolint:errcheck // fully drained above
		sort.Strings(cols)
		out = append(out, tbl+"("+strings.Join(cols, ", ")+")")
	}
	return strings.Join(out, " ")
}

// writeV2Fixture builds a schema-v2 database at path holding operator data.
// withAdmin adds the auth subsystem's table as it stood at v2 — the shape the
// public-view step has to add a role column to. Without it, the step's
// table-missing branch is the one under test instead.
func writeV2Fixture(t *testing.T, path string, withAdmin bool) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test cleanup
	if _, err := db.Exec(v2DDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO meta(id, schema_version, created_at, claimed_at) VALUES(1, 2, '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for k, v := range fixtureSettings {
		if _, err := db.Exec(`INSERT INTO settings(key, value, updated_at, updated_by) VALUES(?, ?, '2026-01-01T00:00:00Z', 'fixture')`, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO applies(at, by, diff) VALUES('2026-01-01T00:00:00Z', 'fixture', '{"general.callsign":"KN4OQW"}')`); err != nil {
		t.Fatal(err)
	}
	if !withAdmin {
		return
	}
	if _, err := db.Exec(v2AdminDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO admin(id, username, password_hash, params, created_at) VALUES(1, 'W4RJM', 'hash', '{}', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
}

// TestMigratesV2ToHead exercises the public-view step against a node that already
// exists, which is the only kind of node it ever runs on.
//
// The claimed/unclaimed split is not decoration: the step alters a table the auth
// subsystem owns, and whether that table is there is a property of the node's
// history rather than of its schema version. Both branches have to leave a usable
// database, and the unclaimed one has to leave the column to auth rather than
// inventing the table itself.
func TestMigratesV2ToHead(t *testing.T) {
	for _, tc := range []struct {
		name      string
		withAdmin bool
	}{
		{"claimed", true},    // admin table present: the ladder adds the column
		{"unclaimed", false}, // no admin table: the ladder leaves it to auth
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.db")
			writeV2Fixture(t, path, tc.withAdmin)

			s, err := Open(path)
			if err != nil {
				t.Fatalf("Open on a v2 store: %v", err)
			}
			defer s.Close() //nolint:errcheck // test cleanup

			v, err := s.Version()
			if err != nil {
				t.Fatal(err)
			}
			if v != SchemaVersion {
				t.Errorf("after Open, version = %d, want %d", v, SchemaVersion)
			}
			assertFixtureIntact(t, s)

			// D2's default-off is the whole security posture of the feature: a node
			// that migrates forward must not start disclosing anything because it
			// was updated. This assertion is the one that would catch a future
			// change to the column default.
			var enabled int
			if err := s.db.QueryRow(`SELECT enabled FROM public_view_settings WHERE id = 1`).Scan(&enabled); err != nil {
				t.Fatalf("public_view_settings row missing after migration: %v", err)
			}
			if enabled != 0 {
				t.Error("a migrated node came up with the public view already enabled — D2 requires opt-in")
			}
			var retention int
			if err := s.db.QueryRow(`SELECT retention_hours FROM public_view_settings WHERE id = 1`).Scan(&retention); err != nil {
				t.Fatal(err)
			}
			if retention != 24 {
				t.Errorf("migrated retention_hours = %d, want the 24 h default (D6)", retention)
			}
			var branding int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM branding WHERE id = 1`).Scan(&branding); err != nil {
				t.Fatal(err)
			}
			if branding != 1 {
				t.Errorf("branding singleton row count = %d, want 1", branding)
			}

			if !tc.withAdmin {
				return
			}
			// The v2 credential now finishes in accounts, not in admin: the accounts
			// step (v6) moves it and drops the old table. This assertion used to read
			// admin.role; it reads the destination now because that is where a v2
			// node's credential ends up, and the claim — "the existing credential
			// arrives as an admin" — is the same one.
			var username, role string
			if err := s.db.QueryRow(`SELECT username, role FROM accounts`).Scan(&username, &role); err != nil {
				t.Fatalf("the v2 admin credential did not reach accounts: %v", err)
			}
			if role != "admin" {
				t.Errorf("existing credential migrated to role %q, want %q", role, "admin")
			}
			if username != "W4RJM" {
				t.Errorf("migrated username = %q, want the v2 fixture's W4RJM", username)
			}
			if err := s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='admin'`).Scan(new(int)); err == nil {
				t.Error("the admin table survived the migration; the accounts step drops it")
			}
		})
	}
}

// TestMigrationIsIdempotent runs Open twice over the same database. The second
// call finds it at head and runs nothing, but the DDL and seed are written to be
// re-runnable regardless — a step that is only correct once is a step that breaks
// whenever a future version has to replay it.
func TestMigrationIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.db")
	writeV2Fixture(t, path, true)

	for i := range 2 {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		v, err := s.Version()
		if err != nil {
			t.Fatal(err)
		}
		if v != SchemaVersion {
			t.Errorf("Open #%d left version %d, want %d", i+1, v, SchemaVersion)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// Re-running must not have duplicated the singleton rows or reset them. An
	// operator's opt-in surviving a restart is the property that matters.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test cleanup
	for _, tbl := range []string{"public_view_settings", "branding"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s has %d rows after two opens, want 1", tbl, n)
		}
	}
}

// TestSeedPreservesOperatorChoice is the other half of idempotency, and the one
// with teeth: an operator who turned the public view on must not find it off
// again after a restart re-runs the seed.
func TestSeedPreservesOperatorChoice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.db")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE public_view_settings SET enabled = 1, retention_hours = 72 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close() //nolint:errcheck // test cleanup
	var (
		enabled   int
		retention int
	)
	if err := s2.db.QueryRow(`SELECT enabled, retention_hours FROM public_view_settings WHERE id = 1`).Scan(&enabled, &retention); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 || retention != 72 {
		t.Errorf("reopen reset the operator's settings: enabled = %d, retention = %d, want 1 and 72", enabled, retention)
	}
}

// TestPreMigrationBackup is the recovery half of property 4: the copy RFC-0001
// mandates exists, is a real database, and is still at the version the previous
// build understood — which is what makes an RFC-0014 revert across a migration
// survivable (RFC-0017 open question 2).
func TestPreMigrationBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.db")
	writeV1Fixture(t, path, false)

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	backup := BackupPath(path, 1)
	if !fileExists(backup) {
		t.Fatalf("no pre-migration copy at %s", backup)
	}

	// Open the copy raw — Open() would migrate it, which is exactly what an older
	// build recovering from it must NOT have already happened.
	db, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test cleanup
	var v int
	if err := db.QueryRow(`SELECT schema_version FROM meta WHERE id = 1`).Scan(&v); err != nil {
		t.Fatalf("pre-migration copy is not a readable store: %v", err)
	}
	if v != 1 {
		t.Errorf("pre-migration copy is at v%d, want v1 (the version the prior build understands)", v)
	}
	var callsign string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'general'`).Scan(&callsign); err != nil {
		t.Fatalf("pre-migration copy lost its settings: %v", err)
	}
	if callsign != fixtureSettings["general"] {
		t.Errorf("pre-migration copy has general = %s, want %s", callsign, fixtureSettings["general"])
	}
}

// TestRefusesNewerSchema is the rollback-safety guarantee, and that the refusal
// points at the recovery file rather than just failing.
func TestRefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.db")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE meta SET schema_version = ? WHERE id = 1`, SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(path)
	if !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("Open on a newer schema = %v, want ErrSchemaNewer", err)
	}

	// With a pre-migration copy beside it, the refusal names the file to recover from.
	backup := BackupPath(path, SchemaVersion)
	if err := os.WriteFile(backup, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Open(path)
	if err == nil || !strings.Contains(err.Error(), backup) {
		t.Errorf("refusal does not name the pre-migration copy: %v", err)
	}
}

// TestInterruptedMigrationLeavesPriorVersion is the crash-injection half of
// property 4. A step that fails part-way must leave the database at the version it
// started at — not half-applied — so the next start simply retries the ladder, and
// the pre-migration copy is there either way.
func TestInterruptedMigrationLeavesPriorVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.db")
	writeV1Fixture(t, path, false)

	s, err := Open(path) // brings it to head normally
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck // test cleanup

	// Inject a step that does real DDL and then fails, standing in for a crash
	// part-way through a future migration.
	boom := SchemaVersion + 1
	orig := migrations
	t.Cleanup(func() { migrations = orig })
	migrations = append(append([]migration{}, orig...), migration{
		to:   boom,
		name: "crash-injected",
		fn: func(tx *sql.Tx) error {
			if _, err := tx.Exec(`ALTER TABLE settings ADD COLUMN doomed TEXT`); err != nil {
				return err
			}
			return fmt.Errorf("crash injected mid-step")
		},
	})

	err = s.migrateFrom(SchemaVersion)
	if err == nil {
		t.Fatal("migrateFrom succeeded on an injected failure")
	}
	if backup := BackupPath(path, SchemaVersion); !strings.Contains(err.Error(), backup) {
		t.Errorf("failure does not name the pre-migration copy %s: %v", backup, err)
	}

	// The version did not move: the step and its bump shared one transaction.
	v, verr := s.Version()
	if verr != nil {
		t.Fatal(verr)
	}
	if v != SchemaVersion {
		t.Errorf("after an interrupted migration, version = %d, want %d (unchanged)", v, SchemaVersion)
	}
	// And the partial DDL rolled back with it.
	has, herr := hasColumnDB(s.db, "settings", "doomed")
	if herr != nil {
		t.Fatal(herr)
	}
	if has {
		t.Error("an interrupted migration left its partial DDL behind")
	}
	assertFixtureIntact(t, s)
}

// hasColumnDB is txHasColumn against the database rather than a transaction.
func hasColumnDB(db *sql.DB, table, col string) (bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close() //nolint:errcheck // rows.Err() below reports iteration failures
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// v4DDL is the schema exactly as shipped at SchemaVersion 4 — the public-view and
// heard-position tables, and no phonebook. Frozen like v1DDL and v2DDL: it is the
// last shape that existed before the phonebook step, and updating it when the
// current schema changes would destroy the only thing it is for.
//
// Only the tables the phonebook step has to coexist with are reproduced. The step
// creates one table and touches nothing else, so a fixture carrying every v4 table
// verbatim would add lines without adding a claim; TestMigratedMatchesFresh is
// what proves the whole shape agrees, from v1.
const v4DDL = `
CREATE TABLE meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  schema_version INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  claimed_at TEXT,
  provisioned INTEGER
);
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  updated_by TEXT NOT NULL
);
CREATE TABLE applies (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  at         TEXT NOT NULL,
  by         TEXT NOT NULL,
  diff       TEXT NOT NULL
);`

// writeV4Fixture builds a schema-v4 database holding operator data — the shape a
// node in the field has immediately before the phonebook step runs.
func writeV4Fixture(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test cleanup
	if _, err := db.Exec(v4DDL); err != nil {
		t.Fatal(err)
	}
	// The public-view tables are created from the same const the baseline uses:
	// they are not what this fixture is pinning, and a frozen copy of them here
	// would be a second place to update for no gain.
	if _, err := db.Exec(publicViewDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(publicViewSeed); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(heardPositionsDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO meta(id, schema_version, created_at, claimed_at) VALUES(1, 4, '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for k, v := range fixtureSettings {
		if _, err := db.Exec(`INSERT INTO settings(key, value, updated_at, updated_by) VALUES(?, ?, '2026-01-01T00:00:00Z', 'fixture')`, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO applies(at, by, diff) VALUES('2026-01-01T00:00:00Z', 'fixture', '{"general.callsign":"KN4OQW"}')`); err != nil {
		t.Fatal(err)
	}
}

// TestMigratesV4ToHead exercises the phonebook step against a node that already
// exists, which is the only kind of node it ever runs on.
//
// The assertions are about what the table can and cannot hold, not merely that it
// appeared: the uniqueness rules are the schema's half of "already in the
// phonebook", and a migration that produced the table without them would leave a
// node whose panel accepts two rows for one operator.
func TestMigratesV4ToHead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.db")
	writeV4Fixture(t, path)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a v4 store: %v", err)
	}
	defer s.Close() //nolint:errcheck // test cleanup

	v, err := s.Version()
	if err != nil {
		t.Fatal(err)
	}
	if v != SchemaVersion {
		t.Errorf("after Open, version = %d, want %d", v, SchemaVersion)
	}
	assertFixtureIntact(t, s)

	// A migrated node starts with an empty phonebook — the same state a fresh one
	// is created in. There is nothing to backfill and nothing to seed.
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM phonebook`).Scan(&n); err != nil {
		t.Fatalf("phonebook table missing after migration: %v", err)
	}
	if n != 0 {
		t.Errorf("migrated node came up with %d phonebook rows, want 0", n)
	}

	if _, err := s.db.Exec(
		`INSERT INTO phonebook(callsign, dmr_id, full_name, email, created_at, updated_at)
		 VALUES('KN4OQW', 3180202, 'Clint', 'clint@example.invalid', '2026-01-03T00:00:00Z', '2026-01-03T00:00:00Z')`); err != nil {
		t.Fatalf("phonebook not writable after migration: %v", err)
	}
	// Callsign uniqueness is case-insensitive: the UNIQUE index inherits the
	// column's NOCASE collation, so a differently-cased duplicate is refused by the
	// schema and not only by the validator above it.
	if _, err := s.db.Exec(
		`INSERT INTO phonebook(callsign, created_at, updated_at) VALUES('kn4oqw', 'x', 'x')`); err == nil {
		t.Error("a differently-cased duplicate callsign was accepted; the UNIQUE index is not NOCASE")
	}
	if _, err := s.db.Exec(
		`INSERT INTO phonebook(callsign, dmr_id, created_at, updated_at) VALUES('W1AW', 3180202, 'x', 'x')`); err == nil {
		t.Error("a duplicate dmr_id was accepted; the UNIQUE constraint is missing")
	}
	// Two rows with no DMR ID at all must coexist: SQLite's unique index permits
	// any number of NULLs, which is what makes "not every operator has one"
	// representable for every row at once rather than for one.
	for _, c := range []string{"W1AW", "N0CALL"} {
		if _, err := s.db.Exec(
			`INSERT INTO phonebook(callsign, created_at, updated_at) VALUES(?, 'x', 'x')`, c); err != nil {
			t.Errorf("second row with a NULL dmr_id refused (%s): %v", c, err)
		}
	}
}

// v5DDL is the schema exactly as shipped at SchemaVersion 5 — the last shape
// before accounts existed. Frozen like the fixtures above it: this is the tree a
// node in the field is running the day the accounts step arrives.
//
// Only the tables the accounts step touches or reads are reproduced. It creates
// one table, moves one row, adds one column and drops one table; a fixture
// carrying every v5 table verbatim would add lines without adding a claim, and
// TestMigratedMatchesFresh is what proves the whole shape agrees.
const v5DDL = `
CREATE TABLE meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  schema_version INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  claimed_at TEXT,
  provisioned INTEGER
);
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  updated_by TEXT NOT NULL
);
CREATE TABLE applies (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  at         TEXT NOT NULL,
  by         TEXT NOT NULL,
  diff       TEXT NOT NULL
);
CREATE TABLE admin (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  username      TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  params        TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  role          TEXT NOT NULL DEFAULT 'admin'
);
CREATE TABLE sessions (
  token_hash TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  last_seen  TEXT NOT NULL
);`

// The credential and session a v5 node is carrying. The hash is not a real
// argon2id record — nothing here verifies it — but it is a distinctive string, so
// a migration that rewrote or re-derived it instead of copying it would be
// visible rather than plausible.
const (
	v5Hash    = "$argon2id$v=19$FIXTURE-SALT-AND-DIGEST-DO-NOT-REWRITE"
	v5Params  = "m=65536,t=1,p=4"
	v5Token   = "fixture-session-token-hash"
	v5Created = "2026-01-01T00:00:00Z"
)

// writeV5Fixture builds a claimed schema-v5 node with a live session.
func writeV5Fixture(t *testing.T, path string, claimed bool) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test cleanup
	for _, ddl := range []string{v5DDL, publicViewDDL, publicViewSeed, heardPositionsDDL, phonebookDDL} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	claimedAt := "'2026-01-02T00:00:00Z'"
	if !claimed {
		claimedAt = "NULL"
	}
	if _, err := db.Exec(`INSERT INTO meta(id, schema_version, created_at, claimed_at) VALUES(1, 5, ?, `+claimedAt+`)`, v5Created); err != nil {
		t.Fatal(err)
	}
	for k, v := range fixtureSettings {
		if _, err := db.Exec(`INSERT INTO settings(key, value, updated_at, updated_by) VALUES(?, ?, ?, 'fixture')`, k, v, v5Created); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO applies(at, by, diff) VALUES(?, 'fixture', '{"general.callsign":"KN4OQW"}')`, v5Created); err != nil {
		t.Fatal(err)
	}
	// A phonebook the reset path must never touch and the migration must not link to.
	if _, err := db.Exec(`INSERT INTO phonebook(callsign, dmr_id, full_name, created_at, updated_at)
		VALUES('KN4OQW', 3180202, 'Clint Chance', ?, ?)`, v5Created, v5Created); err != nil {
		t.Fatal(err)
	}
	if !claimed {
		return
	}
	if _, err := db.Exec(`INSERT INTO admin(id, username, password_hash, params, created_at, role)
		VALUES(1, 'kn4oqw', ?, ?, ?, 'admin')`, v5Hash, v5Params, v5Created); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(token_hash, created_at, expires_at, last_seen)
		VALUES(?, ?, '2026-02-01T00:00:00Z', ?)`, v5Token, v5Created, v5Created); err != nil {
		t.Fatal(err)
	}
}

// TestMigratesV5ToHead is the accounts step against the only kind of node it ever
// runs on: one that is already claimed, with a credential somebody is using and a
// session somebody is holding.
//
// Every assertion here is a promise the amendment makes about this migration, and
// each would be a real incident if it broke — a rewritten hash locks the operator
// out, a revoked session logs them out mid-update, a forced rotation demands a
// change nothing prompted, and a lost phonebook is data loss.
func TestMigratesV5ToHead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.db")
	writeV5Fixture(t, path, true)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a v5 store: %v", err)
	}
	defer s.Close() //nolint:errcheck // test cleanup

	if v, err := s.Version(); err != nil || v != SchemaVersion {
		t.Fatalf("after Open, version = %d (%v), want %d", v, err, SchemaVersion)
	}
	assertFixtureIntact(t, s)

	var (
		id      int64
		pbID    sql.NullInt64
		user, h string
		params  string
		role    string
		rotate  int
		created string
		updated string
	)
	if err := s.db.QueryRow(`SELECT id, phonebook_id, username, password_hash, params, role, must_rotate, created_at, updated_at
		FROM accounts`).Scan(&id, &pbID, &user, &h, &params, &role, &rotate, &created, &updated); err != nil {
		t.Fatalf("the admin credential did not reach accounts: %v", err)
	}

	// The hash is copied byte-for-byte: the operator's password keeps working.
	if h != v5Hash {
		t.Errorf("password_hash was rewritten:\n got %q\nwant %q", h, v5Hash)
	}
	if params != v5Params {
		t.Errorf("params were rewritten:\n got %q\nwant %q", params, v5Params)
	}
	if user != "kn4oqw" || role != "admin" {
		t.Errorf("migrated as %q/%q, want kn4oqw/admin", user, role)
	}
	// No rotation is forced: they chose that password themselves at claim time.
	if rotate != 0 {
		t.Error("the migrated admin was flagged for rotation; nothing prompted one")
	}
	// No phonebook link is invented, even though a matching row exists.
	if pbID.Valid {
		t.Errorf("the migration linked the admin to phonebook row %d; it must not guess an identity", pbID.Int64)
	}
	if created != v5Created {
		t.Errorf("created_at = %q, want the original %q", created, v5Created)
	}
	if updated != created {
		t.Errorf("updated_at = %q, want it seeded from created_at %q", updated, created)
	}

	// The live session survives and now belongs to the migrated account.
	var sessAccount sql.NullInt64
	if err := s.db.QueryRow(`SELECT account_id FROM sessions WHERE token_hash = ?`, v5Token).Scan(&sessAccount); err != nil {
		t.Fatalf("the live session did not survive the migration: %v", err)
	}
	if !sessAccount.Valid || sessAccount.Int64 != id {
		t.Errorf("session account_id = %v, want the migrated account %d", sessAccount, id)
	}

	// The old table is gone, so nothing can read a credential from two places.
	if err := s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='admin'`).Scan(new(int)); err == nil {
		t.Error("the admin table survived the migration")
	}

	// The phonebook is untouched.
	var pbCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM phonebook`).Scan(&pbCount); err != nil {
		t.Fatal(err)
	}
	if pbCount != 1 {
		t.Errorf("phonebook has %d rows after the migration, want the 1 it started with", pbCount)
	}
}

// TestMigratesV5UnclaimedToHead: a node that was never claimed has no credential
// and no session, and must still arrive at head with an empty accounts table
// rather than with a fabricated row.
func TestMigratesV5UnclaimedToHead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.db")
	writeV5Fixture(t, path, false)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on an unclaimed v5 store: %v", err)
	}
	defer s.Close() //nolint:errcheck // test cleanup

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&n); err != nil {
		t.Fatalf("accounts missing after migration: %v", err)
	}
	if n != 0 {
		t.Errorf("an unclaimed node came out of the migration with %d accounts, want 0", n)
	}
}
