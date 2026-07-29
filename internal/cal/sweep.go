package cal

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/KN4OQW/waypoint/internal/modem"
)

// The sweep: step the modem's own receive frequency across a span, score each
// step by the bit error rate of what the operator's radio is transmitting, and
// keep the step that scored best.
//
// Its one structural idea is that it advances on FRAMES, NOT ON TIME
// (RFC-0021 §4). A wall-clock sweep across a span wide enough for a ±10 ppm
// board would need a single PTT hold longer than anyone can manage, and would
// quietly score every candidate the operator was not transmitting through as
// "no errors" — the same number a perfect result produces. Waiting for frames
// instead means the operator keys in comfortable bursts, the sweep pauses when
// they let go, and no candidate is ever judged on a sample too small to mean
// anything.

// Plan is a sweep's shape. The zero value is filled in by defaults chosen for a
// hotspot with an unknown oscillator.
type Plan struct {
	// CoarseSpanHz is the half-width of the first pass. The default is wide
	// because a ±10 ppm TCXO at 70 cm is ±4.4 kHz out, and a sweep that cannot
	// reach the error is worse than none: it reports a clean minimum at the edge
	// of its own span.
	CoarseSpanHz int
	// CoarseStepHz must be smaller than the demodulator's capture window, or the
	// sweep steps straight over the frequency it is looking for.
	CoarseStepHz int
	// FineSpanHz and FineStepHz refine around the coarse winner.
	FineSpanHz int
	FineStepHz int
	// MinFrames is how many voice frames a candidate must be scored on before
	// its number counts.
	MinFrames int
	// Dwell is how long a candidate is given to produce frames WHILE THE RADIO
	// IS BELIEVED TO BE TRANSMITTING. It is what lets the sweep walk quickly
	// past the offsets that are simply too far off frequency to decode.
	Dwell time.Duration
	// IdleGap is how long without a frame anywhere before the sweep decides the
	// operator has let go of the PTT, stops consuming candidates and waits.
	IdleGap time.Duration
	// Settle is how long to leave the modem alone after retuning, and
	// SettleFrames is how many frames to discard once it starts producing them.
	//
	// Retuning reprograms the ADF7021 and restarts the mode's interface
	// configuration, and the receiver then has to re-acquire the transmission it
	// is already in the middle of. Scoring during that reads about 13% — what
	// random bits give through a perfect Golay code, whose covering radius is 3
	// — and that is indistinguishable from a bad frequency except that it
	// repeats to four decimal places.
	//
	// Gating on the receiver's own sync burst instead was tried on the bench and
	// was WORSE — every point then read a uniform 11.5%, where discarding frames
	// had produced 0.44% at the best offset. Scoring from the burst the receiver
	// has only just re-correlated evidently catches it before it is reliable.
	// The mechanism that measured well is the one kept.
	Settle       time.Duration
	SettleFrames int
	// FirstSignalWait is how long to wait for the FIRST frame before starting to
	// consume candidates. Without it a sweep that begins before the operator has
	// keyed up spends its whole grid on silence and reports hearing nothing —
	// seven candidates at a three-second dwell is over in twenty seconds, which
	// is less time than it takes to pick up a radio.
	FirstSignalWait time.Duration
	// Timeout bounds the whole sweep, including time spent waiting.
	Timeout time.Duration
}

func (p Plan) withDefaults() Plan {
	if p.CoarseSpanHz <= 0 {
		p.CoarseSpanHz = 5000
	}
	if p.CoarseStepHz <= 0 {
		p.CoarseStepHz = 500
	}
	if p.FineSpanHz <= 0 {
		p.FineSpanHz = 600
	}
	if p.FineStepHz <= 0 {
		p.FineStepHz = 100
	}
	if p.MinFrames <= 0 {
		// Fifty frames, about three seconds of speech. Ten was the original
		// guess and it was wrong by enough to invalidate a measurement: on the
		// bench the same frequency read 0.44% on an eight-frame sample and
		// 0.807% on a hundred and thirty-one, and neighbouring candidates
		// disagreed by ten percentage points on short samples while the long
		// ones formed a clean curve. The receiver's alignment after a retune is
		// intermittent, so a small sample reports whichever state it caught.
		//
		// The cost is real — fifty frames per point is three seconds of held PTT
		// — which is why the sweep pauses and resumes rather than demanding one
		// long over.
		p.MinFrames = 50
	}
	if p.Dwell <= 0 {
		// Silence at a candidate, not time spent on it — the dwell clock resets
		// on every frame. Three seconds of nothing, while frames are arriving
		// elsewhere, means this frequency genuinely cannot hear the signal.
		p.Dwell = 3 * time.Second
	}
	if p.IdleGap <= 0 {
		p.IdleGap = 2 * time.Second
	}
	if p.Settle <= 0 {
		p.Settle = 250 * time.Millisecond
	}
	if p.SettleFrames <= 0 {
		p.SettleFrames = 3
	}
	if p.FirstSignalWait <= 0 {
		p.FirstSignalWait = 60 * time.Second
	}
	if p.Timeout <= 0 {
		p.Timeout = 10 * time.Minute
	}
	return p
}

// Point is one candidate frequency and what was heard there.
type Point struct {
	OffsetHz int     `json:"offset_hz"`
	FreqHz   uint32  `json:"freq_hz"`
	Frames   int     `json:"frames"`
	Bits     int     `json:"bits"`
	Errors   int     `json:"errors"`
	BER      float64 `json:"ber_percent"`
	// Heard separates "measured, and it was clean" from "never decoded anything
	// here". Without it a silent candidate and a perfect one are both 0.00%.
	Heard bool `json:"heard"`
	// Scored means the candidate reached the plan's minimum frame count. A
	// point that was heard but not scored is real signal measured on too small
	// a sample: worth drawing on the curve, never allowed to win.
	Scored bool `json:"scored"`

	// retry marks a candidate whose measurement was inconclusive THROUGH NO
	// FAULT OF THE FREQUENCY — either it was measured before the sweep had ever
	// heard anything (the operator had not keyed up yet), or the operator let
	// go partway through it. Both deserve a second look once a signal is known
	// to exist; a candidate that simply heard nothing while other candidates
	// were hearing plenty does not, because that is a real answer about that
	// frequency. Unexported: scaffolding, not a result.
	retry bool
}

// Result is a completed sweep.
type Result struct {
	BaseHz    uint32    `json:"base_hz"`
	Points    []Point   `json:"points"`
	Best      *Point    `json:"best,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	// Aborted records a sweep that ran out of time rather than finishing, so a
	// partial curve is not read as a complete one.
	Aborted bool `json:"aborted,omitempty"`
}

// Phase names what the sweep is doing, for the progress stream.
type Phase string

const (
	PhaseCoarse  Phase = "coarse"
	PhaseFine    Phase = "fine"
	PhaseWaiting Phase = "waiting"
	PhaseDone    Phase = "done"
)

// Progress is one update. It is deliberately small: this goes to an in-memory
// broadcaster and never to the event store, for the reason RFC-0019 §8 gives
// about writing five hundred rows to an SD card to animate a progress bar.
type Progress struct {
	Phase    Phase   `json:"phase"`
	OffsetHz int     `json:"offset_hz"`
	FreqHz   uint32  `json:"freq_hz"`
	Frames   int     `json:"frames"`
	BER      float64 `json:"ber_percent"`
	Step     int     `json:"step"`
	Steps    int     `json:"steps"`
	BestHz   *int    `json:"best_offset_hz,omitempty"`
	Detail   string  `json:"detail,omitempty"`
}

// Sweep runs the two-pass search and returns the curve.
//
// It never applies anything. Measuring and applying are separate acts
// (RFC-0021 §7): the operator sees the curve first, and a sweep whose shape they
// do not believe changes nothing about their node.
func (s *Session) Sweep(ctx context.Context, plan Plan, emit func(Progress)) (Result, error) {
	plan = plan.withDefaults()
	if emit == nil {
		emit = func(Progress) {}
	}
	if s.opt.RXFreqHz == 0 {
		return Result{}, errors.New("cal: this node has no receive frequency configured, so there is nothing to sweep around")
	}

	ctx, cancel := context.WithTimeout(ctx, plan.Timeout)
	defer cancel()

	res := Result{BaseHz: s.opt.RXFreqHz, StartedAt: s.opt.now()}
	cfg := SweepConfig(s.opt.ColorCode, s.opt.txLevel())
	// Start in the waiting state: nothing has been heard yet, so the first
	// candidate must not burn its dwell while the operator is still picking up
	// their radio.
	s.lastFrame = time.Time{}

	// Wait for the operator before spending the grid on silence.
	if err := s.awaitFirstSignal(ctx, cfg, plan, emit); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return res, err
	}

	coarse := offsets(-plan.CoarseSpanHz, plan.CoarseSpanHz, plan.CoarseStepHz)
	if err := s.runPass(ctx, PhaseCoarse, coarse, cfg, plan, &res, emit); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return res, err
	}

	// Anything measured before the first frame ever arrived was measured against
	// a radio that may not have been keyed yet. Now that a signal has been
	// heard, those get one more look — otherwise a slow start silently deletes
	// the low end of the curve, and the true minimum with it if it happened to
	// live there.
	if err := s.secondLook(ctx, cfg, plan, &res, emit); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return res, err
	}

	best := bestPoint(res.Points)
	if best == nil {
		res.EndedAt = s.opt.now()
		res.Aborted = ctx.Err() != nil
		return res, notHeardError(res.Points)
	}

	// The fine pass only visits offsets the coarse pass did not, so a candidate
	// is never measured twice and the curve has no duplicated points.
	fine := offsets(best.OffsetHz-plan.FineSpanHz, best.OffsetHz+plan.FineSpanHz, plan.FineStepHz)
	fine = exclude(fine, res.Points)
	if err := s.runPass(ctx, PhaseFine, fine, cfg, plan, &res, emit); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return res, err
	}

	sort.Slice(res.Points, func(i, j int) bool { return res.Points[i].OffsetHz < res.Points[j].OffsetHz })
	res.Best = bestPoint(res.Points)
	res.EndedAt = s.opt.now()
	res.Aborted = ctx.Err() != nil

	if res.Best != nil {
		off := res.Best.OffsetHz
		emit(Progress{Phase: PhaseDone, OffsetHz: off, BestHz: &off, BER: res.Best.BER, Frames: res.Best.Frames,
			Detail: fmt.Sprintf("best offset %+d Hz at %.3f%% BER", off, res.Best.BER)})
	}
	return res, nil
}

func (s *Session) runPass(ctx context.Context, phase Phase, offs []int, cfg Config, plan Plan, res *Result, emit func(Progress)) error {
	for i, off := range offs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.waitForSignal(ctx, cfg, plan, res, emit); err != nil {
			return err
		}
		freq := applyOffset(s.opt.RXFreqHz, off)
		p, err := s.measure(ctx, phase, freq, off, i, len(offs), cfg, plan, res, emit)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				// Keep what was measured. A sweep that ran out of time still has
				// a curve worth showing, and Result.Aborted says it is partial.
				res.Points = append(res.Points, p)
				return err
			}
			return err
		}
		res.Points = append(res.Points, p)
	}
	return nil
}

// awaitFirstSignal holds at the configured frequency until the operator's radio
// is heard, before any candidate is scored.
//
// It is bounded, and then gives up and sweeps anyway. That matters as much as
// the waiting does: the node's own frequency is exactly where a badly-tuned
// board CANNOT hear anything, so a wait that insisted on a signal here would
// hang forever on the boards this feature exists for. Waiting is the common
// case; sweeping regardless is the one that must still work.
func (s *Session) awaitFirstSignal(ctx context.Context, cfg Config, plan Plan, emit func(Progress)) error {
	if plan.FirstSignalWait <= 0 {
		return nil
	}
	if err := s.tune(ctx, s.opt.RXFreqHz, s.opt.TXFreqHz, cfg); err != nil {
		return err
	}
	emit(Progress{Phase: PhaseWaiting, FreqHz: s.opt.RXFreqHz,
		Detail: "waiting for your radio — key up on this frequency in DMR simplex and hold it"})

	deadline := s.opt.now().Add(plan.FirstSignalWait)
	for s.opt.now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := modem.ReadFrame(s.port)
		if err != nil {
			if errors.Is(err, modem.ErrTimeout) {
				continue
			}
			return fmt.Errorf("cal: read from %s: %w", s.opt.Port, err)
		}
		if _, _, ok := scoreFrame(f); ok {
			s.lastFrame = s.opt.now()
			emit(Progress{Phase: PhaseCoarse, FreqHz: s.opt.RXFreqHz, Detail: "heard you — sweeping"})
			return nil
		}
	}
	// Nothing here. That is not a failure: this frequency may be the one the
	// board cannot hear on, which is the whole reason for sweeping.
	emit(Progress{Phase: PhaseCoarse, FreqHz: s.opt.RXFreqHz,
		Detail: "nothing heard on the configured frequency — sweeping to look for it"})
	return nil
}

// waitForSignal pauses the sweep between candidates when the operator has
// stopped transmitting, and — this is the part that makes it work — does the
// waiting on the best frequency found so far rather than on the next candidate.
//
// Waiting on the next candidate would deadlock exactly when it matters: that
// frequency may be one the signal cannot be heard on at all, so no amount of
// patience there would ever detect that the operator had keyed up again. The
// best-so-far offset is, by construction, one where this radio was decoded.
//
// Before anything has ever been heard it returns immediately: there is no known
// good frequency to wait on, and the sweep's job is then to go and find one.
func (s *Session) waitForSignal(ctx context.Context, cfg Config, plan Plan, res *Result, emit func(Progress)) error {
	if s.lastFrame.IsZero() || s.opt.now().Sub(s.lastFrame) <= plan.IdleGap {
		return nil
	}
	best := bestHeard(res.Points)
	if best == nil {
		return nil
	}
	if err := s.tune(ctx, best.FreqHz, applyOffset(s.opt.TXFreqHz, best.OffsetHz), cfg); err != nil {
		return err
	}
	off := best.OffsetHz
	emit(Progress{Phase: PhaseWaiting, OffsetHz: off, FreqHz: best.FreqHz, BestHz: &off,
		Detail: "paused — key your radio again to carry on where the sweep left off"})

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := modem.ReadFrame(s.port)
		if err != nil {
			if errors.Is(err, modem.ErrTimeout) {
				continue
			}
			return fmt.Errorf("cal: read from %s: %w", s.opt.Port, err)
		}
		if _, _, ok := scoreFrame(f); ok {
			s.lastFrame = s.opt.now()
			return nil
		}
	}
}

// secondLook re-measures the candidates whose first measurement was spoiled by
// the operator's timing rather than by the frequency: the ones walked past
// before any signal existed, and the ones the operator stopped transmitting
// through. It runs once, and only when something has since been heard — if the
// radio was never keyed at all there is nothing to re-check and the sweep's
// answer is already the right one.
func (s *Session) secondLook(ctx context.Context, cfg Config, plan Plan, res *Result, emit func(Progress)) error {
	if s.lastFrame.IsZero() {
		return nil
	}
	var stale []int
	for _, p := range res.Points {
		if p.retry && !p.Scored {
			stale = append(stale, p.OffsetHz)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	for i, off := range stale {
		if err := ctx.Err(); err != nil {
			return err
		}
		freq := applyOffset(s.opt.RXFreqHz, off)
		p, err := s.measure(ctx, PhaseCoarse, freq, off, i, len(stale), cfg, plan, res, emit)
		if err != nil {
			return err
		}
		replacePoint(res.Points, p)
	}
	return nil
}

func replacePoint(points []Point, p Point) {
	for i := range points {
		if points[i].OffsetHz == p.OffsetHz {
			points[i] = p
			return
		}
	}
}

// measure scores one candidate frequency.
//
// The loop below is where "advance on frames, not on seconds" is actually
// decided, and the decision it makes is which KIND of silence it is looking at.
// The modem cannot tell them apart — an unkeyed radio and a radio transmitting
// 4 kHz away both produce nothing — so the sweep infers it from whether it has
// heard anything ANYWHERE recently:
//
//   - Nothing heard on any candidate for longer than IdleGap: the operator is
//     not transmitting. Hold position, say so, and consume no candidates. This
//     is also the state a sweep starts in, so the first candidate is not thrown
//     away while the operator is still reading the instructions.
//   - Frames arriving elsewhere but not here: this candidate really is off
//     frequency. Give it Dwell and move on.
//
// Getting this wrong in either direction is a bad sweep: forgiving everything
// means one unheard candidate stalls the run until the global timeout, and
// forgiving nothing means the whole grid is marked unheard while the operator
// is still picking up their radio.
func (s *Session) measure(ctx context.Context, phase Phase, freq uint32, off, step, steps int, cfg Config, plan Plan, res *Result, emit func(Progress)) (Point, error) {
	p := Point{OffsetHz: off, FreqHz: freq}

	// The transmit frequency is moved with the receive one. On a hotspot both
	// come off the same oscillator, so a sweep that moved only the receive path
	// would leave the board transmitting on a frequency it is no longer tuned to
	// receive — and the modem would be tuned two different ways for the length
	// of the sweep.
	if err := s.tune(ctx, freq, applyOffset(s.opt.TXFreqHz, off), cfg); err != nil {
		return p, err
	}

	// Let the synthesiser and the receiver settle before believing anything.
	if plan.Settle > 0 {
		if err := sleepCtx(ctx, plan.Settle); err != nil {
			return p, err
		}
	}

	var meter Meter
	discard := plan.SettleFrames
	dwellStart := s.opt.now()
	waiting := false

	emit(Progress{Phase: phase, OffsetHz: off, FreqHz: freq, Step: step + 1, Steps: steps, BestHz: bestOffset(res.Points)})

	for meter.Frames < plan.MinFrames {
		if err := ctx.Err(); err != nil {
			p.fill(meter, plan.MinFrames)
			return p, err
		}
		f, err := modem.ReadFrame(s.port)
		if err != nil {
			if !errors.Is(err, modem.ErrTimeout) {
				p.fill(meter, plan.MinFrames)
				return p, fmt.Errorf("cal: read from %s: %w", s.opt.Port, err)
			}
			// Silence here means one of two things and the modem cannot say
			// which: this offset is out of the demodulator's reach, or the
			// operator is not transmitting. A candidate assumes the first —
			// it gives the frequency Dwell to prove itself and then moves on.
			// The second is handled between candidates, by waitForSignal, which
			// can afford to be patient because it does its waiting on a
			// frequency already known to work.
			if !waiting && meter.Frames == 0 {
				waiting = true
				emit(Progress{Phase: PhaseWaiting, OffsetHz: off, FreqHz: freq, Step: step + 1, Steps: steps,
					BestHz: bestOffset(res.Points),
					Detail: "listening — key your radio on this frequency in DMR and hold it"})
			}
			if s.opt.now().Sub(dwellStart) > plan.Dwell {
				p.fill(meter, plan.MinFrames)
				p.retry = !p.Scored && (s.lastFrame.IsZero() || meter.Frames > 0)
				return p, nil
			}
			continue
		}

		errs, bits, ok := scoreFrame(f)
		if !ok {
			continue
		}
		if discard > 0 {
			// Still settling after the retune. It counts as SIGNAL — the operator
			// is transmitting and the dwell clock must not run out on them — but
			// not as a measurement.
			discard--
			s.lastFrame = s.opt.now()
			dwellStart = s.lastFrame
			continue
		}
		meter.Add(errs, bits)
		// Dwell is silence AT THIS CANDIDATE, not elapsed time on it. Measuring
		// from the candidate's start instead would abandon any frequency that
		// takes longer than Dwell to reach MinFrames — which is every frequency
		// once frames arrive at DMR's real rate of one per 60 ms.
		s.lastFrame = s.opt.now()
		dwellStart = s.lastFrame
		if waiting {
			waiting = false
			emit(Progress{Phase: phase, OffsetHz: off, FreqHz: freq, Step: step + 1, Steps: steps,
				Frames: meter.Frames, BER: meter.Percent(), BestHz: bestOffset(res.Points), Detail: "signal returned"})
		}
		if meter.Frames%5 == 0 || meter.Frames == plan.MinFrames {
			emit(Progress{Phase: phase, OffsetHz: off, FreqHz: freq, Step: step + 1, Steps: steps,
				Frames: meter.Frames, BER: meter.Percent(), BestHz: bestOffset(res.Points)})
		}
	}

	p.fill(meter, plan.MinFrames)
	return p, nil
}

func (p *Point) fill(m Meter, minFrames int) {
	p.Frames, p.Bits, p.Errors = m.Frames, m.Bits, m.Errors
	p.BER = m.Percent()
	p.Heard = m.Frames > 0
	p.Scored = m.Frames >= minFrames
}

// scoreFrame turns a received frame into an error count, or reports that it
// carries no evidence.
//
// Only VOICE frames are scored. MMDVMCal scores everything that is not a header
// or a terminator, which sweeps DMR data frames into the measurement — they
// carry no AMBE and no voice FEC, so the "errors" found in them are noise in the
// literal sense. Being stricter costs nothing: a calibration transmission is
// voice.
func scoreFrame(f modem.Frame) (errs, bits int, ok bool) {
	switch f.Type {
	case CmdDMRData1, CmdDMRData2:
		if len(f.Payload) < 1 {
			return 0, 0, false
		}
		// The control byte: 0x20 is the voice frame carrying the sync pattern,
		// 1..5 are the rest of the superframe, and 0x40|type is a data frame.
		ctrl := f.Payload[0]
		if ctrl != 0x20 && (ctrl < 1 || ctrl > 5) {
			return 0, 0, false
		}
		e, ok := DMRVoiceFrame(f.Payload[1:])
		if !ok {
			return 0, 0, false
		}
		return e, bitsPerDMRFrame, true

	case CmdDStarData:
		e, ok := DStarVoiceFrame(f.Payload)
		if !ok {
			return 0, 0, false
		}
		return e, bitsPerDStarFrame, true
	}
	return 0, 0, false
}

// offsets builds an inclusive range, always including zero so the sweep always
// measures the frequency the node is actually configured for — the number every
// other point is judged against.
func offsets(lo, hi, step int) []int {
	if step <= 0 {
		return nil
	}
	seen := map[int]bool{}
	var out []int
	add := func(v int) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for v := lo; v <= hi; v += step {
		add(v)
	}
	if lo <= 0 && hi >= 0 {
		add(0)
	}
	sort.Ints(out)
	return out
}

// notHeardError separates the two ways a sweep comes back with no winner. They
// look identical in a log and mean opposite things to an operator: one is "your
// radio never keyed", the other is "it keyed, but never long enough in one
// place".
func notHeardError(points []Point) error {
	for _, p := range points {
		if p.Heard {
			return ErrNotEnoughSignal
		}
	}
	return ErrNothingHeard
}

func exclude(offs []int, done []Point) []int {
	seen := make(map[int]bool, len(done))
	for _, p := range done {
		seen[p.OffsetHz] = true
	}
	out := offs[:0:0]
	for _, o := range offs {
		if !seen[o] {
			out = append(out, o)
		}
	}
	return out
}

// bestPoint is the lowest bit error rate among candidates that were fully
// scored. Ties go to the offset closest to zero: two frequencies that measure
// the same are not equally good, and the one that changes the node least is the
// one to keep.
func bestPoint(points []Point) *Point {
	var best *Point
	for i := range points {
		p := &points[i]
		if !p.Scored {
			continue
		}
		switch {
		case best == nil, p.BER < best.BER,
			p.BER == best.BER && abs(p.OffsetHz) < abs(best.OffsetHz):
			best = p
		}
	}
	return best
}

// bestHeard is the best frequency ANYTHING was decoded on, scored or not. It is
// what the pause waits on: a candidate measured on eight frames instead of ten
// is still a frequency this radio is audible at, which is all the pause needs.
func bestHeard(points []Point) *Point {
	var best *Point
	for i := range points {
		p := &points[i]
		if !p.Heard {
			continue
		}
		if best == nil || p.BER < best.BER {
			best = p
		}
	}
	return best
}

func bestOffset(points []Point) *int {
	if b := bestPoint(points); b != nil {
		off := b.OffsetHz
		return &off
	}
	return nil
}

// applyOffset shifts a frequency, guarding the subtraction: an offset larger
// than the frequency would wrap a uint32 into the gigahertz.
func applyOffset(base uint32, off int) uint32 {
	if base == 0 {
		return 0
	}
	if off < 0 && uint32(-off) > base {
		return 0
	}
	return uint32(int64(base) + int64(off))
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// sleepCtx waits, or gives up early if the caller does.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
