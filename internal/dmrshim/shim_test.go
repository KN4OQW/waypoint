package dmrshim

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A stand-in for one of the two daemons: a UDP socket on the loopback that can
// send to the shim and collect what the shim sends back. Both MMDVM-Host and
// DMRGateway are, from the relay's point of view, exactly this.
type fakeDaemon struct {
	conn *net.UDPConn
	mu   sync.Mutex
	got  [][]byte
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	// Match the relay's own buffer, so a drop in this test is the relay's doing
	// and not the harness running out of kernel buffer on a loaded machine.
	_ = conn.SetReadBuffer(socketBuffer)
	d := &fakeDaemon{conn: conn}
	go d.recv()
	t.Cleanup(func() { _ = conn.Close() })
	return d
}

func (d *fakeDaemon) recv() {
	buf := make([]byte, 2048)
	for {
		n, _, err := d.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		cp := make([]byte, n)
		copy(cp, buf[:n])
		d.mu.Lock()
		d.got = append(d.got, cp)
		d.mu.Unlock()
	}
}

func (d *fakeDaemon) addr() string { return d.conn.LocalAddr().String() }

func (d *fakeDaemon) send(t *testing.T, to *net.UDPAddr, b []byte) {
	t.Helper()
	if _, err := d.conn.WriteToUDP(b, to); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func (d *fakeDaemon) received() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]byte(nil), d.got...)
}

// waitFor polls until cond holds or the deadline passes. UDP on the loopback is
// immediate but not synchronous, and a fixed sleep is either flaky or slow.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitUpTo(t, 2*time.Second, what, cond)
}

// waitUpTo is waitFor with an explicit budget, for the soak — which has real work
// to drain and runs alongside the rest of the suite on whatever machine CI gave
// it.
func waitUpTo(t *testing.T, budget time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// startShim wires a relay between two fake daemons and runs it, returning the
// relay and the two daemons in (host, gateway) order.
func startShim(t *testing.T) (*Shim, *fakeDaemon, *fakeDaemon) {
	t.Helper()
	host, gw := newFakeDaemon(t), newFakeDaemon(t)
	s, err := New(Config{
		HostBind:    "127.0.0.1:0",
		HostPeer:    host.addr(),
		GatewayBind: "127.0.0.1:0",
		GatewayPeer: gw.addr(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after its context was cancelled")
		}
	})
	return s, host, gw
}

// A relay that changed a byte would be worse than no relay: the checksums inside
// a DMR burst are computed by the daemon at each end, and a corrupted frame is a
// dropped transmission with no error anywhere. Every datagram, both directions,
// arrives identical.
func TestBytesPassThroughUnchanged(t *testing.T) {
	s, host, gw := startShim(t)
	rng := rand.New(rand.NewSource(11))

	var toGateway, toHost [][]byte
	for i := 0; i < 200; i++ {
		// A spread of shapes: real DMRD-sized frames, the small Homebrew login
		// packets, and the extremes a length bug would trip over.
		n := []int{55, 8, 1, 4, 302, maxDatagram}[i%6]
		b := make([]byte, n)
		rng.Read(b)
		if n >= 4 {
			copy(b, "DMRD")
		}
		cp := append([]byte(nil), b...)
		if i%2 == 0 {
			host.send(t, s.HostBind(), b)
			toGateway = append(toGateway, cp)
		} else {
			gw.send(t, s.GatewayBind(), b)
			toHost = append(toHost, cp)
		}
	}

	waitFor(t, "every datagram to arrive", func() bool {
		return len(gw.received()) == len(toGateway) && len(host.received()) == len(toHost)
	})

	// UDP on the loopback does not reorder, so position is meaningful and a
	// mismatch here is the relay's fault rather than the network's.
	for i, want := range toGateway {
		if got := gw.received()[i]; !bytes.Equal(got, want) {
			t.Fatalf("to-gateway datagram %d differs: %d bytes vs %d", i, len(got), len(want))
		}
	}
	for i, want := range toHost {
		if got := host.received()[i]; !bytes.Equal(got, want) {
			t.Fatalf("to-host datagram %d differs: %d bytes vs %d", i, len(got), len(want))
		}
	}
	// The counter is incremented AFTER the write, so a datagram can be at its
	// destination microseconds before the relay has finished counting it. Wait for
	// the counters rather than reading them the instant the last frame lands.
	waitFor(t, "the forward counters to settle", func() bool {
		st := s.Stats()
		return st.ForwardedToGateway == uint64(len(toGateway)) && st.ForwardedToHost == uint64(len(toHost))
	})
}

// The relay must send from the port each daemon was configured to expect.
// MMDVM-Host compares the source address AND port against its configured gateway
// (DMRNetwork.cpp, CUDPSocket::match) and drops anything else with a log line that
// looks like a dead link. A relay that replied from an ephemeral socket would work
// in a test that ignored source addresses and fail on the bench.
func TestRepliesComeFromTheConfiguredPort(t *testing.T) {
	_, _, gw := startShim(t)

	// Read the source address the daemon actually sees, rather than trusting the
	// relay to describe itself.
	seen := make(chan *net.UDPAddr, 4)
	probe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			_, src, err := probe.ReadFromUDP(buf)
			if err != nil {
				return
			}
			seen <- src
		}
	}()

	// A relay whose host peer is the probe, so we observe exactly what MMDVM-Host
	// would observe about the source of what arrives.
	s2, err := New(Config{
		HostBind: "127.0.0.1:0", HostPeer: probe.LocalAddr().String(),
		GatewayBind: "127.0.0.1:0", GatewayPeer: gw.addr(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s2.Run(ctx) }()

	gw.send(t, s2.GatewayBind(), []byte("DMRDfromgateway"))
	select {
	case src := <-seen:
		if src.Port != s2.HostBind().Port {
			t.Errorf("MMDVM-Host would see source port %d, want the shim's host bind %d",
				src.Port, s2.HostBind().Port)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing arrived at the host peer")
	}
}

// Injection is the whole reason the relay exists. A frame injected toward the host
// must arrive there and must NOT be echoed to the gateway — an injected message is
// for the radio, and sending it upstream too would put it on the network.
func TestInjectReachesOneSideOnly(t *testing.T) {
	s, host, gw := startShim(t)

	frame := make([]byte, 55)
	copy(frame, "DMRD")
	frame[4] = 0x01
	binary.BigEndian.PutUint32(frame[16:], 0x11223344)

	if err := s.InjectToHost(frame); err != nil {
		t.Fatalf("InjectToHost: %v", err)
	}
	waitFor(t, "the injected frame at the host", func() bool { return len(host.received()) == 1 })
	if got := host.received()[0]; !bytes.Equal(got, frame) {
		t.Errorf("injected frame arrived changed")
	}
	if n := len(gw.received()); n != 0 {
		t.Errorf("the gateway saw %d datagrams; an injection toward the host is not for it", n)
	}

	if err := s.InjectToGateway(frame); err != nil {
		t.Fatalf("InjectToGateway: %v", err)
	}
	waitFor(t, "the injected frame at the gateway", func() bool { return len(gw.received()) == 1 })
	if n := s.Stats().Injected; n != 2 {
		t.Errorf("Injected = %d, want 2", n)
	}
}

func TestInjectRejections(t *testing.T) {
	s, _, _ := startShim(t)
	if err := s.InjectToHost(nil); err == nil {
		t.Error("an empty injection was accepted")
	}
	s.Close()
	if err := s.InjectToHost([]byte("DMRD")); err != ErrClosed {
		t.Errorf("after Close: err = %v, want ErrClosed", err)
	}
}

// Both directions reach the taps, tagged, and with the bytes intact.
func TestTapSeesBothDirections(t *testing.T) {
	s, host, gw := startShim(t)

	var mu sync.Mutex
	seen := map[Direction][][]byte{}
	remove := s.AddTap(func(d Direction, b []byte) {
		mu.Lock()
		defer mu.Unlock()
		seen[d] = append(seen[d], append([]byte(nil), b...))
	})

	host.send(t, s.HostBind(), []byte("from-the-radio"))
	gw.send(t, s.GatewayBind(), []byte("from-the-network"))

	waitFor(t, "both directions at the tap", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen[ToGateway]) == 1 && len(seen[ToHost]) == 1
	})
	mu.Lock()
	if string(seen[ToGateway][0]) != "from-the-radio" || string(seen[ToHost][0]) != "from-the-network" {
		t.Errorf("tap saw %q / %q", seen[ToGateway][0], seen[ToHost][0])
	}
	mu.Unlock()

	// After removal a tap stops being called, and forwarding carries on.
	remove()
	host.send(t, s.HostBind(), []byte("after-removal"))
	waitFor(t, "forwarding to continue after tap removal", func() bool { return len(gw.received()) == 2 })
	mu.Lock()
	defer mu.Unlock()
	if len(seen[ToGateway]) != 1 {
		t.Errorf("a removed tap was called %d times", len(seen[ToGateway]))
	}
}

// Fail-open, the part that matters most: a tap that panics must cost only itself.
// A parser bug in the message feature cannot be allowed to take out the relay that
// carries the node's voice.
func TestAPanickingTapDoesNotStopForwarding(t *testing.T) {
	s, host, gw := startShim(t)

	var calls atomic.Int64
	s.AddTap(func(Direction, []byte) {
		calls.Add(1)
		panic("a parser bug, as they happen")
	})
	// A second, well-behaved tap must keep working after the first one is removed.
	var good atomic.Int64
	s.AddTap(func(Direction, []byte) { good.Add(1) })

	for i := 0; i < 20; i++ {
		host.send(t, s.HostBind(), []byte(fmt.Sprintf("frame-%d", i)))
	}
	waitFor(t, "every frame to be forwarded", func() bool { return len(gw.received()) == 20 })
	waitFor(t, "the surviving tap to see them", func() bool { return good.Load() == 20 })

	if n := calls.Load(); n != 1 {
		t.Errorf("the panicking tap was called %d times, want 1 (it should be removed)", n)
	}
	if s.Stats().TapPanics != 1 {
		t.Errorf("TapPanics = %d, want 1", s.Stats().TapPanics)
	}
	if degraded, why := s.Degraded(); !degraded || why == "" {
		t.Error("a removed tap left the relay reporting healthy observation")
	}
}

// A tap that blocks forever must not hold up a single frame. This is the fail-open
// property stated as a latency bound rather than a correctness one.
func TestABlockingTapDoesNotDelayForwarding(t *testing.T) {
	s, host, gw := startShim(t)

	release := make(chan struct{})
	s.AddTap(func(Direction, []byte) { <-release })
	defer close(release)

	const frames = tapQueue + 64
	start := time.Now()
	for i := 0; i < frames; i++ {
		host.send(t, s.HostBind(), []byte("voice"))
	}
	waitFor(t, "every frame to reach the gateway despite the wedged tap", func() bool {
		return len(gw.received()) == frames
	})
	if el := time.Since(start); el > time.Second {
		t.Errorf("forwarding took %s behind a wedged tap", el)
	}
	if s.Stats().TapDropped == 0 {
		t.Error("the queue never overflowed; the test did not exercise the drop path")
	}
	if degraded, why := s.Degraded(); !degraded || why == "" {
		t.Error("dropped observations were not reported as degraded")
	}
}

// A stray process on the loopback must not be able to speak to either daemon
// through the relay. This is the same check MMDVM-Host makes about us.
func TestDatagramsFromAnUnexpectedSourceAreDropped(t *testing.T) {
	s, _, gw := startShim(t)

	stray, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer stray.Close()
	if _, err := stray.WriteToUDP([]byte("DMRDnotfromthehost"), s.HostBind()); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the stray datagram to be counted", func() bool { return s.Stats().WrongSource == 1 })
	if n := len(gw.received()); n != 0 {
		t.Errorf("the gateway received %d datagrams from a stray source", n)
	}
}

// The soak: sustained traffic in both directions for long enough that a dropping
// or reordering bug in the relay would show. In-process only — voice on real
// hardware is the hardware-validation prompt's job.
//
// The rate is deliberate rather than as-fast-as-possible. Real DMR is one 55-byte
// burst per timeslot per 60 ms: two slots in two directions is about 66 datagrams
// a second. This runs at roughly 2,600 a second, about 40x that, which is a hard
// test of the relay and still well inside what a loopback socket carries without
// the KERNEL dropping frames. Pushing to tens of thousands a second, as an earlier
// version of this test did, measures the harness's socket buffers rather than the
// relay: UDP loss under that load is expected behaviour and asserting against it
// produced a test that failed only when the machine was busy.
func TestSoakNoLossNoReordering(t *testing.T) {
	if testing.Short() {
		t.Skip("soak")
	}
	s, host, gw := startShim(t)

	const (
		frames = 1200
		batch  = 4
		pause  = 3 * time.Millisecond
	)
	send := func(d *fakeDaemon, to *net.UDPAddr, tag byte) {
		for i := 0; i < frames; i++ {
			b := make([]byte, 55)
			copy(b, "DMRD")
			b[4] = tag
			binary.BigEndian.PutUint32(b[5:], uint32(i))
			d.send(t, to, b)
			if i%batch == batch-1 {
				time.Sleep(pause)
			}
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); send(host, s.HostBind(), 1) }()
	go func() { defer wg.Done(); send(gw, s.GatewayBind(), 2) }()
	wg.Wait()

	waitUpTo(t, 15*time.Second, "the soak to drain", func() bool {
		return len(gw.received()) == frames && len(host.received()) == frames
	})

	// Sequence numbers carry the assertion: a gap is a drop, an out-of-order value
	// is a reorder, and either would corrupt a transmission on the air.
	check := func(name string, got [][]byte, tag byte) {
		for i, b := range got {
			if len(b) != 55 || b[4] != tag {
				t.Fatalf("%s frame %d is not the one that was sent: %x", name, i, b[:8])
			}
			if seq := binary.BigEndian.Uint32(b[5:]); seq != uint32(i) {
				t.Fatalf("%s frame %d carries sequence %d — reordered or dropped", name, i, seq)
			}
		}
	}
	check("to-gateway", gw.received(), 1)
	check("to-host", host.received(), 2)

	if st := s.Stats(); st.WriteErrors != 0 || st.WrongSource != 0 {
		t.Errorf("the soak produced errors: %+v", st)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"no host bind", Config{HostPeer: "127.0.0.1:1", GatewayBind: "127.0.0.1:0", GatewayPeer: "127.0.0.1:2"}},
		{"no gateway peer", Config{HostBind: "127.0.0.1:0", HostPeer: "127.0.0.1:1", GatewayBind: "127.0.0.1:0"}},
		{"unresolvable", Config{HostBind: "not an address", HostPeer: "127.0.0.1:1", GatewayBind: "127.0.0.1:0", GatewayPeer: "127.0.0.1:2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New(tc.cfg)
			if err == nil {
				s.Close()
				t.Fatal("bad config was accepted")
			}
		})
	}
}

// A port already owned must fail loudly. A relay that quietly did not bind would
// leave MMDVM-Host talking to nothing, which reads on the dashboard as a dead
// upstream rather than as a misconfiguration.
func TestNewFailsOnABoundPort(t *testing.T) {
	held, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	s, err := New(Config{
		HostBind: held.LocalAddr().String(), HostPeer: "127.0.0.1:1",
		GatewayBind: "127.0.0.1:0", GatewayPeer: "127.0.0.1:2",
	})
	if err == nil {
		s.Close()
		t.Fatal("bound the port a live socket already owns")
	}
	// And the second socket must not be left open when the first one fails, or a
	// retry would fail on the relay's own leak.
	s2, err := New(Config{
		HostBind: "127.0.0.1:0", HostPeer: "127.0.0.1:1",
		GatewayBind: held.LocalAddr().String(), GatewayPeer: "127.0.0.1:2",
	})
	if err == nil {
		s2.Close()
		t.Fatal("bound the port a live socket already owns")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	s, _, _ := startShim(t)
	for i := 0; i < 3; i++ {
		s.Close() // must not panic on the second or third call
	}
	if err := s.InjectToHost([]byte("DMRD")); err != ErrClosed {
		t.Errorf("after repeated Close: err = %v, want ErrClosed", err)
	}
}
