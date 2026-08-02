package publicview

import (
	"fmt"
	"strings"
)

// NormalizeCallsign reduces a heard callsign to the base call the suppress list
// matches on: uppercase, with the SSID and any portable designator removed
// (D8, "case-insensitive exact match on base callsign").
//
// Two shapes have to collapse to the same key, because both are the same operator
// and an operator who asked not to be listed meant all of them:
//
//	n4abc-7    -> N4ABC   (APRS-style SSID)
//	W1AW/4     -> W1AW    (portable in another call area)
//	KP4/W1AW   -> W1AW    (visiting operator, prefix first)
//	W1AW/P     -> W1AW    (portable)
//
// The '/' rule is "keep the longest segment", which is the heuristic every other
// piece of amateur software converges on, and it is right for the three shapes
// above without needing a prefix table. It is wrong for the rare call where the
// appended region is longer than the call itself; that is a miss in the direction
// of listing someone who asked not to be, so callers that care should also compare
// the un-split form. Suppression here is a courtesy control over third-party
// callsigns, not a security boundary — the security boundary is that the public
// structs carry no identifying fields beyond a callsign in the first place.
func NormalizeCallsign(in string) string {
	s := strings.ToUpper(strings.TrimSpace(in))
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s = s[:i]
	}
	if strings.ContainsRune(s, '/') {
		best := ""
		for _, part := range strings.Split(s, "/") {
			if len(part) > len(best) {
				best = part
			}
		}
		s = best
	}
	return s
}

// ValidateCallsign normalizes and then checks that what is left could be a
// callsign at all: letters and digits only, and long enough to be one. It exists
// so the suppress list cannot accumulate typos and empty strings that silently
// match nothing.
func ValidateCallsign(in string) (string, error) {
	s := NormalizeCallsign(in)
	if len(s) < 3 || len(s) > 12 {
		return "", fmt.Errorf("%w: %q", ErrBadCallsign, in)
	}
	digits := false
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			digits = true
		default:
			return "", fmt.Errorf("%w: %q has %q in it", ErrBadCallsign, in, r)
		}
	}
	if !digits {
		// Every amateur callsign carries a number. Requiring one is what keeps
		// "SKYWARN" or "PARROT" out of a list that is supposed to hold callsigns.
		return "", fmt.Errorf("%w: %q has no digit", ErrBadCallsign, in)
	}
	return s, nil
}

// NormalizeGrid validates a Maidenhead locator and returns it in the conventional
// case — field uppercase, square digits, subsquare lowercase (EM60lp) — truncated
// to six characters.
//
// Six is the ceiling rather than a preference: a 6-character grid is about
// 5 x 3 km, which is the precision the public surface is allowed to disclose (D3).
// Anything finer an operator pastes in is cut off here rather than at render, so
// there is no path by which the extra characters reach a response.
func NormalizeGrid(in string) (string, error) {
	s := strings.TrimSpace(in)
	if len(s) > 6 {
		s = s[:6]
	}
	if len(s) != 4 && len(s) != 6 {
		return "", fmt.Errorf("%w: %q is %d characters, want 4 or 6", ErrBadGrid, in, len(s))
	}
	b := []byte(strings.ToUpper(s))
	// Field: A..R, two 20 deg x 10 deg letters covering the globe exactly once.
	if b[0] < 'A' || b[0] > 'R' || b[1] < 'A' || b[1] > 'R' {
		return "", fmt.Errorf("%w: %q field must be A-R", ErrBadGrid, in)
	}
	if b[2] < '0' || b[2] > '9' || b[3] < '0' || b[3] > '9' {
		return "", fmt.Errorf("%w: %q square must be 0-9", ErrBadGrid, in)
	}
	out := string(b[:4])
	if len(b) == 6 {
		// Subsquare: A..X, 24 divisions of the square.
		if b[4] < 'A' || b[4] > 'X' || b[5] < 'A' || b[5] > 'X' {
			return "", fmt.Errorf("%w: %q subsquare must be A-X", ErrBadGrid, in)
		}
		out += strings.ToLower(string(b[4:6]))
	}
	return out, nil
}
