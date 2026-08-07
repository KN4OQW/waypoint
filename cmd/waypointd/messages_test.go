package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/dmrdata"
	"github.com/KN4OQW/waypoint/internal/dmrshim"
	"github.com/KN4OQW/waypoint/internal/events"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/messages"
	"github.com/KN4OQW/waypoint/internal/store"
)

// nullRelay accepts frames and drops them. The API tests are about the HTTP
// surface, not about what reaches the air — internal/messages covers that.
type nullRelay struct{}

func (nullRelay) InjectToHost([]byte) error { return nil }
func (nullRelay) AddTap(dmrshim.Tap) func() { return func() {} }
func (nullRelay) relayFor() messages.Relay  { return nullRelay{} }
func newNullRelay() func() messages.Relay   { return nullRelay{}.relayFor }
func wiringFor(id uint32) func() (messages.Wiring, error) {
	return func() (messages.Wiring, error) { return messages.Wiring{LocalID: id, Slot: 2, ColorCode: 1}, nil }
}
func msgReq(method, path, body string) *http.Request {
	return httptest.NewRequest(method, path, strings.NewReader(body))
}
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("response is not JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return v
}

// msgServer is a server with a live message store and service, but no sender
// goroutine: Send records and queues, which is all the HTTP surface promises.
func msgServer(t *testing.T, localID uint32) *server {
	t.Helper()
	ev, err := events.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ev.Close() })

	s := &server{hub: hub.New(), evStore: ev}
	s.msgs = messages.New(ev, s.hub, newNullRelay(), wiringFor(localID))
	return s
}

func TestMessageSendAccepts(t *testing.T) {
	s := msgServer(t, 3180202)

	rec := httptest.NewRecorder()
	s.messagesCollection(rec, msgReq("POST", "/api/messages", `{"dmr_id":3180299,"text":"hello"}`))

	// 202, not 200: it is queued, not transmitted. Transmitting takes air time and
	// may wait for the channel, and an HTTP client must not be told otherwise.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var m events.Message
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not a message: %v", err)
	}
	if m.ID == 0 || m.State != events.StateQueued || m.Peer != 3180299 || m.Text != "hello" {
		t.Errorf("= %+v", m)
	}
}

func TestMessageSendRejections(t *testing.T) {
	for _, tc := range []struct {
		name    string
		localID uint32
		body    string
		want    int
		says    string
	}{
		{"no destination", 3180202, `{"dmr_id":0,"text":"hi"}`, http.StatusBadRequest, "DMR ID"},
		{"a destination wider than 24 bits", 3180202, `{"dmr_id":16777216,"text":"hi"}`, http.StatusBadRequest, "DMR ID"},
		{"text past the on-air limit", 3180202,
			fmt.Sprintf(`{"dmr_id":3180299,"text":%q}`, strings.Repeat("A", dmrdata.MaxTextUnits+1)),
			http.StatusRequestEntityTooLarge, "too long"},
		{"a field nobody defined", 3180202, `{"dmr_id":1,"text":"hi","urgent":true}`, http.StatusBadRequest, "urgent"},
		{"not JSON at all", 3180202, `not json`, http.StatusBadRequest, ""},
		// The node cannot do this yet and the request was fine, so 409 rather than
		// a 400 that would send an operator looking at their own payload.
		{"no DMR ID on the node", 0, `{"dmr_id":3180299,"text":"hi"}`, http.StatusConflict, "DMR ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := msgServer(t, tc.localID)
			rec := httptest.NewRecorder()
			s.messagesCollection(rec, msgReq("POST", "/api/messages", tc.body))
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.says != "" && !strings.Contains(rec.Body.String(), tc.says) {
				t.Errorf("body = %s, want it to mention %q", rec.Body.String(), tc.says)
			}
		})
	}
}

// An over-length message must say what the limit is. "Too long" alone leaves the
// operator to bisect their own text.
func TestOverLengthSaysTheLimit(t *testing.T) {
	s := msgServer(t, 3180202)
	rec := httptest.NewRecorder()
	body := fmt.Sprintf(`{"dmr_id":3180299,"text":%q}`, strings.Repeat("A", dmrdata.MaxTextUnits+1))
	s.messagesCollection(rec, msgReq("POST", "/api/messages", body))

	got := decode(t, rec)
	if n, ok := got["max_units"].(float64); !ok || int(n) != dmrdata.MaxTextUnits {
		t.Errorf("max_units = %v, want %d", got["max_units"], dmrdata.MaxTextUnits)
	}
	if hint, _ := got["hint"].(string); !strings.Contains(hint, "UTF-16") {
		t.Errorf("hint = %q, want it to explain the unit", hint)
	}
	// And nothing was recorded: the operator was told no.
	if n, _ := s.evStore.CountMessages(); n != 0 {
		t.Errorf("%d rejected messages were stored", n)
	}
}

func TestMessageListAndFilters(t *testing.T) {
	s := msgServer(t, 3180202)
	out, _ := s.evStore.Enqueue(events.Message{Peer: 100, Local: 1, Text: "outbound"})
	in, _ := s.evStore.RecordInbound(events.Message{Peer: 200, Local: 1, Text: "inbound"})

	list := func(t *testing.T, query string) []events.Message {
		t.Helper()
		rec := httptest.NewRecorder()
		s.messagesCollection(rec, msgReq("GET", "/api/messages"+query, ""))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Messages []events.Message `json:"messages"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		return body.Messages
	}

	if got := list(t, ""); len(got) != 2 || got[0].ID != in.ID {
		t.Errorf("unfiltered = %d messages, newest first?", len(got))
	}
	if got := list(t, "?direction=out"); len(got) != 1 || got[0].ID != out.ID {
		t.Errorf("direction filter returned %d", len(got))
	}
	if got := list(t, "?peer=200"); len(got) != 1 || got[0].ID != in.ID {
		t.Errorf("peer filter returned %d", len(got))
	}
	if got := list(t, "?state=queued"); len(got) != 1 || got[0].ID != out.ID {
		t.Errorf("state filter returned %d", len(got))
	}

	// An empty result is [], never null: a client rendering a list should not have
	// to special-case the first node that has sent nothing.
	rec := httptest.NewRecorder()
	s.messagesCollection(rec, msgReq("GET", "/api/messages?peer=999", ""))
	if !strings.Contains(rec.Body.String(), `"messages":[]`) {
		t.Errorf("empty list = %s, want []", rec.Body.String())
	}
}

func TestMessageListRejections(t *testing.T) {
	s := msgServer(t, 3180202)
	for _, q := range []string{"?direction=sideways", "?peer=notanumber", "?peer=16777216", "?limit=lots"} {
		rec := httptest.NewRecorder()
		s.messagesCollection(rec, msgReq("GET", "/api/messages"+q, ""))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, rec.Code)
		}
	}
}

func TestMessageItem(t *testing.T) {
	s := msgServer(t, 3180202)
	m, _ := s.evStore.Enqueue(events.Message{Peer: 100, Local: 1, Text: "one message"})

	rec := httptest.NewRecorder()
	s.messageItem(rec, msgReq("GET", fmt.Sprintf("/api/messages/%d", m.ID), ""))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got events.Message
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != m.ID || got.Text != "one message" {
		t.Errorf("= %+v", got)
	}

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/messages/999999", http.StatusNotFound},
		{"/api/messages/abc", http.StatusBadRequest},
		{"/api/messages/0", http.StatusBadRequest},
		{"/api/messages/-1", http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		s.messageItem(rec, msgReq("GET", tc.path, ""))
		if rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d", tc.path, rec.Code, tc.want)
		}
	}
}

func TestMessageMethodsRefused(t *testing.T) {
	s := msgServer(t, 3180202)
	for _, tc := range []struct {
		method, path string
		handler      func(http.ResponseWriter, *http.Request)
	}{
		{"DELETE", "/api/messages", s.messagesCollection},
		{"PUT", "/api/messages", s.messagesCollection},
		{"DELETE", "/api/messages/1", s.messageItem},
		{"POST", "/api/messages/1", s.messageItem},
	} {
		rec := httptest.NewRecorder()
		tc.handler(rec, msgReq(tc.method, tc.path, ""))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: status %d, want 405", tc.method, tc.path, rec.Code)
		}
	}
}

// A node with no store answers 503 rather than panicking or pretending. That is
// every test server in this package that does not opt in, so it is worth pinning.
func TestMessagesWithoutAStore(t *testing.T) {
	s := &server{hub: hub.New()}
	for _, tc := range []struct {
		method, path string
		handler      func(http.ResponseWriter, *http.Request)
	}{
		{"POST", "/api/messages", s.messagesCollection},
		{"GET", "/api/messages", s.messagesCollection},
		{"GET", "/api/messages/1", s.messageItem},
	} {
		rec := httptest.NewRecorder()
		tc.handler(rec, msgReq(tc.method, tc.path, `{"dmr_id":1,"text":"x"}`))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status %d, want 503", tc.method, tc.path, rec.Code)
		}
	}
}

// The message routes must be registered, and behind the gate. The gate is
// default-deny, so being in the route table IS the authentication — this pins that
// the routes exist at the paths the docs promise.
func TestMessageRoutesAreRegistered(t *testing.T) {
	s := msgServer(t, 3180202)
	mux := http.NewServeMux()
	s.messagesRoutes(mux)
	for _, path := range []string{"/api/messages", "/api/messages/1"} {
		if _, pattern := mux.Handler(httptest.NewRequest("GET", path, nil)); pattern == "" {
			t.Errorf("%s is not routed", path)
		}
	}
}

// messageWiring reads the node's DMR settings, with [DMR] Id winning over
// [General] Id — the same precedence the renderer uses, because a message that
// went out under a different id than the node's traffic would be baffling.
func TestMessageWiring(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &server{store: st}

	if err := st.Set("general", config.General{ID: "3180202", Duplex: true}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("dmr", config.DMR{ID: "3180299", ColorCode: "7"}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("dmrnet", config.DMRNet{Slot2: true}, "test"); err != nil {
		t.Fatal(err)
	}

	w, err := s.messageWiring()
	if err != nil {
		t.Fatal(err)
	}
	if w.LocalID != 3180299 {
		t.Errorf("LocalID = %d, want the [DMR] id 3180299", w.LocalID)
	}
	if w.ColorCode != 7 || !w.Duplex || w.Slot != 2 {
		t.Errorf("= %+v", w)
	}
}

func TestMessageSlotChoice(t *testing.T) {
	for _, tc := range []struct {
		name         string
		slot1, slot2 bool
		want         int
	}{
		{"both on: a hotspot carries traffic on 2", true, true, 2},
		{"only slot 2", false, true, 2},
		{"only slot 1", true, false, 1},
		// Neither is a broken configuration the readiness guard already reports;
		// there is no right answer, so pick the one a hotspot normally uses.
		{"neither", false, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &config.Model{}
			m.DMRNet.Slot1, m.DMRNet.Slot2 = tc.slot1, tc.slot2
			if got := messageSlot(m); got != tc.want {
				t.Errorf("= %d, want %d", got, tc.want)
			}
		})
	}
}
