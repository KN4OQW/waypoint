package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// The DMR loopback relay's wiring, resolved once here and consumed by both the
// renderers and waypointd.
//
// Two daemons and one relay have to agree on four ports, and each of them learns
// about them a different way: MMDVM-Host and DMRGateway from rendered INI files,
// the relay from waypointd's own configuration. A default written down in more
// than one of those places is a disagreement waiting for an Apply, so this file is
// the only place any of them is decided.
//
// # The wiring
//
// Without the relay the two daemons talk to each other directly: MMDVM-Host binds
// LocalPort and sends to GatewayPort; DMRGateway binds that same GatewayPort as
// its own LocalPort and sends back to LocalPort as its RptPort.
//
// With the relay in between, each daemon keeps the port it BINDS and changes the
// port it SENDS TO:
//
//	                 binds 62032                      binds 62031
//	MMDVM-Host ─── sends to 62033 ──► relay ─── sends to 62031 ──► DMRGateway
//	           ◄── 62033 sends to ─── 62033/62034 ◄── sends to 62034 ───
//
// Nothing else on the node moves. The bus's reserved DMR ports (62100-62199) dial
// DMRGateway directly and are untouched.
//
// # Why it is off by default
//
// With the relay switched on, waypointd is in the path of every DMR frame. Today
// it is not: an operator can stop waypointd and the node keeps carrying DMR,
// because MMDVM-Host and DMRGateway are talking to each other and waypointd only
// renders their configuration. Turning the relay on by default would quietly make
// the management daemon a dependency of the radio, which is a trade an operator
// should make deliberately rather than discover during an upgrade.
//
// So DMRShimEnabled is opt-in, and a node that has never heard of it renders
// exactly what it rendered before.

// Default relay ports. They sit next to the pair they relay so anyone reading `ss`
// output can see what they belong to, and clear of the reserved bus range
// (dmrBusPortBase..dmrBusPortCeil) so a bus can never alias one.
const (
	defaultShimHostPort    = "62033" // the relay's MMDVM-Host-facing bind
	defaultShimGatewayPort = "62034" // the relay's DMRGateway-facing bind
)

// Stock loopback ports, the values the DMRNet fields default to.
const (
	defaultDMRHostLocalPort    = "62032" // MMDVM-Host binds
	defaultDMRGatewayLocalPort = "62031" // DMRGateway binds
)

// DMRShim is the resolved relay wiring. Addresses are host:port strings ready to
// hand to internal/dmrshim; ports are the bare values the INI renderers emit.
type DMRShim struct {
	// Enabled reports whether the relay is in the path. When false every other
	// field is still populated, so a caller can log what would have been used.
	Enabled bool

	// HostBind is where the relay listens for MMDVM-Host, and therefore what
	// MMDVM-Host must carry as [DMR Network] GatewayPort.
	HostBind string
	// HostPeer is where MMDVM-Host listens: its [DMR Network] LocalPort.
	HostPeer string
	// GatewayBind is where the relay listens for DMRGateway, and therefore what
	// DMRGateway must carry as [General] RptPort.
	GatewayBind string
	// GatewayPeer is where DMRGateway listens: its [General] LocalPort.
	GatewayPeer string

	// The bare port values, so a renderer does not have to take a string apart.
	HostBindPort    string
	GatewayBindPort string
}

// DMRShim resolves the relay wiring from the model, applying every default.
func (m *Model) DMRShim() DMRShim {
	hostLocal := def(m.DMRNet.LocalPort, defaultDMRHostLocalPort)
	gatewayLocal := def(m.DMRNet.GatewayPort, defaultDMRGatewayLocalPort)
	hostBind := def(m.DMRNet.ShimHostPort, defaultShimHostPort)
	gatewayBind := def(m.DMRNet.ShimGatewayPort, defaultShimGatewayPort)
	return DMRShim{
		Enabled:         m.DMRShimEnabled(),
		HostBind:        net.JoinHostPort("127.0.0.1", hostBind),
		HostPeer:        net.JoinHostPort("127.0.0.1", hostLocal),
		GatewayBind:     net.JoinHostPort("127.0.0.1", gatewayBind),
		GatewayPeer:     net.JoinHostPort("127.0.0.1", gatewayLocal),
		HostBindPort:    hostBind,
		GatewayBindPort: gatewayBind,
	}
}

// DMRShimEnabled reports whether the relay should run and be rendered into the
// loopback.
//
// It is not enough for the operator to have asked. The relay can only sit between
// two daemons that are both local, and it is pointless when DMR is off — so a
// configuration where it cannot work renders the direct wiring rather than a
// loopback pointing at a relay that will not start. dmrProblems says why.
func (m *Model) DMRShimEnabled() bool {
	if m == nil || !m.DMRNet.ShimEnabled || !m.Modes.DMR {
		return false
	}
	return isLoopbackHost(def(m.DMRNet.GatewayAddress, "127.0.0.1")) && m.dmrShimPortsUsable()
}

// dmrShimPortsUsable reports whether the four ports are four distinct, in-range
// ports that do not collide with the reserved bus range.
func (m *Model) dmrShimPortsUsable() bool {
	ports := []string{
		def(m.DMRNet.LocalPort, defaultDMRHostLocalPort),
		def(m.DMRNet.GatewayPort, defaultDMRGatewayLocalPort),
		def(m.DMRNet.ShimHostPort, defaultShimHostPort),
		def(m.DMRNet.ShimGatewayPort, defaultShimGatewayPort),
	}
	seen := map[int]bool{}
	for _, p := range ports {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 1 || n > 65535 || seen[n] {
			return false
		}
		seen[n] = true
	}
	// Only the relay's own two ports are checked against the bus range; the stock
	// loopback pair predates it and is excluded from it by construction.
	for _, p := range ports[2:] {
		n, _ := strconv.Atoi(strings.TrimSpace(p))
		if n >= dmrBusPortBase && n <= dmrBusPortCeil {
			return false
		}
	}
	return true
}

// dmrShimProblems reports the configurations where the relay was asked for and
// cannot be given, so an operator who switched it on and saw nothing happen finds
// out why rather than debugging a message feature that was never in the path.
//
// Each is a warning and not a refusal: the node works, with the direct wiring, and
// refusing the save would block an operator from fixing the port in a second edit.
func (m *Model) dmrShimProblems(add func(field, severity, msg string)) {
	if !m.DMRNet.ShimEnabled {
		return
	}
	if gw := def(m.DMRNet.GatewayAddress, "127.0.0.1"); !isLoopbackHost(gw) {
		add("dmrnet.shim_enabled", SeverityWarning, fmt.Sprintf(
			"The DMR message relay needs DMRGateway on this node, but the gateway address is %s. "+
				"The relay is switched off and the DMR loopback is wired directly.", gw))
		return
	}
	if !m.dmrShimPortsUsable() {
		add("dmrnet.shim_host_port", SeverityWarning,
			"The DMR message relay needs four distinct ports, none of them in the bus range "+
				"62100-62199. The relay is switched off and the DMR loopback is wired directly.")
	}
}

// isLoopbackHost reports whether an address literal names this machine's loopback.
// A name is not resolved: the question is whether the renderer can be SURE it is
// local, and a hostname that resolves to 127.0.0.1 today may not tomorrow.
func isLoopbackHost(h string) bool {
	h = strings.TrimSpace(h)
	if h == "" || strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
