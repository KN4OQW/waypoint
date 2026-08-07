package config

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Per-mode readiness. Every mode gets the same treatment: a working node reports
// nothing, and each way that mode can be configured into silence reports exactly
// one finding naming the field to fix.
//
// The shape of these tests matters as much as the coverage. A check that fires on
// a good configuration is worse than no check — it trains an operator to ignore
// the panel — so every table below carries its clean case alongside its faults,
// and TestModeProblemsCleanNodeIsSilent runs the full fixture with every mode on.

// allModesOn returns the standard fixture with every mode enabled and the
// per-mode identity each one needs, i.e. a node that should report nothing. The
// fixture leaves D-Star, P25 and M17 off; readiness only inspects enabled modes,
// so a per-mode test has to switch its mode on before it can assert anything.
func allModesOn() *Model {
	m := fixture()
	m.Modes = Modes{DStar: true, DMR: true, YSF: true, P25: true, NXDN: true, M17: true, POCSAG: true, FM: true}
	// The fixture's callsign is six characters, which D-Star and M17 both accept.
	// Everything else it already carries: frequencies, a radio ID, in-range mode
	// parameters, an enabled DMR network, and a DAPNET AuthKey.
	return m
}

// find returns the problems matching a mode and field, so an assertion can say
// "this field, this severity" without depending on where in the list it lands.
func find(ps []ModeProblem, mode Mode, field string) []ModeProblem {
	var out []ModeProblem
	for _, p := range ps {
		if p.Mode == mode && p.Field == field {
			out = append(out, p)
		}
	}
	return out
}

// requireProblem asserts exactly one problem for (mode, field) at the given
// severity, and returns it so a caller can check the message.
func requireProblem(t *testing.T, ps []ModeProblem, mode Mode, field, severity string) ModeProblem {
	t.Helper()
	got := find(ps, mode, field)
	if len(got) != 1 {
		t.Fatalf("want exactly one %s problem on %q, got %d:\n%s", mode, field, len(got), format(ps))
	}
	if got[0].Severity != severity {
		t.Errorf("%s %s severity = %q, want %q: %s", mode, field, got[0].Severity, severity, got[0].Message)
	}
	if strings.TrimSpace(got[0].Message) == "" {
		t.Errorf("%s %s reported an empty message", mode, field)
	}
	return got[0]
}

func format(ps []ModeProblem) string {
	var b strings.Builder
	for _, p := range ps {
		b.WriteString("  [" + p.Severity + "] " + string(p.Mode) + "/" + p.Field + ": " + p.Message + "\n")
	}
	if b.Len() == 0 {
		return "  (none)\n"
	}
	return b.String()
}

// A node that works reports nothing — with every mode switched on at once, which
// is the case most likely to produce a spurious finding from a cross-mode rule.
func TestModeProblemsCleanNodeIsSilent(t *testing.T) {
	if got := allModesOn().ModeProblems(); len(got) != 0 {
		t.Fatalf("a fully configured node reported problems:\n%s", format(got))
	}
}

// A mode that is off is never reported, however badly it is configured. An
// operator who turned D-Star off is not asking to hear about its module letter,
// and a node with no modes at all reports nothing whatsoever.
func TestModeProblemsIgnoreDisabledModes(t *testing.T) {
	m := allModesOn()
	// Wreck every per-mode value and the station-wide ones too.
	m.General.Callsign, m.General.ID = "", ""
	m.Modem.RXFreqHz, m.Modem.TXFreqHz = "", ""
	m.DStar.Module, m.DMR.ColorCode, m.P25.NAC = "not-a-letter", "99", "zzz"
	m.NXDN.RAN, m.M17.CAN, m.FM.AccessMode = "99", "99", "9"
	m.POCSAG.AuthKey = ""

	m.Modes = Modes{}
	if got := m.ModeProblems(); len(got) != 0 {
		t.Fatalf("problems reported with every mode off:\n%s", format(got))
	}

	// Switching one mode back on reports that mode and the station-wide faults,
	// and nothing belonging to the seven still off.
	m.Modes.NXDN = true
	got := m.ModeProblems()
	for _, p := range got {
		if p.Mode != "" && p.Mode != ModeNXDN {
			t.Errorf("NXDN alone reported a %s problem: %s", p.Mode, p.Message)
		}
	}
	requireProblem(t, got, ModeNXDN, "nxdn.ran", SeverityError)
	requireProblem(t, got, "", "general.callsign", SeverityError)
}

// The station-wide checks: the callsign every mode identifies with and the
// frequencies the modem tunes. These are reported once, with an empty Mode,
// rather than repeated for each of the eight modes that depend on them.
func TestStationProblems(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*Model)
		field    string
		severity string
	}{
		{"no callsign", func(m *Model) { m.General.Callsign = "" }, "general.callsign", SeverityError},
		{"blank callsign", func(m *Model) { m.General.Callsign = "   " }, "general.callsign", SeverityError},
		{"callsign with a space", func(m *Model) { m.General.Callsign = "KN4OQW R" }, "general.callsign", SeverityError},
		// Blank and unparseable both reach the daemon as 0 Hz; both are errors, and
		// the messages differ because the fix differs.
		{"no rx frequency", func(m *Model) { m.Modem.RXFreqHz = "" }, "modem.rx_freq_hz", SeverityError},
		{"no tx frequency", func(m *Model) { m.Modem.TXFreqHz = "" }, "modem.tx_freq_hz", SeverityError},
		{"non-numeric rx frequency", func(m *Model) { m.Modem.RXFreqHz = "438.8" }, "modem.rx_freq_hz", SeverityError},
		{"zero tx frequency", func(m *Model) { m.Modem.TXFreqHz = "0" }, "modem.tx_freq_hz", SeverityError},
		// The unit slip: megahertz or kilohertz typed into a hertz field. Both parse
		// as valid integers, so neither is an atoi() failure — they are caught by the
		// plausibility bound, and warn rather than error because the bound is a
		// heuristic and a legal allocation must never be refused.
		{"frequency in megahertz", func(m *Model) { m.Modem.RXFreqHz = "438" }, "modem.rx_freq_hz", SeverityWarning},
		{"frequency in kilohertz", func(m *Model) { m.Modem.RXFreqHz = "438800" }, "modem.rx_freq_hz", SeverityWarning},
		{"duplex without a split", func(m *Model) {
			m.General.Duplex = true
			m.Modem.RXFreqHz, m.Modem.TXFreqHz = "438800000", "438800000"
		}, "general.duplex", SeverityWarning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := allModesOn()
			tc.mutate(m)
			// YSF has its own, harder finding on a missing transmit frequency, so
			// switch it off here to keep this table to the station-wide rule.
			m.Modes.YSF = false
			requireProblem(t, m.ModeProblems(), "", tc.field, tc.severity)
		})
	}
}

// A frequency slip of exactly one decimal place lands inside the plausibility
// bound and is deliberately NOT reported: 43880000 Hz is 43.88 MHz, which is
// implausible, but the bound is set wide on purpose so that no legal allocation
// anybody's regulator permits is ever refused. This pins that trade-off so a
// later tightening is a decision rather than an accident.
func TestFrequencyBoundIsDeliberatelyWide(t *testing.T) {
	m := allModesOn()
	m.Modem.RXFreqHz, m.Modem.TXFreqHz = "144390000", "144390000"
	m.General.Duplex = false
	m.Modes.POCSAG = false // its paging channel is on 70cm; a 2m node is a real finding
	// Scoped to the FREQUENCY fields, which is what this test is about. It used to
	// assert that the model produced no findings at all, which made it a catch-all
	// that any unrelated new check broke — the static-talkgroup hint fires on this
	// model quite correctly, because it is simplex and has a network.
	for _, p := range m.ModeProblems() {
		if strings.Contains(p.Field, "freq") {
			t.Errorf("a 2 m simplex node was reported against a frequency field:\n%s", format([]ModeProblem{p}))
		}
	}
}

// D-Star: the module letter both MMDVM-Host and DStarGateway key on, and the
// seven characters D-Star leaves for a callsign.
func TestDStarProblems(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*Model)
		field    string
		severity string
	}{
		{"lower-case module", func(m *Model) { m.DStar.Module = "b" }, "dstar.module", SeverityError},
		{"multi-character module", func(m *Model) { m.DStar.Module = "BB" }, "dstar.module", SeverityError},
		{"numeric module", func(m *Model) { m.DStar.Module = "1" }, "dstar.module", SeverityError},
		// Eight characters is one too many: the module letter takes the last.
		{"callsign too long", func(m *Model) { m.General.Callsign = "KN4OQW/R" }, "general.callsign", SeverityError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := allModesOn()
			tc.mutate(m)
			requireProblem(t, m.ModeProblems(), ModeDStar, tc.field, tc.severity)
		})
	}

	// A blank module is not a fault — the renderer supplies "B".
	m := allModesOn()
	m.DStar.Module = ""
	if got := find(m.ModeProblems(), ModeDStar, "dstar.module"); len(got) != 0 {
		t.Errorf("a blank module was reported, but the renderer defaults it: %s", got[0].Message)
	}
}

// DMR: the ID DMRGateway logs in with, the four-bit colour code, and having
// somewhere to connect.
func TestDMRProblems(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*Model)
		field    string
		severity string
	}{
		{"no id anywhere", func(m *Model) { m.DMR.ID, m.General.ID = "", "" }, "general.id", SeverityError},
		{"non-numeric dmr id", func(m *Model) { m.DMR.ID = "KN4OQW" }, "dmr.id", SeverityError},
		{"implausibly short id", func(m *Model) { m.DMR.ID = "42" }, "dmr.id", SeverityWarning},
		{"colour code above four bits", func(m *Model) { m.DMR.ColorCode = "20" }, "dmr.color_code", SeverityError},
		{"negative colour code", func(m *Model) { m.DMR.ColorCode = "-1" }, "dmr.color_code", SeverityError},
		{"non-numeric colour code", func(m *Model) { m.DMR.ColorCode = "one" }, "dmr.color_code", SeverityError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := allModesOn()
			tc.mutate(m)
			requireProblem(t, m.ModeProblems(), ModeDMR, tc.field, tc.severity)
		})
	}

	// The [DMR] Id overrides [General] Id, so a good DMR id covers a blank general
	// one — and the finding, when there is one, names the field that is actually in
	// force rather than the one that is not.
	t.Run("dmr id overrides the general id", func(t *testing.T) {
		m := allModesOn()
		m.General.ID = ""
		m.Modes.P25 = false // P25 has no override and reports the blank general id
		if got := find(m.ModeProblems(), ModeDMR, "general.id"); len(got) != 0 {
			t.Errorf("blank general id reported while [DMR] Id is set: %s", got[0].Message)
		}
	})

	// The colour code the radios are actually on when nobody set one is the
	// rendered default, not zero — so a blank field is not a fault.
	t.Run("blank colour code is the rendered default", func(t *testing.T) {
		m := allModesOn()
		m.DMR.ColorCode = ""
		if got := find(m.ModeProblems(), ModeDMR, "dmr.color_code"); len(got) != 0 {
			t.Errorf("a blank colour code was reported, but it renders as %s: %s", DefaultDMRColorCode, got[0].Message)
		}
	})

	// The full (Duplex, Slot1, Slot2) matrix against CDMRNetwork::read. Slot 1 is
	// dropped outright on simplex by the DMO rule, then each slot is dropped when
	// its flag is false, so inbound survives only via slot 2, or via slot 1 when
	// the node is duplex. Three of the eight cells can carry nothing.
	t.Run("slot matrix", func(t *testing.T) {
		for _, tc := range []struct {
			name                 string
			duplex, slot1, slot2 bool
			dead                 bool
		}{
			{"simplex, slot 2 only (the conventional hotspot)", false, false, true, false},
			{"simplex, both slots on", false, true, true, false},
			{"simplex, slot 1 only -- the 2026-08-06 incident", false, true, false, true},
			{"simplex, both slots off", false, false, false, true},
			{"duplex, both slots on", true, true, true, false},
			{"duplex, slot 1 only", true, true, false, false},
			{"duplex, slot 2 only", true, false, true, false},
			{"duplex, both slots off", true, false, false, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				m := allModesOn()
				m.General.Duplex, m.DMRNet.Slot1, m.DMRNet.Slot2 = tc.duplex, tc.slot1, tc.slot2
				// A duplex node on one frequency trips the station-wide duplex check,
				// which is a different finding; keep a split so only slots are in play.
				if tc.duplex {
					m.Modem.TXFreqHz = "433800000"
				}
				got := find(m.ModeProblems(), ModeDMR, "dmrnet.slot2")
				if tc.dead {
					requireProblem(t, m.ModeProblems(), ModeDMR, "dmrnet.slot2", SeverityWarning)
					return
				}
				if len(got) != 0 {
					t.Errorf("healthy slot configuration reported as dead: %s", got[0].Message)
				}
			})
		}
	})

	// The slot flags gate only the network side, so they are not a fault when DMR
	// itself is off -- ModeProblems never reaches dmrProblems in that case.
	t.Run("slots ignored when DMR is disabled", func(t *testing.T) {
		m := allModesOn()
		m.Modes.DMR = false
		m.General.Duplex, m.DMRNet.Slot1, m.DMRNet.Slot2 = false, true, false
		if got := find(m.ModeProblems(), ModeDMR, "dmrnet.slot2"); len(got) != 0 {
			t.Errorf("slot finding raised while DMR is disabled: %s", got[0].Message)
		}
	})

	// A gateway with nothing to log into: every network row disabled. Warning, not
	// error — the node is correctly configured, it just has no upstream yet, which
	// is a legitimate state during setup.
	t.Run("no network enabled", func(t *testing.T) {
		m := allModesOn()
		for i := range m.Networks {
			m.Networks[i].Enabled = false
		}
		requireProblem(t, m.ModeProblems(), ModeDMR, "networks", SeverityWarning)
	})
}

// YSF: the frequency assert that takes the daemon down rather than merely
// misconfiguring it (#145), and the DG-ID path that is exempt from it.
func TestYSFProblems(t *testing.T) {
	t.Run("YSFGateway aborts without a transmit frequency", func(t *testing.T) {
		m := allModesOn()
		m.Modem.TXFreqHz = ""
		m.YSFGW.EnableDGId = false

		got := m.ModeProblems()
		p := requireProblem(t, got, ModeYSF, "modem.tx_freq_hz", SeverityError)
		if !strings.Contains(p.Message, "abort") {
			t.Errorf("the YSF finding does not say the daemon aborts: %s", p.Message)
		}
		// The station-wide finding is still there and is a different claim: the
		// modem never tunes. Both are true and the operator needs both.
		requireProblem(t, got, "", "modem.tx_freq_hz", SeverityError)
	})

	// DGIdGateway has no Wires-X and no such assert, so the daemon-abort finding
	// must not fire on the DG-ID path — reporting a restart loop that will not
	// happen is exactly the invented requirement this package refuses to add.
	t.Run("the DG-ID path has no such assert", func(t *testing.T) {
		m := allModesOn()
		m.Modem.TXFreqHz = ""
		m.YSFGW.EnableDGId = true
		if got := find(m.ModeProblems(), ModeYSF, "modem.tx_freq_hz"); len(got) != 0 {
			t.Errorf("the YSFGateway abort was reported for a DG-ID node: %s", got[0].Message)
		}
	})
}

// P25: the NAC that MMDVM-Host reads as hexadecimal, and the source ID it takes
// from the station.
func TestP25Problems(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*Model)
		field    string
		severity string
	}{
		{"non-hex nac", func(m *Model) { m.P25.NAC = "zzz" }, "p25.nac", SeverityError},
		{"nac above twelve bits", func(m *Model) { m.P25.NAC = "1000" }, "p25.nac", SeverityError},
		{"no radio id", func(m *Model) { m.General.ID = "" }, "general.id", SeverityError},
		{"non-numeric radio id", func(m *Model) { m.General.ID = "KN4OQW" }, "general.id", SeverityError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := allModesOn()
			tc.mutate(m)
			requireProblem(t, m.ModeProblems(), ModeP25, tc.field, tc.severity)
		})
	}

	// The field is hexadecimal, so the stock 293 and the top of the range are both
	// valid and neither may be reported.
	for _, nac := range []string{"293", "FFF", "fff", "0"} {
		t.Run("valid nac "+nac, func(t *testing.T) {
			m := allModesOn()
			m.P25.NAC = nac
			if got := find(m.ModeProblems(), ModeP25, "p25.nac"); len(got) != 0 {
				t.Errorf("NAC %q reported: %s", nac, got[0].Message)
			}
		})
	}
}

// NXDN: the six-bit RAN, and the ID check that deliberately does not exist.
func TestNXDNProblems(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ran      string
		severity string
	}{
		{"ran above six bits", "64", SeverityError},
		{"negative ran", "-1", SeverityError},
		{"non-numeric ran", "one", SeverityError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := allModesOn()
			m.NXDN.RAN = tc.ran
			requireProblem(t, m.ModeProblems(), ModeNXDN, "nxdn.ran", tc.severity)
		})
	}

	for _, ran := range []string{"", "0", "1", "63"} {
		t.Run("valid ran "+ran, func(t *testing.T) {
			m := allModesOn()
			m.NXDN.RAN = ran
			if got := find(m.ModeProblems(), ModeNXDN, "nxdn.ran"); len(got) != 0 {
				t.Errorf("RAN %q reported: %s", ran, got[0].Message)
			}
		})
	}

	// NXDN has no ID of its own in this stack — [General] Id feeds DMR and P25 and
	// nothing else, and NXDNGateway identifies by callsign. Requiring one would be
	// an invented rule, so a node with no radio ID must produce no NXDN finding.
	t.Run("no invented nxdn id requirement", func(t *testing.T) {
		m := allModesOn()
		m.Modes = Modes{NXDN: true}
		m.General.ID, m.DMR.ID = "", ""
		for _, p := range m.ModeProblems() {
			if p.Mode == ModeNXDN {
				t.Errorf("NXDN reported a finding for a missing radio ID: %s", p.Message)
			}
		}
	})
}

// M17: the four-bit CAN, and the base-40 alphabet that turns an unencodable
// callsign into spaces rather than an error.
func TestM17Problems(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*Model)
		field    string
		severity string
	}{
		{"can above four bits", func(m *Model) { m.M17.CAN = "16" }, "m17.can", SeverityError},
		{"negative can", func(m *Model) { m.M17.CAN = "-1" }, "m17.can", SeverityError},
		{"non-numeric can", func(m *Model) { m.M17.CAN = "zero" }, "m17.can", SeverityError},
		// '_' is not in M17's alphabet, so encodeCallsign maps it to a space.
		{"unencodable callsign", func(m *Model) { m.General.Callsign = "KN4_OQW" }, "general.callsign", SeverityError},
		{"callsign over nine characters", func(m *Model) { m.General.Callsign = "KN4OQW/PORT" }, "general.callsign", SeverityError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := allModesOn()
			m.Modes.DStar = false // D-Star's seven-character limit would fire too
			tc.mutate(m)
			requireProblem(t, m.ModeProblems(), ModeM17, tc.field, tc.severity)
		})
	}

	// The alphabet includes '-', '/' and '.', and MMDVM-Host upper-cases the
	// callsign before it is encoded, so none of these may be reported.
	for _, cs := range []string{"KN4OQW", "KN4OQW/P", "KN4OQW-1", "kn4oqw"} {
		t.Run("encodable callsign "+cs, func(t *testing.T) {
			m := allModesOn()
			m.Modes.DStar = false
			m.General.Callsign = cs
			if got := find(m.ModeProblems(), ModeM17, "general.callsign"); len(got) != 0 {
				t.Errorf("callsign %q reported: %s", cs, got[0].Message)
			}
		})
	}
}

// POCSAG: the DAPNET credential — which is also the one requirement that
// withholds a daemon — and the paging channel's band.
func TestPOCSAGProblems(t *testing.T) {
	// The reported finding and the withheld daemon must come from one rule, so a
	// key upstream rejects (its own "TOPSECRET" placeholder) is reported here too.
	for _, key := range []string{"", "   ", "TOPSECRET"} {
		t.Run("auth key "+strconv.Quote(key), func(t *testing.T) {
			m := allModesOn()
			m.POCSAG.AuthKey = key
			p := requireProblem(t, m.ModeProblems(), ModePOCSAG, "pocsag.auth_key", SeverityError)
			// The message names the field, never the value: reporting that a secret
			// is unset must not disclose a secret.
			if strings.Contains(p.Message, key) && key != "" {
				t.Errorf("the message echoed the stored key: %s", p.Message)
			}
		})
	}

	t.Run("unparseable paging channel", func(t *testing.T) {
		m := allModesOn()
		m.POCSAG.Frequency = "439.9875"
		requireProblem(t, m.ModeProblems(), ModePOCSAG, "pocsag.frequency", SeverityError)
	})

	// The stock paging channel is on 70 cm. A 2 m node that never changed it pages
	// on a band it is not transmitting on, which nothing else on the node reports.
	t.Run("paging channel in another band", func(t *testing.T) {
		m := allModesOn()
		m.Modem.RXFreqHz, m.Modem.TXFreqHz = "145790000", "145790000"
		m.General.Duplex = false
		requireProblem(t, m.ModeProblems(), ModePOCSAG, "pocsag.frequency", SeverityWarning)
	})

	// A blank paging channel renders the default rather than 0, so it is not a
	// fault on a 70 cm node.
	t.Run("blank paging channel is the rendered default", func(t *testing.T) {
		m := allModesOn()
		m.POCSAG.Frequency = ""
		if got := find(m.ModeProblems(), ModePOCSAG, "pocsag.frequency"); len(got) != 0 {
			t.Errorf("a blank paging channel was reported: %s", got[0].Message)
		}
	})
}

// FM: the access mode and the tone it gates access on.
func TestFMProblems(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*Model)
		field    string
		severity string
	}{
		{"access mode out of range", func(m *Model) { m.FM.AccessMode = "4" }, "fm.access_mode", SeverityError},
		{"non-numeric access mode", func(m *Model) { m.FM.AccessMode = "ctcss" }, "fm.access_mode", SeverityError},
		{"non-numeric tone", func(m *Model) { m.FM.CTCSS = "PL" }, "fm.ctcss", SeverityError},
		// The repeater cannot be opened by anybody in this state.
		{"tone access with no tone", func(m *Model) { m.FM.AccessMode, m.FM.CTCSS = "2", "" }, "fm.ctcss", SeverityError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := allModesOn()
			tc.mutate(m)
			requireProblem(t, m.ModeProblems(), ModeFM, tc.field, tc.severity)
		})
	}

	// Carrier access needs no tone, so a blank tone there is not a fault.
	t.Run("carrier access needs no tone", func(t *testing.T) {
		m := allModesOn()
		m.FM.AccessMode, m.FM.CTCSS = "0", ""
		if got := find(m.ModeProblems(), ModeFM, "fm.ctcss"); len(got) != 0 {
			t.Errorf("carrier access reported a missing tone: %s", got[0].Message)
		}
	})
}

// A mode tab shows the station-wide faults alongside its own, because the missing
// callsign IS the reason the mode does not work and filtering on Mode alone would
// hide it.
func TestProblemsForIncludesStationWide(t *testing.T) {
	m := allModesOn()
	m.General.Callsign = ""
	m.NXDN.RAN = "99"

	got := m.ProblemsFor(ModeNXDN)
	requireProblem(t, got, "", "general.callsign", SeverityError)
	requireProblem(t, got, ModeNXDN, "nxdn.ran", SeverityError)
	for _, p := range got {
		if p.Mode != "" && p.Mode != ModeNXDN {
			t.Errorf("ProblemsFor(nxdn) leaked a %s problem: %s", p.Mode, p.Message)
		}
	}

	// D-Star's tab shows the same station-wide finding and none of NXDN's.
	if got := find(m.ProblemsFor(ModeDStar), ModeNXDN, "nxdn.ran"); len(got) != 0 {
		t.Error("ProblemsFor(dstar) carried NXDN's RAN finding")
	}
}

// The summary a status badge reads: errors mean the node will not work on the
// air, warnings alone do not.
func TestHasModeErrors(t *testing.T) {
	m := allModesOn()
	if m.HasModeErrors() {
		t.Fatalf("a working node reported errors:\n%s", format(m.ModeProblems()))
	}

	// A warning-only fault is not an error.
	for i := range m.Networks {
		m.Networks[i].Enabled = false
	}
	if m.HasModeErrors() {
		t.Errorf("a warning was counted as an error:\n%s", format(m.ModeProblems()))
	}

	m.General.Callsign = ""
	if !m.HasModeErrors() {
		t.Error("a missing callsign was not counted as an error")
	}
}

// The order is stable so a caller diffing two runs sees a changed configuration
// and not a changed map iteration: station-wide first, then modes in the order
// the UI lists them.
func TestModeProblemsStableOrder(t *testing.T) {
	m := allModesOn()
	m.General.Callsign = "" // station-wide
	m.DStar.Module = "bad"  // dstar, first in modeDisplay
	m.DMR.ColorCode = "20"  // dmr, second
	m.NXDN.RAN = "99"       // nxdn, fifth
	m.FM.AccessMode = "9"   // fm, last
	m.Modes.M17 = false     // keep the callsign finding out of M17's bucket

	want := []Mode{"", ModeDStar, ModeDMR, ModeNXDN, ModeFM}
	var got []Mode
	for _, p := range m.ModeProblems() {
		if len(got) == 0 || got[len(got)-1] != p.Mode {
			got = append(got, p.Mode)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("problem groups = %v, want %v:\n%s", got, want, format(m.ModeProblems()))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("problem order = %v, want %v", got, want)
		}
	}

	// And it is the same list twice.
	first, second := format(m.ModeProblems()), format(m.ModeProblems())
	if first != second {
		t.Errorf("two calls disagreed:\n%s\n---\n%s", first, second)
	}
}

// The projection is the only place an operator learns about an advisory finding —
// nothing is withheld and no unit fails, so there is no other evidence. It must
// carry field names and never values, so reporting that a secret is unset cannot
// disclose the secret.
func TestViewSurfacesModeProblems(t *testing.T) {
	m := allModesOn()
	if got := m.View(Sources{}).ModeProblems; len(got) != 0 {
		t.Fatalf("a working node projected problems:\n%s", format(got))
	}

	m.General.Callsign = ""
	m.POCSAG.AuthKey = "dapnet-s3cret-value"
	m.NXDN.RAN = "99"
	got := m.View(Sources{}).ModeProblems
	requireProblem(t, got, "", "general.callsign", SeverityError)
	requireProblem(t, got, ModeNXDN, "nxdn.ran", SeverityError)

	// The projection must equal the model's own answer — a second copy of the rule
	// in the view layer is a copy free to drift.
	if want := m.ModeProblems(); format(got) != format(want) {
		t.Errorf("View().ModeProblems disagreed with ModeProblems():\n%s\n---\n%s", format(got), format(want))
	}

	// No stored secret may appear in any message or field name.
	for _, p := range got {
		if strings.Contains(p.Message, m.POCSAG.AuthKey) || strings.Contains(p.Field, m.POCSAG.AuthKey) {
			t.Errorf("a projected problem carried the DAPNET AuthKey: %+v", p)
		}
	}
}

// A finding that exists only to REPORT must not withhold a daemon.
//
// This claim used to be "no readiness fault withholds anything", which was true
// when the registry held only the DAPNET AuthKey and stopped being true the
// moment MMDVM-Host was registered under ModeModem. The blanket version was
// always too broad for its own intent: the point is that REPORTING does not
// silently acquire the power to refuse, not that no value is ever both reported
// and required. So it is stated against the faults that are advisory and only
// advisory — the ones whose whole point is a node that comes up healthy and
// silent, where there is nothing for a registry to withhold because the daemon
// starts perfectly well.
func TestAdvisoryFindingsWithholdNoDaemon(t *testing.T) {
	m := allModesOn()
	// Every one of these produces a daemon that starts, binds, and reports itself
	// healthy. Callsign, radio ID and modem port are deliberately absent: those are
	// registered requirements as well as findings, which the next test covers.
	m.DMR.ColorCode, m.P25.NAC, m.NXDN.RAN, m.M17.CAN = "20", "zzz", "99", "99"
	m.FM.AccessMode = "9"
	// Frequencies stay in this set: they are reported, and #216 asks for them to be
	// registered as well, but they are not registered today. If that changes, this
	// line is the one that fails and says so.
	m.Modem.RXFreqHz, m.Modem.TXFreqHz = "", ""

	if !m.HasModeErrors() {
		t.Fatal("the wrecked model reported no errors; the rest of this test proves nothing")
	}
	if reqs := m.UnmetGatewayRequirements(); len(reqs) != 0 {
		t.Errorf("an advisory-only fault withheld a gateway: %+v", reqs)
	}
	if units := m.BlockedGatewayUnits(); len(units) != 0 {
		t.Errorf("an advisory-only fault blocked units: %v", units)
	}
}

// The identity values are BOTH reported and registered, and that overlap is
// deliberate rather than a duplicated rule: the registry withholds MMDVM-Host so
// an unidentified station cannot transmit, and the readiness report is what tells
// the operator which control to fill in. A withheld daemon is otherwise invisible
// — the unit is simply not running.
//
// What must not happen is the two disagreeing, so this pins that they name the
// same fields and fire on the same configuration.
func TestIdentityIsBothReportedAndWithheld(t *testing.T) {
	m := allModesOn()
	m.General.Callsign, m.General.ID = "", ""

	// Reported, against the station rather than any one mode.
	requireProblem(t, m.ModeProblems(), "", "general.callsign", SeverityError)

	// And withheld, naming the same fields.
	reqs := m.UnmetGatewayRequirements()
	if len(reqs) != 1 || reqs[0].Mode != ModeModem {
		t.Fatalf("UnmetGatewayRequirements() = %+v, want one ModeModem entry", reqs)
	}
	for _, f := range []string{"general.callsign", "general.id"} {
		if !slices.Contains(reqs[0].Missing, f) {
			t.Errorf("Missing = %v, want it to name %q", reqs[0].Missing, f)
		}
	}

	// Satisfying the identity clears both surfaces together.
	m.General.Callsign, m.General.ID = "KN4OQW", "3180202"
	if got := find(m.ModeProblems(), "", "general.callsign"); len(got) != 0 {
		t.Errorf("callsign still reported once set: %s", got[0].Message)
	}
	if reqs := m.UnmetGatewayRequirements(); len(reqs) != 0 {
		t.Errorf("still withheld once identity is set: %+v", reqs)
	}
}
