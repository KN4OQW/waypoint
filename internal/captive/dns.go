// Package captive is the setup access point's captive portal: the DNS responder
// that points every name at the node, the HTTP endpoints each operating system
// probes to decide whether it is behind a portal, and the session lock that keeps
// setup to one device.
//
// The goal is narrow and worth stating: a phone that joins Waypoint-Setup-XXXX
// should show its own captive-portal sheet with the wizard in it, without the
// operator being told to type an address. Every mechanism here exists because one
// operating system or another needs a different nudge to reach that point.
package captive

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// DefaultAddress is the node's address on its own access point.
//
// It is NetworkManager's shared-mode default, which matters: the DHCP server NM
// starts hands out 10.42.0.x, so anything else here would mean overriding the
// address in two places and getting one of them wrong.
var DefaultAddress = netip.MustParseAddr("10.42.0.1")

// dnsTTL is short so a client that later joins the operator's real network does
// not keep resolving that network's names to this node. Setup lasts minutes; a
// stale cache outlasting it is a support ticket.
const dnsTTL = 10

// DNSResponder answers every A query with the node's own address.
//
// This is a deliberate lie, and it is the mechanism captive portals are built on:
// a client resolves connectivitycheck.gstatic.com, gets the node, sends its probe
// here, and concludes it is behind a portal. It is scoped to the setup AP's
// address and torn down with the AP — nothing about it survives into normal
// operation.
type DNSResponder struct {
	// Answer is returned for every A query. Zero means DefaultAddress.
	Answer netip.Addr
	// Logf receives one line per failure, not per query: a captive client sends
	// dozens of probes a minute and logging each would bury everything else.
	Logf func(format string, args ...any)

	// mu guards the two servers. Shutdown is called both by ListenAndServe's own
	// context branch and by a caller winding the responder down, and those run on
	// different goroutines.
	mu  sync.Mutex
	udp *dns.Server
	tcp *dns.Server
}

func (d *DNSResponder) answer() netip.Addr {
	if d.Answer.IsValid() {
		return d.Answer
	}
	return DefaultAddress
}

func (d *DNSResponder) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

// ServeDNS answers one query.
func (d *DNSResponder) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	m.RecursionAvailable = true

	// Anything that is not a plain query — an update, a notify — is refused rather
	// than answered with a hijacked address.
	if r.Opcode != dns.OpcodeQuery {
		m.SetRcode(r, dns.RcodeRefused)
		d.write(w, m)
		return
	}

	for _, q := range r.Question {
		switch q.Qtype {
		case dns.TypeA:
			rr := &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: dnsTTL},
				A:   net.IP(d.answer().AsSlice()),
			}
			m.Answer = append(m.Answer, rr)
		case dns.TypeAAAA:
			// NOERROR with no answer, not a hijacked v6 address. A client told there
			// is no AAAA falls back to the A record it just got; one handed a
			// nonsense AAAA tries it first and stalls until that times out, which is
			// the difference between a captive sheet appearing at once and appearing
			// after thirty seconds.
		default:
			// Same reasoning: an empty NOERROR moves the client along, where a
			// forged record for a type it asked for would send it somewhere useless.
		}
	}
	d.write(w, m)
}

func (d *DNSResponder) write(w dns.ResponseWriter, m *dns.Msg) {
	if err := w.WriteMsg(m); err != nil {
		d.logf("captive: dns write: %v", err)
	}
}

// ListenAndServe serves DNS on addr until ctx is cancelled.
//
// Both UDP and TCP: a resolver that gets a truncated or unexpected UDP answer
// retries over TCP, and a port that refuses the retry looks like a broken network
// rather than a portal.
func (d *DNSResponder) ListenAndServe(ctx context.Context, addr string) error {
	udp := &dns.Server{Addr: addr, Net: "udp", Handler: d}
	tcp := &dns.Server{Addr: addr, Net: "tcp", Handler: d}
	d.mu.Lock()
	d.udp, d.tcp = udp, tcp
	d.mu.Unlock()

	errs := make(chan error, 2)
	go func() { errs <- udp.ListenAndServe() }()
	go func() { errs <- tcp.ListenAndServe() }()

	select {
	case <-ctx.Done():
		d.Shutdown()
		return nil
	case err := <-errs:
		d.Shutdown()
		return err
	}
}

// Shutdown stops both listeners. It is safe to call more than once.
func (d *DNSResponder) Shutdown() {
	d.mu.Lock()
	udp, tcp := d.udp, d.tcp
	d.udp, d.tcp = nil, nil
	d.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if udp != nil {
		_ = udp.ShutdownContext(ctx)
	}
	if tcp != nil {
		_ = tcp.ShutdownContext(ctx)
	}
}
