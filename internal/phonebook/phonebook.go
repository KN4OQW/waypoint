// Package phonebook is the node's identity and contact store: who the operators
// this node knows about are, keyed by a surrogate id, with a callsign and an
// optional DMR ID as their unique attributes.
//
// Three rules shape everything here.
//
// It holds identity and contact detail and nothing else. No password hash, no
// role, no notification preference. Those arrive in later units as their own
// tables referencing phonebook(id), and they stay out of this one because a table
// carrying a credential is a table every read path has to be careful with — and
// this one is read whole by the admin UI.
//
// It is not part of the config renderer spine. A row here compiles to no INI key
// and no daemon reads it. Nothing in this package touches store→model→render→apply,
// and internal/config carries a test that renders every target over a populated
// phonebook and asserts not one field of it reaches a generated file.
//
// The email column is PII (D4). It is stored, served to an authenticated admin,
// and goes nowhere else: no public-view response type carries it, and the error
// paths below never put a field value in an error string, so it cannot reach a log
// by way of a failed write.
package phonebook

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/KN4OQW/waypoint/internal/store"
)

// The write-time rejections and lookup failures.
//
// They are separate sentinels because the panel maps each to a different message,
// and because the two uniqueness conflicts must stay distinguishable: an operator
// told only "conflict" after typing a callsign and a DMR ID has to guess which
// one the node already knows. None of them ever carries a field VALUE — an email
// address in an error string is an email address in a log.
var (
	ErrNotFound      = errors.New("phonebook: no such entry")
	ErrCallsignTaken = errors.New("phonebook: that callsign is already in the phonebook")
	ErrDMRIDTaken    = errors.New("phonebook: that DMR ID is already in the phonebook")
	ErrBadCallsign   = errors.New("phonebook: a callsign is required")
	ErrBadDMRID      = errors.New("phonebook: a DMR ID must be a positive number no wider than 32 bits")
	ErrBadEmail      = errors.New("phonebook: an email address must contain @ and no spaces")
)

// Entry is one operator.
//
// DMRID is 0 for "none recorded", which the table stores as NULL. Zero is safe as
// the sentinel here in a way it usually is not: 0 is not a DMR ID any authority
// issues, so the mapping between the Go zero value and SQL NULL is total in both
// directions and no legal value is lost. FullName and Email use "" the same way.
//
// CreatedAt and UpdatedAt are RFC-3339 UTC and are set by this package, never by a
// caller — a client-supplied timestamp on an audit-adjacent row is a value nobody
// can trust later.
type Entry struct {
	ID        int64  `json:"id"`
	Callsign  string `json:"callsign"`
	DMRID     uint32 `json:"dmr_id,omitempty"`
	FullName  string `json:"full_name,omitempty"`
	Email     string `json:"email,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Store is the phonebook's persistence. It owns the phonebook table and nothing
// else, sharing waypointd's single store connection (see store.Store.DB), so its
// writes serialize with config writes rather than contending for the file lock.
//
// The table itself is guaranteed by the schema version, so there is nothing to
// create here — unlike auth.Store, which predates the ladder and still migrates
// its own tables.
type Store struct {
	db *sql.DB
	// now is the clock, injected so a test can assert that Update stamps
	// updated_at and leaves created_at alone without sleeping to tell them apart.
	now func() time.Time
}

// New attaches the phonebook to the configuration store.
func New(s *store.Store) *Store { return NewWithDB(s.DB()) }

// NewWithDB is New for callers that already hold the database handle, such as the
// migration tests. It mirrors publicview.NewWithDB.
func NewWithDB(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }

// WithClock replaces the timestamp source. Tests use it; production does not.
func (s *Store) WithClock(now func() time.Time) *Store {
	s.now = now
	return s
}

func (s *Store) stamp() string { return s.now().UTC().Format(time.RFC3339) }

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// NormalizeCallsign trims and uppercases. Callsigns are written uppercase
// everywhere else in this project and on every licence, so the panel storing
// whatever case the operator typed would make the list read as inconsistent for
// no gain.
//
// strings.ToUpper rather than an ASCII fold: it is the same for the ASCII a
// callsign is made of, and does something defensible rather than nothing for input
// that is not.
func NormalizeCallsign(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// validate normalizes an entry in place and rejects what cannot be stored.
//
// The callsign rule is deliberately only "not empty". There is a temptation to
// pattern-match a callsign here, and it is wrong: W1AW/4, GB2RS, VP2E/W1ABC and
// the special-event calls issued worldwide do not share a shape this project can
// enumerate, and a validator that refuses a licensed operator's own callsign is a
// worse failure than one that accepts a typo. Uniqueness is what this column is
// really for; format is not evidence we have.
//
// The email rule is likewise the weakest one that is still worth having: it
// catches the operator who put a name or a phone number in the field, and makes no
// claim about deliverability. RFC 5322 permits addresses that look wrong and
// forbids some that look right, and neither this node nor its operator gains
// anything from being the arbiter — the address is contact detail a human will
// read, not a route this daemon ever sends to.
func (e *Entry) validate() error {
	e.Callsign = NormalizeCallsign(e.Callsign)
	if e.Callsign == "" {
		return ErrBadCallsign
	}
	// There is nothing left to check about DMRID at this point: the type is the
	// 32-bit bound and zero is the encoding of "none recorded", so every value a
	// uint32 can hold is storable. The "> 0 and fits in 32 bits" half of the rule
	// lives in ValidateDMRID, which the HTTP layer calls on the wider number a JSON
	// body can carry — before it has been narrowed to a uint32 and the evidence is
	// gone. Putting it there is what lets an operator who types 5000000000 be told
	// the limit instead of getting a decoder's overflow message.
	e.FullName = strings.TrimSpace(e.FullName)
	e.Email = strings.TrimSpace(e.Email)
	if e.Email != "" && !plausibleEmail(e.Email) {
		return ErrBadEmail
	}
	return nil
}

// plausibleEmail is the "contains @, no whitespace" rule and nothing more. It is
// separate from validate so the rule has a name and a test of its own, and so it
// is obvious at the call site how little it claims.
func plausibleEmail(s string) bool {
	if !strings.Contains(s, "@") {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsSpace)
}

// MaxDMRID is the widest value the table's dmr_id column carries. The ceiling is
// the storage width, not a registry rule: DMR IDs issued today are seven digits,
// but this node also records the IDs it hears, and inventing a narrower bound than
// the column has would refuse rows for a limit nothing in the protocol asserts.
const MaxDMRID = 0xFFFFFFFF

// ValidateDMRID narrows a DMR ID that arrived as a wider number — a JSON body, a
// query parameter — to the uint32 the table stores, rejecting what cannot be one.
//
// It takes an int64 rather than a uint32 on purpose: by the time a value has been
// narrowed, "5000000000" and "705032704" are the same number and the operator
// cannot be told which mistake they made. Callers use it only when the field was
// actually present; an absent DMR ID is zero and needs no check.
func ValidateDMRID(n int64) (uint32, error) {
	if n < 1 || n > MaxDMRID {
		return 0, ErrBadDMRID
	}
	return uint32(n), nil
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

const selectCols = `SELECT id, callsign, dmr_id, full_name, email, created_at, updated_at FROM phonebook`

// scanEntry reads one row, folding the three nullable columns onto their zero
// values. Doing it in one place is what keeps NULL from leaking out of this file
// as a *string every caller would have to dereference.
func scanEntry(sc interface{ Scan(...any) error }) (Entry, error) {
	var (
		e     Entry
		dmrID sql.NullInt64
		name  sql.NullString
		email sql.NullString
	)
	if err := sc.Scan(&e.ID, &e.Callsign, &dmrID, &name, &email, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return Entry{}, err
	}
	if dmrID.Valid {
		e.DMRID = uint32(dmrID.Int64)
	}
	e.FullName, e.Email = name.String, email.String
	return e, nil
}

// List returns every entry, ordered by callsign.
//
// The ordering is stable and alphabetical rather than by id, because the list is
// read by a human looking for a name. A node's phonebook is the people one
// operator knows, so it is read whole rather than paged; if that assumption ever
// stops holding it will be a visible slow panel, not a silent truncation.
func (s *Store) List() ([]Entry, error) {
	rows, err := s.db.Query(selectCols + ` ORDER BY callsign`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // rows.Err() below reports iteration failures
	out := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get returns one entry by its surrogate id.
func (s *Store) Get(id int64) (Entry, error) {
	return s.one(s.db.QueryRow(selectCols+` WHERE id = ?`, id))
}

// LookupByCallsign finds an entry by callsign, case-insensitively.
//
// The comparison is the column's own NOCASE collation rather than an UPPER() on
// both sides: that keeps the unique index usable for the lookup, and it means the
// query and the constraint agree on what "the same callsign" is by construction
// rather than by two pieces of code happening to match.
func (s *Store) LookupByCallsign(callsign string) (Entry, error) {
	c := NormalizeCallsign(callsign)
	if c == "" {
		return Entry{}, ErrBadCallsign
	}
	return s.one(s.db.QueryRow(selectCols+` WHERE callsign = ?`, c))
}

// LookupByDMRID finds an entry by DMR ID. Zero is never stored, so it can only
// miss — answering ErrNotFound rather than returning whichever row happens to have
// no ID recorded.
func (s *Store) LookupByDMRID(id uint32) (Entry, error) {
	if id == 0 {
		return Entry{}, ErrNotFound
	}
	return s.one(s.db.QueryRow(selectCols+` WHERE dmr_id = ?`, id))
}

func (s *Store) one(row *sql.Row) (Entry, error) {
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	return e, nil
}

// ---------------------------------------------------------------------------
// Writes
// ---------------------------------------------------------------------------

// nullIf turns the empty string into a NULL, so "unset" has exactly one
// representation in the table. Without it a row written by the panel ("") and one
// written by a migration (NULL) would compare unequal while meaning the same thing.
func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullID(id uint32) any {
	if id == 0 {
		return nil
	}
	return int64(id)
}

// Create inserts an entry and returns it as stored — with its assigned id, its
// normalized callsign, and its timestamps. Returning the stored row rather than
// echoing the request is what lets the panel show the operator that KN4OQW is
// what their "kn4oqw" became.
func (s *Store) Create(e Entry) (Entry, error) {
	if err := e.validate(); err != nil {
		return Entry{}, err
	}
	at := s.stamp()
	res, err := s.db.Exec(
		`INSERT INTO phonebook(callsign, dmr_id, full_name, email, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		e.Callsign, nullID(e.DMRID), nullIf(e.FullName), nullIf(e.Email), at, at)
	if err != nil {
		return Entry{}, s.conflict(err, e, 0)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Entry{}, err
	}
	e.ID, e.CreatedAt, e.UpdatedAt = id, at, at
	return e, nil
}

// Update replaces every field of an existing entry and returns it as stored.
//
// It is a replacement, not a merge: a PUT that omits full_name clears it. That is
// the same contract publicview's UpdateLink has, and it is the one a form-backed
// panel wants — the form always sends every field, and a merge would make clearing
// one impossible.
//
// created_at is never touched. The row's identity and its age are the two things
// an update must not be able to rewrite.
func (s *Store) Update(e Entry) (Entry, error) {
	if e.ID <= 0 {
		return Entry{}, ErrNotFound
	}
	if err := e.validate(); err != nil {
		return Entry{}, err
	}
	at := s.stamp()
	res, err := s.db.Exec(
		`UPDATE phonebook SET callsign = ?, dmr_id = ?, full_name = ?, email = ?, updated_at = ?
		 WHERE id = ?`,
		e.Callsign, nullID(e.DMRID), nullIf(e.FullName), nullIf(e.Email), at, e.ID)
	if err != nil {
		return Entry{}, s.conflict(err, e, e.ID)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Entry{}, err
	}
	if n == 0 {
		return Entry{}, ErrNotFound
	}
	// Re-read rather than echo: created_at is not in the UPDATE, so it is not in
	// the caller's copy, and returning a zero one would make the panel show an
	// entry that had just lost its age.
	return s.Get(e.ID)
}

// Delete removes an entry. A second click on a delete button gets ErrNotFound,
// which the API answers 404 rather than 500.
func (s *Store) Delete(id int64) error {
	res, err := s.db.Exec(`DELETE FROM phonebook WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Conflict diagnosis
// ---------------------------------------------------------------------------

// isUniqueConflict reports whether err is SQLite's unique-constraint violation.
//
// modernc.org/sqlite surfaces the extended result code via a Code() method;
// SQLITE_CONSTRAINT_UNIQUE is 2067 and SQLITE_CONSTRAINT_PRIMARYKEY is 1555. The
// message fallback keeps this working if the driver ever stops exposing the code —
// the same belt-and-braces auth.isPrimaryKeyConflict uses, and for the same reason.
func isUniqueConflict(err error) bool {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		switch coded.Code() {
		case 1555, 2067:
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "constraint failed") &&
		(strings.Contains(msg, "primary key") || strings.Contains(msg, "unique"))
}

// conflict turns a constraint violation into the sentinel naming WHICH attribute
// collided, by asking the table which of the two values is already spoken for.
//
// It deliberately does not parse the column name out of the driver's message. That
// string ("UNIQUE constraint failed: phonebook.callsign") is stable in practice and
// is not part of any contract, and getting it wrong would mislabel the one thing
// this function exists to distinguish. Re-querying costs a lookup on the failure
// path only, and answers the question the operator is actually asking: which of
// these does the node already know?
//
// self is the id being updated, so an update that changes only an email is not
// reported as colliding with itself. It is 0 for an insert, which no row has.
func (s *Store) conflict(err error, e Entry, self int64) error {
	if !isUniqueConflict(err) {
		return err
	}
	if got, lookupErr := s.LookupByCallsign(e.Callsign); lookupErr == nil && got.ID != self {
		return ErrCallsignTaken
	}
	if e.DMRID != 0 {
		if got, lookupErr := s.LookupByDMRID(e.DMRID); lookupErr == nil && got.ID != self {
			return ErrDMRIDTaken
		}
	}
	// The write was refused by a unique index and neither value is now taken —
	// a concurrent delete between the failure and these lookups is the only way
	// there. Report the original rather than inventing a conflict that is no
	// longer true.
	return fmt.Errorf("phonebook: write refused by a uniqueness constraint: %w", err)
}
