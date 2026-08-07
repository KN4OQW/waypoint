package config

import (
	"strings"
	"testing"
)

// shimModel is a node with DMR on and nothing unusual about it.
func shimModel() *Model {
	m := &Model{}
	m.Modes.DMR = true
	m.General.Callsign = "KN4OQW"
	m.DMR.ID = "3180202"
	return m
}

// The default is the wiring every node has today. This is the assertion that a
// node upgrading into this feature does not have its DMR loopback moved.
func TestRelayIsOffByDefault(t *testing.T) {
	m := shimModel()
	if m.DMRShimEnabled() {
		t.Fatal("the relay is on for a node that never asked for it")
	}
	mmdvm := m.RenderMMDVM()
	if !strings.Contains(mmdvm, "GatewayPort=62031") {
		t.Errorf("MMDVM-Host is not wired straight to DMRGateway:\n%s", dmrNetworkSection(t, mmdvm))
	}
	gw := m.RenderDMRGateway()
	if !strings.Contains(gw, "RptPort=62032") {
		t.Errorf("DMRGateway is not wired straight to MMDVM-Host:\n%s", generalSection(t, gw))
	}
}

// With the relay on, each daemon keeps the port it BINDS and changes only the port
// it sends to. Getting that backwards would have both daemons trying to bind the
// relay's ports.
func TestRelayRewiringMovesOnlyTheDestinations(t *testing.T) {
	m := shimModel()
	m.DMRNet.ShimEnabled = true
	if !m.DMRShimEnabled() {
		t.Fatal("the relay did not switch on")
	}

	mmdvm := m.RenderMMDVM()
	for _, want := range []string{"LocalPort=62032", "GatewayPort=62033"} {
		if !strings.Contains(mmdvm, want) {
			t.Errorf("MMDVM-Host is missing %q:\n%s", want, dmrNetworkSection(t, mmdvm))
		}
	}
	gw := m.RenderDMRGateway()
	for _, want := range []string{"RptPort=62034", "LocalPort=62031"} {
		if !strings.Contains(gw, want) {
			t.Errorf("DMRGateway is missing %q:\n%s", want, generalSection(t, gw))
		}
	}

	// And the four addresses waypointd builds the relay from must be the same four
	// the two INIs just committed to. This is the whole reason the resolution lives
	// in one function.
	s := m.DMRShim()
	for _, tc := range []struct{ name, got, want string }{
		{"HostBind", s.HostBind, "127.0.0.1:62033"},
		{"HostPeer", s.HostPeer, "127.0.0.1:62032"},
		{"GatewayBind", s.GatewayBind, "127.0.0.1:62034"},
		{"GatewayPeer", s.GatewayPeer, "127.0.0.1:62031"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %s, want %s", tc.name, tc.got, tc.want)
		}
	}
}

func TestRelayHonoursCustomPorts(t *testing.T) {
	m := shimModel()
	m.DMRNet.ShimEnabled = true
	m.DMRNet.LocalPort = "52032"
	m.DMRNet.GatewayPort = "52031"
	m.DMRNet.ShimHostPort = "52033"
	m.DMRNet.ShimGatewayPort = "52034"

	if !strings.Contains(m.RenderMMDVM(), "GatewayPort=52033") {
		t.Error("MMDVM-Host did not follow the custom relay port")
	}
	if !strings.Contains(m.RenderDMRGateway(), "RptPort=52034") {
		t.Error("DMRGateway did not follow the custom relay port")
	}
	if s := m.DMRShim(); s.HostPeer != "127.0.0.1:52032" || s.GatewayPeer != "127.0.0.1:52031" {
		t.Errorf("the relay peers did not follow the custom loopback: %+v", s)
	}
}

// The configurations where the relay was asked for and cannot be given. Each one
// renders the DIRECT wiring — a loopback pointing at a relay that will not start
// is a dead DMR link, which is strictly worse than not having the feature.
func TestRelayDeclinesConfigurationsItCannotServe(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*Model)
		field   string
		message string
	}{
		{
			name:    "DMRGateway is on another host",
			mutate:  func(m *Model) { m.DMRNet.GatewayAddress = "192.168.1.50" },
			field:   "dmrnet.shim_enabled",
			message: "192.168.1.50",
		},
		{
			name:    "a relay port collides with the loopback pair",
			mutate:  func(m *Model) { m.DMRNet.ShimHostPort = "62031" },
			field:   "dmrnet.shim_host_port",
			message: "four distinct ports",
		},
		{
			name:    "the two relay ports are the same",
			mutate:  func(m *Model) { m.DMRNet.ShimGatewayPort = "62033" },
			field:   "dmrnet.shim_host_port",
			message: "four distinct ports",
		},
		{
			name:    "a relay port sits in the reserved bus range",
			mutate:  func(m *Model) { m.DMRNet.ShimHostPort = "62150" },
			field:   "dmrnet.shim_host_port",
			message: "62100-62199",
		},
		{
			name:    "a relay port is not a port",
			mutate:  func(m *Model) { m.DMRNet.ShimHostPort = "not-a-port" },
			field:   "dmrnet.shim_host_port",
			message: "four distinct ports",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := shimModel()
			m.DMRNet.ShimEnabled = true
			tc.mutate(m)

			if m.DMRShimEnabled() {
				t.Error("the relay switched on for a configuration it cannot serve")
			}
			if !strings.Contains(m.RenderMMDVM(), "GatewayPort=62031") {
				t.Errorf("MMDVM-Host was rewired to a relay that will not run:\n%s",
					dmrNetworkSection(t, m.RenderMMDVM()))
			}

			var found *ModeProblem
			for i, p := range m.ModeProblems() {
				if p.Field == tc.field {
					found = &m.ModeProblems()[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("nothing reported against %s; the operator would see no effect and no reason", tc.field)
			}
			if found.Severity != SeverityWarning {
				t.Errorf("severity = %q, want a warning: the node still works", found.Severity)
			}
			if !strings.Contains(found.Message, tc.message) {
				t.Errorf("message = %q, want it to mention %q", found.Message, tc.message)
			}
		})
	}
}

// localhost and ::1 are DMRGateway on this node just as much as 127.0.0.1 is.
func TestRelayAcceptsEveryLoopbackSpelling(t *testing.T) {
	for _, addr := range []string{"", "127.0.0.1", "localhost", "LOCALHOST", "::1", "127.0.0.2"} {
		m := shimModel()
		m.DMRNet.ShimEnabled = true
		m.DMRNet.GatewayAddress = addr
		if !m.DMRShimEnabled() {
			t.Errorf("gateway address %q was not recognised as local", addr)
		}
	}
	// A name that is not obviously local is not resolved and not assumed. It may
	// point here today and elsewhere after a DNS change, and a relay that stopped
	// working on a DNS change would be very hard to diagnose.
	m := shimModel()
	m.DMRNet.ShimEnabled = true
	m.DMRNet.GatewayAddress = "dmrgateway.internal"
	if m.DMRShimEnabled() {
		t.Error("a hostname was assumed to be local")
	}
}

// DMR off means no relay: there is no loopback to sit in.
func TestRelayNeedsDMR(t *testing.T) {
	m := shimModel()
	m.DMRNet.ShimEnabled = true
	m.Modes.DMR = false
	if m.DMRShimEnabled() {
		t.Error("the relay switched on with DMR disabled")
	}
	// And nothing is reported: a mode that is off is never reported against.
	for _, p := range m.ModeProblems() {
		if strings.HasPrefix(p.Field, "dmrnet.shim") {
			t.Errorf("reported %q about a disabled mode", p.Field)
		}
	}
}

// A node with the relay off must not be told anything about it.
func TestRelayOffReportsNothing(t *testing.T) {
	m := shimModel()
	m.DMRNet.ShimHostPort = "62031" // nonsense, but nobody asked for the relay
	for _, p := range m.ModeProblems() {
		if strings.HasPrefix(p.Field, "dmrnet.shim") {
			t.Errorf("reported %q about a feature that is switched off", p.Field)
		}
	}
}

// The bus's reserved DMR ports dial DMRGateway directly and must not move when the
// relay goes in, or an attached bus would lose its multiplexed network.
func TestRelayLeavesTheBusLoopbackAlone(t *testing.T) {
	m := shimModel()
	m.DMRNet.ShimEnabled = true
	m.Buses = []Bus{{ID: "shack", Name: "Shack", Enabled: true}}
	m.Attachments = []Attachment{{BusID: "shack", Mode: ModeDMR}}

	gw := m.RenderDMRGateway()
	if !strings.Contains(gw, "Port=62100") {
		t.Errorf("the bus's reserved port moved:\n%s", gw)
	}
	if !strings.Contains(gw, "RptPort=62034") {
		t.Error("the relay wiring did not take effect alongside a bus")
	}
}

func dmrNetworkSection(t *testing.T, ini string) string { return sectionOf(t, ini, "[DMR Network]") }
func generalSection(t *testing.T, ini string) string    { return sectionOf(t, ini, "[General]") }

func sectionOf(t *testing.T, ini, header string) string {
	t.Helper()
	i := strings.Index(ini, header)
	if i < 0 {
		return "(section " + header + " not rendered)"
	}
	rest := ini[i:]
	if j := strings.Index(rest[len(header):], "\n["); j >= 0 {
		rest = rest[:len(header)+j]
	}
	return rest
}
