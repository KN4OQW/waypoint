package publicview

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/status"
)

// The public surface over the resolver chain (D3).
//
// Two claims are being made here and they need different kinds of test. That the
// public JSON does not CONTAIN a name is a test over bytes. That it CANNOT is a
// test over types — and it is the second one that keeps holding after somebody
// edits a handler, which is why the field audit exists at all.

// fullResolver is a resolver that knows a name and an email for a station. It is
// deliberately generous: every test below asks it for a station it knows, so a
// leak has something to leak. A resolver that never resolved anything would make
// these tests pass for the wrong reason.
//
// Note what it cannot do. publicview.CallsignResolver has exactly one method,
// returning exactly one string, so this fake could not hand the package a name
// even if it wanted to — which is the point being asserted by TestCallsignResolverCannotCarryAName
// below. The name and email live here only so the byte-level tests can grep for
// them.
type fullResolver struct {
	byID map[string]string // source (as it arrives) -> callsign
}

const (
	leakName  = "Clint Chance"
	leakEmail = "clint@example.invalid"
)

func (f fullResolver) CallsignForSource(source string) string {
	if c, ok := f.byID[source]; ok {
		return c
	}
	return ""
}

// ---------------------------------------------------------------------------
// The type-level claim (D3)
// ---------------------------------------------------------------------------

// TestCallsignResolverCannotCarryAName is the structural half of D3, and it is
// the assertion that actually holds the line.
//
// This package's whole view of the resolver chain is CallsignResolver. If that
// interface ever grows a method returning a Display, a struct, or a second string,
// then a full name becomes reachable from here and the enforcement stops being
// structural and starts being a matter of handler discipline — which is what D3
// says it must not be.
func TestCallsignResolverCannotCarryAName(t *testing.T) {
	typ := reflect.TypeOf((*CallsignResolver)(nil)).Elem()
	if n := typ.NumMethod(); n != 1 {
		t.Fatalf("CallsignResolver has %d methods, want exactly 1 — the public surface's "+
			"view of the chain is narrow ON PURPOSE (D3)", n)
	}
	m := typ.Method(0)
	if m.Name != "CallsignForSource" {
		t.Errorf("the one method is %q, want CallsignForSource", m.Name)
	}
	ft := m.Type
	if ft.NumOut() != 1 || ft.Out(0).Kind() != reflect.String {
		t.Errorf("%s returns %v; it must return exactly one string, so a full name is not "+
			"something this package forgets to drop but something it cannot obtain", m.Name, ft)
	}
	if ft.NumIn() != 1 || ft.In(0).Kind() != reflect.String {
		t.Errorf("%s takes %v, want one string", m.Name, ft)
	}
}

// TestPublicTypesHaveNoNameField restates it over the response types, so the two
// halves of the enforcement — what can be obtained and what can be carried — both
// fail loudly rather than one silently covering for the other.
func TestPublicTypesHaveNoNameField(t *testing.T) {
	banned := map[string]bool{
		"FullName": true, "Name": true, "Email": true,
		"Operator": true, "Person": true, "Contact": true, "Display": true,
	}
	for _, v := range []any{Status{}, Heard{}, Counters{}, LastHeardResult{}, CountersResult{}} {
		typ := reflect.TypeOf(v)
		for i := range typ.NumField() {
			if f := typ.Field(i); banned[f.Name] {
				t.Errorf("%s.%s: the phonebook's identity fields never reach the public "+
					"surface (D3)", typ.Name(), f.Name)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The byte-level claim
// ---------------------------------------------------------------------------

// TestPublicJSONNeverCarriesPhonebookIdentity serializes what an anonymous
// visitor actually receives, with a resolver that knows a name and an email, and
// greps the bytes. Redundant with the type audit by design: the audit proves the
// shape, this proves the shape is what gets written.
func TestPublicJSONNeverCarriesPhonebookIdentity(t *testing.T) {
	svc, _ := resolverService(t, fullResolver{byID: map[string]string{"3999999": "ZZ9ABC"}})

	heard, err := svc.LastHeard(0)
	if err != nil {
		t.Fatal(err)
	}
	counters, err := svc.Counters()
	if err != nil {
		t.Fatal(err)
	}
	st, err := svc.Status()
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{"LastHeard": heard, "Counters": counters, "Status": st} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		for _, secret := range []string{leakName, leakEmail, "full_name", "source_name", "email"} {
			if strings.Contains(strings.ToLower(body), strings.ToLower(secret)) {
				t.Errorf("public %s JSON contains %q: %s", name, secret, body)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// What the chain is actually FOR on this surface
// ---------------------------------------------------------------------------

// TestResolverPublishesAPhonebookStationThatWasDropped is the behaviour change
// the chain buys the public page, stated plainly.
//
// A station MMDVM-Host could not resolve arrives as a bare decimal ID, and
// publishableCallsign drops it — correctly, because a bare DMR ID is trivially
// resolvable to a name and an address through the public databases. If the
// operator has entered that person in their own phonebook, the chain turns the ID
// into a callsign BEFORE the filter sees a digit, and the station appears under
// the callsign it should always have had.
func TestResolverPublishesAPhonebookStationThatWasDropped(t *testing.T) {
	without, _ := resolverService(t, nil)
	res, err := without.LastHeard(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Callsign != "W1AW" {
		t.Fatalf("without a resolver the numeric station must stay dropped; got %+v", res.Entries)
	}

	with, _ := resolverService(t, fullResolver{byID: map[string]string{"3999999": "ZZ9ABC"}})
	res, err = with.LastHeard(0)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, e := range res.Entries {
		calls = append(calls, e.Callsign)
	}
	if len(calls) != 2 || calls[0] != "ZZ9ABC" || calls[1] != "W1AW" {
		t.Errorf("with a resolver, got %v; want the phonebook station published as ZZ9ABC "+
			"alongside W1AW", calls)
	}
	// And the counters agree with the list — they run through the same seam, so a
	// station counted but not listed (or the reverse) would be a real inconsistency
	// on the page.
	cs, err := with.Counters()
	if err != nil {
		t.Fatal(err)
	}
	if cs.Counters.Callsigns != 2 || cs.Counters.Transmissions != 2 {
		t.Errorf("counters = %+v, want 2 callsigns / 2 transmissions", cs.Counters)
	}
}

// TestUnresolvableIDStaysDropped: the chain adds a leg, it does not weaken the
// filter. An ID no phonebook knows is refused exactly as before, for exactly the
// reason publishableCallsign gives.
func TestUnresolvableIDStaysDropped(t *testing.T) {
	// A resolver that knows nothing about this station returns "", and
	// resolveSource hands the original bytes to the filter.
	svc, _ := resolverService(t, fullResolver{byID: map[string]string{"1111111": "OTHER"}})
	res, err := svc.LastHeard(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Entries {
		if e.Callsign == "3999999" || e.Callsign == "" {
			t.Errorf("an unresolvable ID reached the public list as %q", e.Callsign)
		}
	}
	if len(res.Entries) != 1 {
		t.Errorf("got %d entries, want only the callsign-shaped one", len(res.Entries))
	}
}

// TestEmptyPhonebookIsAByteForByteNoOp is D5 on this surface: a node whose
// phonebook nobody has filled in serves exactly the bytes it served before the
// chain existed. Asserted as a comparison of the marshalled responses rather than
// field by field, so a difference anywhere in the shape fails.
func TestEmptyPhonebookIsAByteForByteNoOp(t *testing.T) {
	// An empty phonebook resolves nothing, which is what a resolver with no
	// entries does. The nil case is the node that wired no chain at all.
	none, _ := resolverService(t, nil)
	empty, _ := resolverService(t, fullResolver{byID: map[string]string{}})

	for _, tc := range []struct {
		name string
		get  func(*Service) (any, error)
	}{
		{"LastHeard", func(s *Service) (any, error) { return s.LastHeard(0) }},
		{"Counters", func(s *Service) (any, error) { return s.Counters() }},
		{"Status", func(s *Service) (any, error) { return s.Status() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := tc.get(none)
			if err != nil {
				t.Fatal(err)
			}
			b, err := tc.get(empty)
			if err != nil {
				t.Fatal(err)
			}
			ja, _ := json.Marshal(a)
			jb, _ := json.Marshal(b)
			if string(ja) != string(jb) {
				t.Errorf("an empty phonebook changed the public %s response (D5):\n without: %s\n with:    %s",
					tc.name, ja, jb)
			}
		})
	}
}

// TestSuppressListStillBitesAfterResolution: D8 is applied to what the chain
// RESOLVED, not to what arrived. An operator who asked to be left off the page
// must not reappear because the node's owner also entered them in the phonebook —
// that would make the suppress list depend on which leg answered.
func TestSuppressListStillBitesAfterResolution(t *testing.T) {
	svc, store := resolverService(t, fullResolver{byID: map[string]string{"3999999": "ZZ9ABC"}})
	if _, err := store.AddSuppressed("ZZ9ABC"); err != nil {
		t.Fatal(err)
	}
	res, err := svc.LastHeard(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Entries {
		if e.Callsign == "ZZ9ABC" {
			t.Error("a suppressed callsign reappeared because the phonebook resolved it (D8)")
		}
	}
	if len(res.Entries) != 1 {
		t.Errorf("got %d entries, want only W1AW", len(res.Entries))
	}
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// resolverService builds a service over two transmissions: one that arrived
// already resolved (a callsign) and one that did not (a bare decimal ID). That
// pairing is the whole point — the second is what the chain can rescue and the
// first is what must not change.
func resolverService(t *testing.T, r CallsignResolver) (*Service, *Store) {
	t.Helper()
	st := newTestStore(t)
	set := DefaultSettings()
	set.Enabled = true
	if err := st.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	// fakeHistory (service_test.go) rather than a second stub: it sorts
	// newest-first the way the real store does, which the ordering assertions here
	// depend on.
	h := &fakeHistory{events: []hub.Event{
		{Time: fixedNow.Add(-2 * time.Minute), Type: status.TypeRFEnd, Mode: "DMR", Source: "3999999"},
		{Time: fixedNow.Add(-5 * time.Minute), Type: status.TypeRFEnd, Mode: "YSF", Source: "W1AW"},
	}}
	svc := NewService(st, h, nil).WithResolver(r)
	svc.now = func() time.Time { return fixedNow }
	return svc, st
}
