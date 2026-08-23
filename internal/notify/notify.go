// Package notify turns things that happened into messages somebody receives.
//
// It is a queue and a set of sinks, and the separation is the whole design. An
// event is written to the queue by whatever noticed it — an API handler, a hub
// subscription — and returns immediately. A single dispatcher goroutine drains
// the queue and hands each notification to the sinks for its recipients. Nothing
// on a request path or an RF path ever waits for an SMTP connection.
//
// # Why a queue and not a callback
//
// Delivery talks to something outside the node: an SMTP server that may be slow,
// unreachable, or refusing. Every property that matters here follows from
// refusing to do that inline.
//
//   - An API handler that returned only after the mail went out would hang for
//     the SMTP timeout when the server is down, and the operator would experience
//     a broken dashboard rather than a late email.
//   - An RF path that did it would drop voice.
//   - A retry needs somewhere to keep state between attempts, and a callback has
//     nowhere to put it.
//
// # Parking, not crash-looping
//
// A notification that cannot be delivered is retried with capped exponential
// backoff and then PARKS: attempts stops rising, the row stays undelivered, and
// last_error holds the reason. It is never retried again until something changes.
//
// That is the lesson from #238, where a permanently failing operation retried
// forever and buried the log it was reporting through. A parked notification is
// visible (the panel lists it) and quiet (the dispatcher does not select it).
// The failure stays diagnosable without becoming noise.
//
// # Sinks
//
// A Sink delivers one notification to one recipient over one channel. SMTP is the
// first. Webhook sinks (Discord, ntfy) are a later unit and are deliberately not
// implemented here — but the interface is what it is so they drop in: Deliver
// takes a context and returns an error, and everything about retry, backoff and
// parking lives in the dispatcher rather than in any sink.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// EventType names what happened. The set is closed: a notification the operator
// cannot describe is one they cannot decide to want.
type EventType string

const (
	// EventAccountCreated fires when an admin creates a login for somebody.
	EventAccountCreated EventType = "account_created"
	// EventPasswordChanged fires when an account's password changes, whoever
	// changed it. Telling somebody their password just changed is how they find
	// out when it was not them.
	EventPasswordChanged EventType = "password_changed"
	// EventSMSReceived fires on an inbound text message.
	EventSMSReceived EventType = "sms_received"
	// EventWXAlert fires on a weather alert the node decided to announce.
	EventWXAlert EventType = "wx_alert"
)

// Valid reports whether t is a type this build knows. An unrecognised type from
// the queue is parked rather than guessed at: a row written by a newer build must
// not be delivered as something it is not.
func (t EventType) Valid() bool {
	switch t {
	case EventAccountCreated, EventPasswordChanged, EventSMSReceived, EventWXAlert:
		return true
	}
	return false
}

// Notification is one thing to tell somebody about, as the sinks see it.
//
// Payload is decoded JSON rather than a typed union because the sinks render it
// and the set of fields differs per event type. Subject and Body below are what a
// sink actually needs; Payload is there for a sink that wants more.
type Notification struct {
	ID        int64
	Type      EventType
	CreatedAt time.Time
	Payload   map[string]any
}

// Recipient is who to tell and how to reach them.
//
// Email is the only channel today. It is sourced from the phonebook, and a
// recipient with no email is not an error — most people in a node's phonebook
// have no login and no address, and their being there is the normal case.
type Recipient struct {
	PhonebookID int64
	Callsign    string
	Name        string
	Email       string
}

// Sink delivers one notification to one recipient.
//
// It returns an error when delivery failed and the dispatcher should retry. It
// must NOT return an error for "this recipient has no address for my channel" —
// that is a no-op, and treating it as a failure would park a notification that
// was never deliverable in the first place. Return ErrNotApplicable instead.
type Sink interface {
	// Name identifies the sink in logs and in a parked notification's error.
	Name() string
	// Deliver sends, or reports why it could not. The context carries the
	// dispatcher's per-attempt deadline.
	Deliver(ctx context.Context, n Notification, r Recipient) error
}

// ErrNotApplicable is a sink saying "not mine" — the recipient has no address for
// this channel. It is not a failure and never counts as an attempt.
var ErrNotApplicable = errors.New("notify: recipient has no address for this sink")

// Subject and Body render a notification as human-readable text.
//
// They live here rather than in the SMTP sink because every sink needs the same
// words, and a webhook that phrased the same event differently would make two
// channels disagree about what happened. A sink decides FORMAT; this decides
// CONTENT.
func (n Notification) Subject(station string) string {
	prefix := station
	if prefix == "" {
		prefix = "Waypoint"
	}
	switch n.Type {
	case EventAccountCreated:
		return prefix + ": an account has been created for you"
	case EventPasswordChanged:
		return prefix + ": your password was changed"
	case EventSMSReceived:
		return prefix + ": a text message arrived"
	case EventWXAlert:
		if h, ok := n.Payload["headline"].(string); ok && h != "" {
			return prefix + ": " + h
		}
		return prefix + ": weather alert"
	default:
		return prefix + ": notification"
	}
}

// Body is the message text. It deliberately carries no secret: an account
// notification names the account and never its password, and an SMS notification
// says a message arrived and never quotes it — the message is correspondence and
// the node has no business copying it into email.
func (n Notification) Body(station string) string {
	who := ""
	if u, ok := n.Payload["username"].(string); ok && u != "" {
		who = u
	}
	switch n.Type {
	case EventAccountCreated:
		return fmt.Sprintf(
			"An account (%s) has been created for you on %s.\n\n"+
				"Your administrator will give you the initial password separately. "+
				"You will be asked to choose a new one the first time you sign in.\n",
			who, stationName(station))
	case EventPasswordChanged:
		return fmt.Sprintf(
			"The password for your account (%s) on %s was just changed.\n\n"+
				"If that was not you, tell whoever administers the node straight away.\n",
			who, stationName(station))
	case EventSMSReceived:
		from := ""
		if f, ok := n.Payload["from"].(string); ok {
			from = f
		}
		return fmt.Sprintf(
			"A text message addressed to you arrived on %s%s.\n\n"+
				"Sign in to read it — the message itself is not copied into this email.\n",
			stationName(station), fromClause(from))
	case EventWXAlert:
		text, _ := n.Payload["text"].(string)
		if text == "" {
			text = "A weather alert was announced."
		}
		return text + "\n"
	default:
		return "A notification was raised on " + stationName(station) + ".\n"
	}
}

func stationName(s string) string {
	if s == "" {
		return "your Waypoint node"
	}
	return s
}

func fromClause(from string) string {
	if from == "" {
		return ""
	}
	return " from " + from
}

// EncodePayload renders a payload for the queue.
func EncodePayload(v map[string]any) (string, error) {
	if v == nil {
		v = map[string]any{}
	}
	b, err := json.Marshal(v)
	return string(b), err
}

// DecodePayload parses a queued payload. A payload that will not parse yields an
// empty map rather than an error: the notification is still worth sending, and
// the renderers above all tolerate missing fields.
func DecodePayload(s string) map[string]any {
	out := map[string]any{}
	if s == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}
