package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/phonebook"
	"github.com/KN4OQW/waypoint/internal/store"
)

// The phonebook is not part of the config renderer spine (D3), and this is the
// test that says so rather than the comment that claims it.
//
// The phonebook shares the config database — it has to, so its writes serialize
// with config writes on the store's single connection — and that shared handle is
// the whole hazard. Nothing stops a future change from reading a phonebook row in
// a renderer, projecting one into the config View, or teaching Load about a
// "phonebook" settings key. Each would be a quiet failure: a callsign or a name
// appearing in a generated INI is a behaviour change nobody asked for, and an
// email address reaching one is a PII disclosure into a world-readable file
// (D4) that no test above this layer would notice.
//
// So the assertions are made against a populated phonebook, with values chosen to
// be unmistakable if they ever surface.

// pbSentinels are the field values written into the phonebook below. Each is a
// string no renderer has any legitimate reason to emit — including the callsign,
// which is deliberately NOT the station's own callsign so a hit cannot be
// confused with the identity MMDVM-Host is correctly told about.
var pbSentinels = []string{
	"ZZ9PBSENTINEL",             // callsign
	"3999999",                   // DMR ID
	"Phonebook Sentinel Person", // full name
	"pb-sentinel@example.invalid",
}

// pbStoreWithEntries opens a store at head, populates the phonebook, and returns
// the store. The entries go in through the real package, not raw SQL, so the
// normalization and the timestamps are the ones production writes.
func pbStoreWithEntries(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() }) //nolint:errcheck // test cleanup

	pb := phonebook.New(st)
	for _, e := range []phonebook.Entry{
		{Callsign: "ZZ9PBSENTINEL", DMRID: 3999999, FullName: "Phonebook Sentinel Person", Email: "pb-sentinel@example.invalid"},
		{Callsign: "ZZ9PBOTHER", DMRID: 3999998},
	} {
		if _, err := pb.Create(e); err != nil {
			t.Fatalf("seeding the phonebook: %v", err)
		}
	}
	return st
}

// pbModel is a model with every mode enabled and every startup requirement met,
// so RenderTargets withholds nothing and the sweep below actually covers each
// generated file rather than silently skipping the ones a gate turned off.
func pbModel() *Model {
	return &Model{
		General: General{Callsign: "KN4OQW", ID: "3180202", Duplex: false, Location: "Test", URL: "https://example.invalid"},
		Modem:   Modem{Port: "/dev/ttyAMA0", RXFreqHz: "438800000", TXFreqHz: "438800000"},
		Modes:   Modes{DStar: true, DMR: true, YSF: true, P25: true, NXDN: true, M17: true, POCSAG: true, FM: true},
		DMR:     DMR{ColorCode: "1", ID: "3180202"},
		DStar:   DStar{Module: "B"},
		M17:     M17{CAN: "1"},
		POCSAG:  POCSAG{Frequency: "439987500", Server: "dapnet.example.invalid", AuthKey: "sentinel-authkey"},
		Networks: []Network{{
			Name: "BrandMeister", Type: NetBrandmeister, Enabled: true,
			Address: "master.example.invalid", Port: "62031", Password: "passw0rd",
		}},
		Buses:       []Bus{{ID: "bus-a", Name: "Bus A", Enabled: true}},
		Attachments: []Attachment{{BusID: "bus-a", Mode: ModeYSF, Target: "US-Test"}},
	}
}

func pbPaths() Paths {
	return Paths{
		MMDVM:         "/etc/waypoint/mmdvm.ini",
		DMRGateway:    "/etc/waypoint/dmrgateway.ini",
		YSFGateway:    "/etc/waypoint/ysfgateway.ini",
		DGIdGateway:   "/etc/waypoint/dgidgateway.ini",
		P25Gateway:    "/etc/waypoint/p25gateway.ini",
		NXDNGateway:   "/etc/waypoint/nxdngateway.ini",
		DStarGateway:  "/etc/waypoint/dstargateway.cfg",
		M17Gateway:    "/etc/waypoint/m17gateway.ini",
		DAPNETGateway: "/etc/waypoint/dapnetgateway.ini",
		BusConfigDir:  "/etc/waypoint/buses",
		PeeringDir:    "/etc/waypoint/peering",
		MQTTBroker:    "127.0.0.1:1883",
	}
}

// TestPhonebookRendersToNoINIKey is the regression test proper: with a populated
// phonebook in the same database, every generated file is rendered and searched
// for every field the phonebook holds.
//
// Both the DGId variant and the stock YSF gateway are covered, because
// RenderTargets swaps one for the other and a sweep of targets alone would leave
// whichever is not selected unrendered.
func TestPhonebookRendersToNoINIKey(t *testing.T) {
	st := pbStoreWithEntries(t)
	m := pbModel()
	if err := m.SaveAtomic(st, "test"); err != nil {
		t.Fatal(err)
	}
	// Reload through the same path waypointd uses, so the model under test is one
	// that has actually been round-tripped through the shared database.
	loaded, err := Load(st)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	rendered := map[string]string{}
	for _, tg := range loaded.RenderTargets(pbPaths()) {
		rendered[tg.Path] = tg.Render(loaded)
	}
	// The renderers the target set can withhold, called directly so none is missed.
	for name, out := range map[string]string{
		"RenderMMDVM":         loaded.RenderMMDVM(),
		"RenderDMRGateway":    loaded.RenderDMRGateway(),
		"RenderYSFGateway":    loaded.RenderYSFGateway(),
		"RenderDGIdGateway":   loaded.RenderDGIdGateway(),
		"RenderP25Gateway":    loaded.RenderP25Gateway(),
		"RenderNXDNGateway":   loaded.RenderNXDNGateway(),
		"RenderDStarGateway":  loaded.RenderDStarGateway(),
		"RenderM17Gateway":    loaded.RenderM17Gateway(),
		"RenderDAPNETGateway": loaded.RenderDAPNETGateway(),
	} {
		rendered[name] = out
	}

	if len(rendered) < 10 {
		t.Fatalf("only %d outputs rendered; the sweep is not covering the generated set", len(rendered))
	}
	for where, out := range rendered {
		if strings.TrimSpace(out) == "" {
			t.Errorf("%s rendered nothing — the sweep proves nothing about an empty file", where)
			continue
		}
		for _, s := range pbSentinels {
			if strings.Contains(out, s) {
				t.Errorf("%s contains the phonebook value %q — phonebook rows render to no INI key (D3), "+
					"and an email address in a generated file is a PII disclosure (D4)", where, s)
			}
		}
	}
}

// TestPhonebookIsNotAConfigSection guards the way the leak would most plausibly
// arrive: someone adds "phonebook" to Model.sections() so the panel can reuse the
// config plumbing. That would put phonebook rows in the settings key tree, in the
// config View, and in an exported profile all at once.
func TestPhonebookIsNotAConfigSection(t *testing.T) {
	m := &Model{}
	for _, name := range []string{"phonebook", "phonebook_entries", "contacts"} {
		if _, ok := m.sections()[name]; ok {
			t.Errorf("Model.sections() has a %q section — the phonebook is not config (D3); "+
				"it is its own table with its own API", name)
		}
	}
}

// TestPhonebookNotInConfigView is the D4 half: the config view is the largest
// authenticated projection this daemon serves and the one most likely to be
// cached, proxied, or pasted into an issue. Populating the phonebook must not
// change a single byte of it.
func TestPhonebookNotInConfigView(t *testing.T) {
	st := pbStoreWithEntries(t)
	m := pbModel()
	if err := m.SaveAtomic(st, "test"); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(st)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(loaded.View(Sources{Store: ":memory:", Listen: "127.0.0.1:443"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range pbSentinels {
		if strings.Contains(string(raw), s) {
			t.Errorf("GET /api/config's view carries the phonebook value %q", s)
		}
	}
	if strings.Contains(strings.ToLower(string(raw)), "phonebook") {
		t.Error("the config view mentions the phonebook; it is a separate surface with a separate endpoint")
	}

	// And the settings key tree itself has no phonebook row: the entries went into
	// their own table, so a store→render→parse→store cycle has nothing of theirs
	// to lose.
	all, err := st.All()
	if err != nil {
		t.Fatal(err)
	}
	for k := range all {
		if strings.Contains(strings.ToLower(k), "phonebook") {
			t.Errorf("settings key %q exists; phonebook rows do not belong in the key tree", k)
		}
	}
}

// TestPhonebookDoesNotChangeRenderOutput is the same claim stated as a diff, and
// it is the stronger form: the sentinel sweep above can only catch a value it
// knows to look for, while this catches any change at all — a count, a generated
// comment, a routing line conditioned on whether the phonebook has rows.
//
// Renders are pure, so an unchanged model must render identically whatever else
// is in the database.
func TestPhonebookDoesNotChangeRenderOutput(t *testing.T) {
	render := func(st *store.Store) map[string]string {
		t.Helper()
		m := pbModel()
		if err := m.SaveAtomic(st, "test"); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(st)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, tg := range loaded.RenderTargets(pbPaths()) {
			out[tg.Path] = tg.Render(loaded)
		}
		return out
	}

	empty, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close() //nolint:errcheck // test cleanup

	before := render(empty)
	after := render(pbStoreWithEntries(t))

	if len(before) != len(after) {
		t.Fatalf("a populated phonebook changed the generated file SET: %d targets vs %d", len(before), len(after))
	}
	for path, want := range before {
		got, ok := after[path]
		if !ok {
			t.Errorf("%s is generated with an empty phonebook and not with a populated one", path)
			continue
		}
		if got != want {
			t.Errorf("%s differs depending on whether the phonebook has rows; renders read the model and nothing else", path)
		}
	}
}
