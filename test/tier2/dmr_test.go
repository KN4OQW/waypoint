//go:build tier2

package tier2

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
)

const (
	// The real keyed Parrot transmission captured on the bench Pi, relative to
	// this package.
	capturePath = "../../internal/bus/frames/testdata/capture/dmr_parrot_9990.bin"

	rptPort = 62032 // MMDVM-Host binds this
	gwPort  = 62031 // DMRGateway binds this

	dmrID = 3180202 // KN4OQW, as captured
)

// gwBin locates a pinned daemon binary. Override the directory with
// WAYPOINT_GW_BIN; build.sh drops them in ./out by default.
func gwBin(name string) string {
	dir := os.Getenv("WAYPOINT_GW_BIN")
	if dir == "" {
		dir = "out"
	}
	return filepath.Join(dir, name)
}

// loadCapture returns the real keyed Parrot transmission: 22 x 55-byte DMRD
// frames captured on the bench Pi in the MMDVM-Host -> DMRGateway direction.
func loadCapture(t *testing.T) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if len(raw)%55 != 0 {
		t.Fatalf("capture is not a whole number of 55-byte frames: %d", len(raw))
	}
	var out [][]byte
	for i := 0; i < len(raw); i += 55 {
		out = append(out, append([]byte(nil), raw[i:i+55]...))
	}
	return out
}

// retarget clones a transmission onto a different talkgroup and stream id,
// leaving the AMBE payload untouched. Addressing rides the DMRD header
// (bytes 5-10), never the codec, so this is the same audio on a new TG.
//
// The capture is a private call (flags 0xe1) because BM Parrot is normally
// dialled that way. Talkgroup routing is a group-call concept, so injected
// transmissions are keyed as group calls unless a test says otherwise.
func retarget(frames [][]byte, dst uint32, stream uint32, group bool) [][]byte {
	out := make([][]byte, len(frames))
	for i, f := range frames {
		c := append([]byte(nil), f...)
		dmrdSetDst(c, dst)
		dmrdSetStreamID(c, stream)
		if group {
			dmrdSetGroup(c)
		}
		out[i] = c
	}
	return out
}

// model is the single profile under test: BrandMeister primary + TGIF, with one
// operator route sending TG 31665 to TGIF. Addresses point at the stub masters.
func model(bmPort, tgifPort int) *config.Model {
	return &config.Model{
		General: config.General{Callsign: "KN4OQW", ID: strconv.Itoa(dmrID)},
		DMR:     config.DMR{ID: strconv.Itoa(dmrID), ColorCode: "1"},
		Networks: []config.Network{
			{
				Name: "BM", Type: config.NetBrandmeister, Primary: true,
				Address: "127.0.0.1", Port: strconv.Itoa(bmPort),
				Password: "passw0rd", Enabled: true,
			},
			{
				Name: "TGIF", Type: config.NetTGIF,
				Address: "127.0.0.1", Port: strconv.Itoa(tgifPort),
				Password: "passw0rd", Enabled: true,
			},
		},
		Routes: []config.DMRRoute{{Slot: "2", TG: "31665", Network: "TGIF"}},
	}
}

// rig is one complete stack: two stub upstream masters, a real DMRGateway
// configured by the real renderer, and the repeater-side loopback socket.
type rig struct {
	bm, tgif *stubMaster
	rep      *repeaterSide
	stop     func()
}

func startRig(t *testing.T, echoOnPrimary bool) *rig {
	t.Helper()
	if _, err := os.Stat(gwBin("DMRGateway")); err != nil {
		t.Skipf("DMRGateway binary not built: %v", err)
	}

	bm, err := newStubMaster("BM", echoOnPrimary)
	if err != nil {
		t.Fatal(err)
	}
	tgif, err := newStubMaster("TGIF", false)
	if err != nil {
		t.Fatal(err)
	}

	m := model(bm.port(), tgif.port())
	ini := m.RenderDMRGateway()

	confPath := filepath.Join(t.TempDir(), "DMRGateway.ini")
	if err := os.WriteFile(confPath, []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := newRepeaterSide(rptPort, gwPort)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(gwBin("DMRGateway"), confPath)
	logBuf := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start DMRGateway: %v", err)
	}

	// DMRGateway blocks in "Waiting for MMDVM to connect....." until the
	// repeater announces itself with a DMRC config packet.
	time.Sleep(400 * time.Millisecond)
	for i := 0; i < 40 && !(bm.isLoggedIn() && tgif.isLoggedIn()); i++ {
		if err := rep.connect(dmrID); err != nil {
			t.Fatalf("DMRC: %v", err)
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !bm.waitLogin(5*time.Second) || !tgif.waitLogin(5*time.Second) {
		t.Fatalf("upstream login incomplete (BM=%v TGIF=%v)\n%s",
			bm.isLoggedIn(), tgif.isLoggedIn(), logBuf.String())
	}
	t.Logf("both upstream networks logged in from one rendered profile (BM=:%d TGIF=:%d)",
		bm.port(), tgif.port())

	return &rig{bm: bm, tgif: tgif, rep: rep, stop: func() {
		cmd.Process.Kill()
		cmd.Wait()
		rep.close()
		bm.close()
		tgif.close()
		if s := logBuf.String(); strings.TrimSpace(s) != "" {
			t.Logf("--- DMRGateway log ---\n%s", s)
		}
	}}
}

func (r *rig) inject(t *testing.T, frames [][]byte) {
	t.Helper()
	for _, f := range frames {
		if err := r.rep.send(f); err != nil {
			t.Fatalf("inject: %v", err)
		}
		time.Sleep(3 * time.Millisecond)
	}
	time.Sleep(800 * time.Millisecond)
}

// TestTier2_TGIFRouting is issue #17 Tier 2, the TGIF claim: the generated
// rewrites must put a keyed TG 31665 on TGIF's socket and not on the primary's.
// There is no TGIF echo service, so this asserts on outbound traffic, not a reply.
func TestTier2_TGIFRouting(t *testing.T) {
	r := startRig(t, false)
	defer r.stop()

	r.inject(t, retarget(loadCapture(t), 31665, 0xAABBCCDD, true))

	tgifGot, bmGot := r.tgif.received(), r.bm.received()
	if len(tgifGot) == 0 {
		t.Fatalf("TGIF received nothing for TG 31665 (BM got %d frames)", len(bmGot))
	}
	t.Logf("PASS: TGIF received %d frames, first: %s", len(tgifGot), describe(tgifGot[0]))

	var leaked int
	for _, f := range bmGot {
		if dmrdDst(f) == 31665 {
			leaked++
		}
	}
	if leaked > 0 {
		t.Errorf("TG 31665 leaked to the BrandMeister primary in %d frames", leaked)
	} else {
		t.Logf("PASS: no TG 31665 frame reached the primary (%d unrelated frames there)", len(bmGot))
	}
}

// TestTier2_ParrotEcho is issue #17 Tier 2, the BM Parrot claim: a keyed TG 9990
// group call reaches the primary, the generated TypeRewrite converts it to the
// private call BM Parrot expects, and the echo returns down the loopback.
func TestTier2_ParrotEcho(t *testing.T) {
	r := startRig(t, true)
	defer r.stop()

	before := len(r.rep.got())
	r.inject(t, retarget(loadCapture(t), 9990, 0x11223344, true))

	var got [][]byte
	for _, f := range r.bm.received() {
		if dmrdDst(f) == 9990 {
			got = append(got, f)
		}
	}
	if len(got) == 0 {
		t.Fatalf("the primary never received the TG 9990 transmission (TGIF got %d)", len(r.tgif.received()))
	}
	t.Logf("PASS: primary received %d frames for 9990, first: %s", len(got), describe(got[0]))

	if !dmrdPrivate(got[0]) {
		t.Errorf("generated TypeRewrite did not convert group TG 9990 to a private call: %s", describe(got[0]))
	} else {
		t.Logf("PASS: TypeRewrite converted the group call to private, as BM Parrot expects")
	}

	for _, f := range r.tgif.received() {
		if dmrdDst(f) == 9990 {
			t.Errorf("TG 9990 leaked to TGIF: %s", describe(f))
		}
	}

	echoed := r.rep.got()[before:]
	if len(echoed) == 0 {
		t.Fatalf("no echo returned down the loopback to :%d", rptPort)
	}
	t.Logf("PASS: %d frames returned to the repeater side, first: %s", len(echoed), describe(echoed[0]))
}

// TestTier2_DMRGatewayDropsAnInboundTalkerAlias is the fact the whole Zello
// talker-alias design rests on, checked against the real pinned binary rather than
// against a reading of its source (issue #279).
//
// The claim: DMRGateway carries Talker Alias in ONE direction only. Its
// processTalkerAlias reads from the repeater and writes to the upstream networks,
// and CDMRNetwork::clock — the side facing a master — has no DMRA case at all, so
// an alias arriving from upstream is dumped as "Unknown packet from the master" and
// discarded. That is why the bus daemon cannot transmit its own alias and instead
// announces the name for waypointd to inject at the DMR relay.
//
// It is asserted in BOTH directions on purpose. A test that only showed "no DMRA
// arrived" would pass just as well against a dead route, a wedged gateway, or a
// master that never logged in — proving nothing. So the same rig must be shown
// still delivering network→RF voice at the moment the alias goes missing, and the
// echo path this uses for that is the one TestTier2_ParrotEcho already proves.
//
// If a future DMRGateway bump adds the missing direction, this test fails, and
// that is the point: the announcement path could then be simplified away.
func TestTier2_DMRGatewayDropsAnInboundTalkerAlias(t *testing.T) {
	r := startRig(t, true)
	defer r.stop()

	// The shape DMRGateway itself puts on a master's socket (CDMRNetwork::
	// writeTalkerAlias): "DMRA", a 4-byte network id, then the alias bytes. The
	// payload is immaterial — there is no DMRA branch on the inbound side for any
	// length to reach — so this is the most favourable input the claim could face.
	id := uint32(dmrID)
	alias := []byte("DMRA")
	alias = append(alias, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	alias = append(alias, 0x00, 'K', 'N', '4', 'O', 'Q', 'W')
	for i := 0; i < 4; i++ { // all four blocks, as a real alias is sent
		blk := append([]byte(nil), alias...)
		blk[8] = byte(i)
		if err := r.bm.sendRaw(blk); err != nil {
			t.Fatalf("send DMRA block %d: %v", i, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	// The positive control: the same master, the same rig, network→RF voice still
	// arriving. Without this the negative below is worthless.
	r.inject(t, retarget(loadCapture(t), 9990, 0xBEEF0001, true))
	if len(r.rep.got()) == 0 {
		t.Fatalf("no echo came back down the loopback — the rig is not delivering " +
			"network->RF voice, so nothing can be concluded about the alias")
	}
	t.Logf("PASS (control): %d DMRD frames reached the repeater side", len(r.rep.got()))

	var dmra int
	for _, d := range r.rep.allGot() {
		if len(d) >= 4 && string(d[:4]) == "DMRA" {
			dmra++
		}
	}
	if dmra > 0 {
		t.Errorf("DMRGateway forwarded %d DMRA datagram(s) to the repeater. The pinned "+
			"binary now carries Talker Alias network->RF, which the Zello announcement "+
			"path was built to work around — revisit cmd/waypoint-bus/zellobridge.go "+
			"and internal/mqtt/talker_alias.go.", dmra)
		return
	}
	t.Logf("PASS: DMRGateway forwarded no DMRA to the repeater while delivering voice normally; " +
		"an alias from a bus cannot reach the radio through it")
}
