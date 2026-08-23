package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/events"
)

// The dispatcher, against a fake sink and a fake queue. No SMTP server is
// contacted anywhere in this file, and none is contacted in CI: the sink seam
// exists partly so delivery can be tested without one.

// fakeSink records what it was asked to deliver and answers however the test says.
type fakeSink struct {
	name     string
	err      error // returned by every Deliver until changed
	delivers []delivery
}

type delivery struct {
	n Notification
	r Recipient
}

func (f *fakeSink) Name() string { return f.name }

func (f *fakeSink) Deliver(_ context.Context, n Notification, r Recipient) error {
	f.delivers = append(f.delivers, delivery{n, r})
	return f.err
}

// fakeQueue is the persistence, in memory, with the same selection rule the real
// one has — including that a parked row is never returned.
type fakeQueue struct {
	rows []events.Notification
	next int64
}

func (q *fakeQueue) add(n events.Notification) int64 {
	q.next++
	n.ID = q.next
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Unix(0, 0).UTC()
	}
	q.rows = append(q.rows, n)
	return n.ID
}

func (q *fakeQueue) DueNotifications(now time.Time, maxAttempts, limit int) ([]events.Notification, error) {
	out := []events.Notification{}
	for _, r := range q.rows {
		if r.Delivered() || r.Attempts >= maxAttempts || r.NextAttemptAt.After(now) {
			continue
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (q *fakeQueue) MarkDelivered(id int64, at time.Time) error {
	for i := range q.rows {
		if q.rows[i].ID == id {
			q.rows[i].DeliveredAt = at
			q.rows[i].LastError = ""
		}
	}
	return nil
}

func (q *fakeQueue) MarkFailed(id int64, attempts int, next time.Time, reason string) error {
	for i := range q.rows {
		if q.rows[i].ID == id {
			q.rows[i].Attempts = attempts
			q.rows[i].NextAttemptAt = next
			q.rows[i].LastError = reason
		}
	}
	return nil
}

func (q *fakeQueue) row(id int64) events.Notification {
	for _, r := range q.rows {
		if r.ID == id {
			return r
		}
	}
	return events.Notification{}
}

// fakeDir is a phonebook.
type fakeDir struct {
	people map[int64]Recipient
	admins []Recipient
}

func (d fakeDir) RecipientFor(id int64) (Recipient, bool) { r, ok := d.people[id]; return r, ok }
func (d fakeDir) Admins() []Recipient                     { return d.admins }

// clock is a settable clock so the backoff schedule can be walked without waiting.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newRig(t *testing.T, sinkErr error) (*Dispatcher, *fakeQueue, *fakeSink, *clock) {
	t.Helper()
	q := &fakeQueue{}
	sink := &fakeSink{name: "fake", err: sinkErr}
	dir := fakeDir{
		people: map[int64]Recipient{
			1: {PhonebookID: 1, Callsign: "KN4OQW", Name: "Clint", Email: "clint@example.invalid"},
			2: {PhonebookID: 2, Callsign: "W1AW", Name: "ARRL"}, // deliberately no email
		},
		admins: []Recipient{{PhonebookID: 1, Callsign: "KN4OQW", Email: "clint@example.invalid"}},
	}
	c := &clock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	d := New(q, dir, func() string { return "KN4OQW" }, sink).WithClock(c.now)
	return d, q, sink, c
}

func enqueue(q *fakeQueue, typ EventType, pbID int64) int64 {
	payload, _ := EncodePayload(map[string]any{"username": "kn4oqw", "from": "3180202"})
	return q.add(events.Notification{
		EventType: string(typ), PhonebookID: pbID, Payload: payload,
		NextAttemptAt: time.Unix(0, 0).UTC(),
	})
}

// TestEnqueueAndDrain: a queued notification reaches the sink once and is marked
// delivered.
func TestEnqueueAndDrain(t *testing.T) {
	d, q, sink, c := newRig(t, nil)
	id := enqueue(q, EventAccountCreated, 1)

	if n := d.DrainOnce(context.Background()); n != 1 {
		t.Fatalf("drained %d, want 1", n)
	}
	if len(sink.delivers) != 1 {
		t.Fatalf("sink saw %d deliveries, want 1", len(sink.delivers))
	}
	got := sink.delivers[0]
	if got.r.Email != "clint@example.invalid" {
		t.Errorf("delivered to %q", got.r.Email)
	}
	if got.n.Type != EventAccountCreated {
		t.Errorf("delivered type %q", got.n.Type)
	}
	row := q.row(id)
	if !row.Delivered() || !row.DeliveredAt.Equal(c.t) {
		t.Errorf("row not marked delivered: %+v", row)
	}
	// And a second drain does nothing: delivered rows are not selected again.
	if n := d.DrainOnce(context.Background()); n != 0 {
		t.Errorf("a delivered notification was drained again (%d)", n)
	}
}

// TestNoEmailIsASilentNoOp: a recipient with no address is not a failure. Most
// people in a phonebook have no email, and parking a notification for that would
// fill the panel with things that were never deliverable.
func TestNoEmailIsASilentNoOp(t *testing.T) {
	d, q, sink, _ := newRig(t, nil)
	// Sink declines, the way SMTPSink does for a recipient with no address.
	sink.err = ErrNotApplicable
	id := enqueue(q, EventSMSReceived, 2) // W1AW, no email

	d.DrainOnce(context.Background())
	row := q.row(id)
	if !row.Delivered() {
		t.Error("a not-applicable delivery left the notification undelivered")
	}
	if row.Attempts != 0 {
		t.Errorf("attempts = %d; declining is not an attempt", row.Attempts)
	}
	if row.LastError != "" {
		t.Errorf("last_error = %q; declining is not an error", row.LastError)
	}
}

// TestUnknownRecipientIsClosed: an event about a phonebook row that has since been
// deleted has nobody to tell, which is done rather than broken.
func TestUnknownRecipientIsClosed(t *testing.T) {
	d, q, sink, _ := newRig(t, nil)
	id := enqueue(q, EventSMSReceived, 999)

	d.DrainOnce(context.Background())
	if len(sink.delivers) != 0 {
		t.Error("the sink was called for a recipient that does not exist")
	}
	if !q.row(id).Delivered() {
		t.Error("a notification with no recipient was left undelivered")
	}
}

// TestBackoffSchedule pins the retry timing. It is a contract rather than an
// implementation detail: the numbers decide whether a node whose mail server is
// down for an afternoon still delivers when it returns.
func TestBackoffSchedule(t *testing.T) {
	for _, tc := range []struct {
		attempts int
		want     time.Duration
	}{
		{1, 30 * time.Second},
		{2, 1 * time.Minute},
		{3, 2 * time.Minute},
		{4, 4 * time.Minute},
		{5, 8 * time.Minute},
		{6, MaxBackoff}, // capped
		{20, MaxBackoff},
		{0, 30 * time.Second}, // defensive: treated as the first
	} {
		if got := Backoff(tc.attempts); got != tc.want {
			t.Errorf("Backoff(%d) = %s, want %s", tc.attempts, got, tc.want)
		}
	}
}

// TestRetryThenPark walks a permanently failing notification all the way through
// and asserts the #238 property: it stops, it stops VISIBLY, and it stays stopped.
func TestRetryThenPark(t *testing.T) {
	d, q, sink, c := newRig(t, errors.New("connection refused"))
	id := enqueue(q, EventAccountCreated, 1)

	for attempt := 1; attempt < MaxAttempts; attempt++ {
		if n := d.DrainOnce(context.Background()); n != 1 {
			t.Fatalf("attempt %d: drained %d, want 1", attempt, n)
		}
		row := q.row(id)
		if row.Attempts != attempt {
			t.Fatalf("after attempt %d, attempts = %d", attempt, row.Attempts)
		}
		if row.Delivered() {
			t.Fatalf("a failing delivery was marked delivered on attempt %d", attempt)
		}
		// The error is stored, not just logged: a parked notification has to be
		// diagnosable later by somebody who was not watching.
		if !strings.Contains(row.LastError, "connection refused") {
			t.Errorf("last_error = %q, want the sink's reason", row.LastError)
		}
		// Not due yet, so a drain right now does nothing.
		if n := d.DrainOnce(context.Background()); n != 0 {
			t.Errorf("attempt %d: retried before the backoff elapsed", attempt)
		}
		// Advance past the backoff for this attempt.
		c.t = c.t.Add(Backoff(attempt) + time.Second)
	}

	// The final attempt parks it.
	if n := d.DrainOnce(context.Background()); n != 1 {
		t.Fatalf("final attempt drained %d, want 1", n)
	}
	row := q.row(id)
	if row.Attempts != MaxAttempts {
		t.Errorf("parked with attempts = %d, want %d", row.Attempts, MaxAttempts)
	}
	if row.Delivered() {
		t.Error("a parked notification was marked delivered")
	}
	if row.LastError == "" {
		t.Error("a parked notification has no error recorded; the reason must stay visible")
	}

	// And it never comes back, however far the clock moves. This is the property
	// #238 was about: a permanent failure must not become a permanent loop.
	sawBefore := len(sink.delivers)
	for range 5 {
		c.t = c.t.Add(24 * time.Hour)
		if n := d.DrainOnce(context.Background()); n != 0 {
			t.Fatalf("a parked notification was retried")
		}
	}
	if len(sink.delivers) != sawBefore {
		t.Errorf("the sink was called %d more times after parking", len(sink.delivers)-sawBefore)
	}
}

// TestRecoveryBeforeParking: a server that comes back mid-schedule delivers, and
// the stored error is cleared so the panel stops reporting a problem that is over.
func TestRecoveryBeforeParking(t *testing.T) {
	d, q, sink, c := newRig(t, errors.New("connection refused"))
	id := enqueue(q, EventPasswordChanged, 1)

	d.DrainOnce(context.Background())
	if q.row(id).Attempts != 1 {
		t.Fatal("first attempt did not record a failure")
	}
	sink.err = nil
	c.t = c.t.Add(Backoff(1) + time.Second)

	d.DrainOnce(context.Background())
	row := q.row(id)
	if !row.Delivered() {
		t.Fatal("delivery after recovery did not mark the row delivered")
	}
	if row.LastError != "" {
		t.Errorf("last_error = %q after a successful delivery; it must be cleared", row.LastError)
	}
}

// TestUnknownEventTypeParksImmediately: a row written by a newer build is parked
// rather than guessed at. Delivering an unknown event as something else is worse
// than not delivering it.
func TestUnknownEventTypeParksImmediately(t *testing.T) {
	d, q, sink, _ := newRig(t, nil)
	id := q.add(events.Notification{
		EventType: "something_a_newer_build_wrote", PhonebookID: 1,
		Payload: "{}", NextAttemptAt: time.Unix(0, 0).UTC(),
	})

	d.DrainOnce(context.Background())
	if len(sink.delivers) != 0 {
		t.Error("an unknown event type was handed to a sink")
	}
	row := q.row(id)
	if row.Attempts != MaxAttempts || row.Delivered() {
		t.Errorf("unknown type not parked: %+v", row)
	}
	if !strings.Contains(row.LastError, "unknown event type") {
		t.Errorf("last_error = %q, want it to name the problem", row.LastError)
	}
}

// TestAdminBroadcastGoesToAdmins: a notification with no phonebook_id is an admin
// event and reaches every admin recipient.
func TestAdminBroadcastGoesToAdmins(t *testing.T) {
	d, q, sink, _ := newRig(t, nil)
	enqueue(q, EventWXAlert, 0)

	d.DrainOnce(context.Background())
	if len(sink.delivers) != 1 {
		t.Fatalf("sink saw %d deliveries, want 1 (one admin)", len(sink.delivers))
	}
	if sink.delivers[0].r.Callsign != "KN4OQW" {
		t.Errorf("delivered to %q", sink.delivers[0].r.Callsign)
	}
}

// TestNoSinksIsNotAFailure: a node with no notification channels configured has
// nothing to do, which is not the same as failing to do it.
func TestNoSinksIsNotAFailure(t *testing.T) {
	q := &fakeQueue{}
	c := &clock{t: time.Now()}
	d := New(q, fakeDir{people: map[int64]Recipient{1: {PhonebookID: 1, Email: "a@b.invalid"}}},
		nil).WithClock(c.now)
	id := enqueue(q, EventSMSReceived, 1)

	d.DrainOnce(context.Background())
	row := q.row(id)
	if !row.Delivered() {
		t.Error("with no sinks the notification should be closed, not left pending")
	}
	if row.Attempts != 0 {
		t.Errorf("attempts = %d with no sinks", row.Attempts)
	}
}
