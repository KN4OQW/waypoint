package modem

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The identity string is the whole of auto-detection.
//
// A CA6JAU-lineage MMDVM_HS firmware answers GET_VERSION with one line that
// carries everything Waypoint wants to know:
//
//	MMDVM_HS_Dual_Hat-v1.6.1 20230526 14.7456MHz dual ADF7021 FW by CA6JAU GitID #899fc2a
//
// That is board family, firmware version, build date, reference oscillator,
// radio count (hence duplex capability) and firmware provenance, in one string
// the modem volunteers before anything else has been configured. Bench evidence:
// docs/on-hardware-report.md.
//
// The grammar has drifted across firmware generations — the MHz field only
// appears from the 1.5.x era, older builds omit it, and full-size MMDVM (F4/F7)
// boards use an entirely different shape — so this is parsed by recognising
// fields wherever they appear rather than by position, and never errors. An
// unparseable string still describes a real modem that is really attached; the
// operator is better served by a partial answer plus a picker than by a refusal.

var (
	reFirmware = regexp.MustCompile(`^(.+?)-v([0-9][0-9A-Za-z.\-]*)$`)
	reBuilt    = regexp.MustCompile(`^\d{8}$`)
	reTCXO     = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)MHz$`)
	reGitID    = regexp.MustCompile(`GitID\s+#?([0-9a-fA-F]{6,40})`)
	reAuthor   = regexp.MustCompile(`(?:FW\s+)?by\s+([A-Za-z0-9]+)`)
	reMCU      = regexp.MustCompile(`\b(STM32[A-Z0-9]*)\b`)
)

// Description is a parsed identity string.
//
// TCXOHz is zero when the firmware did not report one — a real and common state
// on pre-1.5 builds, and the reason the board table carries a fallback value.
// Zero must never be silently rendered as "12.288 MHz"; it means "ask".
type Description struct {
	Raw      string
	HWType   string // the firmware's HW_TYPE token, e.g. "MMDVM_HS_Dual_Hat"
	Firmware string // e.g. "1.6.1"
	Built    string // e.g. "20230526"
	TCXOHz   int    // 0 = not reported
	Dual     bool   // two ADF7021s, i.e. duplex-capable hardware
	ADF7021  bool   // the launch-tier radio chip; false means this is not an HS board
	MCU      string // e.g. "STM32F722", only full-size MMDVM reports it
	Author   string
	GitID    string
}

// ParseDescription reads an identity string into its fields. It never fails.
func ParseDescription(raw string) Description {
	d := Description{Raw: strings.TrimSpace(raw)}
	if d.Raw == "" {
		return d
	}
	fields := strings.Fields(d.Raw)

	// The leading token is the board family, optionally with the firmware
	// version glued on as "-vX.Y.Z".
	d.HWType = fields[0]
	if m := reFirmware.FindStringSubmatch(fields[0]); m != nil {
		d.HWType, d.Firmware = m[1], m[2]
	}

	for i, f := range fields {
		switch {
		case d.Built == "" && reBuilt.MatchString(f):
			d.Built = f
		case d.TCXOHz == 0 && reTCXO.MatchString(f):
			d.TCXOHz = megahertzToHz(reTCXO.FindStringSubmatch(f)[1])
		case strings.EqualFold(f, "ADF7021"):
			d.ADF7021 = true
			// "dual" qualifies the chip it precedes, so only the immediately
			// preceding token counts. A board that merely has the word
			// elsewhere in its name is not a duplex board.
			if i > 0 && strings.EqualFold(fields[i-1], "dual") {
				d.Dual = true
			}
		}
	}
	if m := reMCU.FindStringSubmatch(d.Raw); m != nil {
		d.MCU = m[1]
	}
	if m := reGitID.FindStringSubmatch(d.Raw); m != nil {
		d.GitID = strings.ToLower(m[1])
	}
	if m := reAuthor.FindStringSubmatch(d.Raw); m != nil {
		d.Author = m[1]
	}
	return d
}

// megahertzToHz converts a decimal MHz string to whole Hz without touching a
// float. The repo's rule is that frequencies never round-trip through binary
// floating point (see config.Modem), and 14.7456 is exactly the kind of value
// that does not survive the trip.
func megahertzToHz(mhz string) int {
	whole, frac, _ := strings.Cut(mhz, ".")
	if len(frac) > 6 {
		frac = frac[:6] // sub-Hz precision is not a thing a TCXO label carries
	}
	digits := whole + frac + strings.Repeat("0", 6-len(frac))
	n := 0
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// TCXOLabel renders a reference frequency the way the boards and their firmware
// label it — "14.7456 MHz" — rather than as a bare Hz count nobody recognises.
func TCXOLabel(hz int) string {
	if hz <= 0 {
		return ""
	}
	whole := strconv.Itoa(hz / 1_000_000)
	frac := strings.TrimRight(fmt.Sprintf("%06d", hz%1_000_000), "0")
	if frac == "" {
		return whole + " MHz"
	}
	return whole + "." + frac + " MHz"
}
