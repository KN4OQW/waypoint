package supervisor

import (
	"context"
	"net"
	"time"
)

// probe.go answers "is this endpoint there" without asking the daemon.
//
// It exists because the daemon's own opinion is exactly what is unreliable in the
// failures this package targets. DMRGateway resolves its master's address once, in
// the network's constructor, and its reconnect path re-opens the socket without
// re-resolving — so a gateway that came up while DNS was down reports nothing
// wrong while sending to nowhere, and one whose master has moved keeps talking
// confidently to the old address. Waypoint resolves the same name the renderer
// wrote into that daemon's INI, and disagrees.

// DefaultProbeTimeout bounds a single probe. It is generous: a slow DNS server on
// a congested link is not a failed lookup, and a probe that gives up early would
// manufacture the outage it is supposed to detect.
const DefaultProbeTimeout = 5 * time.Second

// Prober performs endpoint checks. The two function fields are the only places it
// touches the network, so tests drive every path without DNS or sockets.
type Prober struct {
	// Resolve looks up a hostname. Nil uses the system resolver — the same one the
	// daemon uses, which is the point: a probe that resolved differently from the
	// process it is judging would be answering a different question.
	Resolve func(ctx context.Context, host string) ([]net.IPAddr, error)
	// Dial opens a connection for the deep probe. Nil uses a plain net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Timeout bounds one probe. Zero uses DefaultProbeTimeout.
	Timeout time.Duration
}

func (p *Prober) resolve(ctx context.Context, host string) ([]net.IPAddr, error) {
	if p.Resolve != nil {
		return p.Resolve(ctx, host)
	}
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

func (p *Prober) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(ctx, network, addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func (p *Prober) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return DefaultProbeTimeout
}

// Probe checks one attachment's endpoint and returns what it learned.
//
// deep asks for a connection attempt as well as a name lookup, and the caller sets
// it only when something already looks wrong. That restraint is deliberate: a
// connection attempt is a visit to somebody else's server, and a fleet of nodes
// each opening a speculative socket on a timer is the shape of the problem this
// package exists to avoid, not a diagnostic.
//
// The verdicts are narrow on purpose. A name that does not resolve is a definite
// no. A name that does resolve is *not* a yes — DNS answering says nothing about
// whether the master behind it is alive — so it stays unknown, and positive
// evidence has to come from a connection or from the daemon's own report. Being
// unable to distinguish "healthy" from "not yet known" is what makes a supervisor
// restart working daemons.
func (p *Prober) Probe(ctx context.Context, a Attachment, deep bool) (Tri, string) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()

	// An address literal cannot fail to resolve, so a lookup proves nothing about
	// it. Skip straight to the connection question.
	if net.ParseIP(a.Host) == nil {
		addrs, err := p.resolve(ctx, a.Host)
		if err != nil {
			return TriNo, "the address " + a.Host + " does not resolve: " + err.Error()
		}
		if len(addrs) == 0 {
			return TriNo, "the address " + a.Host + " resolves to nothing"
		}
	}

	if !deep || !a.Kind.connectable() {
		return TriUnknown, "the address resolves"
	}

	conn, err := p.dial(ctx, "tcp", a.Endpoint())
	if err != nil {
		return TriNo, "cannot connect to " + a.Endpoint() + ": " + err.Error()
	}
	// Closed at once. The probe asks whether the port answers, and nothing more —
	// it never logs in, so it can never become the duplicate session that gets a
	// real one kicked.
	_ = conn.Close()
	return TriYes, "the endpoint accepts connections"
}

// connectable reports whether a connection attempt tells us anything about this
// kind of attachment.
//
// DAPNET is TCP: a successful connect is real evidence the core is up. A DMR
// master is UDP, where "connecting" is a purely local operation that would succeed
// against an address nothing has listened on for years. The only UDP probe with
// any meaning would be a homebrew RPTL — an unauthenticated login attempt against
// somebody else's master — and this package is not going to send one of those on a
// timer. So a DMR master's positive evidence comes from DMRGateway's own status
// report, and the probe's contribution is the negative case: the name is gone.
func (k Kind) connectable() bool { return k == KindDAPNET }
