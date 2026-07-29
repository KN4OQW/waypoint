package config

import (
	"slices"
	"strings"
	"testing"
)

// A gateway daemon that reads a required value at startup, finds it unset, and
// exits is a restart loop, not a running gateway. These tests pin the rule that
// apply withholds such a daemon rather than starting it: no render target, no
// restart, stopped if it was already running, and disabled for boot.

// The POCSAG gate: enabled + no AuthKey is blocked, enabled + a key is not, and a
// disabled mode is never reported (nothing is being withheld from a mode the
// operator turned off).
func TestUnmetGatewayRequirementsPOCSAG(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		authKey string
		blocked bool
	}{
		{"enabled, no key", true, "", true},
		{"enabled, whitespace key", true, "   ", true},
		// Upstream's own guard rejects the literal placeholder from its stock ini as
		// well as the empty string, so a node that pasted the sample is blocked here
		// instead of being left to crash-loop.
		{"enabled, upstream placeholder", true, "TOPSECRET", true},
		{"enabled, real key", true, "abc123", false},
		{"disabled, no key", false, "", false},
		{"disabled, real key", false, "abc123", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := fixture()
			m.Modes.POCSAG = tc.enabled
			m.POCSAG.AuthKey = tc.authKey

			reqs := m.UnmetGatewayRequirements()
			if got := len(reqs) > 0; got != tc.blocked {
				t.Fatalf("UnmetGatewayRequirements() = %+v, want blocked=%v", reqs, tc.blocked)
			}
			if !tc.blocked {
				return
			}
			r := reqs[0]
			if r.Mode != ModePOCSAG {
				t.Errorf("Mode = %q, want %q", r.Mode, ModePOCSAG)
			}
			if r.Unit != unitDAPNETGateway {
				t.Errorf("Unit = %q, want %q", r.Unit, unitDAPNETGateway)
			}
			if !slices.Contains(r.Missing, "pocsag.auth_key") {
				t.Errorf("Missing = %v, want it to name pocsag.auth_key", r.Missing)
			}
		})
	}
}

// Only DAPNETGateway has such a guard upstream. Every other gateway's early exit
// is a runtime condition (unresolvable host, bound port), not missing config, so
// none of them may be blocked — inventing a requirement here would refuse to start
// daemons that work today.
func TestNoOtherGatewayIsBlocked(t *testing.T) {
	m := fixture()
	m.Modes = Modes{DStar: true, DMR: true, YSF: true, P25: true, NXDN: true, M17: true, POCSAG: true, FM: true}
	// Wipe every gateway's optional settings: startup reflectors, suffixes, the
	// ircDDB login, passwords. None of these gate a daemon.
	m.YSFGW.Startup, m.YSFGW.Suffix = "", ""
	m.P25GW.Static, m.NXDNGW.Static, m.M17GW.Startup = "", "", ""
	m.DStarGW.Reflector, m.DStarGW.IRCDDBUsername, m.DStarGW.IRCDDBPassword = "", "", ""
	m.POCSAG.AuthKey = "a-real-key" // the one that IS gated, satisfied

	if reqs := m.UnmetGatewayRequirements(); len(reqs) != 0 {
		t.Fatalf("a gateway other than DAPNET was blocked: %+v", reqs)
	}
}

// A blocked gateway contributes no render target, so apply writes no config file
// and restarts no unit for it. Satisfying the requirement brings both back.
func TestBlockedGatewayHasNoRenderTarget(t *testing.T) {
	paths := Paths{
		MMDVM: "/etc/MMDVM-Host.ini", DMRGateway: "/etc/DMRGateway.ini",
		YSFGateway: "/etc/YSFGateway.ini", DGIdGateway: "/etc/DGIdGateway.ini",
		P25Gateway: "/etc/P25Gateway.ini", NXDNGateway: "/etc/NXDNGateway.ini",
		DStarGateway: "/etc/dstargateway.cfg", M17Gateway: "/etc/M17Gateway.ini",
		DAPNETGateway: "/etc/DAPNETGateway.ini",
	}
	hasDAPNET := func(m *Model) bool {
		for _, tgt := range m.RenderTargets(paths) {
			if tgt.Unit == unitDAPNETGateway {
				return true
			}
		}
		return false
	}

	m := fixture()
	m.Modes.POCSAG, m.POCSAG.AuthKey = true, ""
	if hasDAPNET(m) {
		t.Error("POCSAG enabled with no AuthKey still rendered a DAPNETGateway target; the daemon would crash-loop")
	}
	// Every other target must survive — the gate is narrow, not a general opt-out.
	if n := len(m.RenderTargets(paths)); n < 7 {
		t.Errorf("blocking DAPNET dropped other targets too: only %d left", n)
	}

	m.POCSAG.AuthKey = "abc123"
	if !hasDAPNET(m) {
		t.Error("POCSAG enabled with an AuthKey did not render a DAPNETGateway target")
	}

	// And the mode enable is still the outer gate, key or not.
	m.Modes.POCSAG = false
	if hasDAPNET(m) {
		t.Error("POCSAG disabled still rendered a DAPNETGateway target")
	}
}

// The apply path must stop and boot-disable a blocked unit. Without this, a daemon
// already looping from an earlier apply that had the key set would keep looping
// (nothing in the target set stops it), and a reboot would start it again only to
// have it exit.
func TestBlockedGatewayIsStoppedAndBootDisabled(t *testing.T) {
	m := fixture()
	m.Modes.POCSAG, m.POCSAG.AuthKey = true, ""

	if got := m.BlockedGatewayUnits(); !slices.Contains(got, unitDAPNETGateway) {
		t.Errorf("BlockedGatewayUnits() = %v, want it to name %q", got, unitDAPNETGateway)
	}
	if got := m.BootDisableUnits(); !slices.Contains(got, unitDAPNETGateway) {
		t.Errorf("BootDisableUnits() = %v, want it to name %q", got, unitDAPNETGateway)
	}

	m.POCSAG.AuthKey = "abc123"
	if got := m.BlockedGatewayUnits(); len(got) != 0 {
		t.Errorf("BlockedGatewayUnits() = %v with the key set, want empty", got)
	}
	if got := m.BootDisableUnits(); slices.Contains(got, unitDAPNETGateway) {
		t.Errorf("BootDisableUnits() = %v still disables DAPNET with the key set", got)
	}
	// Boot-disabling must not have displaced the existing entries it appends to.
	m.Buses = []Bus{{ID: "b1", Name: "b1", Enabled: false}}
	if got := m.BootDisableUnits(); len(got) == 0 {
		t.Error("BootDisableUnits() dropped the disabled-bus units")
	}
}

// The view tells the operator which control to fill in — otherwise the only
// evidence is a daemon that is not running for no visible reason. It carries field
// NAMES only, so reporting that a secret is unset never exposes a secret.
func TestViewSurfacesBlockedGateways(t *testing.T) {
	m := fixture()
	m.Modes.POCSAG, m.POCSAG.AuthKey = true, ""
	v := m.View(Sources{})
	if len(v.BlockedGateways) != 1 || v.BlockedGateways[0].Mode != ModePOCSAG {
		t.Fatalf("View().BlockedGateways = %+v, want one POCSAG entry", v.BlockedGateways)
	}
	if v.POCSAG.HasAuthKey {
		t.Error("has_auth_key true with a blank key")
	}
	for _, f := range v.BlockedGateways[0].Missing {
		if strings.Contains(f, "TOPSECRET") || f == "" {
			t.Errorf("Missing carried something other than a field name: %q", f)
		}
	}

	m.POCSAG.AuthKey = "abc123"
	if got := m.View(Sources{}).BlockedGateways; len(got) != 0 {
		t.Errorf("View().BlockedGateways = %+v with the key set, want empty", got)
	}
}
