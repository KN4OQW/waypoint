package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The defaults are a claim about what a node does the moment the feature is
// switched on, so they are pinned rather than assumed.
func TestDefaultWXIsOffAndConservative(t *testing.T) {
	w := DefaultWX()
	if w.Enabled {
		t.Error("the weather feature defaults to ON; it transmits automatically and must be opted into")
	}
	if got := w.Classes["W"]; !got.SMS || !got.Voice {
		t.Errorf("warnings default to %+v, want both channels on", got)
	}
	if got := w.Classes["A"]; !got.SMS || got.Voice {
		t.Errorf("watches default to %+v, want text only", got)
	}
	// Advisories and statements are the bulk of the feed. Defaulting them on
	// would put a talkgroup under a stream of dense fog advisories.
	for _, sig := range []string{"Y", "S"} {
		if got := w.Classes[sig]; got.SMS || got.Voice {
			t.Errorf("class %s defaults to %+v, want nothing", sig, got)
		}
	}
	if !reflect.DeepEqual(w.AnnounceActions, []string{"NEW"}) {
		t.Errorf("announce actions default to %v, want [NEW]; CON would re-announce every few minutes",
			w.AnnounceActions)
	}
	if !reflect.DeepEqual(w.Talkgroups, []uint32{DefaultWXTalkgroup}) {
		t.Errorf("talkgroups default to %v, want [%d]", w.Talkgroups, DefaultWXTalkgroup)
	}
	if err := ValidateWX(w); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
}

// The trailing wildcard is the single easiest thing to get wrong about this
// feed, and getting it wrong hides every hazard but one per county.
func TestSubscriptionsCarryTheETNWildcard(t *testing.T) {
	w := DefaultWX()
	w.Counties = []WXCounty{{SAME: "012113"}, {SAME: "012033"}}
	got := w.WXSubscriptions()
	want := []string{
		"wxalerts/nws/v1/same/012113/#",
		"wxalerts/nws/v1/same/012033/#",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("subscriptions = %v, want %v", got, want)
	}
	for _, s := range got {
		if !strings.HasSuffix(s, "/#") {
			t.Errorf("%q does not end in /#; a county under two hazards would show only one", s)
		}
	}
}

func TestValidateWXRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*WX)
		want string
	}{
		{"same code too short", func(w *WX) { w.Counties = []WXCounty{{SAME: "12113"}} }, "six digits"},
		{"same code not numeric", func(w *WX) { w.Counties = []WXCounty{{SAME: "01211X"}} }, "six digits"},
		{"duplicate county", func(w *WX) {
			w.Counties = []WXCounty{{SAME: "012113"}, {SAME: "012113"}}
		}, "listed twice"},
		{"bad ugc", func(w *WX) { w.Counties = []WXCounty{{SAME: "012113", UGC: "FL113"}} }, "zone code"},
		{"no talkgroups", func(w *WX) { w.Talkgroups = nil }, "at least one talkgroup"},
		{"talkgroup zero", func(w *WX) { w.Talkgroups = []uint32{0} }, "24-bit"},
		{"talkgroup too large", func(w *WX) { w.Talkgroups = []uint32{0x1000000} }, "24-bit"},
		{"duplicate talkgroup", func(w *WX) { w.Talkgroups = []uint32{9, 9} }, "listed twice"},
		{"unknown class", func(w *WX) { w.Classes = map[string]WXRule{"Q": {SMS: true}} }, "not a hazard class"},
		{"empty override", func(w *WX) { w.Overrides = []WXOverride{{Event: "  "}} }, "no alert type"},
		{"duplicate override", func(w *WX) {
			w.Overrides = []WXOverride{{Event: "Tornado Warning"}, {Event: "tornado  warning"}}
		}, "two overrides"},
		{"no actions", func(w *WX) { w.AnnounceActions = nil }, "at least one alert action"},
		{"unknown action", func(w *WX) { w.AnnounceActions = []string{"SOON"} }, "not an alert action"},
		{"bad holdoff", func(w *WX) { w.Holdoff = "soon" }, "not a duration"},
		{"negative defer", func(w *WX) { w.MaxDefer = "-5s" }, "cannot be negative"},
		{"broker not a url", func(w *WX) { w.Broker = "::::" }, "not a URL"},
		{"broker wrong scheme", func(w *WX) { w.Broker = "mqtt://example.org" }, "ws:// or wss://"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := DefaultWX()
			tc.edit(&w)
			err := ValidateWX(w)
			if err == nil {
				t.Fatalf("accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q; the message has to say what to do", err, tc.want)
			}
		})
	}
}

// A rule table that refuses everything proves nothing. These are the shapes an
// operator will actually save.
func TestValidateWXAcceptsRealConfigurations(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*WX)
	}{
		{"the defaults", func(*WX) {}},
		{"multi-state counties", func(w *WX) {
			w.Counties = []WXCounty{
				{SAME: "012113", UGC: "FLC113", Name: "Santa Rosa", State: "FL", WFO: "KMOB"},
				{SAME: "001053", UGC: "ALC053", Name: "Escambia", State: "AL", WFO: "KMOB"},
			}
		}},
		{"several talkgroups", func(w *WX) { w.Talkgroups = []uint32{9, 31012} }},
		{"an override", func(w *WX) {
			w.Overrides = []WXOverride{{Event: "Tornado Watch", WXRule: WXRule{SMS: true, Voice: true}}}
		}},
		{"blank durations mean the default", func(w *WX) { w.Holdoff, w.MaxDefer = "", "" }},
		{"blank broker is allowed until enabled", func(w *WX) { w.Broker = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := DefaultWX()
			tc.edit(&w)
			if err := ValidateWX(w); err != nil {
				t.Errorf("refused a good configuration: %v", err)
			}
		})
	}
}

func TestRuleForOverrideBeatsClass(t *testing.T) {
	w := DefaultWX()
	// Statements are off by default; an override turns one specific one on.
	w.Overrides = []WXOverride{{Event: "Tornado Warning", WXRule: WXRule{SMS: false, Voice: false}}}

	if got := w.RuleFor("Severe Thunderstorm Warning", "W"); !got.SMS || !got.Voice {
		t.Errorf("an un-overridden warning = %+v, want the class row (both on)", got)
	}
	if got := w.RuleFor("Tornado Warning", "W"); got.SMS || got.Voice {
		t.Errorf("the override did not beat the class row: %+v", got)
	}
	// Case and spacing must not defeat an override; an operator types by hand.
	if got := w.RuleFor("  tornado   warning ", "W"); got.SMS || got.Voice {
		t.Errorf("override missed on differing case/spacing: %+v", got)
	}
}

// An unknown significance must not fall through to something that transmits.
func TestRuleForUnknownClassIsSilent(t *testing.T) {
	w := DefaultWX()
	for _, sig := range []string{"", "Q", "F", "O"} {
		if got := w.RuleFor("Some New Product", sig); got.SMS || got.Voice {
			t.Errorf("significance %q resolved to %+v, want nothing transmitted", sig, got)
		}
	}
}

func TestShouldAnnounce(t *testing.T) {
	w := DefaultWX()
	if !w.ShouldAnnounce("NEW") || !w.ShouldAnnounce("new") {
		t.Error("NEW should announce under the defaults, case-insensitively")
	}
	if w.ShouldAnnounce("CON") {
		t.Error("CON announces under the defaults; it would re-transmit the same hazard repeatedly")
	}
}

// The store round-trip: what goes in comes back, including the fields a partial
// PUT did not mention.
func TestSetWXMergesAndKeepsThePassword(t *testing.T) {
	s := newTestStore(t)
	if err := s.Set("wx", DefaultWX(), "seed"); err != nil {
		t.Fatal(err)
	}
	// A panel round-trip never returns the password, so a body without one must
	// not erase the stored value.
	if err := SetWX(s, []byte(`{"enabled":true,"counties":[{"same":"012113","ugc":"flc113"}]}`), "api"); err != nil {
		t.Fatalf("SetWX: %v", err)
	}
	var got WX
	if _, err := s.GetInto("wx", &got); err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Error("enabled did not persist")
	}
	if got.Password != DefaultWXPassword {
		t.Errorf("password = %q after a body that omitted it, want the stored one kept", got.Password)
	}
	if len(got.Counties) != 1 || got.Counties[0].SAME != "012113" {
		t.Fatalf("counties = %+v", got.Counties)
	}
	// Normalisation happens at save so nothing downstream has to upper-case
	// defensively.
	if got.Counties[0].UGC != "FLC113" {
		t.Errorf("UGC = %q, want it normalised to upper case", got.Counties[0].UGC)
	}
	if !reflect.DeepEqual(got.Talkgroups, []uint32{DefaultWXTalkgroup}) {
		t.Errorf("talkgroups = %v; an unmentioned field was not preserved", got.Talkgroups)
	}
}

func TestSetWXRefusesBadBodyAndLeavesStoreAlone(t *testing.T) {
	s := newTestStore(t)
	if err := s.Set("wx", DefaultWX(), "seed"); err != nil {
		t.Fatal(err)
	}
	if err := SetWX(s, []byte(`{"talkgroups":[0]}`), "api"); err == nil {
		t.Fatal("accepted talkgroup 0")
	}
	var got WX
	if _, err := s.GetInto("wx", &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Talkgroups, []uint32{DefaultWXTalkgroup}) {
		t.Errorf("a refused save changed the store: talkgroups = %v", got.Talkgroups)
	}
	// Unknown fields are refused rather than silently dropped, so a panel that
	// sends a field this build does not know about hears about it.
	if err := SetWX(s, []byte(`{"nonsense":1}`), "api"); err == nil {
		t.Error("accepted an unknown field")
	}
}

// The View is the projection an operator's browser receives. It must carry
// whether a password is set and never the password.
func TestViewWXWithholdsThePassword(t *testing.T) {
	m := &Model{WX: DefaultWX()}
	m.WX.Counties = []WXCounty{{SAME: "012113"}}
	// A distinctive value on purpose. The DEFAULT password is "wxalerts", which
	// is also the default username and a substring of the broker URL, so a
	// blob-contains check against it can never fail and would pass on a view
	// that leaked every secret it had.
	const secret = "not-the-username-6f2a"
	m.WX.Password = secret
	v := m.View(Sources{})

	if !v.WX.HasPassword {
		t.Error("has_password is false with a password stored")
	}
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), secret) {
		t.Error("the serialized view contains the feed password")
	}
	// The panel gets the same subscription strings the ingest will use, so the
	// trailing wildcard cannot drift between what is shown and what is done.
	if !reflect.DeepEqual(v.WX.Subscriptions, []string{"wxalerts/nws/v1/same/012113/#"}) {
		t.Errorf("view subscriptions = %v", v.WX.Subscriptions)
	}
}

func TestViewWXClassesAreCopiedNotShared(t *testing.T) {
	m := &Model{WX: DefaultWX()}
	v := m.View(Sources{})
	v.WX.Classes["W"] = WXRule{}
	if got := m.WX.Classes["W"]; !got.SMS {
		t.Error("mutating the view's class map reached the model; the projection must copy")
	}
}
