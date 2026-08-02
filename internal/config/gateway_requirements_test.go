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

// Only DAPNETGateway and MMDVM-Host have a locally-knowable guard.
// Every other gateway's early exit is a runtime condition (unresolvable host,
// bound port), not missing config, so none of them may be blocked — inventing a
// requirement here would refuse to start daemons that work today.
func TestNoOtherGatewayIsBlocked(t *testing.T) {
	m := fixture()
	m.Modes = Modes{DStar: true, DMR: true, YSF: true, P25: true, NXDN: true, M17: true, POCSAG: true, FM: true}
	// Wipe every gateway's optional settings: startup reflectors, suffixes, the
	// ircDDB login, passwords. None of these gate a daemon.
	m.YSFGW.Startup, m.YSFGW.Suffix = "", ""
	m.P25GW.Static, m.NXDNGW.Static, m.M17GW.Startup = "", "", ""
	m.DStarGW.Reflector, m.DStarGW.IRCDDBUsername, m.DStarGW.IRCDDBPassword = "", "", ""
	m.POCSAG.AuthKey = "a-real-key" // the ones that ARE gated, satisfied
	m.Modem.Port, m.General.Callsign, m.General.ID = "/dev/ttyAMA0", "KN4OQW", "3180202"

	if reqs := m.UnmetGatewayRequirements(); len(reqs) != 0 {
		t.Fatalf("a gateway with no locally-knowable requirement was blocked: %+v", reqs)
	}
}

// MMDVM-Host's own requirements. createModem() failing is a RUNTIME condition and
// deliberately not gated; what is knowable locally is that it cannot open a port
// that was never set, and that the station identity every mode transmits under
// must exist.
func TestModemHostRequirements(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mutate      func(*Model)
		wantMissing string
	}{
		{"no modem port", func(m *Model) { m.Modem.Port = "" }, "modem.port"},
		{"no callsign", func(m *Model) { m.General.Callsign = "" }, "general.callsign"},
		{"no id", func(m *Model) { m.General.ID = "" }, "general.id"},
		{"whitespace port", func(m *Model) { m.Modem.Port = "  " }, "modem.port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := fixture()
			tc.mutate(m)
			if m.ModemHostRuns() {
				t.Fatal("MMDVM-Host must not run when a value it needs is unset")
			}
			var found bool
			for _, r := range m.UnmetGatewayRequirements() {
				if r.Mode == ModeModem && slices.Contains(r.Missing, tc.wantMissing) {
					found = true
					if r.Unit != unitMMDVM {
						t.Errorf("Unit = %q, want %q", r.Unit, unitMMDVM)
					}
				}
			}
			if !found {
				t.Errorf("no ModeModem requirement naming %q: %+v", tc.wantMissing, m.UnmetGatewayRequirements())
			}
		})
	}
}

// A node with every mode off is not misconfigured — it has asked for nothing, so
// nothing is being withheld and the operator must not be shown a fault.
func TestModemHostNotReportedWhenNoModeWantsTheAir(t *testing.T) {
	m := fixture()
	m.Modes = Modes{}
	m.Modem.Port, m.General.Callsign, m.General.ID = "", "", ""

	if m.ModemHostRuns() {
		t.Error("MMDVM-Host must not run when no mode is enabled")
	}
	for _, r := range m.UnmetGatewayRequirements() {
		if r.Mode == ModeModem {
			t.Errorf("an all-modes-off node must report no modem requirement, got %+v", r)
		}
	}
}

// testPaths is a full Paths so every render target has somewhere to go; a blank
// path would drop a target for the wrong reason and mask what these tests assert.
func testPaths() Paths {
	return Paths{
		MMDVM: "/etc/MMDVM-Host.ini", DMRGateway: "/etc/DMRGateway.ini",
		YSFGateway: "/etc/YSFGateway.ini", DGIdGateway: "/etc/DGIdGateway.ini",
		P25Gateway: "/etc/P25Gateway.ini", NXDNGateway: "/etc/NXDNGateway.ini",
		DStarGateway: "/etc/dstargateway.cfg", M17Gateway: "/etc/M17Gateway.ini",
		DAPNETGateway: "/etc/DAPNETGateway.ini",
	}
}

// restartUnitsOf is the unit set apply would (re)start — the rendered targets.
func restartUnitsOf(m *Model, paths Paths) []string {
	var out []string
	for _, t := range m.RenderTargets(paths) {
		if t.Unit != "" {
			out = append(out, t.Unit)
		}
	}
	return out
}

// A blocked gateway contributes no render target, so apply writes no config file
// and restarts no unit for it. Satisfying the requirement brings both back.
func TestBlockedGatewayHasNoRenderTarget(t *testing.T) {
	paths := testPaths()
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
	// Every target for an ENABLED mode must survive — the gate is narrow, not a
	// general opt-out. The fixture enables DMR, YSF, NXDN and POCSAG, so blocking
	// DAPNET must still leave the modem host and the other three gateways.
	for _, want := range []string{unitMMDVM, unitDMRGateway, unitYSFGateway, unitNXDNGateway} {
		if !slices.Contains(restartUnitsOf(m, paths), want) {
			t.Errorf("blocking DAPNET dropped %q too: %v", want, restartUnitsOf(m, paths))
		}
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
	paths := testPaths()
	if got := m.BootDisableUnits(paths); !slices.Contains(got, unitDAPNETGateway) {
		t.Errorf("BootDisableUnits() = %v, want it to name %q", got, unitDAPNETGateway)
	}

	m.POCSAG.AuthKey = "abc123"
	if got := m.BlockedGatewayUnits(); len(got) != 0 {
		t.Errorf("BlockedGatewayUnits() = %v with the key set, want empty", got)
	}
	if got := m.BootDisableUnits(paths); slices.Contains(got, unitDAPNETGateway) {
		t.Errorf("BootDisableUnits() = %v still disables DAPNET with the key set", got)
	}
	// Boot-disabling must not have displaced the existing entries it appends to.
	m.Buses = []Bus{{ID: "b1", Name: "b1", Enabled: false}}
	if got := m.BootDisableUnits(paths); len(got) == 0 {
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

// --- the boot picture is derived, not listed (the waypoint-mmdvm regression) ---

// TestBootPictureMatchesRenderSet is the invariant the whole mechanism exists for:
// what apply starts and what systemd starts at boot are the same set. Before this,
// they were two hand-maintained lists and they disagreed — waypoint-mmdvm.service
// was in neither, so the modem host died at every reboot.
func TestBootPictureMatchesRenderSet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Model)
	}{
		{"fixture as-is", func(*Model) {}},
		{"every mode on", func(m *Model) {
			m.Modes = Modes{DStar: true, DMR: true, YSF: true, P25: true, NXDN: true, M17: true, POCSAG: true, FM: true}
			m.POCSAG.AuthKey = "k"
		}},
		{"every mode off", func(m *Model) { m.Modes = Modes{} }},
		{"only FM", func(m *Model) { m.Modes = Modes{FM: true} }},
		{"POCSAG blocked", func(m *Model) { m.Modes = Modes{POCSAG: true}; m.POCSAG.AuthKey = "" }},
		{"DG-ID swap", func(m *Model) { m.Modes = Modes{YSF: true}; m.YSFGW.EnableDGId = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := fixture()
			tc.mutate(m)
			paths := testPaths()

			rendered := restartUnitsOf(m, paths)
			boot := m.BootEnableUnits(paths)
			if !slices.Equal(rendered, boot) {
				t.Fatalf("boot set diverged from the render set:\n  render = %v\n  boot   = %v", rendered, boot)
			}
			// And the disable set must be the exact complement: every managed unit is
			// accounted for exactly once, so none keeps a stale boot state.
			disabled := m.BootDisableUnits(paths)
			for _, u := range ManagedUnits() {
				inBoot, inDisabled := slices.Contains(boot, u), slices.Contains(disabled, u)
				if inBoot == inDisabled {
					t.Errorf("%q is %s — every managed unit must be enabled xor disabled",
						u, map[bool]string{true: "in BOTH sets", false: "in NEITHER set"}[inBoot])
				}
			}
		})
	}
}

// TestModemHostIsBootEnabled is the #221-adjacent regression proper. The modem
// host is the one unit the stack updater's health gate always requires, and it was
// never boot-enabled — so a node that rebooted failed every subsequent stack
// update until someone applied config by hand.
func TestModemHostIsBootEnabled(t *testing.T) {
	m := fixture() // DMR/YSF/NXDN/POCSAG on, modem port and identity set
	paths := testPaths()

	if !m.ModemHostRuns() {
		t.Fatal("a configured node with modes enabled must run MMDVM-Host")
	}
	if got := m.BootEnableUnits(paths); !slices.Contains(got, unitMMDVM) {
		t.Fatalf("waypoint-mmdvm.service must be enabled for boot; got %v", got)
	}
	if got := m.BootDisableUnits(paths); slices.Contains(got, unitMMDVM) {
		t.Errorf("waypoint-mmdvm.service must not be boot-disabled on a configured node; got %v", got)
	}
}

// The inverse half of the same defect: YSF and NXDN were boot-enabled
// unconditionally, so a node started two daemons whose modes were switched off.
func TestDisabledModeIsNotBootEnabled(t *testing.T) {
	m := fixture()
	m.Modes = Modes{DMR: true} // YSF and NXDN explicitly off
	paths := testPaths()

	boot := m.BootEnableUnits(paths)
	for _, off := range []string{unitYSFGateway, unitDGIdGateway, unitNXDNGateway, unitP25Gateway, unitM17Gateway, unitDStarGateway, unitDAPNETGateway} {
		if slices.Contains(boot, off) {
			t.Errorf("%q is boot-enabled with its mode off; got %v", off, boot)
		}
		if !slices.Contains(m.BootDisableUnits(paths), off) {
			t.Errorf("%q must be boot-DISABLED with its mode off", off)
		}
	}
	// The one enabled mode, and the modem host it needs, still come up.
	for _, on := range []string{unitMMDVM, unitDMRGateway} {
		if !slices.Contains(boot, on) {
			t.Errorf("%q must be boot-enabled; got %v", on, boot)
		}
	}
}

// A node with nothing switched on runs nothing — including the modem host. This is
// the "unclaimed node" the image ships, and it must not be given a crash-looping
// MMDVM-Host just because the unit exists.
func TestNoModesRunsNothing(t *testing.T) {
	m := fixture()
	m.Modes = Modes{}
	paths := testPaths()

	if got := m.BootEnableUnits(paths); len(got) != 0 {
		t.Errorf("a node with every mode off must boot-enable nothing; got %v", got)
	}
	if got := len(m.BootDisableUnits(paths)); got != len(ManagedUnits()) {
		t.Errorf("every managed unit should be boot-disabled; got %d of %d", got, len(ManagedUnits()))
	}
}

// A displacing bus must keep the stock gateway out of the boot set, or a reboot
// races it for the mode's loopback — the original reason this code exists.
func TestDisplacedGatewayStaysOutOfTheBootSet(t *testing.T) {
	m := fixture()
	m.Modes = Modes{YSF: true}
	m.Buses = []Bus{{ID: "b1", Name: "b1", Enabled: true}}
	m.Attachments = []Attachment{{BusID: "b1", Mode: ModeYSF}}
	paths := testPaths()

	if !m.modeDisplacesGateway(ModeYSF) {
		t.Skip("fixture did not produce a displacing YSF bus")
	}
	if got := m.BootEnableUnits(paths); slices.Contains(got, unitYSFGateway) {
		t.Errorf("a displaced YSF gateway must not be boot-enabled; got %v", got)
	}
	if got := m.BootDisableUnits(paths); !slices.Contains(got, unitYSFGateway) {
		t.Errorf("a displaced YSF gateway must be boot-disabled; got %v", got)
	}
}
