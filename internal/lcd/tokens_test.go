package lcd

import (
	"reflect"
	"testing"
	"time"
)

// tokenNow is a fixed instant so time-derived tokens are deterministic.
var tokenNow = time.Date(2026, 7, 13, 15, 4, 5, 0, time.UTC)

func populatedCtx() renderCtx {
	return renderCtx{
		st: &state{
			activeMode: "DMR",
			lastHeard:  &heard{call: "W1ABC", tg: "TG91", mode: "DMR", ber: 0.5, rssi: -70, at: tokenNow.Add(-30 * time.Second)},
		},
		info: Info{Callsign: "KN4OQW", DMRID: "3180202", Modes: []string{"DMR", "YSF"}, Version: "1.2.3", Started: tokenNow.Add(-90 * time.Minute)},
		now:  tokenNow,
		ip:   func() string { return "192.168.1.50" },
	}
}

func TestExpandPopulated(t *testing.T) {
	rc := populatedCtx()
	cases := map[string]string{
		"{callsign}": "KN4OQW",
		"{dmr_id}":   "3180202",
		"{ip}":       "192.168.1.50",
		"{time}":     "15:04",
		"{date}":     "2026-07-13",
		"{uptime}":   "1h30m",
		"{version}":  "1.2.3",
		"{mode}":     "DMR",
		"{modes}":    "DMR YSF",
		"{status}":   "Listening", // not keyed in this ctx
		"{lh_call}":  "W1ABC",
		"{lh_tg}":    "TG91",
		"{lh_mode}":  "DMR",
		"{lh_ber}":   "0.5%",
		"{lh_rssi}":  "-70",
		"{lh_ago}":   "30s",
	}
	for tmpl, want := range cases {
		if got := expand(tmpl, rc); got != want {
			t.Errorf("expand(%q) = %q, want %q", tmpl, got, want)
		}
	}
	// A mixed template with literals and adjacency.
	if got := expand("{callsign}  {mode}", rc); got != "KN4OQW  DMR" {
		t.Errorf("mixed template = %q", got)
	}
}

func TestExpandActiveStatus(t *testing.T) {
	rc := populatedCtx()
	rc.st.active = true
	rc.st.actDir, rc.st.actMode, rc.st.actTG, rc.st.actCall = "RX", "DMR", "TG91", "W1ABC"
	if got := expand("{status}", rc); got != "RX DMR TG91 W1ABC" {
		t.Errorf("active status = %q", got)
	}
}

func TestExpandFallbacks(t *testing.T) {
	rc := renderCtx{st: &state{}, info: Info{}, now: tokenNow, ip: nil}
	cases := map[string]string{
		"{mode}":     fbMode, // IDLE
		"{status}":   fbIdle, // Listening
		"{lh_call}":  fbNone, // -
		"{lh_tg}":    fbNone,
		"{lh_mode}":  fbNone,
		"{lh_ber}":   fbNone,
		"{lh_rssi}":  fbNone,
		"{lh_ago}":   fbNone,
		"{ip}":       fbNoIP, // no-ip (nil ip func)
		"{modes}":    "",     // no enabled modes
		"{callsign}": "",
	}
	for tmpl, want := range cases {
		if got := expand(tmpl, rc); got != want {
			t.Errorf("fallback expand(%q) = %q, want %q", tmpl, got, want)
		}
	}
}

func TestExpandUnknownRendersEmpty(t *testing.T) {
	rc := populatedCtx()
	if got := expand("[{bogus}{callsign}]", rc); got != "[KN4OQW]" {
		t.Errorf("unknown token not blanked: %q", got)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		tmpl string
		want []string
	}{
		{"{callsign} {mode}", nil},
		{"{callsign} {bogus} {mode} {nope} {bogus}", []string{"bogus", "nope"}}, // deduped, in order
		{"no tokens here", nil},
		{"{lh_rssi_bar}", []string{"lh_rssi_bar"}}, // reserved-but-not-implemented reads as unknown
	}
	for _, c := range cases {
		if got := Validate(c.tmpl); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Validate(%q) = %v, want %v", c.tmpl, got, c.want)
		}
	}
}

func TestSanitizeASCII(t *testing.T) {
	if got := sanitizeASCII("café—x"); got != "caf??x" {
		t.Errorf("sanitizeASCII = %q, want %q", got, "caf??x")
	}
	// expand sanitizes its output too (literal non-ASCII in a template).
	rc := populatedCtx()
	if got := expand("hi—{callsign}", rc); got != "hi?KN4OQW" {
		t.Errorf("expand did not sanitize: %q", got)
	}
}

func TestCompactDur(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-5 * time.Second, "0s"},
		{45 * time.Second, "45s"},
		{5 * time.Minute, "5m"},
		{2 * time.Hour, "2h"},
		{2*time.Hour + 15*time.Minute, "2h15m"},
		{24 * time.Hour, "1d"},
		{27 * time.Hour, "1d3h"},
	}
	for _, c := range cases {
		if got := compactDur(c.d); got != c.want {
			t.Errorf("compactDur(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
