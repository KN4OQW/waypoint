//go:build bench

package cal

// Hardware smoke tests, built only with `-tags bench` and run on a node with a
// modem actually fitted. They are the bridge between the scripted modem the rest
// of this package is tested against and a real one.
//
//	GOOS=linux GOARCH=arm GOARM=6 go test -tags bench -c ./internal/cal
//	scp cal.test <node>:  &&  ssh <node> 'sudo systemctl stop waypoint-mmdvmhost && sudo ./cal.test -test.v'
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
	"strconv"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/modem"
)

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

	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	counts := map[byte]int{}
	var total, scored int
	for time.Now().Before(deadline) {
		f, err := modem.ReadFrame(s.port)
		if err != nil {
			continue // silence, or a partial frame
		}
		total++
		counts[f.Type]++
		if total <= 40 { // the first few in full, then just the tally
			ctrl := "-"
			if len(f.Payload) > 0 {
				ctrl = fmt.Sprintf("0x%02X", f.Payload[0])
			}
			t.Logf("  frame type=0x%02X (%s) len=%d ctrl=%s", f.Type, frameName(f.Type), len(f.Payload), ctrl)
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
