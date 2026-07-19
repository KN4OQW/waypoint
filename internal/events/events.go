// Package events is waypointd's persistent event record (RFC-0004): a separate
// SQLite database, events.db, into which every hub event is written by a batched
// subscriber, and which GET /api/history reads so the dashboard renders the same
// last-heard / networks / event log for any client regardless of when it
// connected or whether the daemon has restarted.
//
// It is deliberately its own database, a sibling of the config store (RFC-0001),
// not a table inside it: event traffic is far higher churn than configuration and
// its retention lifecycle is independent, so keeping it separate isolates both the
// write lock and the nightly prune from the config surface. The schema mirrors
// hub.Event exactly, so persistence is a straight projection and the history
// endpoint re-emits the identical wire shape the SSE stream and UI already speak.
package events

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"

	_ "modernc.org/sqlite" // pure-Go driver: cross-compiles CGO-free to armv6
)

// SchemaVersion is the events store's schema version, tracked independently of
// the config store so the two migrate on their own cadences. The daemon refuses
// to open a database from a newer version (rollback safety).
const SchemaVersion = 1

// Store is a handle to the event-history database. It is the only writer.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the events database at path and ensures the
// schema is present and compatible. Use ":memory:" for tests and for -demo mode
// (so synthetic traffic never accretes a persistent history).
//
// The DSN diverges from the config store on one pragma: synchronous=NORMAL. Under
// WAL that fsyncs at checkpoint rather than on every commit — the right trade for
// a high-churn event log on an SD card (a lost last-second event on a power cut is
// acceptable; the per-event fsync it would cost is not). Config writes do not take
// that trade.
func Open(path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		dsn = path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; keeps SQLite happy under :memory: too
	s := &Store{db: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  schema_version INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  ts_ms    INTEGER NOT NULL,          -- event time, unix epoch milliseconds (UTC)
  type     TEXT NOT NULL,
  mode     TEXT,
  slot     INTEGER,
  source   TEXT,
  dest     TEXT,
  network  TEXT,
  seconds  REAL,
  ber      REAL,
  rssi     INTEGER,
  detail   TEXT
);
CREATE INDEX IF NOT EXISTS idx_events_ts     ON events (ts_ms);
CREATE INDEX IF NOT EXISTS idx_events_source ON events (source, ts_ms);
CREATE INDEX IF NOT EXISTS idx_events_type   ON events (type, ts_ms);`
	if _, err := s.db.Exec(ddl); err != nil {
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
			return fmt.Errorf("events: database schema v%d is newer than this build (v%d); refusing to run", ver, SchemaVersion)
		}
		return nil
	default:
		return err
	}
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Insert writes a batch of events in a single transaction. An empty batch is a
// no-op. Batching (plus WAL + synchronous=NORMAL) is what keeps SD writes to a
// trickle under sustained traffic.
func (s *Store) Insert(batch []hub.Event) error {
	if len(batch) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO events
		(ts_ms, type, mode, slot, source, dest, network, seconds, ber, rssi, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, e := range batch {
		if _, err := stmt.Exec(
			e.Time.UnixMilli(), e.Type, e.Mode, e.Slot, e.Source,
			e.Dest, e.Network, e.Seconds, e.BER, e.RSSI, e.Detail,
		); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// HistoryQuery narrows a History read. The zero value (no Since, no Type, Limit
// clamped by History) returns the most recent events across all types.
type HistoryQuery struct {
	Since time.Time // events at or after this instant; zero = from the beginning
	Type  string    // exact event type filter; "" = all types
	Limit int       // max rows; clamped to [1, MaxHistoryLimit] by History
}

// DefaultHistoryLimit and MaxHistoryLimit bound a single history read so one
// request can never scan the whole retention window.
const (
	DefaultHistoryLimit = 500
	MaxHistoryLimit     = 5000
)

// History returns matching events newest-first. It is the read model behind GET
// /api/history. The returned events carry the identical wire shape hub.Event
// marshals for the SSE stream, so a client feeds them through the same reducer it
// uses for live events.
func (s *Store) History(q HistoryQuery) ([]hub.Event, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	if limit > MaxHistoryLimit {
		limit = MaxHistoryLimit
	}

	sqlText := `SELECT ts_ms, type, mode, slot, source, dest, network, seconds, ber, rssi, detail
		FROM events WHERE ts_ms >= ?`
	args := []any{q.Since.UnixMilli()}
	if q.Type != "" {
		sqlText += ` AND type = ?`
		args = append(args, q.Type)
	}
	sqlText += ` ORDER BY ts_ms DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]hub.Event, 0, min(limit, 64))
	for rows.Next() {
		var (
			e    hub.Event
			tsMs int64
		)
		if err := rows.Scan(&tsMs, &e.Type, &e.Mode, &e.Slot, &e.Source,
			&e.Dest, &e.Network, &e.Seconds, &e.BER, &e.RSSI, &e.Detail); err != nil {
			return nil, err
		}
		e.Time = time.UnixMilli(tsMs).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune deletes events older than the cutoff and returns how many rows it removed.
// The nightly prune (RFC-0004) computes the cutoff from the operator's retention
// window; a retention of 0 means keep forever and the caller does not invoke this.
func (s *Store) Prune(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM events WHERE ts_ms < ?`, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Count returns the number of stored events. Used by tests and diagnostics.
func (s *Store) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	return n, err
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
