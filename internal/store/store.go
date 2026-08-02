// Package store is waypointd's authoritative configuration store: a single
// SQLite database of typed settings. Per RFC-0001, the daemons' INI files are
// deterministic compiled outputs of this store and are never parsed back — the
// store is the read model, the write target, and the source of truth, with a
// schema between them. That removes the incumbent platforms' entire family of
// "the config writer clobbered an unrelated setting" bugs.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: cross-compiles CGO-free to armv6
)

// SchemaVersion is the current store schema. The daemon refuses to open a
// database from a newer version (rollback safety) and migrates older ones forward
// through the ladder in migrations.go, taking a pre-migration copy first.
//
// Bumping this is a release-visible act: an older waypointd will refuse the
// migrated database, so a release that raises it must also raise the update
// manifest's min_version (RFC-0014) to the first release that ships the new
// version. See docs/updates.md.
const SchemaVersion = 4

// ErrSchemaNewer is returned by Open when the database was written by a newer
// build. It is a distinct error because it is the one open failure with a real
// recovery path — a pre-migration copy (BackupPath) usually sits beside it.
var ErrSchemaNewer = errors.New("store: database schema is newer than this build")

// Store is a handle to the configuration database. It is the only writer.
type Store struct {
	db *sql.DB
	// path is the database file, retained so the migration ladder can write its
	// pre-migration copy beside it. Empty for ":memory:".
	path string
	// migratedFrom and backupFile record what Open did, for callers that must act
	// on a migration having happened. The update engine is the one that must: an
	// update whose new build migrates the store and then fails its health gate is
	// reverted to a binary that would refuse the migrated schema, so the marker it
	// left behind has to learn where the pre-migration copy is (RFC-0014 revert,
	// RFC-0017 open question 2).
	migratedFrom int
	backupFile   string
}

// Migrated reports whether Open migrated this database forward: the version it
// came from, the pre-migration copy left behind, and whether a migration happened
// at all. backup is empty for an in-memory store, which has nothing to recover to.
func (s *Store) Migrated() (from int, backup string, ok bool) {
	return s.migratedFrom, s.backupFile, s.migratedFrom != 0
}

// Open opens (creating if needed) the config database at path and ensures the
// schema is present and compatible. Use ":memory:" for tests.
func Open(path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		// WAL + a busy timeout so a reader never trips a concurrent apply.
		dsn = path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; keeps SQLite happy under :memory: too
	s := &Store{db: db, path: path}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// publicViewDDL is the public dashboard's schema (D1–D8). It lives in its own
// const rather than inline in the baseline because the ladder step that installs
// it on an existing database executes this exact text — the baseline and the
// migration cannot drift apart if there is only one of them to drift.
//
// The public surface is opt-in and default-off (D2): a node that has never been
// configured for it has enabled = 0, and every public route 404s. The per-field
// toggles decide what a visitor sees once it is on; the never-public list (D2) is
// not represented here at all, because it is enforced by the shape of the response
// structs rather than by a setting an operator could flip.
//
// Both single-row tables are keyed id = 1 so a second INSERT is a primary-key
// conflict, matching meta's convention. Their defaults live on the columns, so the
// seed below is a bare INSERT that adopts them.
const publicViewDDL = `
CREATE TABLE IF NOT EXISTS public_view_settings (
  id               INTEGER PRIMARY KEY CHECK (id = 1),
  enabled          INTEGER NOT NULL DEFAULT 0,   -- D2: opt-in, default off
  -- Reach card ("how to reach this node"), default on when public view is on.
  show_freq        INTEGER NOT NULL DEFAULT 1,
  show_cc_ts       INTEGER NOT NULL DEFAULT 1,
  show_talkgroup   INTEGER NOT NULL DEFAULT 1,
  show_mode        INTEGER NOT NULL DEFAULT 1,
  -- Activity modules (D5).
  show_status      INTEGER NOT NULL DEFAULT 1,
  show_counters    INTEGER NOT NULL DEFAULT 1,
  show_lastheard   INTEGER NOT NULL DEFAULT 1,
  -- Location. Off by default: a grid square is the one reach-card field that
  -- discloses where the operator is, so it is opted into, not out of (D3).
  show_grid        INTEGER NOT NULL DEFAULT 0,
  show_power_line  INTEGER NOT NULL DEFAULT 1,
  show_links       INTEGER NOT NULL DEFAULT 1,
  show_nets        INTEGER NOT NULL DEFAULT 1,
  show_map         INTEGER NOT NULL DEFAULT 1,
  show_qr          INTEGER NOT NULL DEFAULT 1,
  -- D6: bounds are 1..168 h; the model layer clamps, and the CHECK is the
  -- backstop for anything that reaches the table another way.
  retention_hours  INTEGER NOT NULL DEFAULT 24 CHECK (retention_hours BETWEEN 1 AND 168),
  power_line       TEXT NOT NULL DEFAULT '',
  purpose_tags     TEXT NOT NULL DEFAULT '[]',   -- JSON array of predefined keys
  purpose_freetext TEXT NOT NULL DEFAULT '',
  grid_override    TEXT NOT NULL DEFAULT ''      -- manual 6-char Maidenhead, optional
);
-- D8: third-party callsigns excluded from every public output. Stored normalized
-- (uppercase, SSID/suffix stripped) so the comparison is a primary-key lookup
-- rather than a scan with per-row normalization.
CREATE TABLE IF NOT EXISTS public_suppress_list (
  callsign   TEXT PRIMARY KEY,
  added_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS public_links (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  label      TEXT NOT NULL,
  url        TEXT NOT NULL,               -- http/https only, validated at write time
  sort_order INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS public_nets (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT NOT NULL,
  schedule_text TEXT NOT NULL,            -- free text, e.g. "Thursdays 20:00 local"
  target        TEXT NOT NULL,            -- TG / reflector
  note          TEXT NOT NULL DEFAULT '',
  sort_order    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS branding (
  id                 INTEGER PRIMARY KEY CHECK (id = 1),
  logo_path          TEXT,                -- nullable: no logo uploaded
  narrative_markdown TEXT NOT NULL DEFAULT '',
  custom_html        TEXT NOT NULL DEFAULT ''
);`

// heardPositionsDDL is the locally-heard position store (D3).
//
// Positions come from the mesh transports RFC-0018 describes — Meshtastic,
// MeshCore, LoRa APRS — and from nowhere else. APRS-IS is cut entirely, and the
// aprs.fi API is never consulted: republishing either would be someone else's data
// under this node's name, and in aprs.fi's case against its terms.
//
// One row per station per transport, upserted. A position report supersedes the
// previous one from the same station rather than accumulating, because the
// question a map answers is "where is this station now" and a history of a
// third party's movements is a far larger disclosure than a current location.
// That is a privacy decision expressed as a schema: the table cannot leak a track
// because it does not hold one.
const heardPositionsDDL = `
CREATE TABLE IF NOT EXISTS heard_positions (
  station    TEXT NOT NULL,             -- callsign, or the transport's node id
  transport  TEXT NOT NULL,             -- meshtastic | meshcore | lora_aprs | ...
  lat        REAL NOT NULL,
  lon        REAL NOT NULL,
  heard_at   TEXT NOT NULL,             -- RFC-3339 UTC
  PRIMARY KEY (station, transport)
);
CREATE INDEX IF NOT EXISTS idx_heard_positions_at ON heard_positions (heard_at);`

// publicViewSeed materializes the single rows so readers never have to special-case
// "configured but never written". Both are INSERT OR IGNORE: re-running is a no-op,
// and an existing row keeps whatever the operator set.
const publicViewSeed = `
INSERT OR IGNORE INTO public_view_settings(id) VALUES(1);
INSERT OR IGNORE INTO branding(id) VALUES(1);`

func (s *Store) init() error {
	// The baseline is always the CURRENT schema: a fresh database is created at
	// head and runs no migrations. Anything added here must also arrive as a ladder
	// step in migrations.go for databases that already exist — the fixture tests
	// compare a migrated database against a fresh one to keep the two in step.
	//
	// claimed_at (RFC-0002) and provisioned (provision.Mirror) are owned
	// semantically by those subsystems; meta carries them because they are node
	// identity, not config, and must never appear in the settings key tree.
	const ddl = `
CREATE TABLE IF NOT EXISTS meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  schema_version INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  claimed_at TEXT,
  provisioned INTEGER
);
CREATE TABLE IF NOT EXISTS settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,            -- JSON
  updated_at TEXT NOT NULL,
  updated_by TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS applies (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  at         TEXT NOT NULL,
  by         TEXT NOT NULL,
  diff       TEXT NOT NULL             -- JSON summary of what changed
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return err
	}
	if _, err := s.db.Exec(publicViewDDL); err != nil {
		return err
	}
	if _, err := s.db.Exec(publicViewSeed); err != nil {
		return err
	}
	if _, err := s.db.Exec(heardPositionsDDL); err != nil {
		return err
	}

	var ver int
	err := s.db.QueryRow(`SELECT schema_version FROM meta WHERE id = 1`).Scan(&ver)
	switch err {
	case sql.ErrNoRows:
		_, err = s.db.Exec(`INSERT INTO meta(id, schema_version, created_at) VALUES(1, ?, ?)`,
			SchemaVersion, now())
		return err
	case nil:
		if ver > SchemaVersion {
			// Refusing is the rollback-safety guarantee: a newer build may have
			// rewritten values this one would misread. Name the pre-migration copy when
			// one is there, because that file is the whole recovery path.
			if b := BackupPath(s.path, SchemaVersion); s.path != "" && s.path != ":memory:" && fileExists(b) {
				return fmt.Errorf("%w: database is v%d, this build is v%d; a pre-migration copy of v%d is at %s",
					ErrSchemaNewer, ver, SchemaVersion, SchemaVersion, b)
			}
			return fmt.Errorf("%w: database is v%d, this build is v%d", ErrSchemaNewer, ver, SchemaVersion)
		}
		if ver < SchemaVersion {
			return s.migrateFrom(ver)
		}
		return nil
	default:
		return err
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Version returns the schema version recorded in the database. After a successful
// Open it equals SchemaVersion; it is exported so the daemon can report and log
// what it migrated (and so tests can assert the ladder ran).
func (s *Store) Version() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT schema_version FROM meta WHERE id = 1`).Scan(&v)
	return v, err
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB returns the underlying database handle. It exists for subsystems that own
// dedicated tables outside the settings key tree — the auth subsystem (RFC-0002)
// keeps the admin credential and sessions in their own tables so they never touch
// the config surface (never a settings key, never in the config view, never in a
// profile). Those tables share this one connection (SetMaxOpenConns(1)), so their
// writes serialize with config writes rather than contending for the file lock.
// Config code must not reach through this to the settings/applies tables — use the
// typed Get/Set/All methods for those.
func (s *Store) DB() *sql.DB { return s.db }

// Get returns the raw JSON value for key and whether it was present.
func (s *Store) Get(key string) (json.RawMessage, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return json.RawMessage(v), true, nil
}

// GetInto unmarshals key's value into dest. Missing keys leave dest untouched
// and report found=false.
func (s *Store) GetInto(key string, dest any) (found bool, err error) {
	raw, ok, err := s.Get(key)
	if err != nil || !ok {
		return false, err
	}
	return true, json.Unmarshal(raw, dest)
}

// Set writes key to the JSON encoding of value, attributing the change to by.
// It never touches any other key — the isolation the incumbent writers lacked.
func (s *Store) Set(key string, value any, by string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO settings(key, value, updated_at, updated_by) VALUES(?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		key, string(raw), now(), by)
	return err
}

// SetMany writes several settings in a single transaction, attributing every
// change to by. It is all-or-nothing: either every key is upserted or none is, so
// a caller that must switch a set of sections together (a profile activation,
// RFC-0006) can never leave a half-applied hybrid on a crash mid-write. Like Set,
// it touches only the named keys. Values are raw JSON, stored verbatim.
func (s *Store) SetMany(values map[string]json.RawMessage, by string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after a successful Commit
	stmt, err := tx.Prepare(
		`INSERT INTO settings(key, value, updated_at, updated_by) VALUES(?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	at := now()
	for k, v := range values {
		if _, err := stmt.Exec(k, string(v), at, by); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// All returns every setting as key -> raw JSON.
func (s *Store) All() (map[string]json.RawMessage, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = json.RawMessage(v)
	}
	return out, rows.Err()
}

// IsEmpty reports whether the store has no settings yet (used to decide whether
// to seed from the existing INIs on first run).
func (s *Store) IsEmpty() (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// RecordApply journals an apply with a diff summary.
func (s *Store) RecordApply(by string, diff any) error {
	raw, err := json.Marshal(diff)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO applies(at, by, diff) VALUES(?, ?, ?)`, now(), by, string(raw))
	return err
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
