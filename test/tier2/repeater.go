//go:build tier2

package tier2

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// repeaterSide stands in for MMDVM-Host on the DMR loopback. It binds the port
// MMDVM-Host binds (62032) and talks to DMRGateway on 62031, which is exactly
// where a keyed transmission enters the system. Nothing below this line — the
// modem, the RF decode — is involved, and nothing above it is faked.
type repeaterSide struct {
	conn *net.UDPConn
	gw   *net.UDPAddr

	mu       sync.Mutex
	received [][]byte
	done     chan struct{}
}

func newRepeaterSide(localPort, gatewayPort int) (*repeaterSide, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: localPort})
	if err != nil {
		return nil, fmt.Errorf("bind repeater side :%d: %w", localPort, err)
	}
	r := &repeaterSide{
		conn: c,
		gw:   &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: gatewayPort},
		done: make(chan struct{}),
	}
	go r.serve()
	return r, nil
}

func (r *repeaterSide) close() { close(r.done); r.conn.Close() }

func (r *repeaterSide) serve() {
	buf := make([]byte, 1024)
	for {
		select {
		case <-r.done:
			return
		default:
		}
		r.conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, _, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if n == 55 && string(buf[:4]) == "DMRD" {
			r.mu.Lock()
			r.received = append(r.received, append([]byte(nil), buf[:n]...))
			r.mu.Unlock()
		}
	}
}

func (r *repeaterSide) got() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.received...)
}

// connect sends the DMRC config packet. DMRGateway blocks in "Waiting for MMDVM
// to connect....." until it has both a config and an id > 1000 (DMRGateway.cpp
// @ 79edbc4, the loop after createMMDVM), and it forwards this config upstream
// in RPTC — so the stub masters see the same repeater identity a real host sends.
func (r *repeaterSide) connect(id uint32) error {
	cfg := repeaterConfig()
	pkt := make([]byte, 0, 8+len(cfg))
	pkt = append(pkt, "DMRC"...)
	pkt = append(pkt, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	pkt = append(pkt, cfg...)
	_, err := r.conn.WriteToUDP(pkt, r.gw)
	return err
}

// send injects one frame where MMDVM-Host would have put it after decoding RF.
func (r *repeaterSide) send(frame []byte) error {
	_, err := r.conn.WriteToUDP(frame, r.gw)
	return err
}

// ysfRepeaterSide stands in for MMDVM-Host on the YSF loopback: it binds the
// port MMDVM-Host binds (3200) and talks to the gateway on 4200. Like the DMR
// side, injection happens exactly where a decoded transmission enters.
//
// It must answer the gateway's "YSFP" polls: CYSFNetwork::write drops everything
// unless the link reached LINKED, and that transition only happens when a poll
// comes back (YSFNetwork.cpp @ YSFClients 2b480aa). A real MMDVM-Host polls the
// same way, so without this the echo path is silently dead.
type ysfRepeaterSide struct {
	conn     *net.UDPConn
	gw       *net.UDPAddr
	callsign string

	mu       sync.Mutex
	received [][]byte
	done     chan struct{}
}

func newYSFRepeaterSide(localPort, gatewayPort int, callsign string) (*ysfRepeaterSide, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: localPort})
	if err != nil {
		return nil, fmt.Errorf("bind YSF repeater side :%d: %w", localPort, err)
	}
	r := &ysfRepeaterSide{
		conn:     c,
		gw:       &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: gatewayPort},
		callsign: callsign,
		done:     make(chan struct{}),
	}
	go r.serve()
	return r, nil
}

func (r *ysfRepeaterSide) close() { close(r.done); r.conn.Close() }

func (r *ysfRepeaterSide) serve() {
	buf := make([]byte, 1024)
	nextPoll := time.Now()
	for {
		select {
		case <-r.done:
			return
		default:
		}
		// Poll on our own timer as well as on demand: the gateway's receive-poll
		// timer unlinks a silent repeater.
		if time.Now().After(nextPoll) {
			r.poll()
			nextPoll = time.Now().Add(time.Second)
		}
		r.conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, _, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		switch {
		case n >= 4 && string(buf[:4]) == "YSFP":
			r.poll()
		case n == 155 && string(buf[:4]) == "YSFD":
			r.mu.Lock()
			r.received = append(r.received, append([]byte(nil), buf[:n]...))
			r.mu.Unlock()
		}
	}
}

// poll sends the 14-byte "YSFP" + 10-byte callsign poll MMDVM-Host sends.
func (r *ysfRepeaterSide) poll() {
	pkt := make([]byte, 14)
	copy(pkt, "YSFP")
	cs := r.callsign
	for i := 0; i < 10; i++ {
		if i < len(cs) {
			pkt[4+i] = cs[i]
		} else {
			pkt[4+i] = ' '
		}
	}
	r.conn.WriteToUDP(pkt, r.gw)
}

func (r *ysfRepeaterSide) send(frame []byte) error {
	_, err := r.conn.WriteToUDP(frame, r.gw)
	return err
}

func (r *ysfRepeaterSide) got() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.received...)
}

// repeaterConfig is a standard homebrew repeater-config payload: fixed-width
// space-padded fields, 293 bytes, matching what MMDVM-Host sends.
func repeaterConfig() []byte {
	pad := func(s string, n int) string {
		for len(s) < n {
			s += " "
		}
		return s[:n]
	}
	s := pad("KN4OQW", 8) +
		pad("438800000", 9) +
		pad("438800000", 9) +
		pad("01", 2) +
		pad("01", 2) +
		pad("+00.0000", 8) +
		pad("+000.0000", 9) +
		pad("000", 3) +
		pad("Waypoint bench", 20) +
		pad("Tier2 loopback", 19) +
		pad("https://github.com/KN4OQW/waypoint", 124) +
		pad("20250101", 40) +
		pad("waypoint-tier2", 40)
	return []byte(s)
}
