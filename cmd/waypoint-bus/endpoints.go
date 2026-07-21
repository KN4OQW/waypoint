package main

import (
	"context"
	"fmt"
	"net"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/bus/peer"
	"github.com/KN4OQW/waypoint/internal/config"
)

// loopback is the fixed 127.0.0.1 UDP port pair a mode's gateway speaks on. The
// bus is the sole consumer of the pair (RFC-0003 §Motivation-2). Directionality
// mirrors internal/config/render.go's rendered gateway INIs:
//
//   - DMR: the bus rides the LOCAL DMRGateway as its repeater client (as the CM
//     DMR2YSF.ini does: [DMR Network] RptPort=62032 LocalPort=62031). DMRGateway
//     binds 62031 and sends to 62032, so the bus binds 62032 and sends to 62031.
//   - YSF: the bus REPLACES YSFGateway on MMDVM-Host's 3200/4200 pair. The gateway
//     binds 4200 (render.go ysfMMDVMGatewayPort) and sends to MMDVM-Host's 3200
//     (ysfMMDVMLocalPort), so the bus binds 4200 and sends to 3200.
//   - NXDN: the bus REPLACES NXDNGateway on 14020/14021 the same way (render.go
//     nxdnMMDVMGatewayPort/nxdnMMDVMLocalPort).
type loopback struct {
	bind int // UDP port the bus binds on 127.0.0.1 to receive this mode's frames
	peer int // UDP port on 127.0.0.1 the bus sends this mode's frames to
}

// loopbackFrom returns the loopback a bus's local attachment binds: the
// coordinated override from the rendered config when present (RFC-0003 Addendum A
// — a DMR attachment's reserved multiplex port), otherwise the fixed per-mode
// default. Reading the render's choice keeps the daemon from ever binding a stock
// port MMDVM-Host or a live gateway owns.
func loopbackFrom(bc config.BusConfig, m config.Mode) (loopback, error) {
	if lb, ok := bc.Loopbacks[string(m)]; ok {
		return loopback{bind: lb.Bind, peer: lb.Peer}, nil
	}
	return loopbackFor(m)
}

// loopbackFor returns the fixed loopback pair for a reframe-tier mode. It is a
// pure lookup (unit-testable) over the same constants render.go emits.
func loopbackFor(m config.Mode) (loopback, error) {
	switch m {
	case config.ModeDMR:
		return loopback{bind: 62032, peer: 62031}, nil // ride local DMRGateway
	case config.ModeYSF:
		return loopback{bind: 4200, peer: 3200}, nil // replace YSFGateway
	case config.ModeNXDN:
		return loopback{bind: 14020, peer: 14021}, nil // replace NXDNGateway
	}
	return loopback{}, fmt.Errorf("no loopback endpoint for mode %q (reframe tier is DMR/YSF/NXDN)", m)
}

// inbound is one frame entering the bus, tagged with the attachment (mode) it
// entered on — the origin tag §5 rule 1 fans out around. Exactly one source is
// set: `data` for a local loopback datagram (parsed by parseFrame), or `frame`+
// `env` for a frame injected off a peer link (already parsed by the owner's
// session handler, carrying the cross-peer envelope for loop prevention).
type inbound struct {
	mode  config.Mode
	data  []byte         // local loopback datagram (needs parseFrame)
	frame *frames.Frame  // peer-injected, already parsed
	env   *peer.Envelope // origin envelope for a peer-injected frame
}

// endpoint is one attachment's live UDP socket pair. When master is non-nil the
// endpoint is a DMR multiplex master (RFC-0003 Addendum A §Design-1): it speaks the
// Homebrew login handshake to the DMRGateway that dials it, and the reverse-path
// destination is the *learned* client address rather than the fixed peer.
type endpoint struct {
	mode   config.Mode
	conn   *net.UDPConn
	peer   *net.UDPAddr
	master *dmrMaster // non-nil ⇒ DMR multiplex master; nil ⇒ plain loopback
}

// multiplexMaster reports whether a DMR loopback is the reserved-multiplex port on
// which the bus must act as a Homebrew master. The render marks that port by
// setting Bind == Peer (a dedicated bus-owned port, e.g. 62100/62100), which no
// stock loopback pair ever does (a real pair is always bind ≠ peer, e.g.
// 62032/62031). See internal/config/loopback_handoff.go.
func multiplexMaster(m config.Mode, lb loopback) bool {
	return m == config.ModeDMR && lb.bind == lb.peer
}

// openEndpoint binds the receive port and resolves the peer for one mode. Binding
// fails loudly if another process (e.g. a still-running gateway) already owns the
// loopback — the bus must be the sole consumer.
func openEndpoint(m config.Mode, lb loopback) (*endpoint, error) {
	bindAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: lb.bind}
	conn, err := net.ListenUDP("udp", bindAddr)
	if err != nil {
		return nil, fmt.Errorf("bind 127.0.0.1:%d: %w", lb.bind, err)
	}
	ep := &endpoint{
		mode: m,
		conn: conn,
		peer: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: lb.peer},
	}
	if multiplexMaster(m, lb) {
		ep.master = newDMRMaster()
	}
	return ep, nil
}

// recv reads datagrams until the socket is closed (shutdown) and forwards each as
// a tagged inbound frame. A short/oversized read is passed through as-is; the
// parser rejects malformed input without panicking (frames fuzz contract). On a
// DMR multiplex master the Homebrew login/keepalive packets are answered inline
// and consumed — only DMRD voice is forwarded to the router.
func (e *endpoint) recv(ctx context.Context, out chan<- inbound) {
	buf := make([]byte, 2048)
	for {
		n, src, err := e.conn.ReadFromUDP(buf)
		if err != nil {
			return // socket closed on shutdown
		}
		if e.master != nil {
			reply, forward := e.master.handle(buf[:n], src)
			if reply != nil {
				_, _ = e.conn.WriteToUDP(reply, src)
			}
			if !forward {
				continue // login/keepalive packet consumed
			}
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		select {
		case out <- inbound{mode: e.mode, data: data}:
		case <-ctx.Done():
			return
		}
	}
}

// send emits one constructed frame to the mode's peer. For a DMR multiplex master
// the destination is the logged-in DMRGateway's learned address; a frame produced
// before any client has logged in is dropped (there is nowhere to send it yet).
func (e *endpoint) send(data []byte) error {
	dst := e.peer
	if e.master != nil {
		if dst = e.master.clientAddr(); dst == nil {
			return nil // no DMRGateway logged in yet — nothing to emit toward
		}
	}
	_, err := e.conn.WriteToUDP(data, dst)
	return err
}

func (e *endpoint) close() { _ = e.conn.Close() }
