package auth

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/store"
)

// isPrimaryKeyConflict reports whether err is SQLite's primary-key/unique
// constraint violation — the signal that a second concurrent Claim tried to
// insert the fixed admin id=1 and lost the race. modernc.org/sqlite surfaces the
// extended result code (SQLITE_CONSTRAINT_PRIMARYKEY / _UNIQUE) via a Code()
// method; fall back to the message text so this stays driver-string-tolerant.
func isPrimaryKeyConflict(err error) bool {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		switch coded.Code() {
		case 1555, 2067: // SQLITE_CONSTRAINT_PRIMARYKEY, SQLITE_CONSTRAINT_UNIQUE
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "constraint failed") &&
		(strings.Contains(msg, "primary key") || strings.Contains(msg, "unique"))
}

// ErrAlreadyClaimed is returned by Claim when the device already has an admin
// credential. The claim handler maps it to 409 Conflict.
var ErrAlreadyClaimed = errors.New("auth: device already claimed")

// Store is the auth subsystem's persistence. It owns the admin and sessions
// tables and the meta.claimed_at stamp — all outside the config settings tree, so
// none of it is reachable through /api/config or an exported profile. It shares
// waypointd's single store connection (see store.Store.DB), so its writes
// serialize with config writes.
type Store struct {
	db *sql.DB
}

// NewStore attaches the auth subsystem to the configuration store and ensures its
// tables exist. It is idempotent: on an already-migrated store it is a no-op.
func NewStore(s *store.Store) (*Store, error) {
	as := &Store{db: s.DB()}
	if err := as.migrate(); err != nil {
		return nil, err
	}
	return as, nil
}

// migrate creates the auth tables and adds meta.claimed_at. These tables sit
// beside the config store's own (meta, settings, applies) but are never read by
// config code — the config read model comes only from the settings key tree.
//
//   - admin: the single admin credential (id fixed to 1 so a second INSERT is a
//     primary-key conflict — the atomic guard the claim race relies on).
//   - sessions: server-side sessions keyed by the SHA-256 of the cookie token.
//   - meta.claimed_at: the timestamp of the winning claim; null means unclaimed.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS admin (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  username      TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  params        TEXT NOT NULL,
  created_at    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  last_seen  TEXT NOT NULL
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return err
	}
	// meta predates this RFC, so add claimed_at only when it is missing. SQLite
	// has no "ADD COLUMN IF NOT EXISTS"; probe the column list first so a re-run is
	// a clean no-op rather than a duplicate-column error.
	has, err := s.hasColumn("meta", "claimed_at")
	if err != nil {
		return err
	}
	if !has {
		if _, err := s.db.Exec(`ALTER TABLE meta ADD COLUMN claimed_at TEXT`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) hasColumn(table, col string) (bool, error) {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
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

// Admin is the stored admin credential (never the plaintext password).
type Admin struct {
	Username  string
	Record    HashRecord
	CreatedAt time.Time
}

// IsClaimed reports whether the device has been claimed: an admin credential
// exists and meta.claimed_at is set. Both conditions are required so a partially
// written state can never read as claimed.
func (s *Store) IsClaimed() (bool, error) {
	var claimedAt sql.NullString
	if err := s.db.QueryRow(`SELECT claimed_at FROM meta WHERE id = 1`).Scan(&claimedAt); err != nil {
		return false, err
	}
	if !claimedAt.Valid || claimedAt.String == "" {
		return false, nil
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM admin WHERE id = 1`).Scan(&n); err != nil {
		return false, err
	}
	return n == 1, nil
}

// Claim writes the admin credential and stamps meta.claimed_at in a single
// transaction. The first claim wins: the admin row has a fixed id of 1, so a
// second concurrent claim's INSERT hits a primary-key conflict and returns
// ErrAlreadyClaimed. There is no window in which two callers both own the device.
func (s *Store) Claim(username string, rec HashRecord, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	stamp := at.UTC().Format(time.RFC3339)
	if _, err := tx.Exec(
		`INSERT INTO admin(id, username, password_hash, params, created_at) VALUES(1, ?, ?, ?, ?)`,
		username, rec.Hash, rec.Params, stamp); err != nil {
		if isPrimaryKeyConflict(err) {
			return ErrAlreadyClaimed
		}
		return err
	}
	if _, err := tx.Exec(`UPDATE meta SET claimed_at = ? WHERE id = 1`, stamp); err != nil {
		return err
	}
	return tx.Commit()
}

// Admin returns the stored credential and whether one exists.
func (s *Store) Admin() (Admin, bool, error) {
	var a Admin
	var created string
	err := s.db.QueryRow(
		`SELECT username, password_hash, params, created_at FROM admin WHERE id = 1`).
		Scan(&a.Username, &a.Record.Hash, &a.Record.Params, &created)
	if err == sql.ErrNoRows {
		return Admin{}, false, nil
	}
	if err != nil {
		return Admin{}, false, err
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return a, true, nil
}

// ResetClaim returns the device to the unclaimed state: it wipes the admin
// credential, revokes every session, and clears meta.claimed_at — atomically, so
// a reset can never leave a credential without its claim stamp or vice versa.
// This is the common core of both reset paths (the reset-claim subcommand and the
// boot-partition marker).
func (s *Store) ResetClaim() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	if _, err := tx.Exec(`DELETE FROM admin`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE meta SET claimed_at = NULL WHERE id = 1`); err != nil {
		return err
	}
	return tx.Commit()
}

// Session is a stored server-side session. The raw token lives only in the client
// cookie; TokenHash is its SHA-256.
type Session struct {
	TokenHash string
	CreatedAt time.Time
	ExpiresAt time.Time
	LastSeen  time.Time
}

// CreateSession inserts a session for the given token hash.
func (s *Store) CreateSession(tokenHash string, created, expires time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions(token_hash, created_at, expires_at, last_seen) VALUES(?, ?, ?, ?)`,
		tokenHash, created.UTC().Format(time.RFC3339), expires.UTC().Format(time.RFC3339), created.UTC().Format(time.RFC3339))
	return err
}

// LookupSession returns the session for a token hash and whether it exists. The
// caller decides validity (idle expiry) from the returned timestamps.
func (s *Store) LookupSession(tokenHash string) (Session, bool, error) {
	sess := Session{TokenHash: tokenHash}
	var created, expires, seen string
	err := s.db.QueryRow(
		`SELECT created_at, expires_at, last_seen FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&created, &expires, &seen)
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	sess.CreatedAt, _ = time.Parse(time.RFC3339, created)
	sess.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
	sess.LastSeen, _ = time.Parse(time.RFC3339, seen)
	return sess, true, nil
}

// TouchSession slides a session forward on activity: it updates last_seen and
// pushes expires_at to the new idle deadline. This is what makes idle expiry a
// sliding window rather than an absolute one.
func (s *Store) TouchSession(tokenHash string, seen, expires time.Time) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET last_seen = ?, expires_at = ? WHERE token_hash = ?`,
		seen.UTC().Format(time.RFC3339), expires.UTC().Format(time.RFC3339), tokenHash)
	return err
}

// RevokeSession deletes one session (explicit logout actually revokes it).
func (s *Store) RevokeSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// SessionCount returns how many sessions currently exist. It is used by the
// reset-claim subcommand to report how many sessions a reset revoked.
func (s *Store) SessionCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
	return n, err
}

// SweepExpired deletes sessions whose idle deadline has passed. Best-effort
// housekeeping; validity is always re-checked at lookup regardless.
func (s *Store) SweepExpired(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now.UTC().Format(time.RFC3339))
	return err
}
