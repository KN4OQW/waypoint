// Package supervisor is the network-resilience layer (#22): it detects and
// recovers the connection losses the upstream g4klx daemons do not.
//
// The gaps are real and each is an open upstream issue. DMRGateway resolves its
// master's address exactly once, in the network's constructor — a node that
// started while DNS was down never resolves it, and a master whose address moves
// is talked to at the old one forever, because the "master is closing down" path
// re-opens the socket without re-resolving (MMDVM-Host#682). DAPNETGateway's
// connection dies on an IP change without its read ever returning an error, so its
// own recover() never runs (DAPNETGateway#10). Neither is fixable from outside the
// process except by restarting it — which is exactly what this package does, at
// the right moment and no more often than it should.
//
// "No more often than it should" is the other half, and the reason this is not a
// three-line watchdog. Waypoint restarts a daemon that talks to somebody else's
// infrastructure, and the failure mode of getting that wrong is a reconnect storm
// on a shared server — the thing APRS-IS operators had to chase upstream
// (YSFClients#155). So remediation is gated on the node's own connectivity first
// (an outage upstream of us is not the daemon's fault), rate-limited by a jittered
// backoff, deferred while a transmission is on the air, and reset only after a
// sustained recovery rather than the first hopeful sign of one.
package supervisor

import (
	"net"
	"sort"

	"github.com/KN4OQW/waypoint/internal/config"
)

// Kind is what sort of upstream connection an attachment is. It decides how the
// attachment is probed and how a daemon's own status report about it is read;
// the remediation is the same for all of them.
type Kind string

const (
	// KindDMRMaster is a DMR master DMRGateway logs into (BrandMeister, TGIF, a
	// DMR+ server, a custom homebrew master).
	KindDMRMaster Kind = "dmr-master"
	// KindDAPNET is the DAPNET core DAPNETGateway logs into for paging.
	KindDAPNET Kind = "dapnet"
)

// DAPNET's transmitter port is fixed by the protocol, not configurable, and the
// renderer writes it as a literal — so the supervisor probes the same literal
// rather than inventing a setting the operator cannot see.
const (
	dapnetPort           = "43434"
	dapnetDefaultServer  = "dapnet.afu.rwth-aachen.de"
	dmrMasterDefaultPort = "62031"
)

// Attachment is one upstream connection the node depends on and can lose.
//
// Name is the join key that ties the three views of the same thing together: it
// is the [DMR Network N] Name the renderer wrote into the daemon's INI, the name
// the daemon quotes back in its own MQTT status ("Logged into DMR Network: X"),
// and the key the status pipeline files this link under. Keeping one name means
// the supervisor's verdict and the daemon's own report land on the same row
// instead of racing each other as two separate networks.
type Attachment struct {
	Name string
	Kind Kind
	Host string
	Port string
	// Unit is the systemd unit whose restart is the remedy. Several attachments
	// commonly share one — every DMR master rides DMRGateway — which is why
	// remediation has to be coordinated rather than issued per attachment.
	Unit string
}

// Endpoint is the host:port the attachment connects to — what gets probed.
func (a Attachment) Endpoint() string { return net.JoinHostPort(a.Host, a.Port) }

// Attachments derives the supervised set from the configuration store, in a
// stable order (by kind, then name) so a caller diffing two derivations sees only
// real changes.
//
// It reads the same fields the renderer compiles into each daemon's INI, on
// purpose: the supervisor must probe what the daemon was actually told, not what
// the operator meant. A probe against a different address than the daemon uses
// would report health the daemon does not have.
//
// Deliberately absent:
//
//   - APRS-IS. The pinned YSFGateway's [APRS] section carries only Enable and a
//     suffix — no address — because the MQTT-era design routes APRS through
//     APRSGateway, which waypoint-stack does not package yet (#5). There is no
//     endpoint in the store to probe, so claiming to supervise one would be a lie.
//     APRSGateway#1 is in scope for this package the moment that package lands.
//   - XLX. Its address comes from XLXHosts.txt at runtime, not from the store; the
//     [XLX Network] section has a Startup reflector and no Address.
//   - The MMDVM_CM cross-mode bridges. RenderTargets no longer emits units for
//     them (the RFC-0003 bus architecture replaced them), so their dormant master
//     addresses drive no running daemon.
func Attachments(m *config.Model) []Attachment {
	if m == nil {
		return nil
	}
	var out []Attachment

	for _, n := range m.Networks {
		if !n.Enabled || n.Address == "" {
			continue
		}
		if n.Type == config.NetXLX {
			continue // address lives in a hostfile, not the store
		}
		out = append(out, Attachment{
			Name: n.Name,
			Kind: KindDMRMaster,
			Host: n.Address,
			Port: orDefault(n.Port, dmrMasterDefaultPort),
			Unit: config.UnitDMRGateway,
		})
	}

	// POCSAG gates DAPNETGateway's whole render target, so an attachment exists on
	// exactly the nodes where the daemon does.
	if m.Modes.POCSAG {
		out = append(out, Attachment{
			Name: "dapnet",
			Kind: KindDAPNET,
			Host: orDefault(m.POCSAG.Server, dapnetDefaultServer),
			Port: dapnetPort,
			Unit: config.UnitDAPNETGateway,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
