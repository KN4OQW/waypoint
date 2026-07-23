package stackupdate

import (
	"database/sql"
	"time"

	"github.com/KN4OQW/waypoint/internal/store"
)

// History is the applied/previous version audit trail for stack updates. It owns
// the stack_update_history table beside the config store's own tables (sharing the
// single serialized connection, RFC-0001), never reachable through /api/config.
// Each Apply writes a `pending` row set before the install and a terminal
// (`confirmed`/`reverted`/`revert_failed`) set after, so the trail records both the
// intent and the result — and the revert path has a durable record of the versions
// to roll back to.
type History struct {
	db  *sql.DB
	now func() time.Time
}

// NewHistory attaches the stack-update history table to the store, creating it if
// absent. Idempotent.
func NewHistory(s *store.Store) (*History, error) {
	h := &History{db: s.DB(), now: func() time.Time { return time.Now().UTC() }}
	if err := h.migrate(); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *History) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS stack_update_history (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  at           TEXT NOT NULL,
  package      TEXT NOT NULL,
  from_version TEXT,
  to_version   TEXT,
  result       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_stack_update_history_at ON stack_update_history(at);`
	_, err := h.db.Exec(ddl)
	return err
}

// Insert appends one row per HistoryRow, stamped with the current UTC time. All
// rows go in one transaction so a batch is atomic.
func (h *History) Insert(rows []HistoryRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back only if Commit didn't run
	at := h.now().Format(time.RFC3339)
	stmt, err := tx.Prepare(`INSERT INTO stack_update_history(at, package, from_version, to_version, result) VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.Exec(at, r.Package, r.From, r.To, r.Result); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Record is one stored history row (with its timestamp), newest-first from Recent.
type Record struct {
	At      string `json:"at"`
	Package string `json:"package"`
	From    string `json:"from"`
	To      string `json:"to"`
	Result  string `json:"result"`
}

// Recent returns the most recent history rows, newest first (limit <= 0 returns a
// small default page).
func (h *History) Recent(limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := h.db.Query(
		`SELECT at, package, from_version, to_version, result FROM stack_update_history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var from, to sql.NullString
		if err := rows.Scan(&r.At, &r.Package, &from, &to, &r.Result); err != nil {
			return nil, err
		}
		r.From, r.To = from.String, to.String
		out = append(out, r)
	}
	return out, rows.Err()
}
