package main

import (
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/events"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/messages"
	"github.com/KN4OQW/waypoint/internal/notify"
	"github.com/KN4OQW/waypoint/internal/phonebook"
	"github.com/KN4OQW/waypoint/internal/store"
)

// The daemon-side wiring: which hub events become notifications, who they are
// addressed to, and that enqueueing never fails a caller.
//
// Delivery is not exercised here — that is internal/notify's job, against a fake
// sink. What this file asserts is the seam: an event the node already publishes
// turns into a queued row, with the right recipient.

func notifyRig(t *testing.T) (*server, *events.Store, *phonebook.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() }) //nolint:errcheck // test cleanup
	ev, err := events.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ev.Close() }) //nolint:errcheck // test cleanup
	pb := phonebook.New(st)
	if _, err := pb.Create(phonebook.Entry{
		Callsign: "KN4OQW", DMRID: 3180202, FullName: "Clint Chance", Email: "clint@example.invalid",
	}); err != nil {
		t.Fatal(err)
	}
	s := &server{hub: hub.New(), store: st, evStore: ev, phonebook: pb}
	return s, ev, pb
}

func queued(t *testing.T, ev *events.Store) []events.Notification {
	t.Helper()
	// Far future so everything queued is due, whatever its schedule.
	rows, err := ev.DueNotifications(time.Now().Add(24*time.Hour), notify.MaxAttempts, 100)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestInboundMessageQueuesANotification is the sms_received seam. The message
// service already publishes this event, so nothing in that package changes and —
// the part that matters — the DMR shim is not touched at all.
func TestInboundMessageQueuesANotification(t *testing.T) {
	s, ev, pb := notifyRig(t)
	me, err := pb.LookupByCallsign("KN4OQW")
	if err != nil {
		t.Fatal(err)
	}

	s.notifyForEvent(hub.Event{
		Type:   messages.EventMessageIn,
		Source: "3110000",
		Dest:   "3180202", // addressed to KN4OQW
	})

	rows := queued(t, ev)
	if len(rows) != 1 {
		t.Fatalf("queued %d notifications, want 1", len(rows))
	}
	n := rows[0]
	if n.EventType != string(notify.EventSMSReceived) {
		t.Errorf("event_type = %q", n.EventType)
	}
	if n.PhonebookID != me.ID {
		t.Errorf("addressed to phonebook %d, want %d (the station it was sent to)", n.PhonebookID, me.ID)
	}
	// The payload names who it was from and never carries the text: the hub event
	// has no text either, and copying correspondence into an email is exactly what
	// this must not do.
	p := notify.DecodePayload(n.Payload)
	if p["from"] != "3110000" {
		t.Errorf("payload from = %v", p["from"])
	}
	for k := range p {
		if k == "text" || k == "body" || k == "message" {
			t.Errorf("the payload carries the message content under %q", k)
		}
	}
}

// TestUnaddressedMessageBecomesABroadcast: a message for a station nobody has
// entered still notifies somebody. Dropping it would silently lose the
// notification for exactly the node that has not filled its phonebook in.
func TestUnaddressedMessageBecomesABroadcast(t *testing.T) {
	s, ev, _ := notifyRig(t)
	s.notifyForEvent(hub.Event{Type: messages.EventMessageIn, Source: "3110000", Dest: "4242424"})

	rows := queued(t, ev)
	if len(rows) != 1 {
		t.Fatalf("queued %d, want 1", len(rows))
	}
	if rows[0].PhonebookID != 0 {
		t.Errorf("phonebook_id = %d, want 0 (broadcast)", rows[0].PhonebookID)
	}
}

// TestOrdinaryTrafficQueuesNothing: the hub carries voice and link events
// constantly, and an email for each would be unusable. Only the mapped types
// produce a notification.
func TestOrdinaryTrafficQueuesNothing(t *testing.T) {
	s, ev, _ := notifyRig(t)
	for _, typ := range []string{
		"rf_voice_start", "rf_voice_end", "net_voice_start", "net_voice_end",
		"link_up", "link_down", "mode", "message_out",
	} {
		s.notifyForEvent(hub.Event{Type: typ, Source: "3180202", Dest: "3110000"})
	}
	if rows := queued(t, ev); len(rows) != 0 {
		t.Errorf("ordinary traffic queued %d notifications", len(rows))
	}
}

// TestEnqueueNeverFailsTheCaller: every caller is on a path that must not wait or
// fail — an API handler, or a goroutine draining the hub. A node with no event
// store simply queues nothing.
func TestEnqueueNeverFailsTheCaller(t *testing.T) {
	s := &server{hub: hub.New()} // no event store, no phonebook
	// The assertion is that this returns at all rather than panicking or blocking.
	s.notifyForEvent(hub.Event{Type: messages.EventMessageIn, Source: "1", Dest: "2"})
	s.enqueueNotification(notify.EventWXAlert, 0, map[string]any{"headline": "x"})
}

// TestDirectoryResolvesFromThePhonebook covers the recipient seam, including the
// case the design turns on: an entry with no email is a recipient the SMTP sink
// declines, not one the directory refuses to return.
func TestDirectoryResolvesFromThePhonebook(t *testing.T) {
	_, _, pb := notifyRig(t)
	if _, err := pb.Create(phonebook.Entry{Callsign: "W1AW", FullName: "ARRL"}); err != nil {
		t.Fatal(err)
	}
	d := notifyDirectory{pb: pb}

	me, _ := pb.LookupByCallsign("KN4OQW")
	got, ok := d.RecipientFor(me.ID)
	if !ok || got.Email != "clint@example.invalid" || got.Callsign != "KN4OQW" {
		t.Errorf("RecipientFor = %+v, ok=%v", got, ok)
	}
	if _, ok := d.RecipientFor(4242); ok {
		t.Error("resolved a phonebook row that does not exist")
	}
	// Admins are everyone with an address; the entry without one is left out
	// rather than returned with an empty Email for the sink to decline.
	admins := d.Admins()
	if len(admins) != 1 || admins[0].Callsign != "KN4OQW" {
		t.Errorf("Admins() = %+v, want just the entry with an address", admins)
	}
}

// TestSMTPConfigReflectsTheStore: the sink reads settings per delivery, so an
// operator correcting the server does not have to restart the daemon. It also
// pins that "notifications disabled" reads as unconfigured rather than as a
// half-configured server the sink would try to reach.
func TestSMTPConfigReflectsTheStore(t *testing.T) {
	s, _, _ := notifyRig(t)
	if got := s.smtpConfig(); got.Configured() {
		t.Error("an empty store reported a configured mail server")
	}
	if err := s.store.Set("notify", config.Notify{
		Enabled: false, SMTPHost: "mail.example.invalid", SMTPFrom: "a@b.invalid",
	}, "test"); err != nil {
		t.Fatal(err)
	}
	if got := s.smtpConfig(); got.Configured() {
		t.Error("notifications are disabled but the sink was handed a usable config")
	}
	if err := s.store.Set("notify", config.Notify{
		Enabled: true, SMTPHost: "mail.example.invalid", SMTPFrom: "a@b.invalid",
		SMTPPassword: "secret", SMTPAllowPlaintext: false,
	}, "test"); err != nil {
		t.Fatal(err)
	}
	got := s.smtpConfig()
	if !got.Configured() {
		t.Fatal("an enabled, populated config did not report as configured")
	}
	if got.Port != 587 {
		t.Errorf("port = %d, want the 587 default", got.Port)
	}
	// AllowPlaintext off means RequireTLS on — the control is named for what it
	// permits, so the inversion happens exactly once, here.
	if !got.RequireTLS {
		t.Error("TLS should be required when plaintext is not allowed")
	}
}
