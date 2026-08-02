package publicview

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/KN4OQW/waypoint/internal/store"
)

// newTestStore returns a public view model over a fresh on-disk store. On-disk
// rather than :memory: because the schema ladder's backup path is exercised by the
// store's own tests and a file store is the shape the daemon actually runs.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() }) //nolint:errcheck // test cleanup
	return New(s)
}

// TestDefaultsAreClosed is the D2 assertion. A node nobody has configured must
// come up with the public surface off — everything else in this package assumes
// an operator made a decision, and this is the test that says what happens when
// they have not.
func TestDefaultsAreClosed(t *testing.T) {
	ps := newTestStore(t)

	got, err := ps.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Error("a fresh node has the public view enabled; D2 requires opt-in")
	}
	if got.ShowGrid {
		t.Error("a fresh node discloses its grid square by default; D3 requires opting into location")
	}
	if got.RetentionHours != DefaultRetentionHours {
		t.Errorf("retention = %d h, want the %d h default (D6)", got.RetentionHours, DefaultRetentionHours)
	}
	if len(got.PurposeTags) != 0 {
		t.Errorf("fresh purpose tags = %v, want none", got.PurposeTags)
	}

	// The table's column defaults and DefaultSettings() are two statements of the
	// same thing, and this is what keeps them from drifting.
	if want := DefaultSettings(); !reflect.DeepEqual(got, want) {
		t.Errorf("the schema's defaults and DefaultSettings() disagree.\n got: %+v\nwant: %+v", got, want)
	}

	on, err := ps.Enabled()
	if err != nil {
		t.Fatal(err)
	}
	if on {
		t.Error("Enabled() reports on for a fresh node")
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	ps := newTestStore(t)

	want := DefaultSettings()
	want.Enabled = true
	want.ShowGrid = true
	want.ShowMap = false
	want.RetentionHours = 72
	want.PowerLine = "45 W into a DB420 at 140 ft AGL"
	want.PurposeTags = []string{"club_net", "emcomm"}
	want.PurposeFreetext = "Visitors welcome, no membership needed."
	want.GridOverride = "EM60lp"

	if err := ps.SaveSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err := ps.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("settings did not survive a round trip.\n got: %+v\nwant: %+v", got, want)
	}
}

// TestRetentionClamps covers D6's bounds. Clamping rather than rejecting is the
// deliberate choice for this field — see SaveSettings.
func TestRetentionClamps(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   int
		want int
	}{
		{"zero means unset", 0, DefaultRetentionHours},
		{"negative clamps to floor", -5, MinRetentionHours},
		{"below floor clamps up", 0 - 1, MinRetentionHours},
		{"at floor", 1, 1},
		{"in range", 48, 48},
		{"at ceiling", 168, 168},
		{"above ceiling clamps down", 100000, MaxRetentionHours},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := newTestStore(t)
			v := DefaultSettings()
			v.RetentionHours = tc.in
			if err := ps.SaveSettings(v); err != nil {
				t.Fatalf("SaveSettings(%d): %v", tc.in, err)
			}
			got, err := ps.Settings()
			if err != nil {
				t.Fatal(err)
			}
			if got.RetentionHours != tc.want {
				t.Errorf("retention %d stored as %d, want %d", tc.in, got.RetentionHours, tc.want)
			}
		})
	}
}

func TestPurposeTagsRejectUnknown(t *testing.T) {
	ps := newTestStore(t)
	v := DefaultSettings()
	v.PurposeTags = []string{"club_net", "definitely_not_a_tag"}
	err := ps.SaveSettings(v)
	if !errors.Is(err, ErrUnknownTag) {
		t.Fatalf("SaveSettings with an unknown tag: err = %v, want ErrUnknownTag", err)
	}
	// The rejection must be total: nothing from the rejected write may land.
	got, err := ps.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.PurposeTags) != 0 {
		t.Errorf("a rejected write stored tags anyway: %v", got.PurposeTags)
	}
}

func TestPurposeTagsNormalize(t *testing.T) {
	ps := newTestStore(t)
	v := DefaultSettings()
	v.PurposeTags = []string{"EMCOMM", "  club_net ", "emcomm", ""}
	if err := ps.SaveSettings(v); err != nil {
		t.Fatal(err)
	}
	got, err := ps.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"club_net", "emcomm"}; !reflect.DeepEqual(got.PurposeTags, want) {
		t.Errorf("tags normalized to %v, want %v", got.PurposeTags, want)
	}
}

// TestURLSchemesRejected is the stored-XSS check the runbook calls for. These are
// links an operator types once and every anonymous visitor clicks.
func TestURLSchemesRejected(t *testing.T) {
	for _, raw := range []string{
		"javascript:alert(1)",
		"JavaScript:alert(1)",
		"  javascript:alert(document.domain)  ",
		"data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==",
		"vbscript:msgbox(1)",
		"file:///etc/passwd",
		"ftp://example.org/pub",
		"//example.org/protocol-relative",
		"not a url at all",
		"",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ValidateURL(raw); !errors.Is(err, ErrBadURL) {
				t.Errorf("ValidateURL(%q) = %v, want ErrBadURL", raw, err)
			}
		})
	}
}

func TestURLSchemesAccepted(t *testing.T) {
	for _, raw := range []string{
		"http://example.org",
		"https://example.org/repeaters/k4src",
		"https://example.org:8443/x?y=1#z",
		"HTTPS://EXAMPLE.ORG/",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ValidateURL(raw); err != nil {
				t.Errorf("ValidateURL(%q) = %v, want it accepted", raw, err)
			}
		})
	}
}

func TestGridOverrideValidation(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"EM60lp", "EM60lp", false},
		{"em60LP", "EM60lp", false},
		{" em60lp ", "EM60lp", false},
		{"EM60", "EM60", false},
		// D3's precision ceiling: an 8-character locator is cut to 6 rather than
		// refused, so a paste from a GPS app narrows instead of failing.
		{"EM60lp12", "EM60lp", false},
		{"ZZ99xx", "", true}, // field beyond A-R
		{"EMAAlp", "", true}, // square is not digits
		{"EM60zz", "", true}, // subsquare beyond A-X
		{"EM6", "", true},    // wrong length
		{"EM60l", "", true},  // wrong length
		{"", "", true},       // empty is handled by the caller, not here
		{"E-60lp", "", true}, // punctuation
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := NormalizeGrid(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrBadGrid) {
					t.Errorf("NormalizeGrid(%q) = %q, %v; want ErrBadGrid", tc.in, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeGrid(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeGrid(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestGridOverrideRejectedOnSave(t *testing.T) {
	ps := newTestStore(t)
	v := DefaultSettings()
	v.GridOverride = "not-a-grid"
	if err := ps.SaveSettings(v); !errors.Is(err, ErrBadGrid) {
		t.Fatalf("SaveSettings with a bad grid: err = %v, want ErrBadGrid", err)
	}
}

// TestGridOverrideTruncatedOnSave is the one that matters for disclosure: an
// operator pasting an 8-character locator must not end up publishing it.
func TestGridOverrideTruncatedOnSave(t *testing.T) {
	ps := newTestStore(t)
	v := DefaultSettings()
	v.GridOverride = "EM60lp37"
	if err := ps.SaveSettings(v); err != nil {
		t.Fatal(err)
	}
	got, err := ps.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if got.GridOverride != "EM60lp" {
		t.Errorf("stored grid = %q, want it cut to 6 characters (EM60lp) — D3 caps public precision", got.GridOverride)
	}
}
