//go:build tier2

package tier2

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/config"
)

const (
	ysfRptPort    = 3200 // MMDVM-Host binds this
	ysfGwPort     = 4200 // DGIdGateway / YSFGateway binds this
	ysfParrotPort = 42012

	parrotDGId = 1 // [DGId=1] Type=Parrot in the rendered config
)

// TestTier2_DGIdGatewayAcceptsRenderedConfig is issue #17 Tier 2, the YSF
// gateway-startup claim: the DGIdGateway.ini this project generates must be
// accepted by the pinned DGIdGateway (YSFClients @ 2b480aa) — it has to parse,
// bind MMDVM-Host's YSF loopback, and stay up.
//
// Tier 1 only proves we emit the bytes we intended. This proves the real daemon
// agrees they are a valid configuration, which is a different claim.
func TestTier2_DGIdGatewayAcceptsRenderedConfig(t *testing.T) {
	bin := gwBin("DGIdGateway")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("DGIdGateway binary not built: %v", err)
	}

	m := &config.Model{
		General: config.General{Callsign: "KN4OQW", ID: "3180202"},
		YSFGW: config.YSFGateway{
			EnableDGId: true,
			Suffix:     "ND",
			Startup:    "FCS00290",
			YCSNetwork: true,
		},
	}
	ini := m.RenderDGIdGateway()

	confPath := filepath.Join(t.TempDir(), "DGIdGateway.ini")
	if err := os.WriteFile(confPath, []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}

	// The DG-ID 1 Parrot block points at a local YSFParrot on 42012/42013.
	parrot := exec.Command(gwBin("YSFParrot"), strconv.Itoa(ysfParrotPort))
	parrotLog := &strings.Builder{}
	parrot.Stdout, parrot.Stderr = parrotLog, parrotLog
	if err := parrot.Start(); err == nil {
		defer func() { parrot.Process.Kill(); parrot.Wait() }()
	}

	cmd := exec.Command(bin, confPath)
	logBuf := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start DGIdGateway: %v", err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		t.Logf("--- DGIdGateway log ---\n%s", logBuf.String())
		if s := parrotLog.String(); strings.TrimSpace(s) != "" {
			t.Logf("--- YSFParrot log ---\n%s", s)
		}
	}()

	// It must still be alive after startup, i.e. it did not reject the config.
	time.Sleep(3 * time.Second)
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		t.Fatalf("DGIdGateway exited with %v after reading the generated config:\n%s",
			cmd.ProcessState.ExitCode(), logBuf.String())
	}

	// And it must own MMDVM-Host's YSF loopback: binding 4200 should now fail.
	c, err := net.ListenPacket("udp4", "127.0.0.1:"+strconv.Itoa(ysfGwPort))
	if err == nil {
		c.Close()
		t.Errorf("DGIdGateway did not bind the YSF loopback :4200 from the generated config")
	} else {
		t.Logf("PASS: DGIdGateway owns the YSF loopback :4200 (bind refused: %v)", err)
	}
	t.Logf("PASS: pinned DGIdGateway accepted the generated config and stayed up")
}

// ysfTransmission reframes the real bench DMR Parrot capture into a YSF
// transmission addressed to one DG-ID: a voice header, voice frames carrying the
// captured AMBE verbatim, and a terminator (whose end-of-transmission bit is what
// makes YSFParrot turn the recording around). The codec bits are the operator's
// own keyed Parrot audio; only the addressing is synthesized, which is the same
// split the DMR side relies on.
func ysfTransmission(t *testing.T, dgid uint8) [][]byte {
	t.Helper()

	var cws [][]byte
	for _, raw := range loadCapture(t) {
		f, err := frames.ParseDMR(raw)
		if err != nil {
			t.Fatalf("parse capture frame: %v", err)
		}
		cws = append(cws, f.AMBE...)
	}
	per := frames.CodewordsPerFrame(frames.ModeYSF)
	if len(cws) < per {
		t.Fatalf("capture yielded only %d codewords, need at least %d", len(cws), per)
	}

	p := frames.Params{DGId: dgid}
	mk := func(f frames.Frame) []byte {
		b, err := frames.ConstructYSF(f, p, nil)
		if err != nil {
			t.Fatalf("construct YSF %v: %v", f.Kind, err)
		}
		return b
	}

	base := frames.Frame{Mode: frames.ModeYSF, SrcCallsign: "KN4OQW", DstCallsign: "ALL"}
	header := base
	header.Kind = frames.KindHeader
	out := [][]byte{mk(header)}

	for i := 0; i+per <= len(cws); i += per {
		v := base
		v.Kind = frames.KindVoice
		v.Stream.Seq = uint8((i / per) % 8) // FN is 3 bits
		v.AMBE = cws[i : i+per]
		out = append(out, mk(v))
	}

	term := base
	term.Kind = frames.KindTerminator
	return append(out, mk(term))
}

// TestTier2_DGIdParrotEcho is issue #17 Tier 2, the DG-ID plumbing claim, and the
// one item the earlier Tier 2 run left open: voice keyed on DG-ID 1 must reach
// the local Parrot the generated config declares, and come back.
//
// The earlier run could only prove the DG-ID table materializes, because nothing
// in this project could build a frame addressed to a DG-ID — the FICH codec had
// no DG-ID accessor at all. It does now (frames.Params.DGId / frames.YSFDGId), so
// the whole route is observable: DGIdGateway picks the DG-ID 1 network off the
// FICH, forwards to YSFParrot on 42012/42013, and stamps the slot back into the
// returning frames before writing them down MMDVM-Host's loopback.
//
// Entirely self-contained: no reflector, no internet, no RF, no credential.
func TestTier2_DGIdParrotEcho(t *testing.T) {
	for _, name := range []string{"DGIdGateway", "YSFParrot"} {
		if _, err := os.Stat(gwBin(name)); err != nil {
			t.Skipf("%s binary not built: %v", name, err)
		}
	}

	// No startup reflector: the rendered DG-ID table is then exactly DG-ID 0
	// (local Wires-X gateway) and DG-ID 1 (local Parrot), so nothing in this test
	// reaches off the loopback.
	m := &config.Model{
		General: config.General{Callsign: "KN4OQW", ID: "3180202"},
		YSFGW:   config.YSFGateway{EnableDGId: true, Suffix: "ND"},
	}

	confPath := filepath.Join(t.TempDir(), "DGIdGateway.ini")
	if err := os.WriteFile(confPath, []byte(m.RenderDGIdGateway()), 0o600); err != nil {
		t.Fatal(err)
	}

	parrot := exec.Command(gwBin("YSFParrot"), strconv.Itoa(ysfParrotPort))
	parrotLog := &strings.Builder{}
	parrot.Stdout, parrot.Stderr = parrotLog, parrotLog
	if err := parrot.Start(); err != nil {
		t.Fatalf("start YSFParrot: %v", err)
	}
	defer func() { parrot.Process.Kill(); parrot.Wait() }()

	gw := exec.Command(gwBin("DGIdGateway"), confPath)
	gwLog := &strings.Builder{}
	gw.Stdout, gw.Stderr = gwLog, gwLog
	if err := gw.Start(); err != nil {
		t.Fatalf("start DGIdGateway: %v", err)
	}
	defer func() {
		gw.Process.Kill()
		gw.Wait()
		t.Logf("--- DGIdGateway log ---\n%s", gwLog.String())
		if s := parrotLog.String(); strings.TrimSpace(s) != "" {
			t.Logf("--- YSFParrot log ---\n%s", s)
		}
	}()
	time.Sleep(2 * time.Second)

	rep, err := newYSFRepeaterSide(ysfRptPort, ysfGwPort, "KN4OQW")
	if err != nil {
		t.Fatal(err)
	}
	defer rep.close()

	// Let the repeater-side link settle: DGIdGateway will not write anything back
	// to a repeater it has not seen poll.
	time.Sleep(2 * time.Second)

	tx := ysfTransmission(t, parrotDGId)
	sentVoice := tx[1] // first voice frame; its AMBE is what must come back
	for _, f := range tx {
		if err := rep.send(f); err != nil {
			t.Fatalf("inject: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("injected %d YSFD frames on DG-ID %d at :%d", len(tx), parrotDGId, ysfGwPort)

	// YSFParrot waits out a 2s turnaround, then replays a frame every 100ms.
	// Wait for the replay to arrive and then stop growing, rather than for an
	// exact count: what matters is that the voice comes back, not that the
	// daemon chain relays every housekeeping frame.
	var echoed [][]byte
	deadline := time.Now().Add(25 * time.Second)
	for stable := 0; time.Now().Before(deadline) && stable < 4; {
		time.Sleep(500 * time.Millisecond)
		got := rep.got()
		if len(got) > 0 && len(got) == len(echoed) {
			stable++
		} else {
			stable = 0
		}
		echoed = got
	}
	if len(echoed) == 0 {
		t.Fatalf("nothing came back down the YSF loopback to :%d — DG-ID %d never echoed",
			ysfRptPort, parrotDGId)
	}
	// Every voice frame keyed on the DG-ID must come back; the parrot ends its
	// playout with its own end-of-transmission handling, so the terminator is not
	// necessarily relayed and is not required here.
	var gotVoice int
	for _, f := range echoed {
		if p, err := frames.ParseYSF(f); err == nil && p.Kind == frames.KindVoice {
			gotVoice++
		}
	}
	sentVoiceCount := len(tx) - 2 // minus header and terminator
	t.Logf("PASS: %d frames returned to the repeater side on the YSF loopback (%d voice, %d injected)",
		len(echoed), gotVoice, len(tx))
	if gotVoice < sentVoiceCount {
		t.Errorf("only %d of %d injected voice frames came back through DG-ID %d",
			gotVoice, sentVoiceCount, parrotDGId)
	}

	// Every returning frame must carry the DG-ID slot it was routed through:
	// DGIdGateway stamps it on the way back, which is how MMDVM-Host knows which
	// DG-ID the traffic belongs to.
	for i, f := range echoed {
		dgid, err := frames.YSFDGId(f)
		if err != nil {
			t.Fatalf("returned frame %d does not parse: %v", i, err)
		}
		if dgid != parrotDGId {
			t.Fatalf("returned frame %d carries DG-ID %d, want %d", i, dgid, parrotDGId)
		}
	}
	t.Logf("PASS: every returned frame carries DG-ID %d", parrotDGId)

	// And the audio must be the audio: the codec bits are supposed to ride the
	// whole route untouched, so the echo of the first voice frame is byte-exact.
	want, err := frames.ParseYSF(sentVoice)
	if err != nil {
		t.Fatalf("parse injected voice frame: %v", err)
	}
	var matched bool
	for _, f := range echoed {
		got, err := frames.ParseYSF(f)
		if err != nil || got.Kind != frames.KindVoice {
			continue
		}
		same := len(got.AMBE) == len(want.AMBE)
		for i := range got.AMBE {
			if !same {
				break
			}
			same = bytes.Equal(got.AMBE[i], want.AMBE[i])
		}
		if same {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("no returned voice frame carried the injected AMBE back byte-exactly")
	} else {
		t.Logf("PASS: the injected AMBE returned byte-exactly through DG-ID %d", parrotDGId)
	}
}
