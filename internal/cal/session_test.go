package cal

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/modem"
)

// A scripted modem, so the whole engine — arbitration, framing, the sweep and
// the transmit timer — is testable with no hardware on the bench.
//
// The interesting part is that it models a REAL OSCILLATOR ERROR: it decides
// what to send back from how far the session has tuned it away from the
// frequency the "radio" is actually transmitting on, and it builds genuine DMR
// voice frames with a genuine number of bit errors flipped into them. So the
// sweep test is not checking that the code walks a list of numbers; it is the
// acceptance criterion of #20 run in software, with the answer hidden behind the
// same measurement an operator's radio would provide.

type fakeModem struct {
	mu sync.Mutex

	protocol uint8
	// trueOffsetHz is the error the board has. The sweep is supposed to find it
	// and nothing tells it the answer.
	trueOffsetHz int
	baseHz       uint32
	// captureHz is how far off frequency the demodulator still decodes at all.
	captureHz int

	tunedRX uint32
	state   State
	keyed   bool

	// mute makes the modem stop sending voice frames, standing in for an
	// operator who has let go of the PTT.
	mute bool
	// silentAt makes one specific offset produce nothing, standing in for a
	// candidate the operator was not transmitting through.
	silentAt map[int]bool
	// framePeriod paces the voice frames. Zero means "as fast as the session
	// can read them", which keeps most tests quick; the tests that care about
	// the sweep's timing set it to something closer to DMR's real 60 ms.
	framePeriod time.Duration
	lastEmit    time.Time

	out     []byte // bytes waiting to be read by the session
	written [][]byte
	seq     byte
	closed  bool
}

func newFakeModem(base uint32, trueOffset int) *fakeModem {
	return &fakeModem{
		protocol:     1,
		baseHz:       base,
		trueOffsetHz: trueOffset,
		captureHz:    800,
		silentAt:     map[int]bool{},
	}
}

func (m *fakeModem) Read(p []byte) (int, error) {
	m.mu.Lock()
	if len(m.out) == 0 {
		m.fill()
	}
	if len(m.out) == 0 {
		m.mu.Unlock()
		// A real port blocks for its VTIME window before reporting silence.
		// Returning instantly would turn every wait in the engine into a busy
		// loop and make the timing these tests exercise meaningless.
		time.Sleep(2 * time.Millisecond)
		return 0, modem.ErrTimeout
	}
	n := copy(p, m.out)
	m.out = m.out[n:]
	m.mu.Unlock()
	return n, nil
}

// fill produces whatever the modem would be sending right now.
func (m *fakeModem) fill() {
	if m.mute || m.state != StateDMR {
		return
	}
	if m.framePeriod > 0 {
		if time.Since(m.lastEmit) < m.framePeriod {
			return
		}
		m.lastEmit = time.Now()
	}
	if m.silentAt[int(int64(m.tunedRX)-int64(m.baseHz))] {
		return
	}
	errs, heard := m.frameErrors()
	if !heard {
		return
	}
	frame := buildDMRFrame([3]uint32{0x123, 0xABC, 0x456})
	for i := 0; i < errs; i++ {
		// Spread the damage across the three codewords so the count is exact.
		switch i % 3 {
		case 0:
			flipBit(frame, dmrATable[:], 0, i%20)
		case 1:
			flipBit(frame, dmrATable[:], 72, i%20)
		case 2:
			flipBit(frame, dmrBTable[:], 192, i%20)
		}
	}
	// A real DMR superframe is a sync burst (control 0x20) followed by five
	// counted ones. The sweep will not score anything until it sees the sync, so
	// a fake that never sent one would be a fake that cannot be measured.
	m.seq++
	if m.seq > 5 {
		m.seq = 0 // 0 stands in for the sync burst below
	}
	ctrl := m.seq
	if ctrl == 0 {
		ctrl = 0x20
	}
	m.out = append(m.out, dmrFrame(ctrl, frame)...)
}

// frameErrors is the physics: errors climb with the tuning error, and past the
// capture range the demodulator hears nothing at all.
//
// The curve is monotonic with a single minimum on purpose. Real hardware has a
// small plateau at the bottom, and a sweep facing one resolves the tie towards
// the offset closest to zero — correct behaviour, but it would make "did the
// sweep find the injected error" untestable by hiding the answer inside a band
// of equally good candidates.
func (m *fakeModem) frameErrors() (int, bool) {
	tuned := int(int64(m.tunedRX) - int64(m.baseHz))
	delta := abs(tuned - m.trueOffsetHz)
	if delta > m.captureHz {
		return 0, false
	}
	return delta / 50, true
}

func dmrFrame(seq byte, data []byte) []byte {
	out := []byte{0xE0, byte(len(data) + 4), CmdDMRData2, seq}
	return append(out, data...)
}

func (m *fakeModem) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.written = append(m.written, append([]byte(nil), p...))

	if len(p) < 3 || p[0] != 0xE0 {
		return len(p), nil
	}
	cmd := p[2]
	switch cmd {
	case 0x00: // GET_VERSION
		desc := []byte("MMDVM_HS_Dual_Hat-v1.6.1 20210101 14.7456MHz ADF7021 FW by CA6JAU GitID #deadbee")
		reply := []byte{0xE0, byte(len(desc) + 4), 0x00, m.protocol}
		m.out = append(m.out, append(reply, desc...)...)
		return len(p), nil
	case CmdSetFreq:
		m.tunedRX = uint32(p[4]) | uint32(p[5])<<8 | uint32(p[6])<<16 | uint32(p[7])<<24
		// Retuning drops whatever was mid-flight, as a real synthesiser does.
		m.out = nil
	case CmdSetConfig:
		if m.protocol == 2 {
			m.state = State(p[7])
		} else {
			m.state = State(p[6])
		}
	case CmdCalData:
		m.keyed = p[3] == 0x01
	}
	m.out = append(m.out, 0xE0, 4, CmdACK, cmd)
	return len(p), nil
}

func (m *fakeModem) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *fakeModem) setMute(v bool) {
	m.mu.Lock()
	m.mute = v
	m.mu.Unlock()
}

func (m *fakeModem) isKeyed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.keyed
}

// fastPlan keeps the timings real but small. Everything the sweep does is
// driven by wall-clock silence, so the tests scale the durations down rather
// than faking a clock — which also means they exercise the same code paths a
// bench run does.
func fastPlan() Plan {
	return Plan{
		CoarseSpanHz: 2000, CoarseStepHz: 250,
		FineSpanHz: 600, FineStepHz: 100,
		MinFrames:       10,
		Settle:          time.Millisecond,
		FirstSignalWait: 2 * time.Second,
		Dwell:           150 * time.Millisecond,
		IdleGap:         200 * time.Millisecond,
		Timeout:         60 * time.Second,
	}
}

func openFake(t *testing.T, m *fakeModem, mutate func(*Options)) *Session {
	t.Helper()
	opt := Options{
		Port:      "/dev/fake",
		RXFreqHz:  m.baseHz,
		TXFreqHz:  m.baseHz,
		ColorCode: 1,
		TXLevel:   50,
		open: func(string, int, time.Duration) (io.ReadWriteCloser, error) {
			return m, nil
		},
	}
	if mutate != nil {
		mutate(&opt)
	}
	s, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestSweepFindsAnInjectedOffset is #20's acceptance criterion in software: a
// board that is 400 Hz out is brought to a clean bit error rate by the sweep
// alone, with nothing telling it the answer.
func TestSweepFindsAnInjectedOffset(t *testing.T) {
	const injected = 400
	m := newFakeModem(438_800_000, injected)
	s := openFake(t, m, nil)

	res, err := s.Sweep(context.Background(), fastPlan(), nil)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Best == nil {
		t.Fatal("the sweep found no best point")
	}
	if got := res.Best.OffsetHz; got != injected {
		t.Fatalf("best offset %+d Hz, want %+d Hz", got, injected)
	}
	if res.Best.BER >= 1.0 {
		t.Fatalf("best BER is %.3f%%, which is not the sub-1%% the acceptance criterion asks for", res.Best.BER)
	}
	// The point the node was already configured for must be in the curve, and
	// must be visibly worse — that is what the operator is being shown.
	var zero *Point
	for i := range res.Points {
		if res.Points[i].OffsetHz == 0 {
			zero = &res.Points[i]
		}
	}
	if zero == nil {
		t.Fatal("the sweep never measured the node's configured frequency")
	}
	if zero.BER <= res.Best.BER {
		t.Fatalf("the configured frequency scored %.3f%% and the winner %.3f%%; the curve has no shape", zero.BER, res.Best.BER)
	}
}

// TestSweepScoresEveryCandidateOnEnoughFrames is the rule that keeps a lucky
// single frame from winning.
func TestSweepScoresEveryCandidateOnEnoughFrames(t *testing.T) {
	m := newFakeModem(438_800_000, 0)
	s := openFake(t, m, nil)

	plan := fastPlan()
	plan.MinFrames = 12
	res, err := s.Sweep(context.Background(), plan, nil)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	for _, p := range res.Points {
		if p.Heard && p.Frames < plan.MinFrames {
			t.Fatalf("offset %+d Hz was scored on %d frames, fewer than the %d required", p.OffsetHz, p.Frames, plan.MinFrames)
		}
	}
}

// TestSweepDistinguishesSilenceFromCleanReception is the failure this design is
// most worried about: a candidate nothing was heard on scores 0.00% BER, which
// is the same number a perfect candidate scores.
func TestSweepDistinguishesSilenceFromCleanReception(t *testing.T) {
	m := newFakeModem(438_800_000, 0)
	m.silentAt[500] = true
	s := openFake(t, m, nil)

	plan := fastPlan()
	plan.CoarseSpanHz, plan.CoarseStepHz = 1000, 500
	plan.FineSpanHz, plan.FineStepHz = 100, 100
	plan.MinFrames = 4
	res, err := s.Sweep(context.Background(), plan, nil)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Sweep: %v", err)
	}
	for _, p := range res.Points {
		if p.OffsetHz == 500 && p.Heard {
			t.Fatal("a candidate nothing was transmitted on was marked as heard")
		}
	}
	if res.Best != nil && res.Best.OffsetHz == 500 {
		t.Fatal("a candidate nothing was heard on won the sweep")
	}
}

// TestSweepPausesAndResumes covers the operator letting go of the PTT partway
// through a candidate. The sweep must say it is waiting and then pick up where
// it left off, not score the candidate on what it had.
func TestSweepPausesAndResumes(t *testing.T) {
	m := newFakeModem(438_800_000, 0)
	// Pace the frames near DMR's real rate so the sweep is still running when
	// the signal is taken away.
	m.framePeriod = 10 * time.Millisecond
	s := openFake(t, m, nil)
	var (
		mu      sync.Mutex
		waited  bool
		resumed bool
	)
	// Let the sweep hear the radio, then take it away mid-run and give it back.
	go func() {
		time.Sleep(120 * time.Millisecond)
		m.setMute(true)
		time.Sleep(400 * time.Millisecond)
		m.setMute(false)
	}()

	plan := fastPlan()
	plan.CoarseSpanHz, plan.CoarseStepHz = 500, 250
	plan.MinFrames = 25
	res, err := s.Sweep(context.Background(), plan, func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		if p.Phase == PhaseWaiting {
			waited = true
		}
		if waited && p.Frames > 0 {
			resumed = true
		}
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !waited {
		t.Error("the sweep never reported that it was waiting for a signal")
	}
	if !resumed {
		t.Error("the sweep never reported frames after the signal came back")
	}
	if res.Best == nil || !res.Best.Heard {
		t.Error("the candidate was not scored after the signal returned")
	}
}

// TestSweepReportsNothingHeard is the first-attempt case: the radio was never
// keyed. It must be a named outcome, not a fault, and not an offset.
func TestSweepReportsNothingHeard(t *testing.T) {
	m := newFakeModem(438_800_000, 0)
	m.setMute(true)
	s := openFake(t, m, nil)

	plan := fastPlan()
	plan.CoarseSpanHz, plan.CoarseStepHz = 500, 500
	plan.MinFrames = 2
	_, err := s.Sweep(context.Background(), plan, nil)
	if !errors.Is(err, ErrNothingHeard) {
		t.Fatalf("error = %v, want ErrNothingHeard", err)
	}
}

func TestSweepRetunesTheSynthesiser(t *testing.T) {
	m := newFakeModem(438_800_000, 0)
	s := openFake(t, m, nil)

	plan := fastPlan()
	plan.CoarseSpanHz, plan.CoarseStepHz = 500, 500
	plan.MinFrames = 2
	_, _ = s.Sweep(context.Background(), plan, nil)

	// Every SET_FREQ must be followed by a SET_CONFIG, because SET_FREQ alone
	// only stores the value on a hotspot — the synthesiser is not reprogrammed
	// until the interface is configured again.
	m.mu.Lock()
	defer m.mu.Unlock()
	var freqs, pending int
	for _, w := range m.written {
		if len(w) < 3 {
			continue
		}
		switch w[2] {
		case CmdSetFreq:
			freqs++
			pending++
		case CmdSetConfig:
			if pending > 0 {
				pending--
			}
		}
	}
	if freqs == 0 {
		t.Fatal("the sweep never tuned the modem")
	}
	if pending != 0 {
		t.Fatalf("%d SET_FREQ frames were not followed by a SET_CONFIG, so the modem never retuned", pending)
	}
}
