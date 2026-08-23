package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/dmrshim"
	"github.com/KN4OQW/waypoint/internal/idresolve"
	"github.com/KN4OQW/waypoint/internal/phonebook"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/internal/talkeralias"
)

// The injector over a real shim on real loopback sockets.
//
// The claim these tests exist for is the blank-omit one: a node that has not
// configured Talker Alias must put exactly the bytes on the seam that it put
// there before the feature existed. That is only checkable against a live relay,
// counting what MMDVM-Host's socket actually receives.

// seamRig stands up a shim between two fake daemons and returns the sockets they
// would be reading from.
type seamRig struct {
	shim    *dmrshim.Shim
	host    *net.UDPConn // stands in for MMDVM-Host
	gateway *net.UDPConn // stands in for DMRGateway
}

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func newSeamRig(t *testing.T) *seamRig {
	t.Helper()
	bind := func() *net.UDPConn {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() }) //nolint:errcheck // test cleanup
		return c
	}
	host, gw := bind(), bind()

	// Port 0 lets the kernel choose, and the shim reports what it actually bound
	// (HostBind/GatewayBind). Picking a port by binding a probe socket and reading
	// its address does not work: the probe still holds the port, so the shim's own
	// bind fails — which is how the first version of this test came to skip rather
	// than run, proving nothing.
	sh, err := dmrshim.New(dmrshim.Config{
		HostBind:    "127.0.0.1:0",
		HostPeer:    host.LocalAddr().String(),
		GatewayBind: "127.0.0.1:0",
		GatewayPeer: gw.LocalAddr().String(),
	})
	if err != nil {
		t.Fatalf("stand up the shim on loopback: %v", err)
	}
	return &seamRig{shim: sh, host: host, gateway: gw}
}

// drain reads everything the host socket receives within a short window.
func (r *seamRig) drain(t *testing.T) [][]byte {
	t.Helper()
	var out [][]byte
	_ = r.host.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	buf := make([]byte, 2048)
	for {
		n, _, err := r.host.ReadFromUDP(buf)
		if err != nil {
			return out
		}
		out = append(out, append([]byte(nil), buf[:n]...))
	}
}

func testChain(t *testing.T) *idresolve.Chain {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() }) //nolint:errcheck // test cleanup
	pb := phonebook.New(st)
	if _, err := pb.Create(phonebook.Entry{Callsign: "KN4OQW", DMRID: 3180202, FullName: "Clint Chance"}); err != nil {
		t.Fatal(err)
	}
	return idresolve.New(pb, nil)
}

// voiceLCHeader is a DMRD voice LC header as MMDVM-Host writes one.
func voiceLCHeader(srcID uint32, slot int) []byte {
	d := make([]byte, 55)
	copy(d, "DMRD")
	d[5], d[6], d[7] = byte(srcID>>16), byte(srcID>>8), byte(srcID)
	flags := byte(0x20 | 0x01) // data frame, DT_VOICE_LC_HEADER
	if slot == 2 {
		flags |= 0x80
	}
	d[15] = flags
	return d
}

// countDMRA reports how many of the datagrams are Talker Alias frames.
func countDMRA(datagrams [][]byte) int {
	n := 0
	for _, d := range datagrams {
		if len(d) == 15 && string(d[0:4]) == "DMRA" {
			n++
		}
	}
	return n
}

// TestFeatureOffPutsNothingExtraOnTheSeam is the contract that matters most: an
// unconfigured node's traffic is byte-identical to a build without the feature.
//
// It is asserted against what MMDVM-Host's socket actually receives, not against
// the emitter in isolation, because the claim is about the wire.
func TestFeatureOffPutsNothingExtraOnTheSeam(t *testing.T) {
	for _, tmpl := range []talkeralias.Template{
		talkeralias.TemplateOff,
		talkeralias.Template("something a newer build wrote"),
	} {
		rig := newSeamRig(t)
		ctx, cancel := contextWithCancel()
		defer cancel()
		go func() { _ = rig.shim.Run(ctx) }()
		time.Sleep(50 * time.Millisecond)

		inj := &taInjector{}
		inj.reconcile(rig.shim, tmpl, testChain(t))
		defer inj.stop()

		frame := voiceLCHeader(3180202, 1)
		if _, err := rig.gateway.WriteToUDP(frame, rig.shim.GatewayBind()); err != nil {
			t.Fatal(err)
		}
		got := rig.drain(t)

		if n := countDMRA(got); n != 0 {
			t.Errorf("template %q: %d DMRA frames reached the host; an unset feature must emit none", tmpl, n)
		}
		// And the voice frame itself still arrived, unchanged.
		if len(got) != 1 {
			t.Fatalf("template %q: host received %d datagrams, want exactly the forwarded frame", tmpl, len(got))
		}
		if string(got[0]) != string(frame) {
			t.Errorf("template %q: the forwarded frame was altered", tmpl)
		}
		cancel()
	}
}

// TestInjectionAddsFramesAndLeavesVoiceAlone: with the feature on, the alias
// arrives AND the voice frame is still forwarded byte-for-byte. The shim's
// contract is that an observer never mutates or blocks a frame, and injection
// must not be the exception.
func TestInjectionAddsFramesAndLeavesVoiceAlone(t *testing.T) {
	rig := newSeamRig(t)
	ctx, cancel := contextWithCancel()
	defer cancel()
	go func() { _ = rig.shim.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	inj := &taInjector{}
	inj.reconcile(rig.shim, talkeralias.TemplateCallsignName, testChain(t))
	defer inj.stop()

	frame := voiceLCHeader(3180202, 1)
	if _, err := rig.gateway.WriteToUDP(frame, rig.shim.GatewayBind()); err != nil {
		t.Fatal(err)
	}
	got := rig.drain(t)

	// The voice frame is present and untouched.
	var sawVoice bool
	for _, d := range got {
		if len(d) == 55 && string(d) == string(frame) {
			sawVoice = true
		}
	}
	if !sawVoice {
		t.Error("the forwarded voice frame did not arrive unchanged")
	}
	if n := countDMRA(got); n != 4 {
		t.Fatalf("got %d DMRA frames, want the four blocks of one alias", n)
	}

	// And they decode to the phonebook's answer, addressed to the caller.
	var blocks [][]byte
	for _, d := range got {
		if len(d) == 15 && string(d[0:4]) == "DMRA" {
			blocks = append(blocks, d)
		}
	}
	id, alias, _, err := talkeralias.Decode(blocks)
	if err != nil {
		t.Fatalf("the injected frames do not decode: %v", err)
	}
	if id != 3180202 || alias != "KN4OQW Clint Chance" {
		t.Errorf("injected alias = %d/%q, want 3180202/%q", id, alias, "KN4OQW Clint Chance")
	}
}

// TestUnknownCallerGetsNoAlias: the phonebook is the only source, and a station
// nobody entered is left showing its bare id exactly as today.
func TestUnknownCallerGetsNoAlias(t *testing.T) {
	rig := newSeamRig(t)
	ctx, cancel := contextWithCancel()
	defer cancel()
	go func() { _ = rig.shim.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	inj := &taInjector{}
	inj.reconcile(rig.shim, talkeralias.TemplateCallsignName, testChain(t))
	defer inj.stop()

	if _, err := rig.gateway.WriteToUDP(voiceLCHeader(4242424, 1), rig.shim.GatewayBind()); err != nil {
		t.Fatal(err)
	}
	if n := countDMRA(rig.drain(t)); n != 0 {
		t.Errorf("%d DMRA frames for a station nobody knows, want none", n)
	}
}

// TestRendererOmitsTheKeyWhenUnset is the other half of blank-omit: the generated
// INI must be byte-identical too, or a node would hand MMDVM-Host a key it never
// had before merely for having updated.
func TestRendererOmitsTheKeyWhenUnset(t *testing.T) {
	m := &config.Model{Modes: config.Modes{DMR: true}}
	if got := m.RenderMMDVM(); contains(got, "InboundTalkerAlias") {
		t.Error("an unset Talker Alias template still rendered InboundTalkerAlias")
	}
	m.DMR.TalkerAlias = string(talkeralias.TemplateCallsignName)
	if got := m.RenderMMDVM(); !contains(got, "InboundTalkerAlias=1") {
		t.Error("a configured template did not render InboundTalkerAlias=1")
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && stringIndex(hay, needle) >= 0
}

func stringIndex(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
