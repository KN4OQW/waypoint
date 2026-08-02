package publicview

import (
	"errors"
	"reflect"
	"testing"
)

// TestCallsignNormalization is D8's matching rule. The cases that matter are the
// ones where the same operator shows up under two spellings — a suppress list that
// misses the SSID variant has not suppressed anyone.
func TestCallsignNormalization(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"n4abc-7", "N4ABC"},
		{"N4ABC", "N4ABC"},
		{"  n4abc  ", "N4ABC"},
		{"KK4WXT-9", "KK4WXT"},
		{"W1AW/4", "W1AW"},
		{"KP4/W1AW", "W1AW"},
		{"W1AW/P", "W1AW"},
		{"w1aw-1/m", "W1AW"},
		{"", ""},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := NormalizeCallsign(tc.in); got != tc.want {
				t.Errorf("NormalizeCallsign(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCallsignValidation(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"n4abc-7", "N4ABC", false},
		{"kn4oqw", "KN4OQW", false},
		{"", "", true},
		{"K4", "", true},             // too short to be a call
		{"SKYWARN", "", true},        // no digit: a word, not a callsign
		{"N4ABC!", "", true},         // punctuation
		{"N4 ABC", "", true},         // internal space
		{"N4ABCDEFGHIJKL", "", true}, // absurd length
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ValidateCallsign(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrBadCallsign) {
					t.Errorf("ValidateCallsign(%q) = %q, %v; want ErrBadCallsign", tc.in, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateCallsign(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ValidateCallsign(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSuppressListRoundTrip(t *testing.T) {
	ps := newTestStore(t)

	// Added with an SSID, stored as the base call.
	got, err := ps.AddSuppressed("n4abc-7")
	if err != nil {
		t.Fatal(err)
	}
	if got != "N4ABC" {
		t.Errorf("AddSuppressed stored %q, want %q", got, "N4ABC")
	}
	if _, err := ps.AddSuppressed("KK4WXT"); err != nil {
		t.Fatal(err)
	}

	list, err := ps.Suppressed()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"KK4WXT", "N4ABC"}; !reflect.DeepEqual(list, want) {
		t.Errorf("suppress list = %v, want %v", list, want)
	}

	// Every SSID and portable variant of a suppressed call has to resolve to the
	// same key, or the exclusion leaks the moment someone keys up mobile.
	set, err := ps.SuppressSet()
	if err != nil {
		t.Fatal(err)
	}
	for _, heard := range []string{"N4ABC", "n4abc", "N4ABC-1", "N4ABC-9", "N4ABC/M", "KP4/N4ABC"} {
		if !set[NormalizeCallsign(heard)] {
			t.Errorf("%q is not matched by the suppress list", heard)
		}
	}
	if set[NormalizeCallsign("N4ABD")] {
		t.Error("suppress list matched a different callsign")
	}
}

// TestSuppressAddIsIdempotent: the operator's intent is "this call must not
// appear", and adding it twice does not make that more true or an error.
func TestSuppressAddIsIdempotent(t *testing.T) {
	ps := newTestStore(t)
	for range 3 {
		if _, err := ps.AddSuppressed("N4ABC-7"); err != nil {
			t.Fatal(err)
		}
	}
	list, err := ps.Suppressed()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Errorf("suppress list = %v after three adds, want one entry", list)
	}
}

func TestSuppressRemove(t *testing.T) {
	ps := newTestStore(t)
	if _, err := ps.AddSuppressed("N4ABC"); err != nil {
		t.Fatal(err)
	}
	// Removable by any variant, the same way it was addable by any variant.
	if err := ps.RemoveSuppressed("n4abc-9"); err != nil {
		t.Fatal(err)
	}
	list, err := ps.Suppressed()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("suppress list = %v after removal, want empty", list)
	}
	if err := ps.RemoveSuppressed("N4ABC"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing a missing callsign: err = %v, want ErrNotFound", err)
	}
}

func TestSuppressRejectsJunk(t *testing.T) {
	ps := newTestStore(t)
	if _, err := ps.AddSuppressed("not a callsign"); !errors.Is(err, ErrBadCallsign) {
		t.Errorf("AddSuppressed with junk: err = %v, want ErrBadCallsign", err)
	}
}

func TestLinksCRUD(t *testing.T) {
	ps := newTestStore(t)

	id, err := ps.AddLink(Link{Label: "Club website", URL: "https://example.org", SortOrder: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddLink(Link{Label: "RepeaterBook", URL: "https://repeaterbook.example/k4src", SortOrder: 0}); err != nil {
		t.Fatal(err)
	}

	links, err := ps.Links()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("got %d links, want 2", len(links))
	}
	if links[0].Label != "RepeaterBook" {
		t.Errorf("links are not in sort_order: got %q first", links[0].Label)
	}

	if err := ps.UpdateLink(Link{ID: id, Label: "Club site", URL: "https://example.org/new", SortOrder: 2}); err != nil {
		t.Fatal(err)
	}
	links, err = ps.Links()
	if err != nil {
		t.Fatal(err)
	}
	if links[1].Label != "Club site" || links[1].URL != "https://example.org/new" {
		t.Errorf("update did not take: %+v", links[1])
	}

	if err := ps.DeleteLink(id); err != nil {
		t.Fatal(err)
	}
	if err := ps.DeleteLink(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
}

// TestLinkUpdateRevalidatesURL is the hole this closes: a link inserted as https
// and then edited to javascript: would otherwise reach the public page.
func TestLinkUpdateRevalidatesURL(t *testing.T) {
	ps := newTestStore(t)
	id, err := ps.AddLink(Link{Label: "Club website", URL: "https://example.org"})
	if err != nil {
		t.Fatal(err)
	}
	err = ps.UpdateLink(Link{ID: id, Label: "Club website", URL: "javascript:alert(1)"})
	if !errors.Is(err, ErrBadURL) {
		t.Fatalf("UpdateLink to a javascript: URL: err = %v, want ErrBadURL", err)
	}
	links, err := ps.Links()
	if err != nil {
		t.Fatal(err)
	}
	if links[0].URL != "https://example.org" {
		t.Errorf("a rejected update changed the stored URL to %q", links[0].URL)
	}
}

func TestLinkRequiresLabel(t *testing.T) {
	ps := newTestStore(t)
	if _, err := ps.AddLink(Link{Label: "   ", URL: "https://example.org"}); err == nil {
		t.Error("AddLink accepted a blank label")
	}
}

func TestNetsCRUD(t *testing.T) {
	ps := newTestStore(t)

	id, err := ps.AddNet(Net{
		Name:         "Weekly Club Net",
		ScheduleText: "Mondays 20:00 local",
		Target:       "TS2 / TG 31123",
		Note:         "Visitors check in first",
		SortOrder:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	nets, err := ps.Nets()
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 || nets[0].Name != "Weekly Club Net" {
		t.Fatalf("nets = %+v", nets)
	}

	if err := ps.UpdateNet(Net{ID: id, Name: "Club Net", ScheduleText: "Mondays 20:30 local", Target: "TG 31123"}); err != nil {
		t.Fatal(err)
	}
	nets, err = ps.Nets()
	if err != nil {
		t.Fatal(err)
	}
	if nets[0].ScheduleText != "Mondays 20:30 local" || nets[0].Note != "" {
		t.Errorf("update did not take: %+v", nets[0])
	}

	if err := ps.DeleteNet(id); err != nil {
		t.Fatal(err)
	}
	if err := ps.DeleteNet(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
}

func TestNetRequiresNameAndSchedule(t *testing.T) {
	ps := newTestStore(t)
	if _, err := ps.AddNet(Net{ScheduleText: "Mondays 20:00"}); err == nil {
		t.Error("AddNet accepted a net with no name")
	}
	if _, err := ps.AddNet(Net{Name: "Club Net"}); err == nil {
		t.Error("AddNet accepted a net with no schedule")
	}
}

func TestBrandingRoundTrip(t *testing.T) {
	ps := newTestStore(t)

	got, err := ps.Branding()
	if err != nil {
		t.Fatal(err)
	}
	if got != (Branding{}) {
		t.Errorf("fresh branding = %+v, want zero", got)
	}

	// Stored verbatim: sanitizing happens at render (narrative) or is replaced by
	// iframe isolation (custom HTML). A store that quietly rewrote either would
	// corrupt the source an operator edits next.
	want := Branding{
		LogoPath:          "branding/logo.png",
		NarrativeMarkdown: "# K4SRC\n\nOpen to all licensed amateurs.\n\n<script>alert(1)</script>",
		CustomHTML:        `<div id="x"><script>console.log("hi")</script></div>`,
	}
	if err := ps.SaveBranding(want); err != nil {
		t.Fatal(err)
	}
	got, err = ps.Branding()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("branding did not survive a round trip.\n got: %+v\nwant: %+v", got, want)
	}

	if err := ps.SetLogoPath(""); err != nil {
		t.Fatal(err)
	}
	got, err = ps.Branding()
	if err != nil {
		t.Fatal(err)
	}
	if got.LogoPath != "" {
		t.Errorf("clearing the logo left %q", got.LogoPath)
	}
	if got.NarrativeMarkdown != want.NarrativeMarkdown {
		t.Error("SetLogoPath disturbed the narrative")
	}
}
