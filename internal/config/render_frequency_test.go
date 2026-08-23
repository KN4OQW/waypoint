package config

import (
	"strings"
	"testing"
)

// Where the RF frequencies land in MMDVM-Host's INI.
//
// g4klx b7d15b8 deleted [Info] and moved RXFrequency/TXFrequency into [Modem].
// waypointd renders both placements so that it and waypoint-stack can be upgraded
// independently — they are separate packages. These tests pin that, and pin the
// two ways it can go wrong quietly: a blank key that means 0 Hz rather than
// "unset", and a section order that lets [Modem] clobber the paging channel.

// mmdvmSection returns the body of one section of the rendered MMDVM-Host INI.
func mmdvmSection(t *testing.T, m *Model, name string) []string {
	t.Helper()
	var out []string
	in := false
	for _, line := range strings.Split(m.RenderMMDVM(), "\n") {
		switch {
		case line == "["+name+"]":
			in = true
		case strings.HasPrefix(line, "["):
			in = false
		case in && line != "":
			out = append(out, line)
		}
	}
	return out
}

func hasKV(lines []string, kv string) bool {
	for _, l := range lines {
		if l == kv {
			return true
		}
	}
	return false
}

// TestFrequenciesRenderInBothSections is the compatibility claim.
//
// A host before b7d15b8 reads [Info] and ignores unknown keys in [Modem]; one at
// or after it reads [Modem] and drops the whole of an unrecognised [Info]. Writing
// both is what makes a single rendered file correct on either, and it is the only
// reason a stack that has not been bumped yet keeps working.
func TestFrequenciesRenderInBothSections(t *testing.T) {
	m := fixture()
	m.Modem.RXFreqHz = "433900000"
	m.Modem.TXFreqHz = "438900000"

	info := mmdvmSection(t, m, "Info")
	modem := mmdvmSection(t, m, "Modem")
	if len(info) == 0 || len(modem) == 0 {
		t.Fatalf("missing sections: Info=%d lines, Modem=%d lines", len(info), len(modem))
	}
	for _, want := range []string{"RXFrequency=433900000", "TXFrequency=438900000"} {
		if !hasKV(info, want) {
			t.Errorf("[Info] is missing %q — a host older than b7d15b8 would tune 0 Hz", want)
		}
		if !hasKV(modem, want) {
			t.Errorf("[Modem] is missing %q — a host at or after b7d15b8 would tune 0 Hz and NAK SET_FREQ", want)
		}
	}
}

// TestBothPlacementsAgree: two copies of one value is two chances to be wrong.
// Whatever the model says, both sections must say it.
func TestBothPlacementsAgree(t *testing.T) {
	for _, tc := range []struct{ rx, tx string }{
		{"144912500", "144912500"},   // simplex 2m
		{"433900000", "438900000"},   // 70cm split
		{"1298000000", "1270000000"}, // 23cm, above 32 bits of nothing but worth walking
	} {
		m := fixture()
		m.Modem.RXFreqHz, m.Modem.TXFreqHz = tc.rx, tc.tx
		info, modem := mmdvmSection(t, m, "Info"), mmdvmSection(t, m, "Modem")
		for _, k := range []struct{ key, val string }{{"RXFrequency", tc.rx}, {"TXFrequency", tc.tx}} {
			want := k.key + "=" + k.val
			if !hasKV(info, want) || !hasKV(modem, want) {
				t.Errorf("%s=%s: [Info] has it %v, [Modem] has it %v", k.key, k.val, hasKV(info, want), hasKV(modem, want))
			}
		}
	}
}

// TestBlankFrequenciesAreOmittedFromModem. The host reads these with atoi, so
// "RXFrequency=" is not "unset" — it is 0 Hz, which is the modem NAK that takes
// MMDVM-Host down in a restart loop. Blank is omitted, exactly as the per-mode
// levels in the same section are.
//
// [Info] is deliberately NOT held to this: it writes them unconditionally today,
// that is pre-existing, and a node with no frequency has its modem daemon
// withheld before any of it is read. The asymmetry is intentional and this test
// records it rather than leaving the next reader to wonder.
func TestBlankFrequenciesAreOmittedFromModem(t *testing.T) {
	m := fixture()
	m.Modem.RXFreqHz = ""
	m.Modem.TXFreqHz = ""

	for _, l := range mmdvmSection(t, m, "Modem") {
		if strings.HasPrefix(l, "RXFrequency=") || strings.HasPrefix(l, "TXFrequency=") {
			t.Errorf("[Modem] rendered %q; a blank frequency must be omitted, not written as 0 Hz", l)
		}
	}
	// One set, one blank: the set one still renders.
	m.Modem.RXFreqHz = "433900000"
	modem := mmdvmSection(t, m, "Modem")
	if !hasKV(modem, "RXFrequency=433900000") {
		t.Error("a set RXFrequency did not render just because TXFrequency was blank")
	}
	for _, l := range modem {
		if strings.HasPrefix(l, "TXFrequency=") {
			t.Errorf("[Modem] rendered %q for a blank TXFrequency", l)
		}
	}
}

// TestModemPrecedesPOCSAG is an ordering rule the move created.
//
// Parsing [Modem] TXFrequency has a side effect: it assigns m_pocsagFrequency as
// well as m_modemTXFrequency (Conf.cpp:646). [POCSAG] Frequency then overrides it
// — but only if [POCSAG] is parsed AFTER [Modem]. Reversed, every page would go
// out on the node's TX frequency instead of the paging channel, which is a
// transmission on a frequency the operator did not choose and may not be licensed
// for. Nothing in the INI format hints at this, so it is pinned here.
func TestModemPrecedesPOCSAG(t *testing.T) {
	m := fixture()
	m.Modem.TXFreqHz = "438900000"
	m.POCSAG.Frequency = "439987500"

	out := m.RenderMMDVM()
	modemAt := strings.Index(out, "\n[Modem]")
	pocsagAt := strings.Index(out, "\n[POCSAG]")
	if modemAt < 0 || pocsagAt < 0 {
		t.Fatalf("missing sections: [Modem]@%d [POCSAG]@%d", modemAt, pocsagAt)
	}
	if pocsagAt < modemAt {
		t.Fatal("[POCSAG] renders before [Modem]; TXFrequency would overwrite the paging channel and every page would go out on the node's TX frequency")
	}
	// And the paging channel is actually there to do the overriding.
	if !hasKV(mmdvmSection(t, m, "POCSAG"), "Frequency=439987500") {
		t.Error("the POCSAG paging channel did not render")
	}
}

// TestImportReadsEitherPlacement. RFC-0007 import ingests an incumbent node's
// config, and which section its frequencies live in depends on how old that node
// is: Pi-Star and WPSD and any Waypoint node predating this change put them in
// [Info]; anything built on MMDVM-Host at or after b7d15b8 puts them in [Modem].
//
// Reading only one would import a node with no frequency at all, and there is no
// error at the moment it happens — the operator finds out when the imported node
// will not tune.
func TestImportReadsEitherPlacement(t *testing.T) {
	const head = "[General]\nCallsign=KN4OQW\nId=3180202\n"
	for _, tc := range []struct {
		name, ini, wantRX, wantTX string
	}{
		{
			// The old shape: Pi-Star, WPSD, and every Waypoint node before this.
			name:   "[Info] only",
			ini:    head + "[Info]\nRXFrequency=433900000\nTXFrequency=438900000\n[Modem]\nUARTPort=/dev/ttyAMA0\n",
			wantRX: "433900000", wantTX: "438900000",
		},
		{
			// The new shape: upstream at or after b7d15b8, which has no [Info].
			name:   "[Modem] only",
			ini:    head + "[Modem]\nUARTPort=/dev/ttyAMA0\nRXFrequency=144912500\nTXFrequency=144912500\n",
			wantRX: "144912500", wantTX: "144912500",
		},
		{
			// What waypointd itself now renders. Both agree, so either read is
			// correct; this pins that having both does not confuse the importer.
			name:   "both, agreeing",
			ini:    head + "[Info]\nRXFrequency=433900000\nTXFrequency=438900000\n[Modem]\nRXFrequency=433900000\nTXFrequency=438900000\n",
			wantRX: "433900000", wantTX: "438900000",
		},
		{
			// Disagreeing is not a file waypointd produces, but a hand-edited one
			// can. [Modem] wins, because that is the section the running host reads
			// — importing the value the daemon ignores would import a fiction.
			name:   "both, disagreeing: [Modem] wins",
			ini:    head + "[Info]\nRXFrequency=1\nTXFrequency=2\n[Modem]\nRXFrequency=433900000\nTXFrequency=438900000\n",
			wantRX: "433900000", wantTX: "438900000",
		},
		{
			// A blank in [Modem] must not shadow a real value in [Info]: the
			// importer falls through on empty, not merely on absent.
			name:   "[Modem] blank falls back to [Info]",
			ini:    head + "[Info]\nRXFrequency=433900000\nTXFrequency=438900000\n[Modem]\nRXFrequency=\nTXFrequency=\n",
			wantRX: "433900000", wantTX: "438900000",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mm, err := ParseINI(strings.NewReader(tc.ini))
			if err != nil {
				t.Fatal(err)
			}
			dg, err := ParseINI(strings.NewReader("[General]\n"))
			if err != nil {
				t.Fatal(err)
			}
			m := fromINI(mm, dg, nil, nil, nil, nil, nil, nil, nil)
			if m.Modem.RXFreqHz != tc.wantRX {
				t.Errorf("RXFreqHz = %q, want %q", m.Modem.RXFreqHz, tc.wantRX)
			}
			if m.Modem.TXFreqHz != tc.wantTX {
				t.Errorf("TXFreqHz = %q, want %q", m.Modem.TXFreqHz, tc.wantTX)
			}
		})
	}
}

// TestImportedFrequencyRendersToBothSections closes the loop: a node imported
// from the OLD shape must render into the new one as well, or the import would
// hand the operator a config that stops the daemon they just migrated.
func TestImportedFrequencyRendersToBothSections(t *testing.T) {
	mm, err := ParseINI(strings.NewReader(
		"[General]\nCallsign=KN4OQW\nId=3180202\n[Info]\nRXFrequency=433900000\nTXFrequency=438900000\n[Modem]\nUARTPort=/dev/ttyAMA0\n"))
	if err != nil {
		t.Fatal(err)
	}
	dg, err := ParseINI(strings.NewReader("[General]\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := fromINI(mm, dg, nil, nil, nil, nil, nil, nil, nil)
	if !hasKV(mmdvmSection(t, m, "Modem"), "RXFrequency=433900000") {
		t.Error("a config imported from an [Info]-only node does not render RXFrequency into [Modem]; the migrated node would not tune on a current host")
	}
	if !hasKV(mmdvmSection(t, m, "Info"), "RXFrequency=433900000") {
		t.Error("the imported frequency was lost from [Info]")
	}
}
