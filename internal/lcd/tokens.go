package lcd

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Info is the config/health-derived data the token engine needs beyond the live
// event stream. A later wiring stage fills it from config + health; the pure
// layer takes it as a value so tests inject it.
type Info struct {
	Callsign string
	DMRID    string
	Modes    []string // enabled mode names, in display order
	Version  string
	Started  time.Time // process start, for {uptime}
	Hostname string    // os.Hostname(), for {hostname}
	FreqRX   string    // modem RX frequency in Hz (config), for {freq_rx}
	FreqTX   string    // modem TX frequency in Hz (config), for {freq_tx}
}

// renderCtx bundles everything a token needs to resolve at one render instant.
type renderCtx struct {
	st   *state
	info Info
	now  time.Time
	ip   func() string // injected; the real host lookup lands in the wiring stage
}

var tokenRe = regexp.MustCompile(`\{([a-z0-9_]+)\}`)

// Fallbacks (design §5). fbNone is an ASCII hyphen, not an em dash: the HD44780
// character ROM is not UTF-8, so a "—" would sanitize to '?'.
const (
	fbMode = "IDLE"
	fbIdle = "Listening"
	fbNone = "-"
	fbNoIP = "no-ip"
)

// tokens is the single source of truth for the grounded token set (design §5):
// its keys drive Validate, its funcs drive expand — the two can never drift.
var tokens = map[string]func(rc renderCtx) string{
	// Identity + host (config / OS derived).
	"callsign": func(rc renderCtx) string { return rc.info.Callsign },
	"dmr_id":   func(rc renderCtx) string { return rc.info.DMRID },
	"ip":       func(rc renderCtx) string { return resolveIP(rc.ip) },
	"hostname": func(rc renderCtx) string { return orDash(rc.info.Hostname) },
	"version":  func(rc renderCtx) string { return rc.info.Version },
	"freq_rx":  func(rc renderCtx) string { return freqMHz(rc.info.FreqRX) },
	"freq_tx":  func(rc renderCtx) string { return freqMHz(rc.info.FreqTX) },
	// Clock + health.
	"time":   func(rc renderCtx) string { return rc.now.Format("15:04") },
	"date":   func(rc renderCtx) string { return rc.now.Format("2006-01-02") },
	"uptime": func(rc renderCtx) string { return compactDur(rc.now.Sub(rc.info.Started)) },
	// Live activity: {mode}/{modes}/{status}, plus the current transmission's
	// caller/talkgroup ({source}/{tg}, falling back to the last heard when idle)
	// and its signal ({rssi}/{ber}, measured at key-up, so the most-recent value).
	"mode":   func(rc renderCtx) string { return orIdle(rc.st.activeMode) },
	"modes":  func(rc renderCtx) string { return strings.Join(rc.info.Modes, " ") },
	"status": func(rc renderCtx) string { return status(rc.st) },
	"source": func(rc renderCtx) string { return curCall(rc.st) },
	"tg":     func(rc renderCtx) string { return curTG(rc.st) },
	"rssi":   func(rc renderCtx) string { return lh(rc.st, func(h *heard) string { return strconv.Itoa(h.rssi) }) },
	"ber": func(rc renderCtx) string {
		return lh(rc.st, func(h *heard) string { return fmt.Sprintf("%.1f%%", h.ber) })
	},
	// Last heard: the explicit most-recent-completed-transmission set.
	"lh_call": func(rc renderCtx) string { return lh(rc.st, func(h *heard) string { return h.call }) },
	"lh_tg":   func(rc renderCtx) string { return lh(rc.st, func(h *heard) string { return h.tg }) },
	"lh_mode": func(rc renderCtx) string { return lh(rc.st, func(h *heard) string { return h.mode }) },
	"lh_ber": func(rc renderCtx) string {
		return lh(rc.st, func(h *heard) string { return fmt.Sprintf("%.1f%%", h.ber) })
	},
	"lh_rssi": func(rc renderCtx) string { return lh(rc.st, func(h *heard) string { return strconv.Itoa(h.rssi) }) },
	"lh_ago":  func(rc renderCtx) string { return lhAgo(rc) },
	// Reserved (design §10.3): {lh_rssi_bar} — a CGRAM signal-bar glyph — is
	// deferred to keep v1 numeric. Add its func here when the device supports it.
}

func orIdle(m string) string {
	if m == "" || m == "IDLE" {
		return fbMode
	}
	return m
}

// orDash returns s, or the missing-value dash when s is blank.
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return fbNone
	}
	return s
}

// freqMHz renders a frequency stored in Hz (the daemons' unit) as MHz for a
// narrow panel — "433125000" → "433.1250". A blank value is the missing dash; a
// non-numeric value passes through unchanged (never guess at malformed input).
func freqMHz(hz string) string {
	hz = strings.TrimSpace(hz)
	if hz == "" {
		return fbNone
	}
	v, err := strconv.ParseInt(hz, 10, 64)
	if err != nil {
		return hz
	}
	return strconv.FormatFloat(float64(v)/1e6, 'f', 4, 64)
}

// curCall is the caller of the current transmission, or the last heard when idle
// ({source}). curTG is its talkgroup ({tg}). These read live while keyed so an
// activity page shows who is talking now, and hold the last contact once clear.
func curCall(s *state) string {
	if s.active && strings.TrimSpace(s.actCall) != "" {
		return s.actCall
	}
	return lh(s, func(h *heard) string { return h.call })
}

func curTG(s *state) string {
	if s.active && strings.TrimSpace(s.actTG) != "" {
		return s.actTG
	}
	return lh(s, func(h *heard) string { return h.tg })
}

// status is the activity line: "Listening" when idle, else "<dir> <mode> <tg>
// <call>", e.g. "RX DMR TG91 W1ABC".
func status(s *state) string {
	if !s.active {
		return fbIdle
	}
	return strings.TrimSpace(strings.Join([]string{s.actDir, s.actMode, s.actTG, s.actCall}, " "))
}

func lh(s *state, f func(*heard) string) string {
	if s.lastHeard == nil {
		return fbNone
	}
	if v := strings.TrimSpace(f(s.lastHeard)); v != "" {
		return v
	}
	return fbNone
}

func lhAgo(rc renderCtx) string {
	if rc.st.lastHeard == nil {
		return fbNone
	}
	return compactDur(rc.now.Sub(rc.st.lastHeard.at))
}

func resolveIP(f func() string) string {
	if f != nil {
		if v := strings.TrimSpace(f()); v != "" {
			return v
		}
	}
	return fbNoIP
}

// compactDur renders a duration compactly for a narrow display: "3s", "5m",
// "2h", "2h15m", "1d", "1d3h".
func compactDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return strconv.Itoa(h) + "h"
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days, h := int(d.Hours())/24, int(d.Hours())%24
		if h == 0 {
			return strconv.Itoa(days) + "d"
		}
		return fmt.Sprintf("%dd%dh", days, h)
	}
}

// expand replaces every known {token} with its value; an unknown token renders
// empty (Validate surfaces it separately). The result is sanitized to ASCII.
func expand(tmpl string, rc renderCtx) string {
	out := tokenRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		if f, ok := tokens[m[1:len(m)-1]]; ok {
			return f(rc)
		}
		return ""
	})
	return sanitizeASCII(out)
}

// Validate returns the unrecognized token names in a template (deduped, in
// order) so the UI/API can flag a typo instead of silently rendering blank.
func Validate(tmpl string) []string {
	var bad []string
	seen := map[string]bool{}
	for _, m := range tokenRe.FindAllStringSubmatch(tmpl, -1) {
		name := m[1]
		if _, ok := tokens[name]; ok || seen[name] {
			continue
		}
		seen[name] = true
		bad = append(bad, name)
	}
	return bad
}

// sanitizeASCII replaces any non-printable or non-ASCII rune with '?', since the
// HD44780 character ROM is not UTF-8.
func sanitizeASCII(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			b.WriteByte('?')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
