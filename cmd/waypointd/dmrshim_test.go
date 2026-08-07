package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/dmrshim"
)

// freePorts hands back ports nothing is listening on, so the reconcile tests can
// bind for real rather than against a fake. The relay's whole job is holding two
// UDP sockets; a test that mocked them would not be testing it.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	var conns []*net.UDPConn
	var ports []int
	for i := 0; i < n; i++ {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatalf("reserve port: %v", err)
		}
		conns = append(conns, c)
		ports = append(ports, c.LocalAddr().(*net.UDPAddr).Port)
	}
	for _, c := range conns {
		_ = c.Close()
	}
	return ports
}

func wiringOn(ports []int) config.DMRShim {
	addr := func(p int) string { return fmt.Sprintf("127.0.0.1:%d", p) }
	return config.DMRShim{
		Enabled:     true,
		HostBind:    addr(ports[0]),
		HostPeer:    addr(ports[1]),
		GatewayBind: addr(ports[2]),
		GatewayPeer: addr(ports[3]),
	}
}

// The lifecycle an Apply drives: off, on, moved, off again. Each step has to leave
// the sockets in a state the next one can use — a relay that did not release its
// ports would fail to rebuild after a port change, which is exactly when an
// operator is watching.
func TestRelayReconcileLifecycle(t *testing.T) {
	r := &dmrRelay{}
	defer r.stop()
	ctx := context.Background()

	t.Run("off is silent", func(t *testing.T) {
		state, detail := r.reconcile(ctx, config.DMRShim{})
		if state != "" || detail != "" {
			t.Errorf("a switched-off relay reported %q/%q", state, detail)
		}
		if r.shimOrNil() != nil {
			t.Error("a switched-off relay bound sockets")
		}
	})

	first := wiringOn(freePorts(t, 4))
	t.Run("on binds and reports up", func(t *testing.T) {
		state, detail := r.reconcile(ctx, first)
		if state != statusUp {
			t.Fatalf("state = %q (%s), want up", state, detail)
		}
		sh := r.shimOrNil()
		if sh == nil {
			t.Fatal("no relay after switching it on")
		}
		if got := sh.HostBind().String(); got != first.HostBind {
			t.Errorf("bound %s, want %s", got, first.HostBind)
		}
	})

	t.Run("an unchanged wiring is not rebuilt", func(t *testing.T) {
		before := r.shimOrNil()
		if state, _ := r.reconcile(ctx, first); state != statusUp {
			t.Fatalf("state = %q, want up", state)
		}
		if r.shimOrNil() != before {
			t.Error("the relay was rebuilt for a wiring that did not change; every Apply would bounce DMR")
		}
	})

	second := wiringOn(freePorts(t, 4))
	t.Run("a moved port rebuilds", func(t *testing.T) {
		before := r.shimOrNil()
		state, detail := r.reconcile(ctx, second)
		if state != statusUp {
			t.Fatalf("state = %q (%s), want up", state, detail)
		}
		sh := r.shimOrNil()
		if sh == before {
			t.Fatal("the relay kept its old sockets after the ports moved")
		}
		if got := sh.HostBind().String(); got != second.HostBind {
			t.Errorf("bound %s, want %s", got, second.HostBind)
		}
	})

	t.Run("switching off releases the sockets", func(t *testing.T) {
		if state, _ := r.reconcile(ctx, config.DMRShim{}); state != "" {
			t.Error("a switched-off relay still reported a state")
		}
		if r.shimOrNil() != nil {
			t.Error("the relay was left running")
		}
		// The proof that the ports are really free: bind one.
		c, err := net.ListenUDP("udp", mustAddr(t, second.HostBind))
		if err != nil {
			t.Fatalf("the relay did not release %s: %v", second.HostBind, err)
		}
		_ = c.Close()
	})
}

// A port somebody else holds must report down with the reason, and must keep
// retrying: the usual cause is a previous relay that has not finished releasing
// it, and that resolves itself within a cycle.
func TestRelayReportsAPortItCannotBind(t *testing.T) {
	ports := freePorts(t, 4)
	held, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ports[0]})
	if err != nil {
		t.Fatalf("hold the port: %v", err)
	}

	r := &dmrRelay{}
	defer r.stop()
	want := wiringOn(ports)

	state, detail := r.reconcile(context.Background(), want)
	if state != statusDown {
		t.Fatalf("state = %q, want down", state)
	}
	if !strings.Contains(detail, "not running") || !strings.Contains(detail, "bind") {
		t.Errorf("detail = %q, want it to say what failed", detail)
	}
	if r.shimOrNil() != nil {
		t.Error("a relay was retained despite failing to bind")
	}

	// Release it and let the next cycle recover, with no intervention.
	_ = held.Close()
	if state, detail := r.reconcile(context.Background(), want); state != statusUp {
		t.Errorf("state = %q (%s), want up once the port was free", state, detail)
	}
}

// Degradation of the observation side must read as "unknown", not as up and not as
// down. The relay is forwarding — DMR is fine — but a message may have been
// missed, and that is precisely the case the link tri-state exists for.
func TestRelayReportsDegradedObservationAsUnknown(t *testing.T) {
	r := &dmrRelay{}
	defer r.stop()
	want := wiringOn(freePorts(t, 4))
	if state, _ := r.reconcile(context.Background(), want); state != statusUp {
		t.Fatal("setup: the relay did not come up")
	}

	sh := r.shimOrNil()
	sh.AddTap(func(dmrshim.Direction, []byte) { panic("a parser bug") })
	// Drive one datagram through so the tap runs.
	src, err := net.ListenUDP("udp", mustAddr(t, want.HostPeer))
	if err != nil {
		t.Fatalf("stand in for MMDVM-Host: %v", err)
	}
	defer src.Close()
	if _, err := src.WriteToUDP([]byte("DMRD"), mustAddr(t, want.HostBind)); err != nil {
		t.Fatal(err)
	}

	waitForRelay(t, "the relay to report degraded observation", func() bool {
		state, detail := r.reconcile(context.Background(), want)
		return state == statusUnknown && strings.Contains(detail, "DMR traffic is unaffected")
	})
}

// waitForRelay polls a condition that depends on a datagram crossing the loopback
// and a goroutine reacting to it — immediate, but not synchronous.
func waitForRelay(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func mustAddr(t *testing.T, s string) *net.UDPAddr {
	t.Helper()
	a, err := net.ResolveUDPAddr("udp", s)
	if err != nil {
		t.Fatalf("resolve %q: %v", s, err)
	}
	return a
}
