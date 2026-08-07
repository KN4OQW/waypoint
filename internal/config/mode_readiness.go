package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Per-mode configuration readiness.
//
// This is the third of three checks a node's configuration passes, and it is
// deliberately the only advisory one:
//
//   - ValidateModem / ValidateStationID / ValidateBuses REFUSE a save. They cover
//     values that are wrong on their face — a board that does not exist, a level
//     outside 0-100 — where accepting the write helps nobody.
//   - UnmetGatewayRequirements WITHHOLDS a daemon. It covers the one case where a
//     gateway reads a value at startup, finds it unset, and exits before opening
//     anything, so starting it is a restart loop (gateway_requirements.go).
//   - ModeProblems, here, REPORTS. It covers the configurations that save fine and
//     start fine and then do not work on the air: the node comes up, every unit is
//     active, the dashboard is green, and the operator debugs their radio or their
//     reflector for an evening.
//
// HardwareWarnings is the same shape for the same reason, and answers a different
// question: it compares the configuration against what the attached modem said
// about itself, so it reports nothing when no detection has run. These checks need
// no detection — they are about the configuration on its own terms — so the two
// are complementary rather than layered, and a UI shows both.
//
// Every check below is grounded in a specific line of the pinned upstream sources
// or in a protocol field width, and the comment on each says which. That standard
// is not decoration: an invented requirement here becomes an error message telling
// an operator their working node is broken, which is worse than saying nothing.
// A rule that could not be grounded was left out.

// ModeProblem is one configuration fault reported against an enabled mode.
//
// Mode is empty for a station-wide fault — a missing callsign is not DMR's problem
// or YSF's, it is the node's, and reporting it eight times because eight modes are
// enabled would bury the seven findings that are specific. ProblemsFor gathers the
// station-wide ones alongside a given mode's own.
//
// Field is the store path of the value to fix ("modem.rx_freq_hz"), so a UI can
// link the message to the control. It never carries a value, only a name, for the
// same reason GatewayRequirement.Missing does not: reporting that a secret is
// unset must not disclose a secret.
type ModeProblem struct {
	Mode     Mode   `json:"mode"`
	Field    string `json:"field"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// ModeProblems reports every readiness fault for the modes that are switched on.
//
// A mode that is off is never reported. Its gateway is not expected to carry
// traffic, and an operator who turned a mode off is not asking to be told its
// colour code is out of range.
//
// The order is stable — station-wide first, then modes in the order the UI lists
// them — so a caller diffing two runs sees a changed configuration and not a
// changed map iteration.
func (m *Model) ModeProblems() []ModeProblem {
	if m == nil {
		return nil
	}
	var out []ModeProblem
	if !m.AnyModeEnabled() {
		return nil
	}
	out = append(out, m.stationProblems()...)
	for _, md := range modeDisplay {
		if !md.get(m.Modes) {
			continue
		}
		switch Mode(md.key) {
		case ModeDStar:
			out = append(out, m.dstarProblems()...)
		case ModeDMR:
			out = append(out, m.dmrProblems()...)
		case ModeYSF:
			out = append(out, m.ysfProblems()...)
		case ModeP25:
			out = append(out, m.p25Problems()...)
		case ModeNXDN:
			out = append(out, m.nxdnProblems()...)
		case ModeM17:
			out = append(out, m.m17Problems()...)
		case ModePOCSAG:
			out = append(out, m.pocsagProblems()...)
		case ModeFM:
			out = append(out, m.fmProblems()...)
		}
	}
	return out
}

// ProblemsFor returns the problems that block one mode: its own, plus the
// station-wide ones it depends on. A mode tab in the UI shows this rather than
// filtering ModeProblems on Mode alone, which would hide the missing callsign
// that is the actual reason the mode does not work.
func (m *Model) ProblemsFor(mode Mode) []ModeProblem {
	var out []ModeProblem
	for _, p := range m.ModeProblems() {
		if p.Mode == "" || p.Mode == mode {
			out = append(out, p)
		}
	}
	return out
}

// HasModeErrors reports whether any enabled mode carries an error-severity
// problem — the "this node will not work on the air" summary a status badge needs
// without walking the list.
func (m *Model) HasModeErrors() bool {
	for _, p := range m.ModeProblems() {
		if p.Severity == SeverityError {
			return true
		}
	}
	return false
}

// stationProblems covers the values every enabled mode reads, whichever mode it
// is: the callsign the node identifies with and the frequencies the modem tunes.
func (m *Model) stationProblems() []ModeProblem {
	var out []ModeProblem
	add := func(field, sev, msg string) {
		out = append(out, ModeProblem{Field: field, Severity: sev, Message: msg})
	}

	// The callsign. MMDVM-Host upper-cases it on read (Conf.cpp:561-565) and every
	// gateway renders its own Callsign= from the same value, so case is not a
	// fault. Absence is: an empty callsign is transmitted as an empty callsign, the
	// CW identifier keys nothing, and a gateway logs into its network as nobody.
	cs := strings.TrimSpace(m.General.Callsign)
	switch {
	case cs == "":
		add("general.callsign", SeverityError,
			"No callsign is set. The node will transmit and identify with an empty callsign, and every gateway will log in as one.")
	case strings.ContainsAny(cs, " \t"):
		add("general.callsign", SeverityError,
			fmt.Sprintf("The callsign %q contains a space. The gateway INI parsers stop at the space, so the node identifies as %q.",
				cs, strings.Fields(cs)[0]))
	}

	// The frequencies. Unlike every mode parameter below, these render VERBATIM
	// into MMDVM-Host's [Info] with no default (render.go:486-492), so a blank
	// model field reaches the daemon as an empty value and atoi() makes it 0
	// (Conf.cpp:582-585). A modem told to tune 0 Hz does not come up on the air,
	// and one gateway does not come up at all — see ysfProblems.
	rx, rxOK := freqHz(m.Modem.RXFreqHz)
	tx, txOK := freqHz(m.Modem.TXFreqHz)
	for _, f := range []struct {
		field, label, raw string
		ok                bool
	}{
		{"modem.rx_freq_hz", "receive", m.Modem.RXFreqHz, rxOK},
		{"modem.tx_freq_hz", "transmit", m.Modem.TXFreqHz, txOK},
	} {
		if f.ok {
			continue
		}
		if strings.TrimSpace(f.raw) == "" {
			add(f.field, SeverityError, fmt.Sprintf(
				"No %s frequency is set. MMDVM-Host renders it as 0 Hz and the modem never tunes; the node will come up looking healthy and hear nothing.",
				f.label))
			continue
		}
		add(f.field, SeverityError, fmt.Sprintf(
			"The %s frequency %q is not a whole number of hertz. MMDVM-Host reads it with atoi(), which yields 0 for anything it cannot parse.",
			f.label, strings.TrimSpace(f.raw)))
	}

	// A plausibility bound, not a band plan. Deliberately far wider than any
	// amateur allocation, because band plans are regional and refusing a legal
	// frequency somebody else's regulator allows is not this check's business. What
	// it catches is the unit slip — 438.8 or 438800 entered where hertz was asked
	// for — which is the mistake that actually happens.
	for _, f := range []struct {
		field, label string
		hz           uint64
		ok           bool
	}{
		{"modem.rx_freq_hz", "receive", rx, rxOK},
		{"modem.tx_freq_hz", "transmit", tx, txOK},
	} {
		if !f.ok {
			continue
		}
		if f.hz < minPlausibleHz || f.hz > maxPlausibleHz {
			add(f.field, SeverityWarning, fmt.Sprintf(
				"The %s frequency is %s, which no MMDVM board tunes. This field is in HERTZ — 438.8 MHz is 438800000.",
				f.label, mhzLabel(f.hz)))
		}
	}

	// A duplex repeater keys its transmitter while its receiver is live, which is
	// only possible on a split. Same frequency both ways means the node hears
	// itself; on a hotspot it means duplex was switched on by mistake. Warning
	// rather than error because HardwareWarnings already errors on the harder
	// version of this (duplex on a single-ADF7021 board), and the two should not
	// both shout about one checkbox.
	if m.General.Duplex && rxOK && txOK && rx == tx {
		add("general.duplex", SeverityWarning, fmt.Sprintf(
			"Duplex is on but both frequencies are %s. A duplex repeater needs a transmit/receive split.", mhzLabel(rx)))
	}
	return out
}

// dstarProblems: the module letter and the eight-character callsign field.
func (m *Model) dstarProblems() []ModeProblem {
	var out []ModeProblem
	add := func(field, sev, msg string) {
		out = append(out, ModeProblem{Mode: ModeDStar, Field: field, Severity: sev, Message: msg})
	}

	// The module is a band letter. It renders into MMDVM-Host [D-Star] Module and
	// into DStarGateway [Repeater 1] Band (render.go:1173-1177), and the two must
	// agree — they do by construction, both coming from this one field, which is
	// exactly why a bad value breaks both ends at once. Blank renders the "B"
	// default, so only a set-but-wrong value is a fault.
	if mod := strings.TrimSpace(m.DStar.Module); mod != "" {
		if len(mod) != 1 || mod[0] < 'A' || mod[0] > 'Z' {
			add("dstar.module", SeverityError, fmt.Sprintf(
				"The D-Star module is %q. It must be a single capital letter (A-D is conventional); it is the band letter both MMDVM-Host and DStarGateway key on.",
				mod))
		}
	}

	// D-Star's callsign field is eight characters and the module letter occupies
	// the last one, so the station callsign has seven to live in. A longer callsign
	// is not truncated somewhere visible — it displaces the module, and the
	// repeater announces itself as a module nobody is linked to.
	if cs := strings.TrimSpace(m.General.Callsign); len(cs) > 7 {
		add("general.callsign", SeverityError, fmt.Sprintf(
			"The callsign %q is %d characters. D-Star's callsign field holds eight and the module letter takes the last, so a D-Star node's callsign can be at most seven.",
			cs, len(cs)))
	}
	return out
}

// dmrProblems: the radio ID DMRGateway logs in with, the colour code the radios
// key on, and whether there is anything to connect to.
func (m *Model) dmrProblems() []ModeProblem {
	var out []ModeProblem
	add := func(field, sev, msg string) {
		out = append(out, ModeProblem{Mode: ModeDMR, Field: field, Severity: sev, Message: msg})
	}
	// The message relay, when it was asked for and cannot be wired up.
	m.dmrShimProblems(add)
	// And the one piece of guidance here that is a guess rather than a fact.
	m.staticTalkgroupHint(add)

	// The DMR ID. [DMR] Id overrides [General] Id (the same precedence
	// DMRNetworkOrder resolves with), and DMRGateway sends it in the homebrew RPTL
	// login — a master that gets 0 rejects the login, so the mode never links.
	field := "dmr.id"
	id := strings.TrimSpace(m.DMR.ID)
	if id == "" {
		field, id = "general.id", strings.TrimSpace(m.General.ID)
	}
	switch {
	case id == "":
		add(field, SeverityError,
			"No DMR ID is set. DMRGateway logs into every master with it, and a login carrying ID 0 is refused.")
	case !allDigits(id):
		add(field, SeverityError, fmt.Sprintf(
			"The DMR ID %q is not a number. MMDVM-Host reads it with atoi(), which yields 0, and no master accepts a login from ID 0.", id))
	case len(id) < 5 || len(id) > 9:
		add(field, SeverityWarning, fmt.Sprintf(
			"The DMR ID %q is %d digits. Issued RadioIDs are six or seven, and a hotspot ESSID adds two — this is unlikely to be a registered ID.",
			id, len(id)))
	}

	// The colour code is a FOUR-BIT field: DMRSlotType.cpp:62 writes it as
	// (code << 4) & 0xF0. MMDVM-Host does not range-check it (Conf.cpp:810-811 is a
	// bare atoi), so 20 is accepted at every layer and transmitted as 4. Nothing
	// reports an error; radios on colour code 20 simply never decode.
	if cc := strings.TrimSpace(m.DMR.ColorCode); cc != "" {
		n, err := strconv.Atoi(cc)
		switch {
		case err != nil:
			add("dmr.color_code", SeverityError, fmt.Sprintf(
				"The DMR colour code %q is not a number. MMDVM-Host reads it with atoi(), so the node runs on colour code 0.", cc))
		case n < 0 || n > 15:
			add("dmr.color_code", SeverityError, fmt.Sprintf(
				"The DMR colour code is %d. It is a four-bit field, so the modem transmits %d instead and no radio on %d will decode.",
				n, n&0x0F, n))
		}
	}

	// [DMR Network] Slot1/Slot2 gate the network->RF direction and nothing else:
	// MMDVM-Host hands them to CDMRNetwork alone (MMDVM-Host.cpp:2072-2073), whose
	// read() is their only consumer. RF->network is untouched, and that asymmetry
	// is what makes a dead slot configuration so quiet — the node keeps reaching
	// the master and appearing on last-heard while nothing comes back, with every
	// health surface green.
	//
	// read() drops slot 1 outright when the node is not duplex ("DMO mode slot
	// disabling", DMRNetwork.cpp:147-149), then drops each slot whose flag is
	// false (:151-154). Inbound is therefore possible only when slot 2 is enabled,
	// or when duplex and slot 1 are both on — leaving three dead combinations.
	switch {
	case !m.DMRNet.Slot1 && !m.DMRNet.Slot2:
		add("dmrnet.slot2", SeverityWarning,
			"Both DMR timeslots are disabled, so MMDVM-Host drops every inbound network frame. The node still transmits and appears on the master's last-heard, but nothing reaches the radios.")
	case !m.DMRNet.Slot2 && !m.General.Duplex:
		add("dmrnet.slot2", SeverityWarning,
			"Timeslot 2 is disabled on a simplex node. MMDVM-Host drops slot 1 on simplex and slot 2 is switched off, so every inbound network frame is dropped. The node still transmits and appears on the master's last-heard, but nothing reaches the radios.")
	}

	// A DMR node with no network is a gateway with nothing to log into. It starts,
	// it stays up, and it carries no traffic. Bus attachments count: RFC-0003 lets
	// a bus stand in for a network, and dmrBusNetworks is the same enumeration
	// RenderDMRGateway writes sections from, so the two cannot disagree.
	dmrID := firstNonEmpty(m.DMR.ID, m.General.ID)
	if len(m.dmrBusNetworks(dmrID)) == 0 && !m.anyNetworkEnabled() {
		add("networks", SeverityWarning,
			"DMR is on but no network is enabled. DMRGateway will start and stay up with nothing to log into, so the mode carries no traffic.")
	}
	return out
}

// ysfProblems: the one place a missing frequency is fatal rather than merely
// wrong.
func (m *Model) ysfProblems() []ModeProblem {
	var out []ModeProblem

	// YSFGateway builds Wires-X unconditionally, and CWiresX::setInfo asserts
	// txFrequency > 0 — so a profile with no modem frequency does not misbehave,
	// it aborts the daemon at startup. This is issue #145, found by the Tier 2
	// harness, and it is why test/tier2's YSF models carry explicit frequencies.
	//
	// The DG-ID path is exempt: DGIdGateway has no Wires-X and no such assert. That
	// distinction is the reason this is a separate finding from the station-wide
	// frequency error rather than a louder version of it — the consequence differs
	// by which YSF gateway the node renders, and the operator needs to know their
	// daemon will not start at all.
	if _, ok := freqHz(m.Modem.TXFreqHz); !ok && !m.YSFGW.EnableDGId {
		out = append(out, ModeProblem{
			Mode: ModeYSF, Field: "modem.tx_freq_hz", Severity: SeverityError,
			Message: "YSFGateway aborts at startup when the transmit frequency is unset — it builds Wires-X unconditionally and CWiresX::setInfo asserts the frequency is non-zero (#145). The unit will restart-loop, not run.",
		})
	}
	return out
}

// p25Problems: the NAC and the source ID.
func (m *Model) p25Problems() []ModeProblem {
	var out []ModeProblem
	add := func(field, sev, msg string) {
		out = append(out, ModeProblem{Mode: ModeP25, Field: field, Severity: sev, Message: msg})
	}

	// The NAC is a TWELVE-BIT field written across a byte and a nibble
	// (P25NID.cpp:45-46: hdr[0] = nac >> 4, hdr[1] = nac << 4). MMDVM-Host parses
	// it as HEX — strtoul(value, nullptr, 16) at Conf.cpp:906-907 — which is the
	// trap here: an operator who enters their NAC in decimal gets a different NAC
	// on the air, silently, and 293 (the default) reads as a decimal number.
	if nac := strings.TrimSpace(m.P25.NAC); nac != "" {
		n, err := strconv.ParseUint(nac, 16, 32)
		switch {
		case err != nil:
			add("p25.nac", SeverityError, fmt.Sprintf(
				"The P25 NAC %q is not a hexadecimal number. MMDVM-Host parses this field with strtoul(base 16).", nac))
		case n > 0xFFF:
			add("p25.nac", SeverityError, fmt.Sprintf(
				"The P25 NAC 0x%X is out of range. It is a twelve-bit field, so the highest valid value is 0xFFF.", n))
		}
	}

	// P25's source ID is [General] Id — Conf.cpp:566-567 assigns m_p25Id from it,
	// with no P25-specific override anywhere in the section. A node transmitting
	// P25 with source ID 0 is not a valid SU, and P25Gateway's Id Lookup cannot
	// resolve it to a callsign for anyone downstream.
	if id := strings.TrimSpace(m.General.ID); id == "" {
		add("general.id", SeverityError,
			"No radio ID is set. P25 takes its source ID from the station ID, and transmissions from SU 0 are not attributable to this station.")
	} else if !allDigits(id) {
		add("general.id", SeverityError, fmt.Sprintf(
			"The radio ID %q is not a number. MMDVM-Host reads it with atoi(), so P25 transmits from source ID 0.", id))
	}
	return out
}

// nxdnProblems: the RAN.
//
// There is deliberately no NXDN ID check. NXDN has no ID of its own in this
// stack — Conf.cpp's [General] Id assigns m_id, m_p25Id and m_dmrId and nothing
// else, and NXDNGateway identifies by callsign — so requiring one would be an
// invented rule of exactly the kind gateway_requirements.go refuses to add.
func (m *Model) nxdnProblems() []ModeProblem {
	var out []ModeProblem

	// The RAN is a six-bit field (0-63). Conf.cpp:925-926 is a bare atoi with no
	// range check, so an out-of-range value is accepted and transmitted truncated,
	// and radios on the intended RAN never decode.
	if ran := strings.TrimSpace(m.NXDN.RAN); ran != "" {
		n, err := strconv.Atoi(ran)
		switch {
		case err != nil:
			out = append(out, ModeProblem{
				Mode: ModeNXDN, Field: "nxdn.ran", Severity: SeverityError,
				Message: fmt.Sprintf("The NXDN RAN %q is not a number. MMDVM-Host reads it with atoi(), so the node runs on RAN 0.", ran),
			})
		case n < 0 || n > 63:
			out = append(out, ModeProblem{
				Mode: ModeNXDN, Field: "nxdn.ran", Severity: SeverityError,
				Message: fmt.Sprintf("The NXDN RAN is %d. It is a six-bit field, so the valid range is 0 to 63.", n),
			})
		}
	}
	return out
}

// m17Problems: the CAN and the callsign M17 can actually encode.
func (m *Model) m17Problems() []ModeProblem {
	var out []ModeProblem
	add := func(field, sev, msg string) {
		out = append(out, ModeProblem{Mode: ModeM17, Field: field, Severity: sev, Message: msg})
	}

	// The CAN is a four-bit field split across two LSF bytes (M17LSF.cpp:141-148
	// writes one bit into lsf[13] and three into lsf[12]). Conf.cpp:940-941 is a
	// bare atoi, so an out-of-range CAN is accepted and transmitted wrapped.
	if can := strings.TrimSpace(m.M17.CAN); can != "" {
		n, err := strconv.Atoi(can)
		switch {
		case err != nil:
			add("m17.can", SeverityError, fmt.Sprintf(
				"The M17 CAN %q is not a number. MMDVM-Host reads it with atoi(), so the node runs on CAN 0.", can))
		case n < 0 || n > 15:
			add("m17.can", SeverityError, fmt.Sprintf(
				"The M17 CAN is %d. It is a four-bit field, so the valid range is 0 to 15.", n))
		}
	}

	// M17 encodes callsigns in base 40 over the alphabet " A-Z0-9-/."
	// (M17Utils.cpp:25). A character outside it is not rejected and does not
	// truncate the transmission — CM17Utils::encodeCallsign maps it to index 0,
	// which is a SPACE (M17Utils.cpp:55-57). So an unencodable callsign goes out as
	// a callsign with holes in it, which is both unattributable and the sort of
	// thing nothing on the node will ever tell you. Nine characters is the field
	// limit; anything longer is silently cut (M17Utils.cpp:46-48).
	cs := strings.ToUpper(strings.TrimSpace(m.General.Callsign))
	if cs != "" {
		if bad := m17Unencodable(cs); bad != "" {
			add("general.callsign", SeverityError, fmt.Sprintf(
				"The callsign contains %s, which M17 cannot encode. Unencodable characters become spaces on the air rather than an error — M17's alphabet is A-Z, 0-9, and - / . only.",
				bad))
		}
		if len(cs) > 9 {
			add("general.callsign", SeverityError, fmt.Sprintf(
				"The callsign %q is %d characters. M17's callsign field holds nine, and the rest is silently discarded.", cs, len(cs)))
		}
	}
	return out
}

// pocsagProblems: the DAPNET credential, and the paging channel.
func (m *Model) pocsagProblems() []ModeProblem {
	var out []ModeProblem

	// The AuthKey is the one requirement that also WITHHOLDS the daemon, and it is
	// read from UnmetGatewayRequirements rather than re-tested here so the two
	// cannot drift into disagreeing about what "set" means (upstream's guard
	// rejects the literal "TOPSECRET" placeholder as well as the empty string).
	// Reported as well as enforced because a withheld daemon is invisible: the unit
	// is simply not running, with nothing on the dashboard saying why.
	for _, r := range m.UnmetGatewayRequirements() {
		if r.Mode != ModePOCSAG {
			continue
		}
		for _, f := range r.Missing {
			out = append(out, ModeProblem{
				Mode: ModePOCSAG, Field: f, Severity: SeverityError,
				Message: "No DAPNET AuthKey is set. DAPNETGateway exits before it opens anything without one, so Waypoint does not start it at all — POCSAG is off the air until a key from the DAPNET portal is saved.",
			})
		}
	}

	// The paging channel. Unlike the modem frequencies this one HAS a rendered
	// default (render.go:630-633), so a blank field is not the fault — a set one
	// that lands in a different band from the node's transmitter is. MMDVM-Host
	// retunes the modem to m_pocsagFrequency to send a page (Conf.cpp:955-956,
	// which also shows [Info] TXFrequency seeding it), so a paging channel on 70cm
	// and a node on 2m means every page is transmitted somewhere the operator is
	// not listening and may not be licensed.
	pocsag, pOK := freqHz(m.POCSAG.Frequency)
	tx, txOK := freqHz(m.Modem.TXFreqHz)
	switch {
	case strings.TrimSpace(m.POCSAG.Frequency) != "" && !pOK:
		out = append(out, ModeProblem{
			Mode: ModePOCSAG, Field: "pocsag.frequency", Severity: SeverityError,
			Message: fmt.Sprintf("The POCSAG paging channel %q is not a whole number of hertz. MMDVM-Host reads it with atoi(), so pages transmit on 0 Hz.",
				strings.TrimSpace(m.POCSAG.Frequency)),
		})
	case pOK && txOK && !sameBand(pocsag, tx):
		out = append(out, ModeProblem{
			Mode: ModePOCSAG, Field: "pocsag.frequency", Severity: SeverityWarning,
			Message: fmt.Sprintf("The POCSAG paging channel is %s but the node transmits on %s. MMDVM-Host retunes the modem to send a page, so pages will go out on a different band from everything else this node does.",
				mhzLabel(pocsag), mhzLabel(tx)),
		})
	}
	return out
}

// fmProblems: the access tone, which is the whole of FM's access control.
func (m *Model) fmProblems() []ModeProblem {
	var out []ModeProblem
	add := func(field, sev, msg string) {
		out = append(out, ModeProblem{Mode: ModeFM, Field: field, Severity: sev, Message: msg})
	}

	// AccessMode selects how the repeater decides it is being called: 0 carrier
	// with COS, 1 CTCSS only without COS, 2 CTCSS only with COS, 3 CTCSS to start
	// then carrier (render.go's [FM] comment). Anything outside 0-3 is not a mode.
	mode := strings.TrimSpace(m.FM.AccessMode)
	accessN, accessOK := -1, false
	if mode != "" {
		n, err := strconv.Atoi(mode)
		switch {
		case err != nil:
			add("fm.access_mode", SeverityError, fmt.Sprintf(
				"The FM access mode %q is not a number. MMDVM-Host reads it with atoi(), so the repeater falls back to carrier access.", mode))
		case n < 0 || n > 3:
			add("fm.access_mode", SeverityError, fmt.Sprintf(
				"The FM access mode is %d. The valid range is 0 to 3 (carrier, CTCSS without COS, CTCSS with COS, CTCSS-to-start).", n))
		default:
			accessN, accessOK = n, true
		}
	}

	// The CTCSS tone is a decimal frequency in hertz (88.5, 127.3), not an integer,
	// so it is checked with ParseFloat rather than the integer path the RF
	// frequencies take.
	tone := strings.TrimSpace(m.FM.CTCSS)
	if tone != "" {
		if _, err := strconv.ParseFloat(tone, 64); err != nil {
			add("fm.ctcss", SeverityError, fmt.Sprintf(
				"The CTCSS tone %q is not a number. Tones are decimal hertz, e.g. 127.3.", tone))
		}
	}

	// Modes 1, 2 and 3 all gate access on the tone. Configured without one, the
	// repeater is not merely misconfigured — it cannot be accessed at all, and the
	// symptom is a repeater that never responds to anybody.
	//
	// This fires only on a BLANK tone, not on an unparseable one. A tone of "PL"
	// is already reported above, and adding "none is set" on top of it would be a
	// second finding on one field that also happens to be untrue.
	if accessOK && accessN >= 1 && tone == "" {
		add("fm.ctcss", SeverityError, fmt.Sprintf(
			"FM access mode %d requires a CTCSS tone and none is set. The repeater will not open for any transmission.", accessN))
	}
	return out
}

// anyNetworkEnabled reports whether any DMR network is switched on. A network row
// that exists but is disabled still renders a section (with Enabled=0) and still
// consumes a slot, so it is not something DMRGateway can log into.
func (m *Model) anyNetworkEnabled() bool {
	for _, n := range m.Networks {
		if n.Enabled {
			return true
		}
	}
	return false
}

// Plausibility bounds for a modem frequency, in hertz. The ADF7021 the MMDVM
// boards use tunes roughly 80-650 and 862-940 MHz, and the duplex full-size
// boards go higher; these bounds sit well outside all of it on purpose, because
// the mistake worth catching is a unit slip (megahertz typed into a hertz field),
// not an unusual but legal allocation.
const (
	minPlausibleHz = 100_000_000   // 100 MHz
	maxPlausibleHz = 1_400_000_000 // 1.4 GHz
)

// freqHz parses a frequency field the way MMDVM-Host's atoi() would succeed:
// a non-empty run of digits that is not zero. It reports ok=false for everything
// that reaches the daemon as 0 Hz, which is the condition that matters.
func freqHz(v string) (uint64, bool) {
	s := strings.TrimSpace(v)
	if s == "" || !allDigits(s) {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return n, true
}

// sameBand reports whether two frequencies fall in the same amateur band, to the
// coarse resolution this needs: 2 m, 1.25 m, 70 cm and 23 cm are far enough apart
// that "same 100 MHz decade" separates them without encoding a band plan.
func sameBand(a, b uint64) bool {
	return a/100_000_000 == b/100_000_000
}

// mhzLabel renders a frequency the way the message needs to read. A plausible
// frequency reads in megahertz, because that is how an operator says it. A
// nonsense one reads in hertz — the whole point of that finding is that the
// number in the field is not the number the operator meant, and "0.000438 MHz"
// obscures the 438 they actually typed.
func mhzLabel(hz uint64) string {
	if hz < 1_000_000 {
		return strconv.FormatUint(hz, 10) + " Hz"
	}
	return strconv.FormatFloat(float64(hz)/1e6, 'f', -1, 64) + " MHz"
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// m17Chars is CM17Utils' base-40 alphabet, verbatim from M17Utils.cpp:25. Index 0
// is the space that any unrecognized character silently becomes.
const m17Chars = " ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-/."

// m17Unencodable returns a human-readable list of the characters in an
// already-upper-cased callsign that M17 would turn into spaces, or "" if the
// callsign encodes cleanly.
func m17Unencodable(cs string) string {
	var bad []string
	seen := map[rune]bool{}
	for _, r := range cs {
		if strings.ContainsRune(m17Chars, r) || seen[r] {
			continue
		}
		seen[r] = true
		bad = append(bad, strconv.QuoteRune(r))
	}
	return strings.Join(bad, ", ")
}
