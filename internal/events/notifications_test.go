package events

import (
	"testing"
	"time"
)

// The queue against the real database, which is where the selection rule has to
// hold: a parked row must never be READ BACK, or a node with one permanently
// failing notification would wake, load it, skip it and sleep, forever.

func newNotifyStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() }) //nolint:errcheck // test cleanup
	return s
}

func TestEnqueueAndSelectDue(t *testing.T) {
	s := newNotifyStore(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	n, err := s.EnqueueNotification(Notification{
		EventType: "sms_received", PhonebookID: 7, Payload: `{"from":"3180202"}`, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.ID == 0 {
		t.Fatal("enqueue returned no id")
	}

	due, err := s.DueNotifications(now, 6, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due, want 1", len(due))
	}
	got := due[0]
	if got.EventType != "sms_received" || got.PhonebookID != 7 || got.Payload != `{"from":"3180202"}` {
		t.Errorf("round trip lost a field: %+v", got)
	}
	if got.Attempts != 0 || got.Delivered() || got.LastError != "" {
		t.Errorf("a fresh notification is not fresh: %+v", got)
	}
}

// TestBroadcastHasNoPhonebookID: an admin event stores NULL and reads back as 0,
// so callers need no nil check for the ordinary case.
func TestBroadcastHasNoPhonebookID(t *testing.T) {
	s := newNotifyStore(t)
	now := time.Now().UTC()
	if _, err := s.EnqueueNotification(Notification{EventType: "wx_alert", Payload: "{}", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	due, err := s.DueNotifications(now, 6, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due = %d (%v)", len(due), err)
	}
	if due[0].PhonebookID != 0 {
		t.Errorf("phonebook_id = %d, want 0 for a broadcast", due[0].PhonebookID)
	}
}

// TestNotDueUntilItsTime: a row scheduled forward is not selected early.
func TestNotDueUntilItsTime(t *testing.T) {
	s := newNotifyStore(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	n, err := s.EnqueueNotification(Notification{EventType: "wx_alert", Payload: "{}", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFailed(n.ID, 1, now.Add(30*time.Second), "refused"); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.DueNotifications(now.Add(10*time.Second), 6, 10); len(due) != 0 {
		t.Errorf("selected %d rows before the backoff elapsed", len(due))
	}
	if due, _ := s.DueNotifications(now.Add(31*time.Second), 6, 10); len(due) != 1 {
		t.Errorf("did not select the row once it was due")
	}
}

// TestParkedIsNeverSelected is the #238 property at the storage layer. A parked
// row is inert because the QUERY excludes it, not because a caller remembers to
// skip it — so it survives a restart and cannot be revived by a code path that
// forgot.
func TestParkedIsNeverSelected(t *testing.T) {
	s := newNotifyStore(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	n, err := s.EnqueueNotification(Notification{EventType: "sms_received", PhonebookID: 1, Payload: "{}", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFailed(n.ID, 6, now, "no mail server is configured"); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now, now.Add(time.Hour), now.Add(30 * 24 * time.Hour)} {
		if due, _ := s.DueNotifications(at, 6, 10); len(due) != 0 {
			t.Fatalf("a parked notification was selected at %s", at)
		}
	}
	// But it IS visible, which is the other half: it stops being noisy without
	// becoming invisible.
	parked, err := s.ParkedNotifications(6, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(parked) != 1 || parked[0].LastError != "no mail server is configured" {
		t.Fatalf("parked list = %+v, want the row with its reason", parked)
	}
}

// TestDeliveredClearsTheError: a notification that succeeds after failing must not
// leave a stale reason behind for the panel to report.
func TestDeliveredClearsTheError(t *testing.T) {
	s := newNotifyStore(t)
	now := time.Now().UTC()
	n, _ := s.EnqueueNotification(Notification{EventType: "sms_received", PhonebookID: 1, Payload: "{}", CreatedAt: now})
	if err := s.MarkFailed(n.ID, 1, now, "temporary failure"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDelivered(n.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Notification(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Delivered() {
		t.Error("not marked delivered")
	}
	if got.LastError != "" {
		t.Errorf("last_error = %q after delivery, want it cleared", got.LastError)
	}
}

// TestPruneKeepsParked: housekeeping removes what succeeded and keeps what never
// did. Deleting a parked row would make the failure disappear rather than the
// problem.
func TestPruneKeepsParked(t *testing.T) {
	s := newNotifyStore(t)
	old := time.Now().UTC().Add(-90 * 24 * time.Hour)

	done, _ := s.EnqueueNotification(Notification{EventType: "sms_received", PhonebookID: 1, Payload: "{}", CreatedAt: old})
	if err := s.MarkDelivered(done.ID, old); err != nil {
		t.Fatal(err)
	}
	stuck, _ := s.EnqueueNotification(Notification{EventType: "sms_received", PhonebookID: 1, Payload: "{}", CreatedAt: old})
	if err := s.MarkFailed(stuck.ID, 6, old, "refused"); err != nil {
		t.Fatal(err)
	}

	n, err := s.PruneNotifications(time.Now().UTC().Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want 1", n)
	}
	if _, err := s.Notification(stuck.ID); err != nil {
		t.Error("the parked notification was pruned; a failure must not be tidied away")
	}
}

// TestNotificationSchemaSurvivesReopen: the queue is added by the same idempotent
// DDL the rest of this store uses, so opening an existing database twice must not
// disturb it.
func TestNotificationSchemaSurvivesReopen(t *testing.T) {
	dir := t.TempDir() + "/events.db"
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := s.EnqueueNotification(Notification{EventType: "wx_alert", Payload: "{}", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(dir)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer again.Close() //nolint:errcheck // test cleanup
	due, err := again.DueNotifications(now, 6, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Errorf("the queued notification did not survive a reopen (%d rows)", len(due))
	}
}
