//go:build tier2

package tier2

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"
)

// A stub upstream DMR master: enough of the g4klx/BrandMeister "homebrew"
// protocol for a real DMRGateway to complete login and start passing traffic.
// It never checks the password hash, so no real credential is involved — the
// point is to observe *which* master DMRGateway routes a frame to, which is a
// property of the generated rewrite rules, not of upstream authentication.
//
// Sequence (from DMRGateway's DMRNetwork.cpp @ 79edbc4):
//
//	client RPTL + id(4)                  -> master RPTACK + salt(4)
//	client RPTK + id(4) + sha256(32)     -> master RPTACK + id(4)
//	client RPTC + id(4) + config         -> master RPTACK + id(4)
//	client RPTO + id(4) + options        -> master RPTACK + id(4)   (only if Options set)
//	client RPTPING + id(4)               -> master MSTPONG + id(4)
//	either  DMRD ... (55 bytes)
type stubMaster struct {
	name string
	conn *net.UDPConn

	mu       sync.Mutex
	loggedIn bool
	frames   [][]byte
	peer     *net.UDPAddr

	// echo, when set, mirrors a received DMRD back to the gateway after
	// swapping src/dst — a stand-in for the BrandMeister Parrot.
	echo bool

	done     chan struct{}
	closeOne sync.Once
}

func newStubMaster(name string, echo bool) (*stubMaster, error) {
	return newStubMasterOnPort(name, 0, echo)
}

// newStubMasterOnPort binds a chosen port (0 picks one). The resilience tests need
// a master that can go away and come back at the same address — DMRGateway is
// configured once and never re-reads where its master lives, so "the same master
// returned" and "a different one appeared" are only distinguishable by address.
func newStubMasterOnPort(name string, port int, echo bool) (*stubMaster, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		return nil, err
	}
	s := &stubMaster{name: name, conn: c, echo: echo, done: make(chan struct{})}
	go s.serve()
	return s, nil
}

// logout forgets the login without closing the socket, so a master that comes
// back can be observed completing a fresh handshake rather than being assumed to
// have done so.
func (s *stubMaster) logout() {
	s.mu.Lock()
	s.loggedIn = false
	s.mu.Unlock()
}

func (s *stubMaster) port() int { return s.conn.LocalAddr().(*net.UDPAddr).Port }

// close is idempotent: the resilience tests take a master away mid-scenario and
// the rig's cleanup closes whatever it still holds, so the same master is
// routinely closed twice.
func (s *stubMaster) close() {
	s.closeOne.Do(func() {
		close(s.done)
		s.conn.Close()
	})
}

func (s *stubMaster) serve() {
	buf := make([]byte, 1024)
	for {
		select {
		case <-s.done:
			return
		default:
		}
		s.conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		pkt := append([]byte(nil), buf[:n]...)
		s.mu.Lock()
		s.peer = addr
		s.mu.Unlock()

		switch {
		case n >= 4 && string(pkt[:4]) == "RPTL":
			// RPTACK + a 4-byte salt; any salt works because we never verify.
			out := append([]byte("RPTACK"), 0xDE, 0xAD, 0xBE, 0xEF)
			s.conn.WriteToUDP(out, addr)
		case n >= 4 && string(pkt[:4]) == "RPTK",
			n >= 4 && string(pkt[:4]) == "RPTC",
			n >= 4 && string(pkt[:4]) == "RPTO":
			out := append([]byte("RPTACK"), pkt[4:8]...)
			s.conn.WriteToUDP(out, addr)
			if string(pkt[:4]) == "RPTC" || string(pkt[:4]) == "RPTO" {
				s.mu.Lock()
				s.loggedIn = true
				s.mu.Unlock()
			}
		case n >= 7 && string(pkt[:7]) == "RPTPING":
			out := append([]byte("MSTPONG"), pkt[7:11]...)
			s.conn.WriteToUDP(out, addr)
		case n == 55 && string(pkt[:4]) == "DMRD":
			s.mu.Lock()
			s.frames = append(s.frames, pkt)
			s.mu.Unlock()
			if s.echo {
				s.conn.WriteToUDP(parrot(pkt), addr)
			}
		}
	}
}

func (s *stubMaster) isLoggedIn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loggedIn
}

func (s *stubMaster) received() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.frames...)
}

// sendRaw pushes an arbitrary datagram down to the gateway, for the cases that are
// about a packet type the master would send rather than about routing. It needs the
// login to have completed, because that is when the gateway's address is learned.
func (s *stubMaster) sendRaw(payload []byte) error {
	s.mu.Lock()
	peer := s.peer
	s.mu.Unlock()
	if peer == nil {
		return fmt.Errorf("%s: no gateway peer yet", s.name)
	}
	_, err := s.conn.WriteToUDP(payload, peer)
	return err
}

// waitLogin blocks until the gateway has completed the login handshake.
func (s *stubMaster) waitLogin(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s.isLoggedIn() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// --- DMRD frame helpers (layout per DMRNetwork.cpp read/write) -------------
//
//	[0:4]   "DMRD"
//	[4]     sequence number
//	[5:8]   source id (24-bit)
//	[8:11]  destination id (24-bit)
//	[11:15] repeater id (32-bit)
//	[15]    bit7 slot2, bit6 private, bit5 dataSync, bit4 voiceSync, bits3-0 n/dataType
//	[16:20] stream id
//	[20:53] 33 bytes of DMR payload (the AMBE-bearing part)
//	[53]    BER
//	[54]    RSSI
func dmrdSrc(f []byte) uint32 { return uint32(f[5])<<16 | uint32(f[6])<<8 | uint32(f[7]) }
func dmrdDst(f []byte) uint32 { return uint32(f[8])<<16 | uint32(f[9])<<8 | uint32(f[10]) }

func dmrdSetSrc(f []byte, id uint32) { f[5], f[6], f[7] = byte(id>>16), byte(id>>8), byte(id) }
func dmrdSetDst(f []byte, id uint32) { f[8], f[9], f[10] = byte(id>>16), byte(id>>8), byte(id) }

func dmrdSlot(f []byte) int {
	if f[15]&0x80 != 0 {
		return 2
	}
	return 1
}
func dmrdPrivate(f []byte) bool { return f[15]&0x40 != 0 }

// dmrdSetGroup clears the private-call bit. The real Parrot capture is a
// *private* call to 9990 (flags 0xe1), which is how BM Parrot is normally
// dialled — so a TG (group) rewrite must not match it, and doesn't. Tests that
// exercise talkgroup routing have to say "group call" explicitly.
func dmrdSetGroup(f []byte) { f[15] &^= 0x40 }

func dmrdStreamID(f []byte) uint32        { return binary.LittleEndian.Uint32(f[16:20]) }
func dmrdSetStreamID(f []byte, id uint32) { binary.LittleEndian.PutUint32(f[16:20], id) }

// parrot mirrors a frame back the way an echo service does: swap src and dst
// and make it a private call to the originator.
func parrot(in []byte) []byte {
	out := append([]byte(nil), in...)
	src, dst := dmrdSrc(in), dmrdDst(in)
	dmrdSetSrc(out, dst)
	dmrdSetDst(out, src)
	out[15] |= 0x40 // private call back to the caller
	return out
}

func describe(f []byte) string {
	kind := "voice"
	switch {
	case f[15]&0x20 != 0:
		kind = fmt.Sprintf("dataType=%d", f[15]&0x0F)
	case f[15]&0x10 != 0:
		kind = "voiceSync"
	}
	call := "group"
	if dmrdPrivate(f) {
		call = "private"
	}
	return fmt.Sprintf("src=%d dst=%d slot=%d %s %s stream=%08x",
		dmrdSrc(f), dmrdDst(f), dmrdSlot(f), call, kind, dmrdStreamID(f))
}
