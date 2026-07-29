//go:build bench

package cal

// Hardware smoke tests, built only with `-tags bench` and run on a node with a
// modem actually fitted. They are the bridge between the scripted modem the rest
// of this package is tested against and a real one.
//
//	GOOS=linux GOARCH=arm GOARM=6 go test -tags bench -c ./internal/cal
//	scp cal.test <node>:  &&  ssh <node> 'sudo systemctl stop waypoint-mmdvm && sudo ./cal.test -test.v'
//
// NOTHING HERE TRANSMITS. Every test in this file is receive-only: it opens the
// port, asks the modem what it is, writes the calibration configuration, tunes
// the synthesiser, and listens. Keying a transmitter is not something a test
// binary should do while nobody is watching the antenna, and the one test that
// would need to — measuring an actual bit error rate — needs a human holding a
// PTT anyway, so it is a documented procedure rather than an automated check
// (docs/bench-calibration.md).
//
// Stopping MMDVM-Host first is the caller's job, deliberately: a test that
// quietly takes a node off the air is a worse idea than one that says it could
// not open the port.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/modem"
)

// requireModemFree refuses to run while MMDVM-Host holds the UART.
//
// This guard exists because its absence cost a whole bench session. Both
// processes can open the port — root bypasses the advisory exclusive lock — so
// nothing fails loudly: MMDVM-Host polls the modem for its version every 1.8 s,
// reconfigures it out from under the session, and consumes the DMR frames the
// sweep is waiting for. What the operator sees is a sweep that hears nothing and
// a handheld that will not key, with no indication that a second program is on
// the wire.
//
// The unit is `waypoint-mmdvm.service`. The DEBIAN PACKAGE is called
// `waypoint-mmdvmhost`, which is the trap: stopping "waypoint-mmdvmhost" gets
// "Unit not loaded", which reads as "not installed" rather than "wrong name".
func requireModemFree(t *testing.T) {
	t.Helper()
	out, _ := exec.Command("systemctl", "is-active", "waypoint-mmdvm.service").Output()
	switch strings.TrimSpace(string(out)) {
	case "active", "activating", "reloading":
		t.Fatalf("MMDVM-Host is running and owns %s — both processes would be on the UART.\n"+
			"Stop it first (note the unit name, which is NOT the package name):\n"+
			"    sudo systemctl stop waypoint-mmdvm\n"+
			"and start it again afterwards.", benchPort())
	}
}

func benchPort() string {
	if p := os.Getenv("WAYPOINT_BENCH_PORT"); p != "" {
		return p
	}
	return "/dev/ttyAMA0"
}

func benchFreq() uint32 {
	if v := os.Getenv("WAYPOINT_BENCH_FREQ"); v != "" {
		if hz, err := strconv.ParseUint(v, 10, 32); err == nil {
			return uint32(hz)
		}
	}
	return 438_800_000
}

// benchEnvInt reads a plan knob from the environment so a bench run can be
// re-shaped without a rebuild — the operator holding the PTT is the scarce
// resource here, not the compiler.
func benchEnvInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func benchPlan() Plan {
	return Plan{
		CoarseSpanHz: benchEnvInt("WAYPOINT_BENCH_SPAN", 1000),
		CoarseStepHz: benchEnvInt("WAYPOINT_BENCH_STEP", 500),
		FineSpanHz:   benchEnvInt("WAYPOINT_BENCH_FINE_SPAN", 200),
		FineStepHz:   benchEnvInt("WAYPOINT_BENCH_FINE_STEP", 100),
		MinFrames:    benchEnvInt("WAYPOINT_BENCH_FRAMES", 5),
		Dwell:        time.Duration(benchEnvInt("WAYPOINT_BENCH_DWELL_MS", 2000)) * time.Millisecond,
		IdleGap:      time.Duration(benchEnvInt("WAYPOINT_BENCH_IDLE_MS", 3000)) * time.Millisecond,
		Timeout:      time.Duration(benchEnvInt("WAYPOINT_BENCH_TIMEOUT_S", 60)) * time.Second,
	}
}

func benchOpen(t *testing.T) *Session {
	t.Helper()
	requireModemFree(t)
	freq := benchFreq()
	s, err := Open(context.Background(), Options{
		Port:      benchPort(),
		RXFreqHz:  freq,
		TXFreqHz:  freq,
		ColorCode: uint8(benchEnvInt("WAYPOINT_BENCH_CC", 1)),
		TXLevel:   50,
		RFLevel:   100,
	})
	if err != nil {
		t.Fatalf("open %s: %v", benchPort(), err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestBenchIdentity confirms the session opens and the modem answers, and logs
// which SET_CONFIG layout everything after this will use.
func TestBenchIdentity(t *testing.T) {
	s := benchOpen(t)
	v := s.Identity()
	t.Logf("protocol %d: %s", v.Protocol, v.Description)
	t.Logf("UDID %s, modes known=%v dmr=%v", v.UDID, v.Modes().Known, v.Modes().DMR)
	if !v.Modes().DMR {
		t.Fatal("this firmware does not carry DMR, so it cannot be swept")
	}
}

// TestBenchSetConfigAccepted is the test this whole file exists for.
//
// The calibration frames were transcribed from two firmware parsers, and a
// wrong byte in the middle of a 27-byte frame does not fail loudly — the
// firmware NAKs with a reason code, or worse, accepts a configuration that
// means something other than what was intended. This writes the real sweep
// configuration to the real board and requires an ACK.
func TestBenchSetConfigAccepted(t *testing.T) {
	s := benchOpen(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := SweepConfig(1, 50)
	if err := s.setConfig(ctx, cfg); err != nil {
		t.Fatalf("the modem refused the sweep configuration: %v", err)
	}
	t.Log("sweep configuration accepted (DMR, simplex, receive state)")
}

// TestBenchTuneAcceptsAnOffsetRange walks the synthesiser across the span a
// real sweep uses and requires every step to be accepted.
//
// This is where a board's own frequency limits show up: MMDVM_HS refuses a
// frequency outside the ranges its build supports, and refuses the satellite
// segments outright, both with the same bare reason code. Finding that here is
// far better than finding it partway through a sweep with an operator holding a
// PTT.
func TestBenchTuneAcceptsAnOffsetRange(t *testing.T) {
	s := benchOpen(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := SweepConfig(1, 50)
	base := benchFreq()
	for _, off := range []int{-5000, -2500, -500, 0, 500, 2500, 5000} {
		rx := applyOffset(base, off)
		if err := s.tune(ctx, rx, rx, cfg); err != nil {
			t.Fatalf("tuning to %+d Hz (%d) was refused: %v", off, rx, err)
		}
		t.Logf("%+6d Hz → %d accepted", off, rx)
	}
}

// TestBenchListenForFrames runs the real sweep loop over a narrow span and
// reports what it heard. With no radio transmitting it should report that it is
// waiting and finish with nothing heard, which is itself the check: the flow
// must reach that conclusion rather than hang or claim a clean measurement.
//
// With a radio transmitting DMR on the node's frequency it becomes the real
// thing, and the log is the measurement.
func TestBenchListenForFrames(t *testing.T) {
	s := benchOpen(t)
	plan := benchPlan()
	ctx, cancel := context.WithTimeout(context.Background(), plan.Timeout+30*time.Second)
	defer cancel()

	t.Logf("sweeping %d Hz ±%d in %d Hz steps, %d frames per point, %s budget",
		benchFreq(), plan.CoarseSpanHz, plan.CoarseStepHz, plan.MinFrames, plan.Timeout)
	res, err := s.Sweep(ctx, plan, func(p Progress) {
		t.Logf("[%s] %+d Hz  frames=%d ber=%.3f%%  %s", p.Phase, p.OffsetHz, p.Frames, p.BER, p.Detail)
	})
	t.Logf("points=%d best=%v aborted=%v err=%v", len(res.Points), res.Best, res.Aborted, err)
	for _, p := range res.Points {
		t.Logf("  %+6d Hz  heard=%v scored=%v frames=%d ber=%.4f%%", p.OffsetHz, p.Heard, p.Scored, p.Frames, p.BER)
	}
	// No assertion on the outcome: with no transmitter this is expected to end
	// in ErrNothingHeard, and with one it is expected to find a minimum. What
	// this test proves either way is that the loop runs against real firmware
	// and terminates.
	if err != nil && err != ErrNothingHeard && err != ErrNotEnoughSignal {
		t.Fatalf("the sweep failed for a reason other than silence: %v", err)
	}
}

// TestBenchCalDataIsRefusedFromAReceiveState is RFC-0021 §2's structural safety
// property, checked against the firmware that actually enforces it: in an
// ordinary receive state the board must REFUSE the command that keys the
// transmitter. If this ever passes silently, the sweep is no longer inherently
// receive-only and the safety argument needs rewriting.
//
// It writes the key command directly rather than going through Transmit,
// because Transmit would refuse it here in software — the point is to hear the
// firmware refuse it.
func TestBenchCalDataIsRefusedFromAReceiveState(t *testing.T) {
	s := benchOpen(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.setConfig(ctx, SweepConfig(1, 50)); err != nil {
		t.Fatalf("configure: %v", err)
	}
	err := s.command(ctx, setTransmit(true), CmdCalData)
	if err == nil {
		// Leave nothing on the air even in the failure case.
		_ = s.command(ctx, setTransmit(false), CmdCalData)
		t.Fatal("the modem ACCEPTED a transmit command from a receive state — the sweep is not inherently receive-only on this firmware")
	}
	t.Logf("refused as expected: %v", err)
}

// TestBenchRawListen is the diagnostic for "the sweep heard nothing".
//
// The sweep only counts DMR VOICE frames, so a run that reports nothing heard
// has three quite different causes it cannot tell apart: no RF at all, RF the
// modem cannot sync to, or frames arriving that the scorer rejects. This sits on
// one frequency and prints every frame the modem sends, whatever it is — voice,
// data, lost-sync, RSSI, debug — so the next question is obvious.
//
//	WAYPOINT_BENCH_FREQ=433900000 WAYPOINT_BENCH_LISTEN_S=40 ./cal.test \
//	  -test.v -test.run TestBenchRawListen
func TestBenchRawListen(t *testing.T) {
	s := benchOpen(t)
	secs := benchEnvInt("WAYPOINT_BENCH_LISTEN_S", 30)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs+15)*time.Second)
	defer cancel()

	freq := benchFreq()
	cfg := SweepConfig(uint8(benchEnvInt("WAYPOINT_BENCH_CC", 1)), 50)
	if err := s.tune(ctx, freq, freq, cfg); err != nil {
		t.Fatalf("tune to %d: %v", freq, err)
	}
	t.Logf("listening on %d Hz for %ds — key your radio NOW and hold it", freq, secs)

	started := time.Now()
	deadline := started.Add(time.Duration(secs) * time.Second)
	counts := map[byte]int{}
	var total, scored int
	for time.Now().Before(deadline) {
		f, err := modem.ReadFrame(s.port)
		if err != nil {
			continue // silence, or a partial frame
		}
		total++
		counts[f.Type]++
		if total <= 6 { // the first few in full, then just the tally
			ctrl := "-"
			if len(f.Payload) > 0 {
				ctrl = fmt.Sprintf("0x%02X", f.Payload[0])
			}
			// The printable text of the payload, because "type 0x00" alone does
			// not distinguish a genuine version reply from a mis-framed read.
			var txt []rune
			for _, b := range f.Payload {
				if b >= 0x20 && b < 0x7F {
					txt = append(txt, rune(b))
				} else {
					txt = append(txt, '.')
				}
			}
			t.Logf("  +%6dms type=0x%02X (%s) len=%d ctrl=%s\n      %q",
				time.Since(started).Milliseconds(), f.Type, frameName(f.Type), len(f.Payload), ctrl, string(txt))
		}
		if _, _, ok := scoreFrame(f); ok {
			scored++
		}
	}

	t.Logf("RESULT: %d frames, %d scoreable as DMR/D-Star voice", total, scored)
	for typ, n := range counts {
		t.Logf("  0x%02X %-16s ×%d", typ, frameName(typ), n)
	}
	switch {
	case total == 0:
		t.Log("NOTHING AT ALL: the modem sent no frames. Either no RF reached it, or it could not sync to what did.")
	case scored == 0:
		t.Log("FRAMES BUT NONE SCOREABLE: the modem is hearing something and forwarding it, but not as DMR voice.")
	default:
		t.Log("The sweep should work: scoreable voice frames are arriving.")
	}
}

func frameName(t byte) string {
	switch t {
	case CmdDMRData1:
		return "DMR slot 1"
	case CmdDMRData2:
		return "DMR slot 2"
	case CmdDMRLost1:
		return "DMR lost 1"
	case CmdDMRLost2:
		return "DMR lost 2"
	case CmdDStarHeader:
		return "D-Star header"
	case CmdDStarData:
		return "D-Star data"
	case CmdDStarLost:
		return "D-Star lost"
	case CmdRSSIData:
		return "RSSI"
	case CmdGetStatus:
		return "status"
	case CmdACK:
		return "ACK"
	case CmdNAK:
		return "NAK"
	case 0xF1, 0xF2, 0xF3, 0xF4, 0xF5:
		return "debug"
	}
	return "?"
}

// TestBenchMeasureHere scores ONE frequency for a fixed window and prints every
// frame's error count, rather than sweeping.
//
// It exists to separate two explanations of a flat, high bit error rate that a
// sweep cannot tell apart: a link that really is that bad at every offset, or a
// scorer that is looking at the wrong bits. Scoring random data through a
// perfect Golay code yields about 19 errors in 141 bits — near 13% — so a
// suspiciously constant 13% is exactly what a misaligned payload looks like.
//
// A healthy DMR link at the correct frequency should score well under 2%, and
// per-frame counts should vary. Twenty identical-ish counts near 19 mean the
// bits being scored are not the AMBE codewords.
//
//	WAYPOINT_BENCH_OFFSET=0 WAYPOINT_BENCH_LISTEN_S=25 ./cal.test -test.v -test.run TestBenchMeasureHere
func TestBenchMeasureHere(t *testing.T) {
	s := benchOpen(t)
	secs := benchEnvInt("WAYPOINT_BENCH_LISTEN_S", 25)
	off := benchEnvInt("WAYPOINT_BENCH_OFFSET", 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs+20)*time.Second)
	defer cancel()

	rx := applyOffset(benchFreq(), off)
	cfg := SweepConfig(uint8(benchEnvInt("WAYPOINT_BENCH_CC", 1)), 50)
	if err := s.tune(ctx, rx, rx, cfg); err != nil {
		t.Fatalf("tune to %d (%+d Hz): %v", rx, off, err)
	}
	t.Logf("measuring at %d Hz (%+d Hz offset) for %ds — key up and hold", rx, off, secs)

	var meter Meter
	var perFrame []int
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	for time.Now().Before(deadline) {
		f, err := modem.ReadFrame(s.port)
		if err != nil {
			continue
		}
		errs, bits, ok := scoreFrame(f)
		if !ok {
			continue
		}
		meter.Add(errs, bits)
		perFrame = append(perFrame, errs)
		if len(perFrame) <= 30 {
			t.Logf("  frame %2d: %3d errors of %d bits (%.2f%%)", len(perFrame), errs, bits, float64(errs)*100/float64(bits))
		}
	}

	t.Logf("RESULT at %+d Hz: %d frames, %d/%d bits, BER %.4f%%",
		off, meter.Frames, meter.Errors, meter.Bits, meter.Percent())
	if meter.Frames == 0 {
		t.Log("nothing decoded here")
		return
	}
	// The distribution is the evidence. Real errors cluster low and vary; random
	// data pinned by a perfect code clusters tightly around 19.
	lo, hi := perFrame[0], perFrame[0]
	for _, e := range perFrame {
		if e < lo {
			lo = e
		}
		if e > hi {
			hi = e
		}
	}
	t.Logf("per-frame errors: min %d, max %d, mean %.1f", lo, hi, float64(meter.Errors)/float64(meter.Frames))
	switch {
	case meter.Percent() < 2:
		t.Log("HEALTHY: this is a good link and the scorer is working.")
	case lo > 10 && hi < 30:
		t.Log("SUSPICIOUS: every frame lands near 19 errors, which is what scoring NON-AMBE bits looks like.")
	default:
		t.Log("Degraded but varying — consistent with a real frequency error.")
	}
}

// TestBenchCompareOffsets measures a handful of offsets properly, in one over.
//
// The sweep's eight-frame samples turned out to be far too small on this link:
// the same frequency read 0.44% in a sweep and 4.52% over 245 frames, because
// the receiver's alignment after a retune is intermittent and a short sample
// catches whichever state it happened to land in. Rather than sweep more points
// badly, this measures FEW points WELL — long enough that each number means
// something — which is also kinder to a radio with a time-out timer.
//
//	WAYPOINT_BENCH_OFFSETS=-200,0,200 WAYPOINT_BENCH_EACH_S=8 ./cal.test \
//	  -test.v -test.run TestBenchCompareOffsets
func TestBenchCompareOffsets(t *testing.T) {
	s := benchOpen(t)
	each := time.Duration(benchEnvInt("WAYPOINT_BENCH_EACH_S", 8)) * time.Second

	list := os.Getenv("WAYPOINT_BENCH_OFFSETS")
	if list == "" {
		list = "-200,-100,0,100,200"
	}
	var offs []int
	for _, part := range strings.Split(list, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			t.Fatalf("bad offset %q", part)
		}
		offs = append(offs, n)
	}

	total := time.Duration(len(offs))*each + 60*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), total)
	defer cancel()

	cfg := SweepConfig(uint8(benchEnvInt("WAYPOINT_BENCH_CC", 1)), 50)
	t.Logf("measuring %d offsets for %s each — key up and hold for about %s total",
		len(offs), each, time.Duration(len(offs))*each)

	type result struct {
		off   int
		meter Meter
	}
	var results []result
	for _, off := range offs {
		rx := applyOffset(benchFreq(), off)
		if err := s.tune(ctx, rx, rx, cfg); err != nil {
			t.Fatalf("tune %+d: %v", off, err)
		}
		time.Sleep(300 * time.Millisecond) // settle before believing anything
		var meter Meter
		discard := 3
		deadline := time.Now().Add(each)
		for time.Now().Before(deadline) {
			f, err := modem.ReadFrame(s.port)
			if err != nil {
				continue
			}
			errs, bits, ok := scoreFrame(f)
			if !ok {
				continue
			}
			if discard > 0 {
				discard--
				continue
			}
			meter.Add(errs, bits)
		}
		results = append(results, result{off, meter})
		t.Logf("  %+5d Hz: %3d frames, BER %6.3f%%  (%d/%d bits)",
			off, meter.Frames, meter.Percent(), meter.Errors, meter.Bits)
	}

	best := -1
	for i, r := range results {
		if r.meter.Frames < 20 {
			continue // too small a sample to trust, whatever it says
		}
		if best < 0 || r.meter.Percent() < results[best].meter.Percent() {
			best = i
		}
	}
	if best < 0 {
		t.Log("no offset gathered enough frames to be trusted — was the radio keyed throughout?")
		return
	}
	t.Logf("BEST: %+d Hz at %.3f%% BER over %d frames",
		results[best].off, results[best].meter.Percent(), results[best].meter.Frames)
	if results[best].meter.Percent() < 1.0 {
		t.Logf("Under 1%% — this meets issue #20's acceptance threshold.")
	}
}
