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
// One GATEWAY is registered here, and it is the only one:
//
//   - DAPNETGateway.cpp:283 — an empty (or literal "TOPSECRET") AuthKey logs
//     "AuthKey not set or invalid" and returns 1, BEFORE constructing the DAPNET
//     network object. So POCSAG enabled with no AuthKey is a guaranteed crash
//     loop.
//
// This survey used to claim that was the end of it — that "the rest have no
// equivalent: their early `return 1` paths are all runtime conditions, not missing
// configuration". The bench contradicted that twice, and the correction is worth
// keeping because the survey's method is what failed, not just its conclusion: it
// looked for `return 1` and did not look for an assert.
//
//   - #215 — YSFGateway with no TX frequency aborts on WiresX.cpp:103's
//     assert(txFrequency > 0U). Missing configuration, not a runtime condition;
//     found looping with a restart counter over 2,000. Read the framing above as
//     "exits OR ABORTS before opening anything".
//   - #216 — MMDVM-Host, by a third route again: 0 Hz earns a NAK to SET_FREQ and
//     the host exits 1, with DisplayLevel=0 hiding the cause.
//
// The two are treated DIFFERENTLY, and the split is the point of this paragraph.
// MMDVM-Host is registered below (modem.port, and the identity values). YSFGateway
// is not: its abort is only REPORTED, by mode_readiness.go's YSF finding, and
// executed against the pinned binary by test/tier2/modes_test.go so the claim
// rests on the daemon's behaviour rather than a reading of its source.
//
// Why the asymmetry: withholding MMDVM-Host takes every mode off the air at once,
// so it is only done for values that are unarguable — no port to open, or no
// identity to transmit under. YSFGateway's frequency requirement is one daemon's,
// and a frequency is a value an operator is mid-way through setting on a node
// being configured; refusing to start the daemon during that is worse than
// reporting it. Registering it is still open (#215) and is a decision, not an
// oversight.
//
// The remaining daemons stay absent rather than being given invented
// requirements, which would refuse to start daemons that work fine today. That
// caution is exactly why the wrong claim mattered: it read as a survey that had
// looked, when it had looked for one thing.
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
// Callers: RenderTargets (skip the target), BlockedGatewayUnits (stop and
// boot-disable it), and the View (tell the operator why).
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
