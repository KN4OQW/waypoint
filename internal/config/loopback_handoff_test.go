package config

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// loopback_handoff_test.go is the RFC-0003 Addendum A §8 render-coordination
// contract: DMR multiplexes as one [DMR Network N], YSF/NXDN displace the stock
// gateway target, the coordination is a pure function of the model, and — the D3
// invariant — no two rendered consumers ever bind one port.

func handoffPaths() Paths { return Paths{BusConfigDir: "/etc/waypoint/buses"} }

// dmrNetworkCount counts [DMR Network N] sections in a rendered DMRGateway.ini.
func dmrNetworkCount(ini string) int {
	n := 0
	for _, line := range strings.Split(ini, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[DMR Network ") {
			n++
		}
	}
	return n
}

// TestDMRMultiplexAddsExactlyOneNetwork: a DMR bus attachment adds exactly one
// [DMR Network N] dialing the bus's reserved port, and removing the attachment
// restores the DMRGateway render byte-identically (Addendum §8.1).
func TestDMRMultiplexAddsExactlyOneNetwork(t *testing.T) {
	base := &Model{
		Networks: []Network{{Name: "BM", Address: "3102.master", Enabled: true}},
	}
	withBus := &Model{
		Networks:    base.Networks,
		Buses:       []Bus{{ID: "a", Name: "A", Enabled: true}},
		Attachments: []Attachment{{BusID: "a", Mode: ModeDMR, Slot: "2", DefaultTG: "91"}, {BusID: "a", Mode: ModeYSF}},
	}

	baseINI := base.RenderDMRGateway()
	busINI := withBus.RenderDMRGateway()
	if dmrNetworkCount(busINI)-dmrNetworkCount(baseINI) != 1 {
		t.Fatalf("DMR bus should add exactly one [DMR Network N]; base=%d bus=%d",
			dmrNetworkCount(baseINI), dmrNetworkCount(busINI))
	}
	port, _ := withBus.dmrBusPort("a")
	for _, want := range []string{"Name=Bus_a", "Address=127.0.0.1", "Port=" + strconv.Itoa(port), fmt.Sprintf("TGRewrite0=2,91,2,91,1")} {
		if !strings.Contains(busINI, want) {
			t.Fatalf("bus network missing %q in:\n%s", want, busINI)
		}
	}

	// Detach: remove the DMR attachment — the render returns to the baseline exactly.
	detached := &Model{Networks: base.Networks}
	if detached.RenderDMRGateway() != baseINI {
		t.Fatal("removing the DMR bus did not restore the DMRGateway render byte-identically")
	}
}

// TestDMRBusNetworkRenderedBeforeOperatorNetworks guards the RFC-0003 Addendum A
// §1 precedence fix: the bus's [DMR Network] must render BEFORE the operator's
// networks so its specific TGRewrite wins DMRGateway's two-pass RF router (Pass 1
// = specific rewrites, first match across all networks) even when an operator
// network carries its OWN specific rewrite for the same TG. On the bench, an
// operator BrandMeister network with TGRewrite0=2,9,... claimed keyed RF on TG 9
// before the bus (rendered last) could — so "exactly the bus's talkgroups route to
// it" only holds if the bus is rendered first.
func TestDMRBusNetworkRenderedBeforeOperatorNetworks(t *testing.T) {
	m := &Model{
		// An operator network whose own rewrite claims the very TG the bus wants.
		Networks: []Network{{Name: "BM", Address: "3102.master", Type: NetCustom, Enabled: true,
			Rewrites: []string{"TGRewrite0=2,9,2,9,1", "PassAllTG0=1", "PassAllTG1=2"}}},
		Buses:       []Bus{{ID: "a", Name: "A", Enabled: true}},
		Attachments: []Attachment{{BusID: "a", Mode: ModeDMR, Slot: "2", DefaultTG: "9"}, {BusID: "a", Mode: ModeYSF}},
	}
	ini := m.RenderDMRGateway()
	busAt := strings.Index(ini, "Name=Bus_a")
	bmAt := strings.Index(ini, "Name=BM")
	if busAt < 0 || bmAt < 0 {
		t.Fatalf("expected both bus and operator networks in render:\n%s", ini)
	}
	if busAt > bmAt {
		t.Fatalf("bus network must render BEFORE the operator network so its TGRewrite wins Pass 1; got bus@%d after BM@%d:\n%s", busAt, bmAt, ini)
	}
	// The bus is [DMR Network 1]; the operator network follows.
	if i := strings.Index(ini, "[DMR Network 1]"); i < 0 || i > busAt {
		t.Fatalf("the bus should be [DMR Network 1] (rendered first)")
	}
}

// TestDisplaceDropsGatewayTarget: a YSF bus drops the YSF gateway target and adds
// the bus unit; removing it restores the gateway target (Addendum §8.1).
func TestDisplaceDropsGatewayTarget(t *testing.T) {
	units := func(m *Model) map[string]bool {
		out := map[string]bool{}
		for _, tg := range m.RenderTargets(handoffPaths()) {
			if tg.Unit != "" {
				out[tg.Unit] = true
			}
		}
		return out
	}

	ysfBus := &Model{
		Buses:       []Bus{{ID: "a", Enabled: true}},
		Attachments: []Attachment{{BusID: "a", Mode: ModeYSF}, {BusID: "a", Mode: ModeNXDN}},
	}
	u := units(ysfBus)
	if u[unitYSFGateway] || u[unitDGIdGateway] {
		t.Fatal("YSF bus must displace the YSF gateway target")
	}
	if u[unitNXDNGateway] {
		t.Fatal("NXDN bus must displace the NXDN gateway target")
	}
	if !u["waypoint-bus@a.service"] {
		t.Fatal("the displacing bus unit must be a target")
	}
	// The displaced gateway units are the apply-stop set.
	got := ysfBus.DisplacedGatewayUnits()
	if !containsUnit(got, unitYSFGateway) || !containsUnit(got, unitNXDNGateway) {
		t.Fatalf("DisplacedGatewayUnits = %v, want YSF+NXDN gateways", got)
	}

	// No bus ⇒ the gateway targets are back.
	none := units(&Model{})
	if !none[unitYSFGateway] || !none[unitNXDNGateway] {
		t.Fatal("with no bus, the stock gateway targets must be present")
	}
}

// TestBusConfigCarriesReservedDMRPort: the rendered bus config carries the DMR
// attachment's reserved multiplex port and no override for a displacing mode.
func TestBusConfigCarriesReservedDMRPort(t *testing.T) {
	m := busModel() // bus-a: DMR + YSF
	lb := m.busLoopbacksFor("bus-a")
	dmr, ok := lb[string(ModeDMR)]
	if !ok {
		t.Fatal("DMR attachment must carry a loopback override")
	}
	if dmr.Bind < dmrBusPortBase || dmr.Bind > dmrBusPortCeil {
		t.Fatalf("DMR bus port %d outside reserved range %d-%d", dmr.Bind, dmrBusPortBase, dmrBusPortCeil)
	}
	if _, ok := lb[string(ModeYSF)]; ok {
		t.Fatal("a displacing YSF attachment must NOT carry an override (reuses the stock loopback)")
	}
	if dmr.Bind == 62031 || dmr.Bind == 62032 {
		t.Fatal("the DMR bus port must never be the MMDVM-Host↔DMRGateway pair")
	}
}

// TestDMRPortDeterministicAndStable: the port is a pure function of the model and
// stable under an unrelated bus toggling (Addendum §4).
func TestDMRPortDeterministicAndStable(t *testing.T) {
	m := busModel()
	p1, _ := m.dmrBusPort("bus-a")
	p2, _ := m.dmrBusPort("bus-a")
	if p1 != p2 {
		t.Fatal("dmrBusPort must be deterministic")
	}
	// Toggling the unrelated disabled bus does not move bus-a's port (stable index).
	m2 := busModel()
	m2.Buses[1].Enabled = true
	if p3, _ := m2.dmrBusPort("bus-a"); p3 != p1 {
		t.Fatalf("bus-a port moved (%d→%d) when an unrelated bus toggled", p1, p3)
	}
}

// TestPortCollisionImpossibility is the D3 invariant as a property (Addendum §8.3):
// over a spread of valid models, no port is bound by two rendered consumers. Every
// consumer's bind is the LocalPort it listens on (MMDVM-Host's per-mode LocalPorts
// and each gateway's [General] LocalPort — verified identical convention in
// Appendix A), plus each enabled bus's effective loopback binds.
func TestPortCollisionImpossibility(t *testing.T) {
	// A spread of valid reframe-tier bus mode-sets crossed with EnableDGId.
	modeSets := [][]Mode{
		nil,
		{ModeDMR, ModeYSF},
		{ModeDMR, ModeNXDN},
		{ModeYSF, ModeNXDN},
		{ModeDMR, ModeYSF, ModeNXDN},
	}
	// A node has at most ONE reframe bus: a bus needs ≥2 of {DMR,YSF,NXDN} and a mode
	// attaches to ≤1 bus (RFC-0003 §5), so two buses would need ≥4 distinct modes from
	// only 3 — impossible. So the valid space is one bus with a mode subset, crossed
	// with EnableDGId and a disabled second bus (which contributes no target).
	for _, dgid := range []bool{false, true} {
		for i, ms := range modeSets {
			m := &Model{Networks: []Network{{Name: "BM", Address: "m", Enabled: true}}}
			m.YSFGW.EnableDGId = dgid
			// A disabled bus always exists to prove it never contributes a binder.
			m.Buses = append(m.Buses, Bus{ID: "z-disabled", Enabled: false})
			m.Attachments = append(m.Attachments, Attachment{BusID: "z-disabled", Mode: ModeDMR}, Attachment{BusID: "z-disabled", Mode: ModeYSF})
			if len(ms) > 0 {
				m.Buses = append(m.Buses, Bus{ID: "a", Enabled: true})
				for _, mode := range ms {
					m.Attachments = append(m.Attachments, Attachment{BusID: "a", Mode: mode, Slot: "2", DefaultTG: "91"})
				}
			}
			assertNoPortCollision(t, m, fmt.Sprintf("dgid=%v set=%d", dgid, i))
		}
	}
}

func assertNoPortCollision(t *testing.T, m *Model, label string) {
	t.Helper()
	owner := map[int]string{}
	claim := func(port int, who string) {
		if prev, ok := owner[port]; ok && prev != who {
			t.Fatalf("[%s] port %d bound by BOTH %s and %s", label, port, prev, who)
		}
		owner[port] = who
	}
	for _, tg := range m.RenderTargets(handoffPaths()) {
		if strings.HasPrefix(tg.Unit, "waypoint-bus@") {
			id := strings.TrimSuffix(strings.TrimPrefix(tg.Unit, "waypoint-bus@"), ".service")
			for _, a := range m.Attachments {
				if a.BusID != id {
					continue
				}
				lb, _ := m.busEffectiveLoopback(id, a.Mode)
				claim(lb.Bind, tg.Unit+"/"+string(a.Mode))
			}
			continue
		}
		// A gateway/MMDVM INI: every LocalPort= line is a port that consumer binds.
		for _, p := range localPortsIn(tg.Render(m)) {
			claim(p, tg.Unit)
		}
	}
}

// localPortsIn extracts the LocalPort= values (the bind side, by the Appendix-A
// convention shared across MMDVM-Host and every gateway) from a rendered INI.
func localPortsIn(ini string) []int {
	var out []int
	for _, line := range strings.Split(ini, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "LocalPort="); ok {
			if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				out = append(out, p)
			}
		}
	}
	return out
}

func containsUnit(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
