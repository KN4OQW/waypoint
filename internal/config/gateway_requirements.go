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
// Surveyed against the pinned upstream sources, exactly one gateway is REGISTERED
// here today:
//
//   - DAPNETGateway.cpp:283 — an empty (or literal "TOPSECRET") AuthKey logs
//     "AuthKey not set or invalid" and returns 1, BEFORE constructing the DAPNET
//     network object. So POCSAG enabled with no AuthKey is a guaranteed crash
//     loop.
//
// It is NOT the only daemon that fails this way, and this survey used to say it
// was — that "the rest have no equivalent: their early `return 1` paths are all
// runtime conditions, not missing configuration". The bench contradicted it:
//
//   - #215 — YSFGateway with no TX frequency aborts on WiresX.cpp:103's
//     assert(txFrequency > 0U). That is missing configuration, not a runtime
//     condition; it was found looping with a restart counter over 2,000. The
//     survey missed it by looking for `return 1` and not for an assert, so read
//     the framing above as "exits OR ABORTS before opening anything".
//   - #216 — MMDVM-Host itself, by a third route: 0 Hz earns a NAK to SET_FREQ
//     and the host exits 1, with DisplayLevel=0 hiding the cause.
//
// Both are REPORTED rather than registered today: mode_readiness.go's YSF finding
// names the abort, and test/tier2/modes_test.go executes it against the pinned
// binary, so the claim rests on the daemon's behaviour rather than on a reading of
// its source. Registering them — withholding the target, stopping the unit,
// boot-disabling it, the treatment POCSAG gets — is what #215 and #216 ask for and
// is deliberately not done here: it also has to cover the host, which this
// registry does not model, and withholding MMDVM-Host takes every mode off the air
// at once rather than one gateway.
//
// The remaining daemons stay absent rather than being given invented
// requirements, which would refuse to start daemons that work fine today. That
// caution is exactly why the wrong claim mattered: it read as a survey that had
// looked, when it had looked for one thing.
//
// What this cannot cover: a *wrong* AuthKey. DAPNETGateway also exits on "Cannot
// login to the DAPNET network", and only DAPNET can say whether a key is valid.
// The gate is for the case that is knowable locally.

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
