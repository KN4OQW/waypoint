package events

import (
	"database/sql"
	"errors"
	"time"
)

// The notification queue: one row per thing somebody should be told about, with
// the state a retry loop needs to make progress without ever spinning.
//
// It lives in the events database rather than config.db because it is a work
// queue — written on every notifiable event, updated on every attempt, deleted
// when it ages out. See the SchemaVersion comment in events.go for why that trade
// belongs here and not in the configuration of record.
//
// The columns that matter are the delivery-state ones, and they encode the
// PR #238 lesson directly: a notification that cannot be delivered PARKS with its
// error visible instead of retrying forever. attempts counts what has been tried,
// next_attempt_at is when the dispatcher may try again, delivered_at is set once
// and never cleared, and last_error is the operator-facing reason it is stuck.
// A row with attempts at the cap and no delivered_at is parked: the dispatcher
// will not pick it up again, and the panel can show why.
const notificationsDDL = `
CREATE TABLE IF NOT EXISTS notifications (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  event_type      TEXT NOT NULL,
  phonebook_id    INTEGER,               -- null for admin/broadcast events
  payload         TEXT NOT NULL,         -- JSON; the sink renders it
  created_at      TEXT NOT NULL,         -- RFC-3339 UTC
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL,         -- RFC-3339 UTC; due when <= now
  delivered_at    TEXT,                  -- set once on success, never cleared
  last_error      TEXT NOT NULL DEFAULT ''
);
-- The dispatcher's only hot query is "what is due", so that is the index.
CREATE INDEX IF NOT EXISTS idx_notifications_due
  ON notifications (delivered_at, next_attempt_at);`

// Notification is one queued item.
//
// PhonebookID is 0 rather than a pointer for "no particular person" — an admin or
// broadcast event. Zero is safe as the sentinel because the phonebook's ids are
// AUTOINCREMENT and start at 1, so no row can ever have it.
type Notification struct {
	ID            int64
	EventType     string
	PhonebookID   int64
	Payload       string
	CreatedAt     time.Time
	Attempts      int
	NextAttemptAt time.Time
	DeliveredAt   time.Time // zero until delivered
	LastError     string
}

// Delivered reports whether this notification has been delivered.
func (n Notification) Delivered() bool { return !n.DeliveredAt.IsZero() }

// EnqueueNotification writes a notification due immediately.
//
// It returns the stored row so a caller can log its id — which is what makes a
// stuck notification findable later, from a log line to a table row.
func (s *Store) EnqueueNotification(n Notification) (Notification, error) {
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	if n.NextAttemptAt.IsZero() {
		n.NextAttemptAt = n.CreatedAt
	}
	var pb any
	if n.PhonebookID != 0 {
		pb = n.PhonebookID
	}
	res, err := s.db.Exec(
		`INSERT INTO notifications(event_type, phonebook_id, payload, created_at, attempts, next_attempt_at, last_error)
		 VALUES(?, ?, ?, ?, 0, ?, '')`,
		n.EventType, pb, n.Payload,
		n.CreatedAt.UTC().Format(time.RFC3339Nano),
		n.NextAttemptAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Notification{}, err
	}
	n.ID, err = res.LastInsertId()
	return n, err
}

// DueNotifications returns undelivered notifications whose next attempt has come,
// oldest first, up to limit.
//
// attempts < maxAttempts is part of the query rather than a filter afterwards:
// a parked row must never be READ back into the dispatcher's working set, or a
// node with one permanently failing notification would wake, load it, skip it and
// sleep, forever. Parked rows are inert by not being selected.
func (s *Store) DueNotifications(now time.Time, maxAttempts, limit int) ([]Notification, error) {
	rows, err := s.db.Query(
		`SELECT id, event_type, phonebook_id, payload, created_at, attempts, next_attempt_at, delivered_at, last_error
		   FROM notifications
		  WHERE delivered_at IS NULL AND attempts < ? AND next_attempt_at <= ?
		  ORDER BY next_attempt_at, id
		  LIMIT ?`,
		maxAttempts, now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // rows.Err() below reports iteration failures
	out := []Notification{}
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkDelivered records a successful delivery and clears the error.
func (s *Store) MarkDelivered(id int64, at time.Time) error {
	_, err := s.db.Exec(
		`UPDATE notifications SET delivered_at = ?, last_error = '' WHERE id = ?`,
		at.UTC().Format(time.RFC3339Nano), id)
	return err
}

// MarkFailed records a failed attempt and when the next one may happen.
//
// The error text is stored rather than only logged: a log line scrolls away and a
// parked notification has to be diagnosable weeks later, from the panel, by
// somebody who was not watching when it failed.
func (s *Store) MarkFailed(id int64, attempts int, next time.Time, reason string) error {
	_, err := s.db.Exec(
		`UPDATE notifications SET attempts = ?, next_attempt_at = ?, last_error = ? WHERE id = ?`,
		attempts, next.UTC().Format(time.RFC3339Nano), reason, id)
	return err
}

// Notification reads one row by id.
func (s *Store) Notification(id int64) (Notification, error) {
	n, err := scanNotification(s.db.QueryRow(
		`SELECT id, event_type, phonebook_id, payload, created_at, attempts, next_attempt_at, delivered_at, last_error
		   FROM notifications WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Notification{}, errors.New("events: no such notification")
	}
	return n, err
}

// ParkedNotifications returns notifications that have exhausted their attempts
// and are not delivered — what the panel shows when it says something is stuck.
func (s *Store) ParkedNotifications(maxAttempts, limit int) ([]Notification, error) {
	rows, err := s.db.Query(
		`SELECT id, event_type, phonebook_id, payload, created_at, attempts, next_attempt_at, delivered_at, last_error
		   FROM notifications
		  WHERE delivered_at IS NULL AND attempts >= ?
		  ORDER BY id DESC
		  LIMIT ?`, maxAttempts, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // rows.Err() below reports iteration failures
	out := []Notification{}
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// PruneNotifications deletes delivered notifications older than before, and
// returns how many went. Parked rows are deliberately NOT pruned: they are the
// record of something that never got through, and deleting them would make the
// failure disappear rather than the problem.
func (s *Store) PruneNotifications(before time.Time) (int64, error) {
	res, err := s.db.Exec(
		`DELETE FROM notifications WHERE delivered_at IS NOT NULL AND delivered_at < ?`,
		before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanNotification(sc interface{ Scan(...any) error }) (Notification, error) {
	var (
		n         Notification
		pb        sql.NullInt64
		created   string
		next      string
		delivered sql.NullString
	)
	if err := sc.Scan(&n.ID, &n.EventType, &pb, &n.Payload, &created, &n.Attempts, &next, &delivered, &n.LastError); err != nil {
		return Notification{}, err
	}
	if pb.Valid {
		n.PhonebookID = pb.Int64
	}
	n.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	n.NextAttemptAt, _ = time.Parse(time.RFC3339Nano, next)
	if delivered.Valid && delivered.String != "" {
		n.DeliveredAt, _ = time.Parse(time.RFC3339Nano, delivered.String)
	}
	return n, nil
}
