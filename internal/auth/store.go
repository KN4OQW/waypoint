package auth

import (
	"database/sql"
	"errors"
	"fmt"
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
//
// admin.role carries 'admin' and nothing else today. It exists so the public
// dashboard's two-level access model (D1: public and admin) can grow a second role
// without a schema migration on every node in the field. Nothing reads it yet, and
// no code should branch on it until multi-user is actually designed — an unused
// column is cheap, a half-enforced permission check is not. Databases that predate
// it get it from the store ladder (migratePublicView); a fresh one gets it here.
func (s *Store) migrate() error {
	// sessions is this subsystem's own table and is created at head shape, which
	// now includes account_id. accounts itself belongs to the store's schema ladder
	// (RFC-0001) rather than here, because the ladder is what moves an existing
	// node's credential into it — the same division that already applies to
	// meta.claimed_at, whose existence the ladder owns and whose meaning this
	// package does.
	//
	// The foreign key is declared here and NOT by the ladder's ALTER, because
	// SQLite cannot add a column with a REFERENCES clause to a table that already
	// has rows. A migrated node therefore carries account_id without the
	// constraint and a fresh one carries it with; both behave identically, because
	// the delete-cascade the constraint expresses is also done explicitly wherever
	// an account is removed. That is belt and braces on purpose: this is the one
	// place where losing the cascade would leave a live session pointing at a
	// deleted account.
	const ddl = `
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  last_seen  TEXT NOT NULL,
  account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return err
	}
	// meta predates RFC-0002, so add claimed_at only when it is missing. SQLite
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
	// A node that reached this build through the ladder already has account_id; a
	// node whose sessions table predates it does not, and the CREATE above is a
	// no-op on an existing table. Probe and add, same discipline as claimed_at.
	hasCol, err := s.hasColumn("sessions", "account_id")
	if err != nil {
		return err
	}
	if !hasCol {
		if _, err := s.db.Exec(`ALTER TABLE sessions ADD COLUMN account_id INTEGER`); err != nil {
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

// Role is what an account may do. Exactly three, per RFC-0002 Amendment 1: an
// appliance whose permission model needs a policy engine has the wrong model.
type Role string

const (
	// RoleAdmin holds everything: accounts, identity, trust, and anything that
	// changes what the node is to the outside world.
	RoleAdmin Role = "admin"
	// RoleOperator changes what the radio does — config, apply, calibration,
	// firmware, hardware — and nothing about who may reach the node.
	RoleOperator Role = "operator"
	// RoleViewer changes nothing.
	RoleViewer Role = "viewer"
)

// Valid reports whether r is one of the three. An unrecognised role from the
// store is never treated as a permissive default; callers deny.
func (r Role) Valid() bool {
	switch r {
	case RoleAdmin, RoleOperator, RoleViewer:
		return true
	}
	return false
}

// Account is a stored credential. The plaintext password is never part of it.
type Account struct {
	ID int64
	// PhonebookID is the operator this login belongs to, or 0 when unlinked. It is
	// nullable in the table because a node claimed before accounts existed has an
	// admin with no phonebook row, and the migration must not invent one.
	PhonebookID int64
	Username    string
	Record      HashRecord
	Role        Role
	// MustRotate is set when an admin chose this account's password. Until it is
	// cleared the account may reach the password-change route and nothing else: an
	// admin-chosen password is a password two people know, and the flag is what
	// makes that state brief rather than permanent.
	MustRotate bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ErrNoSuchAccount is returned when an account id or username does not exist.
var ErrNoSuchAccount = errors.New("auth: no such account")

// ErrUsernameTaken is returned when a username is already in use. Usernames are
// unique case-insensitively, so "KN4OQW" and "kn4oqw" are the same login.
var ErrUsernameTaken = errors.New("auth: that username is already in use")

// ErrLastAdmin is returned by any write that would leave the device with no
// admin account. Without it an ordinary mistake — deleting yourself, or demoting
// yourself — locks the device out of itself with no path back except a physical
// reset.
var ErrLastAdmin = errors.New("auth: this is the only admin account; the device would be left with no administrator")

const accountCols = `id, phonebook_id, username, password_hash, params, role, must_rotate, created_at, updated_at`

func scanAccount(sc interface{ Scan(...any) error }) (Account, error) {
	var (
		a       Account
		pb      sql.NullInt64
		role    string
		rotate  int
		created string
		updated string
	)
	if err := sc.Scan(&a.ID, &pb, &a.Username, &a.Record.Hash, &a.Record.Params, &role, &rotate, &created, &updated); err != nil {
		return Account{}, err
	}
	if pb.Valid {
		a.PhonebookID = pb.Int64
	}
	a.Role = Role(role)
	a.MustRotate = rotate != 0
	a.CreatedAt, _ = time.Parse(time.RFC3339, created)
	a.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return a, nil
}

// IsClaimed reports whether the device has been claimed: at least one admin
// account exists and meta.claimed_at is set.
//
// This is RFC-0002's definition with "an admin credential exists" read against
// the accounts table instead of the fixed admin row — the same guarantee, the
// same two conditions, and the same refusal to read a half-written state as
// claimed.
func (s *Store) IsClaimed() (bool, error) {
	var claimedAt sql.NullString
	if err := s.db.QueryRow(`SELECT claimed_at FROM meta WHERE id = 1`).Scan(&claimedAt); err != nil {
		return false, err
	}
	if !claimedAt.Valid || claimedAt.String == "" {
		return false, nil
	}
	n, err := s.AdminCount()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// AdminCount returns how many admin accounts exist. It is the guard behind
// ErrLastAdmin and the second half of IsClaimed.
func (s *Store) AdminCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE role = ?`, string(RoleAdmin)).Scan(&n)
	return n, err
}

// Claim writes the first admin account and stamps meta.claimed_at in a single
// transaction.
//
// The first claim still wins atomically, but the mechanism has changed with the
// schema. RFC-0002 relied on the admin row's fixed id of 1, so a second INSERT
// was a primary-key conflict; accounts has no fixed id. The guard is now the
// claim stamp itself: the UPDATE carries `WHERE claimed_at IS NULL`, so exactly
// one caller can affect a row, and the loser rolls back having written nothing.
// There is still no window in which two callers both own the device.
func (s *Store) Claim(username string, rec HashRecord, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	stamp := at.UTC().Format(time.RFC3339)

	res, err := tx.Exec(`UPDATE meta SET claimed_at = ? WHERE id = 1 AND claimed_at IS NULL`, stamp)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAlreadyClaimed
	}
	if _, err := tx.Exec(
		`INSERT INTO accounts(phonebook_id, username, password_hash, params, role, must_rotate, created_at, updated_at)
		 VALUES(NULL, ?, ?, ?, ?, 0, ?, ?)`,
		username, rec.Hash, rec.Params, string(RoleAdmin), stamp, stamp); err != nil {
		if isPrimaryKeyConflict(err) {
			return ErrAlreadyClaimed
		}
		return err
	}
	return tx.Commit()
}

// AccountByUsername looks a login up case-insensitively, through the column's own
// NOCASE collation so the query and the unique constraint agree by construction.
func (s *Store) AccountByUsername(username string) (Account, bool, error) {
	a, err := scanAccount(s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE username = ?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, err
	}
	return a, true, nil
}

// AccountByID looks an account up by its surrogate id.
func (s *Store) AccountByID(id int64) (Account, error) {
	a, err := scanAccount(s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNoSuchAccount
	}
	return a, err
}

// Accounts returns every account, ordered by username.
func (s *Store) Accounts() ([]Account, error) {
	rows, err := s.db.Query(`SELECT ` + accountCols + ` FROM accounts ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // rows.Err() below reports iteration failures
	out := []Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateAccount adds a login. phonebookID of 0 stores NULL (unlinked).
//
// mustRotate is the caller's decision rather than this function's: an account an
// admin created carries it, and the first account written by Claim does not.
func (s *Store) CreateAccount(phonebookID int64, username string, rec HashRecord, role Role, mustRotate bool, at time.Time) (Account, error) {
	if !role.Valid() {
		return Account{}, fmt.Errorf("auth: unknown role %q", role)
	}
	var pb any
	if phonebookID != 0 {
		pb = phonebookID
	}
	stamp := at.UTC().Format(time.RFC3339)
	rot := 0
	if mustRotate {
		rot = 1
	}
	res, err := s.db.Exec(
		`INSERT INTO accounts(phonebook_id, username, password_hash, params, role, must_rotate, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		pb, username, rec.Hash, rec.Params, string(role), rot, stamp, stamp)
	if err != nil {
		if isPrimaryKeyConflict(err) {
			return Account{}, ErrUsernameTaken
		}
		return Account{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Account{}, err
	}
	return s.AccountByID(id)
}

// SetRole changes an account's role, refusing to demote the last admin.
//
// The check and the write are one transaction. Two admins demoting each other at
// the same moment would otherwise both see a count of two, both proceed, and
// leave a device with no administrator.
func (s *Store) SetRole(id int64, role Role, at time.Time) error {
	if !role.Valid() {
		return fmt.Errorf("auth: unknown role %q", role)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	if err := lastAdminGuard(tx, id, role != RoleAdmin); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE accounts SET role = ?, updated_at = ? WHERE id = ?`,
		string(role), at.UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoSuchAccount
	}
	return tx.Commit()
}

// SetPassword replaces an account's credential and sets or clears the rotation
// flag. A successful rotation by the account holder clears it; an admin resetting
// somebody else's password sets it again.
func (s *Store) SetPassword(id int64, rec HashRecord, mustRotate bool, at time.Time) error {
	rot := 0
	if mustRotate {
		rot = 1
	}
	res, err := s.db.Exec(
		`UPDATE accounts SET password_hash = ?, params = ?, must_rotate = ?, updated_at = ? WHERE id = ?`,
		rec.Hash, rec.Params, rot, at.UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoSuchAccount
	}
	return nil
}

// DeleteAccount removes a login and every session it owns, refusing to delete the
// last admin.
//
// The session delete is explicit rather than left to ON DELETE CASCADE. The
// constraint is there on a freshly created table, but a node migrated from before
// accounts existed carries account_id without it (SQLite cannot add a column with
// a REFERENCES clause to a populated table), and "revoking a login leaves its
// sessions live" is not a difference two nodes may have.
func (s *Store) DeleteAccount(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	if err := lastAdminGuard(tx, id, true); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE account_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoSuchAccount
	}
	return tx.Commit()
}

// RevokeAccountSessions logs an account out everywhere without removing it.
func (s *Store) RevokeAccountSessions(id int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE account_id = ?`, id)
	return err
}

// lastAdminGuard refuses a write that would remove the final administrator.
// losing reports whether the write takes admin away from this account.
func lastAdminGuard(tx *sql.Tx, id int64, losing bool) error {
	if !losing {
		return nil
	}
	var role string
	err := tx.QueryRow(`SELECT role FROM accounts WHERE id = ?`, id).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoSuchAccount
	}
	if err != nil {
		return err
	}
	if Role(role) != RoleAdmin {
		return nil // not an admin; removing it cannot remove the last one
	}
	var admins int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM accounts WHERE role = ?`, string(RoleAdmin)).Scan(&admins); err != nil {
		return err
	}
	if admins <= 1 {
		return ErrLastAdmin
	}
	return nil
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
	// accounts, not admin: the credential table changed shape in Amendment 1 and
	// the reset's meaning did not. The phonebook is deliberately untouched — reset
	// removes trust, not identity, and forgetting the people a node knows would
	// turn a password recovery into data loss.
	if _, err := tx.Exec(`DELETE FROM accounts`); err != nil {
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
	// AccountID is who the session belongs to. A session carries an OWNER, not a
	// capability: the role is read from accounts at authentication time and never
	// copied here, so demoting an account takes effect on its next request rather
	// than at its next login.
	AccountID int64
	CreatedAt time.Time
	ExpiresAt time.Time
	LastSeen  time.Time
}

// CreateSession inserts a session for the given token hash.
func (s *Store) CreateSession(tokenHash string, accountID int64, created, expires time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions(token_hash, account_id, created_at, expires_at, last_seen) VALUES(?, ?, ?, ?, ?)`,
		tokenHash, accountID, created.UTC().Format(time.RFC3339), expires.UTC().Format(time.RFC3339), created.UTC().Format(time.RFC3339))
	return err
}

// LookupSession returns the session for a token hash and whether it exists. The
// caller decides validity (idle expiry) from the returned timestamps.
func (s *Store) LookupSession(tokenHash string) (Session, bool, error) {
	sess := Session{TokenHash: tokenHash}
	var created, expires, seen string
	var account sql.NullInt64
	err := s.db.QueryRow(
		`SELECT created_at, expires_at, last_seen, account_id FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&created, &expires, &seen, &account)
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	sess.CreatedAt, _ = time.Parse(time.RFC3339, created)
	sess.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
	sess.LastSeen, _ = time.Parse(time.RFC3339, seen)
	if account.Valid {
		sess.AccountID = account.Int64
	}
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
