package cal

import "fmt"

// Where a transmitter may be keyed, checked before anything in this package
// keys one.
//
// The table is a UNION OF THE ITU REGIONS, and that is a deliberate choice
// rather than an oversight. Waypoint does not know which region a node is in —
// nothing in the config says so — and a check that guessed would refuse legal
// operation somewhere in the world. So it is scoped to the failure it can
// actually catch: a transposed digit or a stale frequency putting a carrier
// outside the amateur service altogether. Region 1's narrower 2 m and 70 cm
// allocations are the operator's responsibility, as they are with every other
// radio they own.
//
// The satellite segments are refused outright, and that is not a courtesy — the
// firmware refuses them too (MMDVM_HS IO.h BAN1/BAN2, "Banned amateur frequency
// ranges (satellite only, ISS, etc)"), so a node configured there would be NAKed
// by the board with a bare reason code. Refusing here means the operator is told
// which segment and why instead.

type band struct {
	lo, hi uint32
	name   string
}

// amateurBands is the union across regions of the allocations an MMDVM modem can
// physically reach.
var amateurBands = []band{
	{144_000_000, 148_000_000, "2 m"},
	{219_000_000, 225_000_000, "1.25 m"},
	{420_000_000, 450_000_000, "70 cm"},
	{902_000_000, 928_000_000, "33 cm"},
	{1_240_000_000, 1_300_000_000, "23 cm"},
}

// satelliteSegments are amateur allocations reserved for satellite and space
// operation, where a terrestrial hotspot does not belong.
var satelliteSegments = []band{
	{145_800_000, 146_000_000, "the 2 m satellite segment"},
	{435_000_000, 438_000_000, "the 70 cm satellite segment"},
}

// CheckTXFrequency reports whether a transmitter may be keyed on hz, and says
// why not when it may not. It is the gate every transmit path in this package
// passes through.
func CheckTXFrequency(hz uint32) error {
	if hz == 0 {
		return fmt.Errorf("cal: no transmit frequency is configured on this node, so nothing may be keyed")
	}
	for _, s := range satelliteSegments {
		if hz >= s.lo && hz <= s.hi {
			return fmt.Errorf("cal: %s is in %s, which is reserved for satellite and space operation — the modem's own firmware refuses it too",
				mhz(hz), s.name)
		}
	}
	for _, b := range amateurBands {
		if hz >= b.lo && hz < b.hi {
			return nil
		}
	}
	return fmt.Errorf("cal: %s is not in an amateur allocation this modem can work on, so Waypoint will not key a transmitter there", mhz(hz))
}

// BandName names the allocation a frequency falls in, for display. It is
// separate from the check so a receive-only sweep can label a frequency it will
// never transmit on.
func BandName(hz uint32) string {
	for _, b := range amateurBands {
		if hz >= b.lo && hz < b.hi {
			return b.name
		}
	}
	return ""
}

func mhz(hz uint32) string {
	return fmt.Sprintf("%d.%06d MHz", hz/1_000_000, hz%1_000_000)
}
