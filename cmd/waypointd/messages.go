package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/dmrdata"
	"github.com/KN4OQW/waypoint/internal/events"
	"github.com/KN4OQW/waypoint/internal/messages"
)

// The text-message API.
//
// Every route here sits behind s.auth.Gate like the rest of /api — the gate is
// default-deny, so registering a route is what makes it authenticated, and there
// is nothing to opt in to. That is deliberate for this surface in particular:
// messages are correspondence, and a public dashboard must never carry them. They
// appear in no public-view response type, which internal/publicview's field audit
// enforces against the types themselves rather than against handler discipline.

// messageRequest is POST /api/messages.
type messageRequest struct {
	// DMRID is who to send to: a radio's id, or a talkgroup with Group set.
	DMRID uint32 `json:"dmr_id"`
	Text  string `json:"text"`
	Group bool   `json:"group,omitempty"`
}

// messagesRoutes registers the message surface.
func (s *server) messagesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/messages", s.messagesCollection)
	mux.HandleFunc("/api/messages/", s.messageItem)
}

// messagesCollection serves POST (send) and GET (list).
func (s *server) messagesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.messageSend(w, r)
	case http.MethodGet:
		s.messageList(w, r)
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// messageSend accepts a message for transmission.
//
// It answers 202, not 200. The message has been recorded and queued; it has not
// been transmitted, because transmitting takes air time and may have to wait for
// the channel. The response body is the stored message, so a client has the id to
// watch and the state to start from.
func (s *server) messageSend(w http.ResponseWriter, r *http.Request) {
	if s.msgs == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable,
			map[string]string{"error": "messaging is not available on this node"})
		return
	}
	body, err := readBody(r)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "unreadable body"})
		return
	}
	var req messageRequest
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	m, err := s.msgs.Send(req.DMRID, req.Text, req.Group)
	switch {
	case err == nil:
		writeJSONStatus(w, http.StatusAccepted, m)
	case errors.Is(err, dmrdata.ErrTextTooLong):
		// Say what the limit IS. "Too long" alone leaves the operator to bisect.
		writeJSONStatus(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error":     err.Error(),
			"max_units": dmrdata.MaxTextUnits,
			"hint":      "the limit is in UTF-16 code units, so a character outside the BMP costs two",
		})
	case errors.Is(err, dmrdata.ErrInvalidText),
		errors.Is(err, dmrdata.ErrBadAddress),
		errors.Is(err, events.ErrBadMessage):
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, messages.ErrNotConfigured), errors.Is(err, messages.ErrNoRelay):
		// The node cannot do this yet, and the reason is a setting rather than the
		// request. 409 rather than 400: nothing about the message was wrong.
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, messages.ErrQueueFull):
		writeJSONStatus(w, http.StatusTooManyRequests, map[string]any{"error": err.Error(), "message": m})
	default:
		log.Printf("messages: send: %v", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not queue the message"})
	}
}

// messageList serves GET /api/messages, filterable.
func (s *server) messageList(w http.ResponseWriter, r *http.Request) {
	if s.evStore == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable,
			map[string]string{"error": "no message store on this node"})
		return
	}
	q := r.URL.Query()
	query := events.MessageQuery{
		Direction: events.Direction(q.Get("direction")),
		State:     events.MessageState(q.Get("state")),
	}
	if v := q.Get("peer"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil || n > 0xFFFFFF {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "peer must be a DMR ID"})
			return
		}
		query.Peer = uint32(n)
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "limit must be a number"})
			return
		}
		query.Limit = n // the store clamps it
	}
	switch query.Direction {
	case "", events.Outbound, events.Inbound:
	default:
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": `direction must be "out" or "in"`})
		return
	}

	list, err := s.evStore.Messages(query)
	if err != nil {
		log.Printf("messages: list: %v", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not read messages"})
		return
	}
	// Never null: a client rendering a list should get an empty one, not a type
	// error on the first node that has sent nothing.
	if list == nil {
		list = []events.Message{}
	}
	writeJSON(w, map[string]any{"messages": list})
}

// messageItem serves GET /api/messages/{id}.
func (s *server) messageItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.evStore == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable,
			map[string]string{"error": "no message store on this node"})
		return
	}
	idText := strings.TrimPrefix(r.URL.Path, "/api/messages/")
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "id must be a number"})
		return
	}
	m, err := s.evStore.Message(id)
	switch {
	case errors.Is(err, events.ErrMessageNotFound):
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "no such message"})
	case err != nil:
		log.Printf("messages: read %d: %v", id, err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not read the message"})
	default:
		writeJSON(w, m)
	}
}

// startMessages brings the message service up. It is live-mode only and needs both
// an event store to record into and a relay to transmit through; without either it
// stays nil and the API answers 503 rather than pretending.
func (s *server) startMessages(ctx context.Context) {
	if s.evStore == nil {
		return
	}
	s.msgs = messages.New(s.evStore, s.hub,
		func() messages.Relay {
			// Per call, not captured: the relay is reconciled on a tick and rebuilt
			// whenever its ports move, and a captured one would go stale on the first
			// Apply that touched them. A typed nil must not be returned as a non-nil
			// interface, so the nil case is explicit.
			if sh := s.relay.shimOrNil(); sh != nil {
				return sh
			}
			return nil
		},
		s.messageWiring)
	go s.msgs.Run(ctx)
	// The inbound capture watches the same relay. It is a tap and cannot hold a
	// frame up, so radio-to-network messages keep working exactly as before —
	// see internal/messages/inbound.go.
	s.msgs.StartCapture(ctx)
}

// messageWiring reads the node's DMR settings for one message. Read per message
// rather than captured, so changing the DMR ID or the timeslot takes effect on the
// next message without a restart.
func (s *server) messageWiring() (messages.Wiring, error) {
	m, err := config.Load(s.store)
	if err != nil {
		return messages.Wiring{}, err
	}
	w := messages.Wiring{
		Duplex: m.General.Duplex,
		Slot:   messageSlot(m),
	}
	// [DMR] Id overrides [General] Id, the same precedence the renderer uses.
	dmrID := strings.TrimSpace(m.DMR.ID)
	if dmrID == "" {
		dmrID = strings.TrimSpace(m.General.ID)
	}
	if id, err := strconv.ParseUint(dmrID, 10, 32); err == nil {
		w.LocalID = uint32(id)
	}
	if cc, err := strconv.Atoi(strings.TrimSpace(m.DMR.ColorCode)); err == nil {
		w.ColorCode = byte(cc)
	}
	return w, nil
}

// messageSlot picks the timeslot to transmit on: slot 2 when it is enabled, which
// is where a hotspot carries its traffic, and slot 1 only when slot 2 is off. A
// configuration with neither enabled has no working DMR at all and is reported by
// the readiness guard; sending on 2 is as good a guess as any.
func messageSlot(m *config.Model) int {
	if m.DMRNet.Slot2 || !m.DMRNet.Slot1 {
		return 2
	}
	return 1
}
