package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/dmrshim"
	"github.com/KN4OQW/waypoint/internal/idresolve"
	"github.com/KN4OQW/waypoint/internal/mqtt"
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

// voiceLCHeaderStream is voiceLCHeader with a stream id, which is the key an
// announced name is matched on.
func voiceLCHeaderStream(srcID uint32, slot int, streamID uint32) []byte {
	d := voiceLCHeader(srcID, slot)
	d[16], d[17], d[18], d[19] = byte(streamID>>24), byte(streamID>>16), byte(streamID>>8), byte(streamID)
	return d
}

// dmraBlocks picks the Talker Alias frames out of what the host received.
func dmraBlocks(datagrams [][]byte) [][]byte {
	var out [][]byte
	for _, d := range datagrams {
		if len(d) == 15 && string(d[0:4]) == "DMRA" {
			out = append(out, d)
		}
	}
	return out
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
		inj.reconcile(rig.shim, tmpl, testChain(t), nil)
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
	inj.reconcile(rig.shim, talkeralias.TemplateCallsignName, testChain(t), nil)
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
	inj.reconcile(rig.shim, talkeralias.TemplateCallsignName, testChain(t), nil)
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

// --- announced (bus/Zello) callers -------------------------------------------

// TestAnnouncedZelloCallerIsNamedOnTheSeam is the end-to-end claim of issue #279:
// a name a bus published reaches MMDVM-Host's own socket as the four blocks of an
// alias, and it is the ANNOUNCED name rather than the phonebook's answer for the
// transmitting id.
//
// It is asserted at the socket rather than against the emitter because the whole
// fault being corrected was about which process can reach that socket.
func TestAnnouncedZelloCallerIsNamedOnTheSeam(t *testing.T) {
	rig := newSeamRig(t)
	ctx, cancel := contextWithCancel()
	defer cancel()
	go func() { _ = rig.shim.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	inj := &taInjector{}
	// 3180202 is the node's own id and IS in the test phonebook as "KN4OQW Clint
	// Chance" — which is exactly the wrong answer here, so the assertion below
	// distinguishes the announcement from the fallback rather than from silence.
	inj.reconcile(rig.shim, talkeralias.TemplateCallsignName, testChain(t), []uint32{3180202})
	defer inj.stop()

	inj.announce(mqtt.TalkerAliasNote{
		Type: mqtt.TalkerAliasNoteType, StreamID: 0xC0FFEE, SrcID: 3180202, Name: "waypoint dev",
	})

	frame := voiceLCHeaderStream(3180202, 1, 0xC0FFEE)
	if _, err := rig.gateway.WriteToUDP(frame, rig.shim.GatewayBind()); err != nil {
		t.Fatal(err)
	}
	got := rig.drain(t)

	blocks := dmraBlocks(got)
	if len(blocks) != 4 {
		t.Fatalf("got %d DMRA frames, want the four blocks of one alias", len(blocks))
	}
	id, alias, _, err := talkeralias.Decode(blocks)
	if err != nil {
		t.Fatalf("the injected frames do not decode: %v", err)
	}
	// Addressed to the transmitting station, or the fork's slot matching drops them
	// (CDMRSlot::setTalkerAlias compares m_netLC->getSrcId()).
	if id != 3180202 {
		t.Errorf("injected alias addressed to %d, want 3180202", id)
	}
	if alias != "waypoint dev" {
		t.Errorf("injected alias = %q, want the announced name (the phonebook would say KN4OQW Clint Chance)", alias)
	}
	// The voice frame still went through untouched.
	var sawVoice bool
	for _, d := range got {
		if len(d) == 55 && string(d) == string(frame) {
			sawVoice = true
		}
	}
	if !sawVoice {
		t.Error("the forwarded voice frame did not arrive unchanged")
	}
}

// TestAnnounceOnlyCallerWithNoAnnouncementGetsNoAlias covers the failure mode that
// would have looked like success: the announcement lost its race with the audio,
// or nothing published one, and the phonebook answers with THIS node's callsign.
// A radio showing a bare id is a worse screen; a radio showing the wrong operator
// is a wrong one.
func TestAnnounceOnlyCallerWithNoAnnouncementGetsNoAlias(t *testing.T) {
	rig := newSeamRig(t)
	ctx, cancel := contextWithCancel()
	defer cancel()
	go func() { _ = rig.shim.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	inj := &taInjector{}
	inj.reconcile(rig.shim, talkeralias.TemplateCallsignName, testChain(t), []uint32{3180202})
	defer inj.stop()

	if _, err := rig.gateway.WriteToUDP(voiceLCHeaderStream(3180202, 1, 0x1234), rig.shim.GatewayBind()); err != nil {
		t.Fatal(err)
	}
	if blocks := dmraBlocks(rig.drain(t)); len(blocks) != 0 {
		_, alias, _, _ := talkeralias.Decode(blocks)
		t.Errorf("an unannounced transmission was named %q from the phonebook", alias)
	}
}

// TestAnnouncementSurvivesAPhonebookThisNodeCouldNotBuild: an announced name came
// off the wire and needs no resolver. Treating a nil chain as "feature off" would
// have silently disabled the Zello path on a node whose phonebook failed to open.
func TestAnnouncementSurvivesAPhonebookThisNodeCouldNotBuild(t *testing.T) {
	rig := newSeamRig(t)
	ctx, cancel := contextWithCancel()
	defer cancel()
	go func() { _ = rig.shim.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	inj := &taInjector{}
	inj.reconcile(rig.shim, talkeralias.TemplateCallsign, nil, []uint32{3180202})
	defer inj.stop()

	inj.announce(mqtt.TalkerAliasNote{
		Type: mqtt.TalkerAliasNoteType, StreamID: 9, SrcID: 3180202, Name: "waypoint dev",
	})
	if _, err := rig.gateway.WriteToUDP(voiceLCHeaderStream(3180202, 1, 9), rig.shim.GatewayBind()); err != nil {
		t.Fatal(err)
	}
	blocks := dmraBlocks(rig.drain(t))
	if len(blocks) != 4 {
		t.Fatalf("got %d DMRA frames with no phonebook, want 4", len(blocks))
	}
	if _, alias, _, err := talkeralias.Decode(blocks); err != nil || alias != "waypoint dev" {
		t.Errorf("alias = %q, err = %v", alias, err)
	}
}

// TestNoPhonebookAndNoAnnouncerIsStillOff: with neither a resolver nor an
// announce-only id there is nothing this node could ever say, and it must put
// nothing on the seam rather than attach a tap that always returns nil.
func TestNoPhonebookAndNoAnnouncerIsStillOff(t *testing.T) {
	rig := newSeamRig(t)
	ctx, cancel := contextWithCancel()
	defer cancel()
	go func() { _ = rig.shim.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	inj := &taInjector{}
	inj.reconcile(rig.shim, talkeralias.TemplateCallsign, nil, nil)
	defer inj.stop()
	if inj.remove != nil {
		t.Error("a tap was attached with nothing to resolve and nobody to announce")
	}
}

// TestAnnounceIsSafeBeforeReconcile: the MQTT consumer is running before the
// relay's first tick, so a note can arrive with no emitter built. It must be
// dropped, not panic on a live daemon.
func TestAnnounceIsSafeBeforeReconcile(t *testing.T) {
	inj := &taInjector{}
	inj.announce(mqtt.TalkerAliasNote{Type: mqtt.TalkerAliasNoteType, StreamID: 1, SrcID: 2, Name: "x"})
	var nilInj *taInjector
	nilInj.announce(mqtt.TalkerAliasNote{Type: mqtt.TalkerAliasNoteType, StreamID: 1, SrcID: 2, Name: "x"})
}
