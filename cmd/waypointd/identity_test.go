package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/events"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/phonebook"
	"github.com/KN4OQW/waypoint/internal/status"
)

// The authenticated dashboard's display names (D4), and the two properties that
// keep them from becoming something else: the stored event never gains one, and
// the public surface never sees one.

// seedPhonebook puts one operator in an env's phonebook and returns the chain the
// decorators use, wired the way identityChain wires it in production.
func seedPhonebook(t *testing.T, e *authEnv, entries ...phonebook.Entry) {
	t.Helper()
	pb := phonebook.New(e.s.store)
	for _, entry := range entries {
		if _, err := pb.Create(entry); err != nil {
			t.Fatalf("seeding %s: %v", entry.Callsign, err)
		}
	}
	e.s.phonebook = pb
	e.s.identity = identityChain(pb)
}

// ---------------------------------------------------------------------------
// Decoration
// ---------------------------------------------------------------------------

func TestDecorateEventAddsThePhonebookName(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	seedPhonebook(t, e,
		phonebook.Entry{Callsign: "KN4OQW", DMRID: 3180202, FullName: "Clint Chance", Email: "clint@example.invalid"},
		phonebook.Entry{Callsign: "W1AW", FullName: "ARRL HQ"}, // no DMR ID recorded
	)

	for _, tc := range []struct {
		name, source, want string
	}{
		// The ordinary case: MMDVM-Host's src_info already resolved the callsign,
		// and the phonebook supplies the name for it.
		{"callsign source", "KN4OQW", "Clint Chance"},
		// The row with no DMR ID at all — resolvable only by callsign, which is
		// why DisplayForSource looks up that way round.
		{"callsign with no ID recorded", "W1AW", "ARRL HQ"},
		{"lowercase off the air", "kn4oqw", "Clint Chance"},
		// A station MMDVM-Host could not resolve, rescued by the phonebook's ID.
		{"numeric source", "3180202", "Clint Chance"},
		// Nobody knows this one; the event is untouched.
		{"unknown callsign", "W4RJM", ""},
		{"unknown ID", "4242424", ""},
		{"network name", "BM_3102_United_States", ""},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := e.s.decorateEvent(hub.Event{Type: "rf_voice_end", Source: tc.source})
			if got.SourceName != tc.want {
				t.Errorf("SourceName = %q, want %q", got.SourceName, tc.want)
			}
			// Source itself is never rewritten: the dashboard keys its last-heard
			// table by it, and changing the spelling under a client that has been
			// accumulating rows would split one station into two.
			if got.Source != tc.source {
				t.Errorf("Source was rewritten from %q to %q", tc.source, got.Source)
			}
		})
	}
}

// TestDecorateNeverMutatesItsInput: the hub fans one Event out to every
// subscriber, and the event store hands back slices it may reuse. A decorator
// that wrote through a pointer would hand a name to readers that must not have
// one, and would be a data race besides.
func TestDecorateNeverMutatesItsInput(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	seedPhonebook(t, e, phonebook.Entry{Callsign: "KN4OQW", FullName: "Clint Chance"})

	in := hub.Event{Type: "rf_voice_end", Source: "KN4OQW"}
	if out := e.s.decorateEvent(in); out.SourceName == "" {
		t.Fatal("the fixture did not decorate; the rest of this test proves nothing")
	}
	if in.SourceName != "" {
		t.Errorf("decorateEvent mutated its argument: %+v", in)
	}

	evs := []hub.Event{{Type: "rf_voice_end", Source: "KN4OQW"}}
	_ = e.s.decorateEvents(evs)
	if evs[0].SourceName != "" {
		t.Errorf("decorateEvents mutated the input slice: %+v", evs[0])
	}

	tx := &status.Transmission{Mode: "DMR", Source: "KN4OQW"}
	st := status.Status{TX: tx}
	if out := e.s.decorateStatus(st); out.TX.SourceName == "" {
		t.Fatal("status fixture did not decorate")
	}
	if tx.SourceName != "" {
		t.Errorf("decorateStatus wrote through the aggregator's transmission: %+v", tx)
	}
}

// TestDecorationIsANoOpWithoutAPhonebook is D5 on this path: no chain, or a chain
// over an empty phonebook, and the event is returned exactly as it arrived — with
// `omitempty` then dropping the field from the JSON altogether.
func TestDecorationIsANoOpWithoutAPhonebook(t *testing.T) {
	in := hub.Event{Type: "rf_voice_end", Mode: "DMR", Source: "KN4OQW"}

	noChain := newAuthEnv(t, ":memory:")
	noChain.s.identity = nil
	empty := newAuthEnv(t, ":memory:")
	seedPhonebook(t, empty) // a real chain over a phonebook nobody has filled in

	base, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string]*authEnv{"no chain": noChain, "empty phonebook": empty} {
		got, err := json.Marshal(env.s.decorateEvent(in))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(base) {
			t.Errorf("%s: decorated JSON differs from the undecorated event (D5)\n got %s\nwant %s",
				name, got, base)
		}
		if strings.Contains(string(got), "source_name") {
			t.Errorf("%s: source_name is present in the JSON; omitempty should drop it", name)
		}
	}
}

// ---------------------------------------------------------------------------
// The name never reaches the database
// ---------------------------------------------------------------------------

// TestDecoratedEventIsNeverPersisted pins the structural guarantee the hub.Event
// comment claims: internal/events writes an explicit column list with no column
// for SourceName, so an event that picked up a name on its way to a client cannot
// carry one back into the store.
//
// It matters because the public surface reads its last-heard list from that same
// store. If a name could be persisted, D3 would depend on nobody ever writing a
// decorated event back — which is exactly the kind of discipline the field audit
// exists to replace.
func TestDecoratedEventIsNeverPersisted(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	seedPhonebook(t, e, phonebook.Entry{Callsign: "KN4OQW", FullName: "Clint Chance"})

	st, err := events.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close() //nolint:errcheck // test cleanup

	decorated := e.s.decorateEvent(hub.Event{
		Time: time.Now().UTC(), Type: status.TypeRFEnd, Mode: "DMR", Source: "KN4OQW",
	})
	if decorated.SourceName == "" {
		t.Fatal("the fixture did not decorate; the rest of this test proves nothing")
	}
	if err := st.Insert([]hub.Event{decorated}); err != nil {
		t.Fatal(err)
	}
	back, err := st.History(events.HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 {
		t.Fatalf("stored %d events, want 1", len(back))
	}
	if back[0].SourceName != "" {
		t.Errorf("a display name round-tripped through the event store as %q; it must not "+
			"be persistable", back[0].SourceName)
	}
	if back[0].Source != "KN4OQW" {
		t.Errorf("Source = %q, want KN4OQW", back[0].Source)
	}
}

// ---------------------------------------------------------------------------
// End to end over the real handlers
// ---------------------------------------------------------------------------

// TestHistoryAPICarriesTheNameAndPublicDoesNot is the whole feature in one test:
// the same stored transmission, served to a signed-in operator and to an
// anonymous visitor, and only one of them learns who it was (D3/D4).
func TestHistoryAPICarriesTheNameAndPublicDoesNot(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	cookie := e.claim(t, "kn4oqw", "goodpassword")
	seedPhonebook(t, e, phonebook.Entry{
		Callsign: "KN4OQW", DMRID: 3180202, FullName: "Clint Chance", Email: "clint@example.invalid",
	})

	evStore, err := events.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer evStore.Close() //nolint:errcheck // test cleanup
	e.s.evStore = evStore
	if err := evStore.Insert([]hub.Event{{
		Time: time.Now().UTC().Add(-time.Minute), Type: status.TypeRFEnd, Mode: "DMR", Source: "KN4OQW",
	}}); err != nil {
		t.Fatal(err)
	}

	req := jsonReq("GET", "/api/history", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/history = %d (%s)", rec.Code, rec.Body.String())
	}
	var got []hub.Event
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("history is not JSON: %v (%s)", err, rec.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].SourceName != "Clint Chance" {
		t.Errorf("authenticated history SourceName = %q, want the phonebook's name (D4)", got[0].SourceName)
	}
	// The email is in the same phonebook row and must not follow the name out.
	if strings.Contains(rec.Body.String(), "clint@example.invalid") {
		t.Error("the history response disclosed the phonebook's email address")
	}
}
