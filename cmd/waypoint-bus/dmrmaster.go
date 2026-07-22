package main

import (
	"crypto/rand"
	"net"
	"sync"
)

// dmrMaster implements the minimal MMDVM/Homebrew DMR *master* handshake the bus
// must speak on a reserved multiplex port (RFC-0003 Addendum A §Design-1). When a
// DMR mode multiplexes onto the live DMRGateway, DMRGateway dials the bus as one
// more `[DMR Network N]` — i.e. it is the Homebrew *client* (RPTL/RPTK/RPTC login,
// RPTPING keepalive) and the bus is the *master* it logs into. Without a master
// answering, DMRGateway's login never completes and no DMRD voice ever reaches the
// bus. This closes RFC-0003 Addendum A Open Question 3.
//
// Auth is accept-any by design: the peer is a 127.0.0.1 loopback, not a network
// boundary (the render marks the rendered password "not a network credential").
// The master still issues a real per-login salt so the client's own auth flow
// completes; it simply does not reject the result. What the master *does* need is
// the client's live UDP address, learned from the login exchange, so the reverse
// path (a reframed YSF/NXDN->DMR emission) can be delivered back to DMRGateway.
//
// The type is deliberately socket-free and pure apart from salt generation (which
// is injectable) so the state machine is unit-tested without a live DMRGateway.
type dmrMaster struct {
	saltFn func() [4]byte // injectable for tests; defaults to crypto/rand

	mu     sync.Mutex
	client *net.UDPAddr // the logged-in DMRGateway's live address (reverse path)
	id     [4]byte      // the client's 4-byte repeater id, echoed in ACK/PONG
	salt   [4]byte      // this login's nonce
	authed bool         // RPTK seen (login effectively complete)
}

func newDMRMaster() *dmrMaster {
	return &dmrMaster{saltFn: randSalt}
}

func randSalt() [4]byte {
	var b [4]byte
	_, _ = rand.Read(b[:]) // a failed read leaves zeros — harmless on a trusted loopback
	return b
}

// hasPrefix reports whether b starts with the ASCII command tag s.
func hasPrefix(b []byte, s string) bool {
	if len(b) < len(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if b[i] != s[i] {
			return false
		}
	}
	return true
}

// handle processes one datagram from src. It returns the bytes to write back to
// src (nil for none) and whether the datagram is DMRD voice that must be forwarded
// to the router. Command tags are checked longest-first because the short tags are
// prefixes of the long ones ("RPTC" ⊂ "RPTCL", "RPTP" ⊂ "RPTPING").
func (d *dmrMaster) handle(pkt []byte, src *net.UDPAddr) (reply []byte, forward bool) {
	switch {
	case hasPrefix(pkt, "DMRD"):
		// Voice data — remember the sender (a client that skipped straight to data
		// after a prior login) and forward for reframing.
		d.setClient(src)
		return nil, true

	case hasPrefix(pkt, "RPTPING"):
		// Keepalive: MSTPONG + the repeater id keeps DMRGateway's network "linked".
		d.setClient(src)
		return append([]byte("MSTPONG"), d.idBytes()...), false

	case hasPrefix(pkt, "RPTCL"):
		// Client closing down: forget it so the reverse path stops emitting.
		d.reset()
		return nil, false

	case hasPrefix(pkt, "RPTL"):
		// Login request: RPTL + 4-byte id. Answer RPTACK + a fresh 4-byte salt.
		var id [4]byte
		if len(pkt) >= 8 {
			copy(id[:], pkt[4:8])
		}
		salt := d.saltFn()
		d.mu.Lock()
		d.client, d.id, d.salt, d.authed = src, id, salt, false
		d.mu.Unlock()
		return append([]byte("RPTACK"), salt[:]...), false

	case hasPrefix(pkt, "RPTK"):
		// Auth key: RPTK + id + SHA256(salt||password). Accept-any on the loopback.
		d.mu.Lock()
		d.client, d.authed = src, true
		id := d.id
		d.mu.Unlock()
		return append([]byte("RPTACK"), id[:]...), false

	case hasPrefix(pkt, "RPTC"):
		// Repeater config: acknowledge so the client transitions to RUNNING and
		// starts forwarding DMRD. (Checked after RPTCL since "RPTC" ⊂ "RPTCL".)
		d.setClient(src)
		return append([]byte("RPTACK"), d.idBytes()...), false
	}
	return nil, false // RPTO/options and anything else: ignore, do not forward
}

// clientAddr returns the logged-in client's address for the reverse (bus->DMR)
// path, or nil if no client has logged in yet.
func (d *dmrMaster) clientAddr() *net.UDPAddr {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.client
}

func (d *dmrMaster) setClient(src *net.UDPAddr) {
	d.mu.Lock()
	if src != nil {
		d.client = src
	}
	d.mu.Unlock()
}

func (d *dmrMaster) idBytes() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := d.id
	return id[:]
}

func (d *dmrMaster) reset() {
	d.mu.Lock()
	d.client, d.authed = nil, false
	d.mu.Unlock()
}
