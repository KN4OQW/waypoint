package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The schema ladder (RFC-0001: "Schema migrations are explicit — numbered Go
// migration functions; the daemon refuses to run on a future schema (rollback
// safety) and migrates forward automatically with a pre-migration backup file").
//
// Two rules keep the ladder honest, and breaking either is how this class of code
// usually rots:
//
//  1. The baseline DDL in init() is always the CURRENT schema — a fresh database is
//     created at head and runs no migrations at all. A migration exists only to move
//     a database that already has rows.
//  2. Every step is therefore written twice, once in the baseline and once here, and
//     the two must agree. The fixture tests in migrations_test.go are what enforce
//     that: they build a database at each historical version, migrate it to head,
//     and compare its shape against a freshly created one.
//
// Steps run in one transaction each, together with the meta.schema_version bump, so
// an interrupted migration leaves the database at the last completed version rather
// than half-applied — and the ladder is safely re-runnable on the next start.

// migration is one numbered step. fn moves a database at version to-1 to version
// to; the runner commits the version bump in the same transaction.
type migration struct {
	to   int
	name string
	fn   func(*sql.Tx) error
}

// migrations is the ordered ladder. Append only — never renumber or rewrite a
// released step, because databases in the field have already run it.
var migrations = []migration{
	{to: 2, name: "meta-claim-and-provision-columns", fn: migrateMetaColumns},
	{to: 3, name: "public-view-tables-and-admin-role", fn: migratePublicView},
}

// migrateMetaColumns brings meta's claim and provisioning columns under the
// ladder. Both were previously added opportunistically by the subsystems that own
// them — auth's claimed_at and provision's provisioned — each with its own
// probe-then-ALTER, which meant meta's shape depended on which subsystems had
// happened to initialize rather than on the schema version. Those probes still run
// and are still correct; after this step they are a no-op, and the columns are
// guaranteed by version rather than by startup ordering.
//
// The subsystems keep owning the *semantics* of their columns (that placement is
// deliberate — see provision.Mirror); the ladder owns only their existence.
func migrateMetaColumns(tx *sql.Tx) error {
	for _, c := range []struct{ name, typ string }{
		{"claimed_at", "TEXT"},
		{"provisioned", "INTEGER"},
	} {
		has, err := txHasColumn(tx, "meta", c.name)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := tx.Exec(`ALTER TABLE meta ADD COLUMN ` + c.name + ` ` + c.typ); err != nil {
			return fmt.Errorf("add meta.%s: %w", c.name, err)
		}
	}
	return nil
}

// migratePublicView installs the public dashboard's tables (D1–D8) on a database
// that already has rows, and adds admin.role.
//
// The tables come from publicViewDDL — the same text the baseline executes — so
// there is no second copy of the schema to keep in step. The seed runs here too:
// an existing node must come out of the migration with the same single rows a
// fresh one is created with, and default-off is the whole point of D2, so a
// migrated node's public surface stays dark until an operator turns it on.
//
// admin.role is D1's "role-ready without a second migration": one column, one
// value ('admin'), no behavior attached. Multi-user is not being built now, but
// the schema change that would make it painful later is cheap today. The column is
// added rather than the table created, because the auth subsystem owns admin's
// shape (see auth.Store.migrate) — the ladder owns only the column's existence,
// the same division migrateMetaColumns settled for meta. A database that has never
// attached auth has no admin table yet; auth creates it at head shape, including
// role, so skipping it here is correct rather than a gap.
func migratePublicView(tx *sql.Tx) error {
	if _, err := tx.Exec(publicViewDDL); err != nil {
		return fmt.Errorf("create public view tables: %w", err)
	}
	if _, err := tx.Exec(publicViewSeed); err != nil {
		return fmt.Errorf("seed public view tables: %w", err)
	}
	has, err := txHasTable(tx, "admin")
	if err != nil {
		return err
	}
	if !has {
		return nil
	}
	hasCol, err := txHasColumn(tx, "admin", "role")
	if err != nil {
		return err
	}
	if hasCol {
		return nil
	}
	if _, err := tx.Exec(`ALTER TABLE admin ADD COLUMN role TEXT NOT NULL DEFAULT 'admin'`); err != nil {
		return fmt.Errorf("add admin.role: %w", err)
	}
	return nil
}

// headVersion is the version the ladder ends at. It must equal SchemaVersion; the
// mismatch is a build-time authoring error, caught by TestLadderReachesHead rather
// than by a node in the field.
func headVersion() int {
	if len(migrations) == 0 {
		return 1
	}
	return migrations[len(migrations)-1].to
}

// migrateFrom runs every ladder step above `from`, after taking the pre-migration
// backup RFC-0001 mandates. The backup is the rollback story for the hazard
// RFC-0017 names (open question 2): an update that migrates the store and then
// fails its health gate is reverted to the prior binary, which would otherwise
// meet a schema it refuses to open. The copy left here is what that older build
// can be pointed at.
func (s *Store) migrateFrom(from int) error {
	backup, err := s.backupBeforeMigrate(from)
	if err != nil {
		return err
	}
	s.migratedFrom, s.backupFile = from, backup
	for _, m := range migrations {
		if m.to <= from {
			continue
		}
		if err := s.applyMigration(m); err != nil {
			if backup != "" {
				return fmt.Errorf("store: migration to v%d (%s) failed; the database is unchanged at v%d and a pre-migration copy is at %s: %w",
					m.to, m.name, m.to-1, backup, err)
			}
			return fmt.Errorf("store: migration to v%d (%s) failed: %w", m.to, m.name, err)
		}
	}
	return nil
}

// applyMigration runs one step and its version bump in a single transaction.
// SQLite's DDL is transactional, so a failure mid-step rolls back the ALTERs too —
// the database is left exactly at the previous version, which is what makes the
// ladder re-runnable after a crash.
func (s *Store) applyMigration(m migration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	if err := m.fn(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE meta SET schema_version = ? WHERE id = 1`, m.to); err != nil {
		return err
	}
	return tx.Commit()
}

// BackupPath is where the pre-migration copy of the store at path lives, taken
// before migrating away from schema version v. It is exported because the recovery
// path is off-box: an operator (or an older waypointd) pointed at this file gets
// the store exactly as the previous version left it.
func BackupPath(path string, v int) string {
	return fmt.Sprintf("%s.pre-v%d", path, v)
}

// backupBeforeMigrate writes a consistent copy of the database aside before the
// ladder touches it. It returns "" for an in-memory store, which has no path and
// nothing to recover to.
//
// VACUUM INTO is SQLite's own transactionally consistent copy — unlike copying the
// file, it cannot capture a torn page or miss a WAL frame, and it needs no
// checkpoint or exclusive lock first.
func (s *Store) backupBeforeMigrate(from int) (string, error) {
	if s.path == "" || s.path == ":memory:" {
		return "", nil
	}
	dst := BackupPath(s.path, from)
	// VACUUM INTO refuses an existing target, so clear a copy left by an earlier
	// attempt. Losing it is safe: we are about to write the same version again.
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("store: clear stale pre-migration copy %s: %w", dst, err)
	}
	if _, err := s.db.Exec(`VACUUM INTO ?`, dst); err != nil {
		return "", fmt.Errorf("store: pre-migration backup to %s: %w", dst, err)
	}
	// The copy is worthless if it is still in the page cache when the power goes,
	// so make it durable — both the file and the directory entry naming it.
	if err := fsync(dst); err != nil {
		return "", fmt.Errorf("store: fsync pre-migration copy: %w", err)
	}
	if err := fsync(filepath.Dir(dst)); err != nil {
		return "", fmt.Errorf("store: fsync pre-migration copy's directory: %w", err)
	}
	return dst, nil
}

func fsync(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // Sync below is what reports a write failure
	return f.Sync()
}

// txHasTable reports whether table exists. A step that alters a table another
// subsystem owns has to ask, because whether that subsystem has ever run against
// this database is not something the schema version records.
func txHasTable(tx *sql.Tx, table string) (bool, error) {
	var name string
	err := tx.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// txHasColumn reports whether table has a column named col. SQLite has no
// "ADD COLUMN IF NOT EXISTS", so every column-adding step probes first.
func txHasColumn(tx *sql.Tx, table, col string) (bool, error) {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
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
