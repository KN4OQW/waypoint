package modem

import (
	"sort"
	"strings"
)

// The board table exists because the identity string names the FIRMWARE, not the
// product.
//
// A JumboSpot is a JumboSpot on the invoice and an "MMDVM_HS_Hat" on the wire,
// because that is the firmware its maker flashed. The ZUMspot line, the Libre
// Kit and several clones all answer to strings that describe the design they
// were derived from. So detection can be honest about the family, the
// oscillator and the radio count — those come off the wire — but the retail
// model is genuinely ambiguous on the wire, and issue #18 says so: "board/TCXO
// auto-detect where possible, explicit picker where not".
//
// The split this table encodes:
//
//   - HWTypes, Dual and (on modern firmware) the TCXO are MATCHING criteria.
//     They come from the modem and narrow the candidate list.
//   - Name, Transport and Firmware are what the OPERATOR gets to say when the
//     narrowing leaves more than one answer.
//
// Verified marks the entries confirmed against physical hardware. Only the Dual
// Hat has been: it is the project's reference bench board (issue #18, README).
// Everything else is seeded from the firmware's HW_TYPE defines and vendor
// documentation, and its TCXO fallback is a hint, never an assertion — which is
// why TCXOHz here is only consulted when the firmware reports nothing, and is
// flagged as assumed when it is.

// Transport is how a board attaches to the host.
type Transport uint8

const (
	TransportAny  Transport = iota // unknown, or a design sold in both forms
	TransportGPIO                  // the Pi's PL011 UART on the 40-pin header
	TransportUSB                   // USB CDC-ACM, or a USB-serial bridge
)

func (t Transport) String() string {
	switch t {
	case TransportGPIO:
		return "gpio"
	case TransportUSB:
		return "usb"
	}
	return "any"
}

// Board is one retail board Waypoint knows by name.
type Board struct {
	ID        string    // stable config value; never renamed once shipped
	Name      string    // what the operator calls it
	HWTypes   []string  // identity-string HW_TYPE tokens this board reports as
	TCXOHz    int       // fallback for firmware too old to report one; 0 = unknown
	Dual      bool      // two ADF7021s: duplex-capable
	Transport Transport // how it attaches
	Verified  bool      // confirmed against physical hardware
	Note      string    // shown next to the name in the picker

	// BOOT0Line and ResetLine are the Pi header lines wired to the board's BOOT0
	// and nRST pins, which is how firmware flashing gets a board into its ROM
	// bootloader without anyone touching a jumper (#19). Zero means the
	// family default — BCM20 and BCM21, which every launch-tier hat uses — and a
	// board that wires them elsewhere says so here.
	//
	// They live in this table because a pin assignment is a hardware fact about a
	// board, and the alternative is the flashing code growing a second, parallel
	// table of boards that has to be kept in step with this one.
	BOOT0Line int
	ResetLine int
}

// Boards is the launch tier from issue #18: the STM32F103 + ADF7021 family.
// Full-size MMDVM (#25) and DVMega (#26) are separate tiers and are deliberately
// absent — a board this table cannot name is offered as "other", not guessed at.
var Boards = []Board{
	{
		ID: "mmdvm_hs_hat", Name: "MMDVM_HS_Hat", HWTypes: []string{"MMDVM_HS_Hat"},
		TCXOHz: 12_288_000, Transport: TransportGPIO,
		Note: "Single ADF7021 GPIO hat, 12.288 MHz reference",
	},
	{
		ID: "mmdvm_hs_dual_hat", Name: "MMDVM_HS_Dual_Hat", HWTypes: []string{"MMDVM_HS_Dual_Hat"},
		TCXOHz: 14_745_600, Dual: true, Transport: TransportGPIO, Verified: true,
		Note: "Dual ADF7021 GPIO hat, 14.7456 MHz reference; duplex-capable",
	},
	{
		ID: "jumbospot", Name: "JumboSpot", HWTypes: []string{"MMDVM_HS_Hat", "ZUMspot"},
		TCXOHz: 12_288_000, Transport: TransportGPIO,
		Note: "MMDVM_HS_Hat-derived clone; reports as whatever firmware it was flashed with",
	},
	{
		ID: "zumspot_rpi", Name: "ZUMspot RPi", HWTypes: []string{"ZUMspot"},
		TCXOHz: 14_745_600, Transport: TransportGPIO,
		Note: "GPIO board from the ZUMspot line",
	},
	{
		ID: "zumspot_usb", Name: "ZUMspot USB", HWTypes: []string{"ZUMspot"},
		TCXOHz: 14_745_600, Transport: TransportUSB,
		Note: "USB stick form factor",
	},
	{
		ID: "zumspot_libre", Name: "ZUMspot Libre Kit", HWTypes: []string{"ZUMspot", "MMDVM_HS"},
		TCXOHz: 14_745_600, Transport: TransportAny,
		Note: "Kit build; sold for both GPIO and USB attachment",
	},
	{
		ID: "zumspot_duplex", Name: "ZUMspot Duplex", HWTypes: []string{"MMDVM_HS_Dual_Hat", "ZUMspot_Duplex"},
		TCXOHz: 14_745_600, Dual: true, Transport: TransportGPIO,
		Note: "Dual ADF7021; duplex-capable",
	},
	{
		ID: "nano_hotspot", Name: "Nano hotSPOT", HWTypes: []string{"Nano_hotSPOT"},
		TCXOHz: 12_288_000, Transport: TransportUSB,
		Note: "USB stick, BI7JTA",
	},
	{
		ID: "nanodv_npi", Name: "NanoDV NPi", HWTypes: []string{"NanoDV"},
		TCXOHz: 14_745_600, Transport: TransportGPIO,
		Note: "GPIO variant of the NanoDV",
	},
	{
		ID: "nanodv_usb", Name: "NanoDV USB", HWTypes: []string{"NanoDV"},
		TCXOHz: 14_745_600, Transport: TransportUSB,
		Note: "USB variant of the NanoDV",
	},
	{
		ID: "d2rg_mmdvm_hs", Name: "D2RG MMDVM_HS", HWTypes: []string{"D2RG_MMDVM_HS", "MMDVM_HS_Hat"},
		TCXOHz: 12_288_000, Transport: TransportGPIO,
	},
	{
		ID: "lonestar_usb", Name: "LoneStar USB", HWTypes: []string{"MMDVM_HS", "ZUMspot"},
		TCXOHz: 12_288_000, Transport: TransportUSB,
	},
	{
		ID: "lonestar_dual", Name: "LoneStar Dual Hat", HWTypes: []string{"MMDVM_HS_Dual_Hat"},
		TCXOHz: 14_745_600, Dual: true, Transport: TransportGPIO,
		Note: "Duplex-capable",
	},
	{
		ID: "skybridge", Name: "SkyBridge", HWTypes: []string{"SkyBridge", "MMDVM_HS_Hat"},
		TCXOHz: 14_745_600, Transport: TransportGPIO,
	},
	{
		ID: "euronode", Name: "EuroNode", HWTypes: []string{"MMDVM_HS_Hat", "MMDVM_HS"},
		TCXOHz: 12_288_000, Transport: TransportGPIO,
	},
	{
		ID: "generic_hs", Name: "Generic MMDVM_HS", HWTypes: []string{"MMDVM_HS"},
		Transport: TransportAny,
		Note:      "Any ADF7021 board this list does not name; behaves like the family",
	},
}

// BoardByID returns a board by its stable ID.
func BoardByID(id string) (Board, bool) {
	for _, b := range Boards {
		if b.ID == id {
			return b, true
		}
	}
	return Board{}, false
}

// Resolution is what detection concluded about which board is attached.
//
// It deliberately distinguishes three outcomes, because they need three
// different things from the operator:
//
//   - exactly one candidate: adopt it, say nothing
//   - several candidates: show the picker, pre-narrowed to those
//   - none: show the picker over the whole list, and say why nothing matched
type Resolution struct {
	BoardID     string   // set only when the answer is unambiguous
	Candidates  []string // board IDs, in table order; len != 1 means "ask"
	TCXOHz      int      // reported if the firmware said so, else the candidates' agreed value
	TCXOAssumed bool     // true when TCXOHz came from the table rather than the modem
	Dual        bool     // duplex-capable, as reported
}

// Resolve narrows the board table against what the modem said and how it is
// attached.
//
// Transport is a filter, not a criterion the modem supplies: the caller knows
// whether it found this modem on the GPIO UART or on a USB device, and that one
// fact separates most of the otherwise-identical GPIO/USB sibling pairs in the
// table. Pass TransportAny to skip the filter.
func Resolve(d Description, t Transport) Resolution {
	var r Resolution
	r.Dual = d.Dual
	r.TCXOHz = d.TCXOHz

	for _, b := range Boards {
		if !matchesHWType(b, d.HWType) {
			continue
		}
		// A board with two radios cannot be a single-radio product and vice
		// versa. This is the sharpest discriminator the wire gives us.
		if b.Dual != d.Dual {
			continue
		}
		// Only filter on a reported oscillator. An unreported one (old
		// firmware) must not silently exclude the right board.
		if d.TCXOHz != 0 && b.TCXOHz != 0 && b.TCXOHz != d.TCXOHz {
			continue
		}
		if t != TransportAny && b.Transport != TransportAny && b.Transport != t {
			continue
		}
		r.Candidates = append(r.Candidates, b.ID)
	}

	if len(r.Candidates) == 1 {
		r.BoardID = r.Candidates[0]
	}
	if r.TCXOHz == 0 {
		r.TCXOHz, r.TCXOAssumed = agreedTCXO(r.Candidates)
	}
	return r
}

// matchesHWType compares the identity string's family token against a board's
// declared tokens, case-insensitively — firmware forks have been known to
// change the casing, and the token is a product name, not a protocol constant.
func matchesHWType(b Board, hwType string) bool {
	if hwType == "" {
		return false
	}
	for _, t := range b.HWTypes {
		if strings.EqualFold(t, hwType) {
			return true
		}
	}
	return false
}

// agreedTCXO returns the reference frequency the candidates unanimously claim,
// so a modem whose firmware is too old to report one still gets a usable value
// when every board it could possibly be uses the same oscillator. Disagreement
// yields zero: an oscillator guessed wrong detunes the radio, so a tie is
// resolved by asking, not by picking.
func agreedTCXO(candidates []string) (hz int, assumed bool) {
	seen := map[int]bool{}
	for _, id := range candidates {
		b, ok := BoardByID(id)
		if ok && b.TCXOHz != 0 {
			seen[b.TCXOHz] = true
		}
	}
	if len(seen) != 1 {
		return 0, false
	}
	for hz := range seen {
		return hz, true
	}
	return 0, false
}

// BoardIDs lists every known board ID, sorted, for schema validation and for
// tests that assert the table has not grown a duplicate.
func BoardIDs() []string {
	out := make([]string, 0, len(Boards))
	for _, b := range Boards {
		out = append(out, b.ID)
	}
	sort.Strings(out)
	return out
}
