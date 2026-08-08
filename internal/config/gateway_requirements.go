package config

import "strings"

// Gateway startup requirements.
//
// Most gateway daemons tolerate a half-configured node: with their mode off they
// idle on a loopback socket doing nothing, and a missing optional setting just
// means a feature is unused. A few do not — they read one specific value at
// startup, find it empty, and exit non-zero before opening anything. Combined
// with `Restart=on-failure` in the unit, that is a restart loop which never
// converges, and the reason for it is invisible from the dashboard: the rendered
// INIs carry DisplayLevel=0, so the journal shows the exit and not the cause.
//
// A daemon that cannot possibly start should not be started. This is the registry
// of what each gateway must have before apply is willing to run it. A gateway
// whose requirements are unmet contributes no render target — so apply writes no
// config and restarts no unit for it — and any instance still running is stopped
// and disabled for boot, the same treatment a displaced gateway gets.
//
// Two GATEWAYS are registered here:
//
//   - DAPNETGateway.cpp:283 — an empty (or literal "TOPSECRET") AuthKey logs
//     "AuthKey not set or invalid" and returns 1, BEFORE constructing the DAPNET
//     network object. So POCSAG enabled with no AuthKey is a guaranteed crash
//     loop.
//   - YSFGateway — CWiresX::setInfo asserts on WiresX.cpp:103 and :104, so a node
//     with no RF frequency aborts (SIGABRT) before it links anything. Found on the
//     bench with a restart counter at 2,127 (#215, originally #145).
//
// This survey used to claim DAPNETGateway was the end of it — that "the rest have
// no equivalent: their early `return 1` paths are all runtime conditions, not
// missing configuration". The bench contradicted that twice, and the correction is
// worth keeping because the survey's METHOD is what failed, not just its
// conclusion: it looked for `return 1` and did not look for an assert. Read the
// framing above as "exits OR ABORTS before opening anything".
//
// It then failed a second time, more quietly, and that correction matters more.
// Having found the YSFGateway abort, this file declined to register it, arguing:
//
//	"withholding MMDVM-Host takes every mode off the air at once, so it is only
//	done for values that are unarguable [...] a frequency is a value an operator is
//	mid-way through setting on a node being configured; refusing to start the
//	daemon during that is worse than reporting it."
//
// That was wrong twice over. It conflated refusing a SAVE with withholding a
// DAEMON — nothing here touches the store, the operator's half-typed frequency is
// saved either way, and the mode stays enabled. And it weighed the cost of
// withholding against a daemon that RUNS, when the alternative on the bench was a
// daemon aborting every three seconds forever. There is no configuration in which
// starting YSFGateway without a frequency is better than not starting it: the
// process cannot reach the point of carrying traffic. What misled it was framing
// the choice as "block or report" when the real choice was "block or crash-loop".
//
// MMDVM-Host's frequencies (#216) are registered for the same reason and are not a
// harder case than its port: 0 Hz earns a NAK to SET_FREQ and the host exits 1,
// with DisplayLevel=0 hiding the cause. Withholding it does take every mode off the
// air — but every mode is already off the air, because the host is dead. What
// changes is that the dashboard now says which field to fill in.
//
// The remaining daemons stay absent rather than being given invented
// requirements, which would refuse to start daemons that work fine today. That
// caution is exactly why the wrong claim mattered: it read as a survey that had
// looked, when it had looked for one thing. So the survey is no longer a reading
// at all — test/tier2/modes_test.go's TestTier2_UnsetFrequencyDaemonSurvey runs
// every daemon whose config carries frequencies, plus the ones whose config does
// not as controls, and records what each one actually does. DGIdGateway,
// M17Gateway, P25Gateway, NXDNGateway and dstargateway all start and bind their
// loopback with no frequency set, so none of them is registered. MMDVM-Host is the
// one entry that test cannot cover — build.sh does not build it — and its evidence
// is the #216 bench journal.
//
// What this cannot cover: a *wrong* AuthKey. DAPNETGateway also exits on "Cannot
// login to the DAPNET network", and only DAPNET can say whether a key is valid.
// The gate is for the case that is knowable locally.
//
// MMDVM-Host is registered here too, under the pseudo-mode ModeModem — it is not
// a mode's gateway but the modem host every mode sits on. It contributes the only
// entries here that are NOT crash guards, so the distinction is worth stating:
//
//   - modem.port is a crash guard, the same kind as DAPNET's AuthKey.
//     MMDVM-Host's hard failure is createModem() returning false (MMDVM-Host.cpp
//     run() -> return 1). Whether the hardware answers is a *runtime* condition
//     and is deliberately not gated; that there is no port to open at all is
//     locally knowable, and createModem cannot succeed without one.
//
//   - modem.rx_freq_hz and modem.tx_freq_hz are crash guards too (#216), by a
//     third route: the modem enumerates, the host sends SET_FREQ for 0 Hz, the
//     modem NAKs it and the host returns 1. Locally knowable, and the only
//     frequency fault that is — see frequencyStops for why a merely IMPLAUSIBLE
//     frequency is reported rather than blocked.
//
//   - general.callsign and general.id are IDENTITY requirements. MMDVM-Host
//     starts happily without them — it would simply transmit unidentified, which
//     is not a thing a station may do. This is the one place the registry means
//     "must not run" rather than "cannot run", and it is stated rather than
//     smuggled in as a pretend crash guard.
//
// Nothing else was added. DMRGateway looked like a candidate — it logs into a
// master under the CCS7 ID — but it has no startup guard on it, so requiring one
// would refuse a daemon that runs fine. A DMR node with no ID is already held back
// by the identity gate above, since DMR cannot reach the air without MMDVM-Host.

// GatewayRequirement reports one gateway that will not start because a value it
// needs is not set. Missing names the operator-facing fields, so the message a UI
// builds from it points at the control the operator has to fill in.
type GatewayRequirement struct {
	// Mode is the mode whose gateway is blocked, matching the Modes section's key.
	Mode Mode `json:"mode"`
	// Unit is the systemd unit that is not started.
	Unit string `json:"unit"`
	// Missing lists the store paths of the unset values, e.g. "pocsag.auth_key".
	Missing []string `json:"missing"`
}

// UnmetGatewayRequirements returns one entry per enabled mode whose gateway
// cannot start for want of a required value. A mode that is switched off is not
// reported: its gateway is not expected to run, so nothing is being withheld.
//
// Callers: RenderTargets (skip the target) and the View (tell the operator why).
// Stopping and boot-disabling a withheld unit needs no caller here — dropping the
// target is enough, because BootDisableUnits is the complement of the rendered set
// over managedUnits, and the apply path stops everything in it. BlockedGatewayUnits
// exists for tests and for anyone wanting the unit names directly; this comment
// used to name it as the mechanism, which overstated it.
func (m *Model) UnmetGatewayRequirements() []GatewayRequirement {
	var out []GatewayRequirement
	// MMDVM-Host: only asked when some mode wants it on the air (AnyModeEnabled),
	// for the same reason a mode that is off is not reported — nothing is being
	// withheld from a node that has asked for nothing.
	if m.AnyModeEnabled() {
		var missing []string
		if strings.TrimSpace(m.Modem.Port) == "" {
			missing = append(missing, "modem.port")
		}
		missing = append(missing, m.stoppingFrequencies()...)
		if strings.TrimSpace(m.General.Callsign) == "" {
			missing = append(missing, "general.callsign")
		}
		if strings.TrimSpace(m.General.ID) == "" {
			missing = append(missing, "general.id")
		}
		if len(missing) > 0 {
			out = append(out, GatewayRequirement{Mode: ModeModem, Unit: unitMMDVM, Missing: missing})
		}
	}
	// YSF / YSFGateway: the RF frequencies, which reach it through [Info] and then
	// CWiresX::setInfo's two asserts. Wires-X is built unconditionally
	// (YSFGateway.cpp:293, before startupLinking) and no config key gates the
	// constructor, so there is no way to run this daemon without satisfying them.
	//
	// DG-ID is exempt, and that exemption is measured rather than assumed:
	// DGIdGateway has no Wires-X, and the survey starts it with no frequency at all
	// and watches it bind :4200. So the block follows the SLOT — swapping the YSF
	// unit to DGIdGateway lifts it, exactly as it swaps the render target.
	if m.Modes.YSF && !m.YSFGW.EnableDGId {
		if missing := m.stoppingFrequencies(); len(missing) > 0 {
			out = append(out, GatewayRequirement{Mode: ModeYSF, Unit: unitYSFGateway, Missing: missing})
		}
	}
	// POCSAG / DAPNETGateway: the DAPNET AuthKey. The literal "TOPSECRET" is
	// upstream's own placeholder from the stock ini, and its guard rejects that
	// string as well as the empty one — so a node that pasted the sample value is
	// blocked here too rather than left to crash-loop.
	if m.Modes.POCSAG {
		key := strings.TrimSpace(m.POCSAG.AuthKey)
		if key == "" || key == "TOPSECRET" {
			out = append(out, GatewayRequirement{
				Mode:    ModePOCSAG,
				Unit:    unitDAPNETGateway,
				Missing: []string{"pocsag.auth_key"},
			})
		}
	}
	return out
}

// stoppingFrequencies names the RF frequency fields whose value would stop a
// daemon that reads them, in the store-path order a UI lists them.
func (m *Model) stoppingFrequencies() []string {
	var missing []string
	for _, f := range []struct{ field, val string }{
		{"modem.rx_freq_hz", m.Modem.RXFreqHz},
		{"modem.tx_freq_hz", m.Modem.TXFreqHz},
	} {
		if frequencyStops(f.val) {
			missing = append(missing, f.field)
		}
	}
	return missing
}

// frequencyStops reports whether a frequency value is one the daemons that read it
// cannot start on. It is deliberately NARROWER than mode_readiness.go's freqHz,
// and the gap between them is the difference between the two mechanisms.
//
// Both YSFGateway and MMDVM-Host read these through C's atoi, so what decides the
// outcome is the number atoi produces, not whether the string is a sensible
// frequency. Measured against the pinned YSFGateway (test/tier2 probes behind
// TestTier2_UnsetFrequencyDaemonSurvey):
//
//	""             -> 0            -> aborts
//	"0", "00"      -> 0            -> aborts
//	"abc", "0x10"  -> 0            -> aborts
//	"438.8"        -> 438          -> STARTS (a nonsense 438 Hz node, but it runs)
//	"438800000abc" -> 438800000    -> starts
//	"-1"           -> -1, unsigned -> starts
//
// So this blocks on atoi == 0 and nothing else. "438.8" is a real fault and
// mode_readiness reports it as one, but a daemon that starts must not be withheld
// — that is rule 3 in CLAUDE.md, and blocking it would take a running (if useless)
// YSF gateway off a node whose operator typed megahertz into a hertz field.
//
// Negative values are the uncomfortable corner: atoi yields a negative int, the
// daemon stores it in an unsigned, and the assert passes on a garbage frequency.
// It is not blocked because it does not stop the daemon. mode_readiness has it.
func frequencyStops(v string) bool { return atoiLeadingInt(v) == 0 }

// atoiLeadingInt reproduces what C's atoi returns for a string: leading whitespace
// is skipped, an optional sign is consumed, and digits are taken until the first
// non-digit. No digits means 0. Reproduced rather than approximated with
// strconv.Atoi because the difference is the whole point — strconv rejects
// "438800000abc" and " 438800000", and treating either as unset would withhold a
// daemon that starts.
func atoiLeadingInt(v string) int64 {
	s := strings.TrimLeft(v, " \t\n\v\f\r")
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	var n int64
	for i := 0; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		// Saturate rather than overflow: any value this large is non-zero, which is
		// the only thing the caller asks.
		if n > (1<<62)/10 {
			return 1
		}
		n = n*10 + int64(s[i]-'0')
	}
	if neg {
		return -n
	}
	return n
}

// ModeEnabled reports whether the operator has switched this mode on. It is the
// single place the Modes section is read by key, so a caller asking "should this
// mode's daemon run?" cannot drift from the section's field list.
func (m *Model) ModeEnabled(mode Mode) bool {
	switch mode {
	case ModeDStar:
		return m.Modes.DStar
	case ModeDMR:
		return m.Modes.DMR
	case ModeYSF:
		return m.Modes.YSF
	case ModeP25:
		return m.Modes.P25
	case ModeNXDN:
		return m.Modes.NXDN
	case ModeM17:
		return m.Modes.M17
	case ModePOCSAG:
		return m.Modes.POCSAG
	case ModeFM:
		return m.Modes.FM
	}
	return false
}

// AnyModeEnabled reports whether the node is meant to be on the air at all. It is
// MMDVM-Host's run condition: the modem host serves every mode, so it is wanted
// when any one of them is, and wanted by none when the node carries no mode.
func (m *Model) AnyModeEnabled() bool {
	for _, mode := range rfModes {
		if m.ModeEnabled(mode) {
			return true
		}
	}
	return false
}

// rfModes is every mode that puts MMDVM-Host on the air. FM and POCSAG are in it
// because both are carried by the modem host itself (POCSAG's DAPNETGateway is
// only the network side of it), so either one alone still wants a modem.
var rfModes = []Mode{ModeDStar, ModeDMR, ModeYSF, ModeP25, ModeNXDN, ModeM17, ModePOCSAG, ModeFM}

// GatewayRuns is the single predicate behind "should this mode's stock gateway be
// rendered, started, and enabled for boot?" — the mode is on, no bus has displaced
// it, and nothing it needs is missing. RenderTargets and the boot picture both
// read it, which is what keeps the running set and the boot set from disagreeing.
func (m *Model) GatewayRuns(mode Mode) bool {
	return m.ModeEnabled(mode) && !m.modeDisplacesGateway(mode) && !m.gatewayBlocked(mode)
}

// ModemHostRuns is the same question for MMDVM-Host, which is not a mode's
// gateway: it runs when any mode wants the air and its own requirements are met.
func (m *Model) ModemHostRuns() bool {
	return m.AnyModeEnabled() && !m.gatewayBlocked(ModeModem)
}

// gatewayBlocked reports whether this mode's gateway is held back by an unmet
// requirement. RenderTargets consults it the way it consults modeDisplacesGateway.
func (m *Model) gatewayBlocked(mode Mode) bool {
	for _, r := range m.UnmetGatewayRequirements() {
		if r.Mode == mode {
			return true
		}
	}
	return false
}

// BlockedGatewayUnits names the gateway units NOT rendered because a required
// value is unset. Apply stops these and disables them for boot — without that, a
// unit already crash-looping from before the value was cleared would keep
// looping (nothing in the target set stops it), and a reboot would start it again
// only to fail.
func (m *Model) BlockedGatewayUnits() []string {
	reqs := m.UnmetGatewayRequirements()
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Unit)
	}
	return out
}
