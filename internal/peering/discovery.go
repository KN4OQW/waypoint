package peering

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/libp2p/zeroconf/v2"
)

// discovery.go is the mDNS advertise/browse layer (RFC-0016 §3). It is behind the
// Discovery interface so tests run without multicast (a fake stands in), and it is
// a CONVENIENCE ONLY: every pairing flow also works with a typed host:port, so a
// network that filters mDNS still pairs.
//
// Library choice: github.com/libp2p/zeroconf/v2 — pure Go (no cgo), actively
// maintained, RFC-6762/6763 mDNS+DNS-SD over miekg/dns (already the ecosystem's
// standard resolver). It fits the project's dependency posture (maintained,
// cgo-free) better than the abandoned grandcat fork it descends from.

// ServiceName is the DNS-SD service type Waypoint nodes advertise.
const ServiceName = "_waypoint._tcp"

// Found is one discovered peer node.
type Found struct {
	Instance string   `json:"instance"` // the advertised instance name (the node name)
	Host     string   `json:"host"`     // resolved address
	Port     int      `json:"port"`
	Text     []string `json:"text,omitempty"` // TXT records (e.g. fingerprint=...)
}

// Discovery advertises this node and browses for peers. It is an interface so the
// handshake/manager tests never touch multicast.
type Discovery interface {
	// Advertise announces this node's peering endpoint under `instance` (the node
	// name) on `port` with TXT records, until the returned stop is called.
	Advertise(instance string, port int, text []string) (stop func(), err error)
	// Browse returns peers discovered within the timeout.
	Browse(ctx context.Context, timeout time.Duration) ([]Found, error)
}

// mdns is the real zeroconf-backed Discovery.
type mdns struct{}

// NewMDNS returns the real mDNS discovery.
func NewMDNS() Discovery { return mdns{} }

func (mdns) Advertise(instance string, port int, text []string) (func(), error) {
	srv, err := zeroconf.Register(instance, ServiceName, "local.", port, text, nil)
	if err != nil {
		return nil, err
	}
	return srv.Shutdown, nil
}

func (mdns) Browse(ctx context.Context, timeout time.Duration) ([]Found, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entries := make(chan *zeroconf.ServiceEntry, 16)
	var out []Found
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range entries {
			host := ""
			if len(e.AddrIPv4) > 0 {
				host = e.AddrIPv4[0].String()
			} else if len(e.AddrIPv6) > 0 {
				host = e.AddrIPv6[0].String()
			} else {
				host = e.HostName
			}
			out = append(out, Found{Instance: e.Instance, Host: host, Port: e.Port, Text: e.Text})
		}
	}()
	if err := zeroconf.Browse(cctx, ServiceName, "local.", entries); err != nil {
		return nil, err
	}
	<-cctx.Done()
	<-done
	return out, nil
}

// JoinHostPort is a small convenience the API/manual-fallback path uses.
func JoinHostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
