// Package dmrshim relays the DMR loopback between MMDVM-Host and DMRGateway,
// with somewhere to watch the traffic and somewhere to add to it.
//
// # Why a relay and not a fork of either end
//
// Both daemons are upstream C++ that Waypoint renders configuration for and does
// not patch. To originate a data burst toward the radio, something has to put a
// DMRD frame on the wire that MMDVM-Host will accept — and MMDVM-Host accepts
// datagrams from exactly one address:port, the GatewayAddress:GatewayPort it was
// configured with (DMRNetwork.cpp: CUDPSocket::match(m_addr, address), which
// compares address AND port). Nothing else can speak to it. So the only seam
// available is to BE that address: sit on the loopback, forward everything, and
// occasionally add a frame of our own.
//
// The same seam is what a hotspot-side talker-alias injection would need, so it is
// built once and generically rather than as an SMS-specific side channel.
//
// # Wiring
//
//	MMDVM-Host                shim                 DMRGateway
//	  binds LocalPort ────────► hostBind
//	  (62032)          ◄──────── (62033)
//	                            gatewayBind ───────► binds LocalPort
//	                              (62034) ◄────────── (62031)
//
// Each socket both receives from and sends to its own peer, so the source address
// a daemon sees is always the port it was configured to expect. That symmetry is
// not decoration: get it wrong and MMDVM-Host logs "packet received from an
// invalid source" and drops every frame, which looks exactly like a dead link.
//
// # Fail-open
//
// Voice must never depend on this package's extras. The forwarding path does no
// allocation-free heroics but it also does nothing clever: read, forward, and hand
// a copy to the observers through a buffered channel that DROPS when full. A tap
// that blocks, panics or falls behind loses its own frames and delays nobody. A
// tap that panics is removed and counted. There is no error the observation side
// can raise that stops a frame reaching the other daemon.
package dmrshim

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
)

// maxDatagram is the read buffer. A DMRD frame is 55 bytes and the Homebrew
// login exchange is smaller still; 2048 matches cmd/waypoint-bus and leaves room
// for anything either daemon might grow.
const maxDatagram = 2048

// socketBuffer is the kernel receive buffer asked for on each socket.
//
// The default is around 200 KiB, which is far more than DMR needs — a burst is 55
// bytes every 60 ms per slot. It is raised anyway because the cost of being wrong
// is asymmetric: a relay that is descheduled during a GC pause or under load on a
// small Pi drops voice frames the kernel had already accepted, and a dropped voice
// frame is an audible gap with nothing anywhere to explain it. The request is
// advisory (the kernel clamps to net.core.rmem_max) and a failure to grant it is
// not worth failing to start over.
const socketBuffer = 4 << 20

// tapQueue is how many datagrams may be waiting for the observers before the
// relay starts dropping them. One second of full-rate DMR on both slots is about
// 34 frames, so 256 absorbs a long GC pause without ever making a voice frame
// wait on an observer.
const tapQueue = 256

// Direction says which way a datagram was travelling when it was observed.
type Direction int

const (
	// ToGateway is MMDVM-Host → DMRGateway: what the radio sent.
	ToGateway Direction = iota
	// ToHost is DMRGateway → MMDVM-Host: what the network sent.
	ToHost
)

func (d Direction) String() string {
	if d == ToGateway {
		return "to-gateway"
	}
	return "to-host"
}

// Tap observes one datagram. It is called from the shim's own goroutine, off the
// forwarding path, and MUST NOT retain the slice — the buffer is reused. Copy
// what you need.
//
// A tap that is slow costs itself frames and costs the relay nothing. A tap that
// panics is removed, and the panic is counted rather than taking the daemon down:
// a bug in message parsing must not be able to kill voice.
type Tap func(Direction, []byte)

// Config is the four addresses the relay sits between. All four are required;
// there are no defaults here because the renderer is the single place that
// decides ports, and a default in two places is a disagreement waiting to happen.
type Config struct {
	// HostBind is where the shim listens for MMDVM-Host, and therefore what
	// MMDVM-Host must have as its GatewayAddress:GatewayPort.
	HostBind string
	// HostPeer is where MMDVM-Host listens: its LocalAddress:LocalPort.
	HostPeer string
	// GatewayBind is where the shim listens for DMRGateway, and therefore what
	// DMRGateway must have as its RptAddress:RptPort.
	GatewayBind string
	// GatewayPeer is where DMRGateway listens: its LocalAddress:LocalPort.
	GatewayPeer string
}

func (c Config) validate() error {
	for _, f := range []struct {
		name, val string
	}{
		{"HostBind", c.HostBind}, {"HostPeer", c.HostPeer},
		{"GatewayBind", c.GatewayBind}, {"GatewayPeer", c.GatewayPeer},
	} {
		if f.val == "" {
			return fmt.Errorf("dmrshim: %s is required", f.name)
		}
	}
	return nil
}

// Stats is what the relay has seen and what it has had to give up on. Every
// counter that is not Forwarded is a thing an operator would otherwise have to
// discover from a packet capture.
type Stats struct {
	ForwardedToGateway uint64
	ForwardedToHost    uint64
	Injected           uint64
	// WrongSource counts datagrams from an address neither daemon should be
	// using. The relay drops them, exactly as MMDVM-Host does.
	WrongSource uint64
	// WriteErrors counts datagrams that could not be handed on. The usual cause
	// is the far daemon being restarted; the relay keeps reading either way.
	WriteErrors uint64
	// ReadErrors counts failed socket reads that were not a shutdown — almost
	// always an ICMP port-unreachable from a daemon that is restarting.
	ReadErrors uint64
	// TapDropped counts datagrams the observers were too slow to take. Voice was
	// unaffected; a message may have been missed.
	TapDropped uint64
	// TapPanics counts taps removed for panicking.
	TapPanics uint64
}

// Shim is the relay. Build it with New, run it with Run, and stop it by
// cancelling Run's context or calling Close.
type Shim struct {
	hostConn *net.UDPConn
	gwConn   *net.UDPConn
	hostPeer *net.UDPAddr
	gwPeer   *net.UDPAddr

	taps   chan tapped
	closed chan struct{}
	once   sync.Once

	mu       sync.Mutex
	handlers []Tap

	// Counters are atomic so Stats can be read from a status goroutine without
	// contending with the forwarding path.
	forwardedToGateway atomic.Uint64
	forwardedToHost    atomic.Uint64
	injected           atomic.Uint64
	wrongSource        atomic.Uint64
	writeErrors        atomic.Uint64
	readErrors         atomic.Uint64
	tapDropped         atomic.Uint64
	tapPanics          atomic.Uint64
}

type tapped struct {
	dir  Direction
	data []byte
}

// New binds both sockets. It fails if either port is already owned — by a
// still-running relay, or by a daemon whose configuration was not re-rendered —
// because a relay that silently did not bind would leave the loopback wired to
// nothing and every mode's DMR link dead.
func New(cfg Config) (*Shim, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	hostBind, err := net.ResolveUDPAddr("udp", cfg.HostBind)
	if err != nil {
		return nil, fmt.Errorf("dmrshim: HostBind %q: %w", cfg.HostBind, err)
	}
	gwBind, err := net.ResolveUDPAddr("udp", cfg.GatewayBind)
	if err != nil {
		return nil, fmt.Errorf("dmrshim: GatewayBind %q: %w", cfg.GatewayBind, err)
	}
	hostPeer, err := net.ResolveUDPAddr("udp", cfg.HostPeer)
	if err != nil {
		return nil, fmt.Errorf("dmrshim: HostPeer %q: %w", cfg.HostPeer, err)
	}
	gwPeer, err := net.ResolveUDPAddr("udp", cfg.GatewayPeer)
	if err != nil {
		return nil, fmt.Errorf("dmrshim: GatewayPeer %q: %w", cfg.GatewayPeer, err)
	}

	hostConn, err := net.ListenUDP("udp", hostBind)
	if err != nil {
		return nil, fmt.Errorf("dmrshim: bind %s for MMDVM-Host: %w", cfg.HostBind, err)
	}
	gwConn, err := net.ListenUDP("udp", gwBind)
	if err != nil {
		_ = hostConn.Close()
		return nil, fmt.Errorf("dmrshim: bind %s for DMRGateway: %w", cfg.GatewayBind, err)
	}
	// Best effort: a kernel that refuses the size still gives a working relay.
	_ = hostConn.SetReadBuffer(socketBuffer)
	_ = gwConn.SetReadBuffer(socketBuffer)
	return &Shim{
		hostConn: hostConn,
		gwConn:   gwConn,
		hostPeer: hostPeer,
		gwPeer:   gwPeer,
		taps:     make(chan tapped, tapQueue),
		closed:   make(chan struct{}),
	}, nil
}

// HostBind and GatewayBind report the addresses actually bound, which is how a
// test that asked for port 0 finds out what it got.
func (s *Shim) HostBind() *net.UDPAddr    { return s.hostConn.LocalAddr().(*net.UDPAddr) }
func (s *Shim) GatewayBind() *net.UDPAddr { return s.gwConn.LocalAddr().(*net.UDPAddr) }

// AddTap registers an observer and returns a function that removes it.
//
// Taps may be added and removed while the relay is running; later prompts attach
// the message capture this way rather than at construction, so a message feature
// that is switched off costs nothing but a nil check.
func (s *Shim) AddTap(t Tap) (remove func()) {
	if t == nil {
		return func() {}
	}
	// Removal nils the slot rather than shortening the slice, so an index handed
	// out earlier never comes to mean a different tap. The slice therefore grows
	// with add/remove churn; the callers are a handful of features toggling at
	// configuration time, not a per-frame allocation.
	s.mu.Lock()
	s.handlers = append(s.handlers, t)
	idx := len(s.handlers) - 1
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if idx < len(s.handlers) {
			s.handlers[idx] = nil
		}
	}
}

// Run pumps both directions until ctx is cancelled or Close is called. It always
// returns nil: a relay that gave up because a socket read failed would take the
// DMR link down with it, so read errors end the pump only when the sockets have
// actually been closed.
func (s *Shim) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); s.pump(s.hostConn, s.gwConn, s.gwPeer, s.hostPeer, ToGateway) }()
	go func() { defer wg.Done(); s.pump(s.gwConn, s.hostConn, s.hostPeer, s.gwPeer, ToHost) }()
	go func() { defer wg.Done(); s.fanOut() }()

	<-ctx.Done()
	s.Close()
	wg.Wait()
	return nil
}

// pump reads from in, forwards to out's peer, and hands a copy to the observers.
//
// from is the address datagrams on this socket are expected to come from. Anything
// else is dropped and counted, which is what MMDVM-Host does with the same
// question and keeps a stray process on the loopback from injecting into either
// daemon through us.
func (s *Shim) pump(in, out *net.UDPConn, dst, from *net.UDPAddr, dir Direction) {
	buf := make([]byte, maxDatagram)
	for {
		n, src, err := in.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return // shutting down, as expected
			}
			// Everything else is transient and local: an ICMP port-unreachable
			// because the far daemon is restarting surfaces here as a read error on
			// the NEXT read, once per undelivered datagram. Abandoning the link over
			// that would mean a gateway restart took DMR down until waypointd
			// noticed. Count it and read again.
			s.readErrors.Add(1)
			continue
		}
		if !sameAddr(src, from) {
			s.wrongSource.Add(1)
			continue
		}
		if _, err := out.WriteToUDP(buf[:n], dst); err != nil {
			s.writeErrors.Add(1)
		} else if dir == ToGateway {
			s.forwardedToGateway.Add(1)
		} else {
			s.forwardedToHost.Add(1)
		}
		s.offer(dir, buf[:n])
	}
}

// offer hands a copy of a datagram to the observers, or drops it. It never blocks:
// the whole point of the queue is that the forwarding path returns to the socket
// immediately whatever the observers are doing.
func (s *Shim) offer(dir Direction, data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	select {
	case s.taps <- tapped{dir: dir, data: cp}:
	default:
		s.tapDropped.Add(1)
	}
}

// fanOut delivers queued datagrams to the taps. A tap that panics is removed —
// once, with the panic counted — so a parser bug degrades observation instead of
// taking down the daemon that carries the node's voice.
//
// The queue is never closed. Closing it would race the forwarding path, which is
// allowed to be mid-offer when Close runs, and a send on a closed channel is a
// panic in the one goroutine that must never panic.
func (s *Shim) fanOut() {
	for {
		var t tapped
		select {
		case t = <-s.taps:
		case <-s.closed:
			return
		}
		s.mu.Lock()
		handlers := append([]Tap(nil), s.handlers...)
		s.mu.Unlock()
		for i, h := range handlers {
			if h == nil {
				continue
			}
			if s.callTap(h, t.dir, t.data) {
				continue
			}
			s.tapPanics.Add(1)
			s.mu.Lock()
			if i < len(s.handlers) {
				s.handlers[i] = nil
			}
			s.mu.Unlock()
		}
	}
}

// callTap runs one tap, reporting whether it returned normally.
func (s *Shim) callTap(h Tap, dir Direction, data []byte) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	h(dir, data)
	return true
}

// ErrClosed is returned by Inject after the relay has shut down.
var ErrClosed = errors.New("dmrshim: closed")

// InjectToHost sends a datagram to MMDVM-Host as though DMRGateway had sent it.
// This is how a locally originated message reaches the radio.
//
// It does NOT arbitrate against traffic already in flight. Deciding when it is
// safe to transmit is the caller's problem, because only the caller knows whether
// what it is sending can wait.
func (s *Shim) InjectToHost(datagram []byte) error {
	return s.inject(s.hostConn, s.hostPeer, datagram)
}

// InjectToGateway sends a datagram to DMRGateway as though MMDVM-Host had sent
// it — a locally originated message headed for the network rather than the radio.
func (s *Shim) InjectToGateway(datagram []byte) error {
	return s.inject(s.gwConn, s.gwPeer, datagram)
}

func (s *Shim) inject(conn *net.UDPConn, dst *net.UDPAddr, datagram []byte) error {
	select {
	case <-s.closed:
		return ErrClosed
	default:
	}
	if len(datagram) == 0 {
		return fmt.Errorf("dmrshim: refusing to inject an empty datagram")
	}
	if _, err := conn.WriteToUDP(datagram, dst); err != nil {
		s.writeErrors.Add(1)
		return fmt.Errorf("dmrshim: inject: %w", err)
	}
	s.injected.Add(1)
	return nil
}

// Stats reports the counters. Safe to call from any goroutine.
func (s *Shim) Stats() Stats {
	return Stats{
		ForwardedToGateway: s.forwardedToGateway.Load(),
		ForwardedToHost:    s.forwardedToHost.Load(),
		Injected:           s.injected.Load(),
		WrongSource:        s.wrongSource.Load(),
		WriteErrors:        s.writeErrors.Load(),
		ReadErrors:         s.readErrors.Load(),
		TapDropped:         s.tapDropped.Load(),
		TapPanics:          s.tapPanics.Load(),
	}
}

// Degraded reports whether observation has lost anything, with a sentence saying
// what. Forwarding is never degraded — if it were, the relay would be down rather
// than degraded — so this answers "can I trust that I saw every message", not "is
// the link up".
func (s *Shim) Degraded() (bool, string) {
	st := s.Stats()
	switch {
	case st.TapPanics > 0:
		return true, fmt.Sprintf("message capture stopped after %d failure(s); DMR traffic is unaffected", st.TapPanics)
	case st.TapDropped > 0:
		return true, fmt.Sprintf("%d datagram(s) not seen by message capture; DMR traffic is unaffected", st.TapDropped)
	}
	return false, ""
}

// Close shuts the relay down. It is safe to call more than once and safe to call
// concurrently with Run.
//
// It returns nothing. Closing a UDP socket that is being torn down has no failure
// a caller could act on, and an error return nobody can use is an error return
// every caller discards — which is how the one that matters gets discarded too.
func (s *Shim) Close() {
	s.once.Do(func() {
		close(s.closed)
		_ = s.hostConn.Close()
		_ = s.gwConn.Close()
	})
}

// sameAddr compares an observed source against the address a daemon was
// configured to speak from. IP.Equal handles the 4-in-6 form the kernel may hand
// back for a v4 loopback socket, which a byte comparison would get wrong.
func sameAddr(a, b *net.UDPAddr) bool {
	return a != nil && b != nil && a.Port == b.Port && a.IP.Equal(b.IP)
}
