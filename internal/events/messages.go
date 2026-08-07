package events

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// The text-message record: what the node sent to a radio, what a radio sent to
// it, and how far along each one got.
//
// It lives in the events database rather than the config store for the same
// reason the event log does — it is traffic, not configuration, with its own churn
// and its own retention — and in the same database rather than a third one so
// there is one handle, one writer, and one prune loop to reason about.
//
// # Why there is a state machine at all
//
// An outbound message is not delivered when the API returns. It is queued, then
// transmitted over a second or so of air time, and then it has been sent — which
// is as much as this node can ever honestly claim, because unconfirmed DMR data
// carries no acknowledgement. Anything that reported "delivered" would be
// inventing a fact the protocol does not supply. The states say exactly what is
// known and no more, and the transitions are enforced here rather than trusted to
// each caller, so a bug in the transmit path cannot leave a message reading "sent"
// because a write failed halfway.

// Direction says who originated a message.
type Direction string

const (
	// Outbound is a message this node originated toward a radio.
	Outbound Direction = "out"
	// Inbound is a message a radio originated toward this node.
	Inbound Direction = "in"
)

// MessageState is where a message has got to.
type MessageState string

const (
	// StateQueued is an outbound message accepted and not yet on the air.
	StateQueued MessageState = "queued"
	// StateTransmitting is an outbound message whose bursts are being emitted.
	StateTransmitting MessageState = "transmitting"
	// StateSent is an outbound message fully emitted. It does NOT mean delivered:
	// unconfirmed DMR data has no acknowledgement, and the radio may have been off.
	StateSent MessageState = "sent"
	// StateReceived is an inbound message, reassembled and checksum-verified.
	StateReceived MessageState = "received"
	// StateFailed is an outbound message that will not be sent, with a reason.
	StateFailed MessageState = "failed"
)

// messageTransitions is the whole state machine, as the set of states each state
// may move to. A state absent from the map, or present with an empty set, is
// terminal.
//
// Written as data rather than as a switch so the test can walk every state and
// every pair, and so adding a state to the type without adding it here fails
// loudly instead of quietly permitting nothing.
var messageTransitions = map[MessageState][]MessageState{
	StateQueued:       {StateTransmitting, StateFailed},
	StateTransmitting: {StateSent, StateFailed},
	StateSent:         nil,
	StateReceived:     nil,
	StateFailed:       nil,
}

// CanTransition reports whether from may move to to.
func CanTransition(from, to MessageState) bool {
	for _, s := range messageTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Terminal reports whether a state is the end of the road for a message.
func Terminal(s MessageState) bool { return len(messageTransitions[s]) == 0 }

// Errors the message store returns. Callers distinguish them with errors.Is; the
// API layer maps them to status codes.
var (
	// ErrMessageNotFound is a message id nothing in the table matches.
	ErrMessageNotFound = errors.New("events: no such message")
	// ErrBadTransition is a state change the machine does not allow.
	ErrBadTransition = errors.New("events: invalid message state transition")
	// ErrBadMessage is a message that could not be stored as given.
	ErrBadMessage = errors.New("events: invalid message")
)

// MaxMessageBytes bounds the stored text.
//
// It is far above anything a DMR text message carries — the on-air dialect tops
// out at 123 UTF-16 units — because this is the store's own guard against a
// runaway writer, not the radio's limit. The transmit path enforces the real
// bound, which is stricter and belongs where the format is known.
const MaxMessageBytes = 4096

// Message is one text message.
type Message struct {
	ID int64 `json:"id"`
	// Direction is who originated it.
	Direction Direction `json:"direction"`
	// Peer is the other party's 24-bit DMR ID: the destination of an outbound
	// message, the source of an inbound one. It is the id a UI groups a
	// conversation by, which is why it is one field and not two.
	Peer uint32 `json:"peer"`
	// Local is this node's DMR ID as it appeared on the air. Stored rather than
	// looked up: the configured id can change, and a message should keep saying
	// which id actually carried it.
	Local uint32 `json:"local"`
	// Group is true for a talkgroup message rather than an individual one.
	Group bool `json:"group"`
	// Text is the message body.
	Text string `json:"text"`
	// State is where it has got to. Reason is why, and is only ever set on failed.
	State  MessageState `json:"state"`
	Reason string       `json:"reason,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// messagesDDL is the table and its indexes, applied by init alongside the event
// table's. IF NOT EXISTS covers both a fresh database and an upgrade from the
// schema that predates messages.
const messagesDDL = `
CREATE TABLE IF NOT EXISTS messages (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  direction  TEXT NOT NULL,
  peer       INTEGER NOT NULL,
  local      INTEGER NOT NULL,
  group_call INTEGER NOT NULL DEFAULT 0,
  body       TEXT NOT NULL,
  state      TEXT NOT NULL,
  reason     TEXT NOT NULL DEFAULT '',
  created_ms INTEGER NOT NULL,
  updated_ms INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_created ON messages (created_ms);
CREATE INDEX IF NOT EXISTS idx_messages_peer    ON messages (peer, created_ms);
CREATE INDEX IF NOT EXISTS idx_messages_state   ON messages (state, created_ms);`

// Enqueue accepts an outbound message and stores it as queued.
//
// Direction and State are set here rather than read from m: an outbound message
// that arrived already claiming to be "sent" is not something the caller should be
// able to express.
func (s *Store) Enqueue(m Message) (Message, error) {
	m.Direction, m.State, m.Reason = Outbound, StateQueued, ""
	return s.insertMessage(m)
}

// RecordInbound stores a received message. Inbound messages have no lifecycle:
// by the time one exists it has already been reassembled and checksum-verified,
// so it goes straight in as received and never moves again.
func (s *Store) RecordInbound(m Message) (Message, error) {
	m.Direction, m.State, m.Reason = Inbound, StateReceived, ""
	return s.insertMessage(m)
}

func (s *Store) insertMessage(m Message) (Message, error) {
	if err := validateMessage(m); err != nil {
		return Message{}, err
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	m.CreatedAt, m.UpdatedAt = now, now

	res, err := s.db.Exec(`INSERT INTO messages
		(direction, peer, local, group_call, body, state, reason, created_ms, updated_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(m.Direction), m.Peer, m.Local, boolInt(m.Group), m.Text,
		string(m.State), m.Reason, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return Message{}, err
	}
	if m.ID, err = res.LastInsertId(); err != nil {
		return Message{}, err
	}
	return m, nil
}

// Advance moves a message to a new state, returning it as stored.
//
// The check and the write are one transaction. Without that, two goroutines
// advancing the same message could both read "queued" and both write, and the one
// that lost would silently overwrite the other's verdict — which for a pair of
// (sent, failed) writes means an operator is told the wrong thing about a message
// that is already on the air.
//
// reason is kept only on StateFailed. A reason on a success is either noise or a
// lie, and neither belongs in a record an operator reads to work out what happened.
func (s *Store) Advance(id int64, to MessageState, reason string) (Message, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back only if Commit did not run

	var from string
	err = tx.QueryRow(`SELECT state FROM messages WHERE id = ?`, id).Scan(&from)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Message{}, fmt.Errorf("%w: id %d", ErrMessageNotFound, id)
	case err != nil:
		return Message{}, err
	}
	if !CanTransition(MessageState(from), to) {
		return Message{}, fmt.Errorf("%w: %s -> %s", ErrBadTransition, from, to)
	}
	if to != StateFailed {
		reason = ""
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := tx.Exec(`UPDATE messages SET state = ?, reason = ?, updated_ms = ? WHERE id = ?`,
		string(to), reason, now.UnixMilli(), id); err != nil {
		return Message{}, err
	}
	m, err := scanMessage(tx.QueryRow(messageSelect+` WHERE id = ?`, id))
	if err != nil {
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, err
	}
	return m, nil
}

// Message returns one message by id.
func (s *Store) Message(id int64) (Message, error) {
	m, err := scanMessage(s.db.QueryRow(messageSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, fmt.Errorf("%w: id %d", ErrMessageNotFound, id)
	}
	return m, err
}

// MessageQuery narrows a Messages read. The zero value returns the most recent
// messages of every direction and state.
type MessageQuery struct {
	// Direction filters to outbound or inbound; "" is both.
	Direction Direction
	// Peer filters to one correspondent; 0 is all of them.
	Peer uint32
	// State filters to one state; "" is all of them.
	State MessageState
	// Since returns messages created at or after this instant; zero is all.
	Since time.Time
	// Limit is clamped to [1, MaxMessageLimit] by Messages.
	Limit int
}

// DefaultMessageLimit and MaxMessageLimit bound one read, for the same reason the
// history read is bounded: no single request may scan the whole retention window.
const (
	DefaultMessageLimit = 200
	MaxMessageLimit     = 2000
)

// Messages returns matching messages newest-first.
func (s *Store) Messages(q MessageQuery) ([]Message, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultMessageLimit
	}
	if limit > MaxMessageLimit {
		limit = MaxMessageLimit
	}

	var where []string
	var args []any
	if q.Direction != "" {
		where = append(where, `direction = ?`)
		args = append(args, string(q.Direction))
	}
	if q.Peer != 0 {
		where = append(where, `peer = ?`)
		args = append(args, q.Peer)
	}
	if q.State != "" {
		where = append(where, `state = ?`)
		args = append(args, string(q.State))
	}
	if !q.Since.IsZero() {
		where = append(where, `created_ms >= ?`)
		args = append(args, q.Since.UnixMilli())
	}

	text := messageSelect
	if len(where) > 0 {
		text += ` WHERE ` + strings.Join(where, ` AND `)
	}
	// id breaks ties: two messages written in the same millisecond must come back
	// in a stable order, or a paging client sees one twice and another never.
	text += ` ORDER BY created_ms DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(text, args...)
	if err != nil {
		return nil, err
	}
	// The error is unactionable — the read has already happened and there is
	// nothing to flush — but the linter's rule is right in general, so it is
	// discarded explicitly rather than left to look like an oversight.
	defer func() { _ = rows.Close() }()

	out := make([]Message, 0, min(limit, 64))
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PruneMessages deletes messages created before the cutoff, returning how many
// went. Messages share the event log's retention window: they are traffic records
// in the traffic database, and a second setting for the same question is a second
// thing to get wrong. RunPrune calls this alongside Prune.
func (s *Store) PruneMessages(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM messages WHERE created_ms < ?`, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountMessages returns the number of stored messages. Used by tests and
// diagnostics.
func (s *Store) CountMessages() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&n)
	return n, err
}

const messageSelect = `SELECT id, direction, peer, local, group_call, body, state, reason,
	created_ms, updated_ms FROM messages`

// scanner is what QueryRow and Rows have in common, so one scan serves both.
type scanner interface{ Scan(dest ...any) error }

func scanMessage(sc scanner) (Message, error) {
	var (
		m                    Message
		dir, state           string
		groupCall            int
		createdMs, updatedMs int64
	)
	if err := sc.Scan(&m.ID, &dir, &m.Peer, &m.Local, &groupCall, &m.Text, &state,
		&m.Reason, &createdMs, &updatedMs); err != nil {
		return Message{}, err
	}
	m.Direction, m.State, m.Group = Direction(dir), MessageState(state), groupCall != 0
	m.CreatedAt = time.UnixMilli(createdMs).UTC()
	m.UpdatedAt = time.UnixMilli(updatedMs).UTC()
	return m, nil
}

func validateMessage(m Message) error {
	if m.Peer > 0xFFFFFF || m.Local > 0xFFFFFF {
		return fmt.Errorf("%w: a DMR ID is a 24-bit value", ErrBadMessage)
	}
	if len(m.Text) > MaxMessageBytes {
		return fmt.Errorf("%w: text is %d bytes, the store's limit is %d",
			ErrBadMessage, len(m.Text), MaxMessageBytes)
	}
	// Invalid UTF-8 would round-trip through SQLite as replacement characters and
	// be indistinguishable from text that really contained them. Refuse it here so
	// a decoder bug upstream surfaces as an error rather than as mojibake nobody
	// can trace back.
	if !utf8.ValidString(m.Text) {
		return fmt.Errorf("%w: text is not valid UTF-8", ErrBadMessage)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
