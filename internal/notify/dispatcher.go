package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/KN4OQW/waypoint/internal/events"
)

// Retry policy.
//
// The schedule is capped exponential: each failure doubles the wait from
// BaseBackoff up to MaxBackoff, and after MaxAttempts the notification parks. The
// numbers are chosen so a node whose mail server is down for an afternoon still
// delivers when it comes back, and one whose SMTP settings are simply wrong stops
// trying within an hour rather than for the life of the process.
//
//	attempt 1 fails -> 30s      attempt 4 fails -> 4m
//	attempt 2 fails -> 1m       attempt 5 fails -> 8m
//	attempt 3 fails -> 2m       attempt 6 fails -> parked (~15m total)
const (
	// MaxAttempts is how many times a notification is tried before it parks.
	MaxAttempts = 6
	// BaseBackoff is the wait after the first failure.
	BaseBackoff = 30 * time.Second
	// MaxBackoff caps the doubling. Without a cap the sixth failure would wait
	// sixteen minutes and the twentieth would wait a week.
	MaxBackoff = 10 * time.Minute
	// AttemptTimeout bounds one delivery. An SMTP server that accepts a connection
	// and then says nothing must not hold the dispatcher: this is the whole reason
	// delivery is off the request path, and it would be undone by an unbounded wait.
	AttemptTimeout = 20 * time.Second
	// drainBatch is how many due notifications one pass takes. Bounded so a
	// backlog is worked through steadily rather than in one long burst that starves
	// the rest of the daemon.
	drainBatch = 20
	// idleInterval is how long the dispatcher sleeps when there is nothing due.
	idleInterval = 15 * time.Second
)

// Backoff returns the wait after the given number of failed attempts. Exported
// because the schedule is a contract the test pins, not an implementation detail.
func Backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := BaseBackoff
	for range attempts - 1 {
		d *= 2
		if d >= MaxBackoff {
			return MaxBackoff
		}
	}
	return d
}

// Queue is the persistence the dispatcher needs. Narrowed to these five methods
// so the dispatcher can be tested against a fake without standing up a database,
// and so it is obvious that the dispatcher never reads an event or a message.
type Queue interface {
	DueNotifications(now time.Time, maxAttempts, limit int) ([]events.Notification, error)
	MarkDelivered(id int64, at time.Time) error
	MarkFailed(id int64, attempts int, next time.Time, reason string) error
}

// Directory resolves who a notification is for.
//
// It is an interface over the phonebook rather than the phonebook itself, so this
// package imports no store and the dispatcher can be tested with a map. A lookup
// that misses is not an error — see Dispatcher.deliver.
type Directory interface {
	// RecipientFor returns the person a notification is addressed to. ok is false
	// when there is no such phonebook row.
	RecipientFor(phonebookID int64) (Recipient, bool)
	// Admins returns everyone who should receive an admin or broadcast event —
	// a notification with no phonebook_id.
	Admins() []Recipient
}

// Dispatcher drains the queue and hands notifications to sinks.
type Dispatcher struct {
	q     Queue
	dir   Directory
	sinks []Sink
	// station is the node's callsign, used in subject lines. Read through a
	// function because it changes when the operator edits the config, and a
	// dispatcher that captured it at construction would keep the old one forever.
	station func() string
	now     func() time.Time
	logf    func(string, ...any)
}

// New builds a dispatcher. sinks may be empty, which makes every delivery a
// no-op — the state a node with no notification channels configured is in, and
// not an error.
func New(q Queue, dir Directory, station func() string, sinks ...Sink) *Dispatcher {
	if station == nil {
		station = func() string { return "" }
	}
	return &Dispatcher{
		q: q, dir: dir, sinks: sinks,
		station: station,
		now:     time.Now,
		logf:    func(string, ...any) {},
	}
}

// WithLogger attaches a log sink. Without one the dispatcher is silent.
func (d *Dispatcher) WithLogger(logf func(string, ...any)) *Dispatcher {
	if logf != nil {
		d.logf = logf
	}
	return d
}

// WithClock replaces the clock. Tests use it to walk the backoff schedule without
// sleeping through it.
func (d *Dispatcher) WithClock(now func() time.Time) *Dispatcher {
	if now != nil {
		d.now = now
	}
	return d
}

// Run drains the queue until ctx is cancelled. One goroutine, by design: SMTP
// servers rate-limit, and a node that opened six connections because six things
// happened at once would be doing the wrong thing on purpose.
func (d *Dispatcher) Run(ctx context.Context) {
	t := time.NewTicker(idleInterval)
	defer t.Stop()
	for {
		// Drain before waiting, so a notification enqueued while the dispatcher was
		// starting is not held for a full interval.
		if n := d.DrainOnce(ctx); n > 0 {
			continue // there may be more; come straight back
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// DrainOnce processes one batch of due notifications and returns how many it
// handled. Exported so a test can step the dispatcher deterministically instead
// of racing its goroutine.
func (d *Dispatcher) DrainOnce(ctx context.Context) int {
	due, err := d.q.DueNotifications(d.now(), MaxAttempts, drainBatch)
	if err != nil {
		d.logf("notify: reading the queue failed: %v", err)
		return 0
	}
	handled := 0
	for _, row := range due {
		if ctx.Err() != nil {
			return handled
		}
		d.handle(ctx, row)
		handled++
	}
	return handled
}

// handle delivers one queued row, recording the outcome.
func (d *Dispatcher) handle(ctx context.Context, row events.Notification) {
	n := Notification{
		ID:        row.ID,
		Type:      EventType(row.EventType),
		CreatedAt: row.CreatedAt,
		Payload:   DecodePayload(row.Payload),
	}
	if !n.Type.Valid() {
		// A row written by a newer build. Park it immediately rather than guessing:
		// delivering an unknown event as something else is worse than not
		// delivering it, and the error text says exactly what happened.
		d.park(row, fmt.Sprintf("unknown event type %q; this build does not know how to render it", row.EventType))
		return
	}

	recipients := d.recipients(row)
	if len(recipients) == 0 {
		// Nobody to tell. That is a completed notification, not a failure: an event
		// about somebody with no phonebook row, or an admin event on a node with no
		// admin addresses, has been handled as fully as it can be.
		d.markDelivered(row, "no recipient")
		return
	}

	var failures []string
	delivered := 0
	for _, r := range recipients {
		for _, sink := range d.sinks {
			attemptCtx, cancel := context.WithTimeout(ctx, AttemptTimeout)
			err := sink.Deliver(attemptCtx, n, r)
			cancel()
			switch {
			case err == nil:
				delivered++
			case errors.Is(err, ErrNotApplicable):
				// Not a failure and not an attempt. A recipient with no email is the
				// ordinary case, not a fault.
			default:
				failures = append(failures, fmt.Sprintf("%s -> %s: %v", sink.Name(), r.Callsign, err))
			}
		}
	}

	switch {
	case len(failures) == 0:
		// Either something delivered, or every sink declined as not-applicable.
		// Both are done: there is nothing left to retry.
		reason := ""
		if delivered == 0 {
			reason = "no applicable channel"
		}
		d.markDelivered(row, reason)
	default:
		d.retryOrPark(row, joinFailures(failures))
	}
}

// recipients resolves who a row is for. A row with a phonebook_id is for that
// person; one without is an admin/broadcast event.
func (d *Dispatcher) recipients(row events.Notification) []Recipient {
	if d.dir == nil {
		return nil
	}
	if row.PhonebookID == 0 {
		return d.dir.Admins()
	}
	r, ok := d.dir.RecipientFor(row.PhonebookID)
	if !ok {
		return nil
	}
	return []Recipient{r}
}

func (d *Dispatcher) markDelivered(row events.Notification, why string) {
	if err := d.q.MarkDelivered(row.ID, d.now()); err != nil {
		d.logf("notify: marking notification %d delivered failed: %v", row.ID, err)
		return
	}
	if why != "" {
		d.logf("notify: notification %d (%s) closed with no delivery: %s", row.ID, row.EventType, why)
	}
}

// retryOrPark records a failure and schedules the next attempt, or parks.
func (d *Dispatcher) retryOrPark(row events.Notification, reason string) {
	attempts := row.Attempts + 1
	if attempts >= MaxAttempts {
		d.park(row, reason)
		return
	}
	next := d.now().Add(Backoff(attempts))
	if err := d.q.MarkFailed(row.ID, attempts, next, reason); err != nil {
		d.logf("notify: recording a failed attempt for %d failed: %v", row.ID, err)
		return
	}
	d.logf("notify: notification %d (%s) attempt %d/%d failed, retrying in %s: %s",
		row.ID, row.EventType, attempts, MaxAttempts, Backoff(attempts), reason)
}

// park stops retrying and leaves the reason visible.
//
// attempts is set to MaxAttempts so DueNotifications will not select the row
// again — parking is expressed in the data rather than in dispatcher state, so it
// survives a restart. That is the #238 property: the failure stays diagnosable
// and stops being noisy, and a daemon restart does not resurrect the loop.
func (d *Dispatcher) park(row events.Notification, reason string) {
	if err := d.q.MarkFailed(row.ID, MaxAttempts, d.now(), reason); err != nil {
		d.logf("notify: parking notification %d failed: %v", row.ID, err)
		return
	}
	d.logf("notify: notification %d (%s) PARKED after %d attempts and will not be retried: %s",
		row.ID, row.EventType, MaxAttempts, reason)
}

func joinFailures(f []string) string {
	if len(f) == 1 {
		return f[0]
	}
	out := f[0]
	for _, s := range f[1:] {
		out += "; " + s
	}
	return out
}
