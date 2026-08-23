package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/events"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/messages"
	"github.com/KN4OQW/waypoint/internal/notify"
	"github.com/KN4OQW/waypoint/internal/phonebook"
)

// Wiring for the notifier: where events are noticed, who they are for, and the
// one goroutine that delivers them.
//
// Nothing here delivers anything. Every path enqueues and returns — a hub
// subscription that sees a message arrive, an API handler that created an
// account — and the dispatcher started by startNotify does the rest on its own
// goroutine. That separation is the point of the package; see internal/notify.

// notifyDirectory resolves recipients out of the phonebook.
//
// It is the seam internal/notify declares rather than the phonebook itself, so
// that package imports no store and can be tested against a map. Everything here
// is a read.
type notifyDirectory struct {
	pb    *phonebook.Store
	accts func() []notify.Recipient // admin recipients; see Admins
}

func (d notifyDirectory) RecipientFor(id int64) (notify.Recipient, bool) {
	if d.pb == nil {
		return notify.Recipient{}, false
	}
	e, err := d.pb.Get(id)
	if err != nil {
		return notify.Recipient{}, false
	}
	return notify.Recipient{
		PhonebookID: e.ID, Callsign: e.Callsign, Name: e.FullName, Email: e.Email,
	}, true
}

// Admins returns who receives an admin or broadcast notification.
//
// Today that is every phonebook entry with an email address, which is a
// deliberately blunt answer and is written down as such: the precise one is
// "everyone holding an admin account", and accounts do not exist on this branch
// yet. When they do, this becomes a query over accounts joined to phonebook and
// nothing else here changes.
func (d notifyDirectory) Admins() []notify.Recipient {
	if d.accts != nil {
		return d.accts()
	}
	if d.pb == nil {
		return nil
	}
	list, err := d.pb.List()
	if err != nil {
		return nil
	}
	out := []notify.Recipient{}
	for _, e := range list {
		if strings.TrimSpace(e.Email) == "" {
			continue
		}
		out = append(out, notify.Recipient{
			PhonebookID: e.ID, Callsign: e.Callsign, Name: e.FullName, Email: e.Email,
		})
	}
	return out
}

// smtpConfig reads the SMTP settings out of the store on every delivery, so an
// operator correcting the server address does not have to restart the daemon for
// the next retry to use it.
func (s *server) smtpConfig() notify.SMTPConfig {
	m, err := config.Load(s.store)
	if err != nil || !m.Notify.Enabled {
		// Disabled reads as unconfigured: the sink declines, the dispatcher retries
		// and then parks with a visible reason, and nothing is sent.
		return notify.SMTPConfig{}
	}
	port, _ := strconv.Atoi(m.Notify.Port())
	return notify.SMTPConfig{
		Host:               m.Notify.SMTPHost,
		Port:               port,
		ImplicitTLS:        m.Notify.SMTPImplicitTLS,
		RequireTLS:         !m.Notify.SMTPAllowPlaintext,
		InsecureSkipVerify: m.Notify.SMTPInsecureSkipVerify,
		Username:           m.Notify.SMTPUsername,
		Password:           m.Notify.SMTPPassword,
		From:               m.Notify.SMTPFrom,
	}
}

// startNotify brings the dispatcher up and subscribes it to the event sources
// that already exist.
//
// It is a no-op without an event store: the queue lives there, and a node running
// with no history (demo mode) has nowhere to put a notification.
func (s *server) startNotify(ctx context.Context) {
	if s.evStore == nil {
		return
	}
	sink := notify.NewSMTPSink(s.smtpConfig, s.stationCallsign)
	s.notifier = notify.New(
		s.evStore,
		notifyDirectory{pb: s.phonebook},
		s.stationCallsign,
		sink,
	).WithLogger(log.Printf)

	go s.notifier.Run(ctx)
	go s.watchForNotifiableEvents(ctx)
}

// watchForNotifiableEvents turns hub events into queued notifications.
//
// Subscribing to the hub rather than calling into internal/messages is what keeps
// this wiring at zero coupling: the message service already publishes an event
// when one arrives, so nothing in that package changes, and — importantly —
// nothing touches the DMR shim, whose contract is that an observer never mutates
// or blocks a frame.
//
// It is also how weather alerts will arrive. The weather service publishes hub
// events too, so when it lands the only change here is another case in the switch.
func (s *server) watchForNotifiableEvents(ctx context.Context) {
	ch, _, cancel := s.hub.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-ch:
			s.notifyForEvent(e)
		}
	}
}

// notifyForEvent maps one hub event onto a notification, or ignores it.
//
// The overwhelming majority of hub events are voice traffic and link state, which
// nobody wants an email about. Only the types below produce a notification.
func (s *server) notifyForEvent(e hub.Event) {
	switch e.Type {
	// The constant rather than the literal: a rename in internal/messages would
	// otherwise leave this switch silently matching nothing, and a notification
	// that stops being sent is not a failure anything reports.
	case messages.EventMessageIn:
		// An inbound text message. The notification says one arrived and names who
		// it came from; it never carries the text, because that is correspondence
		// and copying it into email would move it onto a surface the operator never
		// agreed to. The same reason the hub event itself carries no text.
		s.enqueueNotification(notify.EventSMSReceived, s.phonebookIDForDMRID(e.Dest), map[string]any{
			"from": e.Source,
			"to":   e.Dest,
		})
	}
}

// phonebookIDForDMRID resolves the addressed station to a phonebook row, so an
// inbound message notifies the person it was addressed to rather than everybody.
//
// A miss returns 0, which enqueues the notification as a broadcast. That is the
// right fallback for a message this node received: somebody should see it, and
// the alternative — dropping it because the addressee is not in the phonebook —
// would silently lose the notification for exactly the node that has not filled
// its phonebook in.
func (s *server) phonebookIDForDMRID(dest string) int64 {
	if s.phonebook == nil || dest == "" {
		return 0
	}
	id, err := strconv.ParseUint(strings.TrimSpace(dest), 10, 32)
	if err != nil || id == 0 {
		return 0
	}
	e, err := s.phonebook.LookupByDMRID(uint32(id))
	if err != nil {
		return 0
	}
	return e.ID
}

// enqueueNotification writes one notification and returns immediately.
//
// Every caller is on a path that must not wait — an API handler, a hub
// subscription draining a channel the hub publishes into — so a failure here is
// logged and dropped rather than propagated. A notification that could not be
// queued is a missed email; a caller that blocked or failed because of one would
// be a broken dashboard or a stalled event stream.
func (s *server) enqueueNotification(t notify.EventType, phonebookID int64, payload map[string]any) {
	if s.evStore == nil {
		return
	}
	body, err := notify.EncodePayload(payload)
	if err != nil {
		log.Printf("notify: encoding a %s payload: %v", t, err)
		return
	}
	n, err := s.evStore.EnqueueNotification(events.Notification{
		EventType:   string(t),
		PhonebookID: phonebookID,
		Payload:     body,
		CreatedAt:   time.Now().UTC(),
	})
	if err != nil {
		log.Printf("notify: queueing a %s notification: %v", t, err)
		return
	}
	log.Printf("notify: queued notification %d (%s)", n.ID, t)
}

// registerNotifyRoutes mounts the notification API. Admin-only by the route
// mapping; nothing here re-derives its own permission.
func (s *server) registerNotifyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/notify/test", s.notifyTest)
	mux.HandleFunc("/api/notify/parked", s.notifyParked)
}

// notifyTest enqueues a test notification to the caller's own phonebook address.
//
// It ENQUEUES rather than sending, which is the whole point: a test button that
// delivered inline would hang the dashboard for the SMTP timeout against exactly
// the misconfigured server it is there to diagnose. The response says it was
// queued and names the id, so an operator who sees nothing arrive can find the
// row and read its error.
func (s *server) notifyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.evStore == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "no event store on this node"})
		return
	}
	var body struct {
		PhonebookID int64 `json:"phonebook_id"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body)

	if body.PhonebookID == 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "name the phonebook entry to send the test to",
			"field": "phonebook_id",
		})
		return
	}
	dir := notifyDirectory{pb: s.phonebook}
	rec, ok := dir.RecipientFor(body.PhonebookID)
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "no such phonebook entry"})
		return
	}
	if strings.TrimSpace(rec.Email) == "" {
		// Said plainly rather than queued and silently no-opped: a test that
		// reported success and delivered nothing is worse than no test.
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "that phonebook entry has no email address, so there is nowhere to send a test",
			"field": "phonebook_id",
		})
		return
	}
	s.enqueueNotification(notify.EventAccountCreated, body.PhonebookID, map[string]any{
		"username": rec.Callsign,
		"test":     true,
	})
	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"queued": true,
		"to":     rec.Email,
		"note":   "queued for delivery; if nothing arrives, check the parked list for the reason",
	})
}

// notifyParked lists notifications that gave up, which is how an operator finds
// out WHY nothing arrived.
func (s *server) notifyParked(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.evStore == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "no event store on this node"})
		return
	}
	rows, err := s.evStore.ParkedNotifications(notify.MaxAttempts, 50)
	if err != nil {
		log.Printf("notify: reading parked notifications: %v", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not read the queue"})
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, n := range rows {
		out = append(out, map[string]any{
			"id":         n.ID,
			"event_type": n.EventType,
			"created_at": n.CreatedAt.UTC().Format(time.RFC3339),
			"attempts":   n.Attempts,
			"last_error": n.LastError,
		})
	}
	writeJSON(w, map[string]any{"parked": out})
}
