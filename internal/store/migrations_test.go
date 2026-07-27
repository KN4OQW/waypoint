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
