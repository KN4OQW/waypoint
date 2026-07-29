package cal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/KN4OQW/waypoint/internal/modem"
)

// Transmitting, and the timer on it.
//
// This is the only door in Waypoint through which a carrier reaches an antenna,
// and everything about it is built so that no path — a dropped browser, a
// cancelled request, a panicking goroutine, a wedged read — leaves a node keyed.

// MaxBurst bounds a single transmission. It is not a preference: it is the
// longest a Waypoint node will transmit for a calibration test, and the timer
// enforcing it lives here rather than in a caller.
const MaxBurst = 30 * time.Second

// ErrTransmitting rejects a second burst while one is running.
var ErrTransmitting = errors.New("cal: this session is already transmitting")

// clampBurst bounds a requested burst. A caller asking for nothing, or for
// something absurd, gets MaxBurst rather than an error: the ceiling is not a
// negotiation, and refusing outright would only tempt a caller into a loop of
// shorter bursts that adds up to the same carrier.
func clampBurst(d time.Duration) time.Duration {
	if d <= 0 || d > MaxBurst {
		return MaxBurst
	}
	return d
}

// Burst is a completed transmission, reported back so the operator sees what the
// node actually did rather than what was asked for.
type Burst struct {
	State    State         `json:"state"`
	FreqHz   uint32        `json:"freq_hz"`
	Duration time.Duration `json:"duration"`
	// Levels is the modem's own report of what it heard while transmitting,
	// present only on a full-size MMDVM (a hotspot sends no level replies).
	Levels *LevelReport `json:"levels,omitempty"`
}

// Transmit keys the radio in a calibration state for at most d, then unkeys it.
//
// Every guard is applied before a single byte is written:
//
//   - the state must be one that transmits, so a caller cannot key "in DMR";
//   - the transmit frequency must be configured and in an amateur allocation,
//     checked against the node's own configuration and never against anything
//     from a request;
//   - the duration is clamped to MaxBurst;
//   - one burst at a time.
//
// The unkey is deferred with a context that does NOT inherit the caller's. A
// cancelled request must end a transmission, not abandon one — the caller going
// away is precisely the case this exists for.
func (s *Session) Transmit(ctx context.Context, state State, d time.Duration) (Burst, error) {
	if !state.Transmits() {
		return Burst{}, fmt.Errorf("cal: %s is a receive state and cannot be used to transmit", state)
	}
	if err := CheckTXFrequency(s.opt.TXFreqHz); err != nil {
		return Burst{}, err
	}
	d = clampBurst(d)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Burst{}, ErrClosed
	}
	if s.txStop != nil {
		s.mu.Unlock()
		return Burst{}, ErrTransmitting
	}
	burstCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), d)
	s.txStop = stop
	s.mu.Unlock()

	defer func() {
		stop()
		s.mu.Lock()
		s.txStop = nil
		s.mu.Unlock()
		// Unkey on a context of its own. If the caller's context is already
		// cancelled — the browser closed, the request timed out — the unkey must
		// still be written, and this is the line that guarantees it.
		unkey, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.command(unkey, setTransmit(false), CmdCalData); err != nil {
			// Best effort has already failed once; try the raw write. Close()
			// tries again after this, and the modem also drops its carrier when
			// the port closes.
			_ = s.writeFrame(setTransmit(false))
		}
	}()

	cfg := TXConfig(state, s.opt.ColorCode, s.opt.txLevel())
	if err := s.tune(ctx, s.opt.RXFreqHz, s.opt.TXFreqHz, cfg); err != nil {
		return Burst{}, err
	}

	started := s.opt.now()
	if err := s.command(ctx, setTransmit(true), CmdCalData); err != nil {
		return Burst{}, err
	}

	b := Burst{State: state, FreqHz: s.opt.TXFreqHz}
	// Drain replies for the length of the burst. A full-size MMDVM reports its
	// levels while transmitting; a hotspot says nothing, and the loop is then
	// simply how the burst is timed.
	for {
		select {
		case <-burstCtx.Done():
			b.Duration = s.opt.now().Sub(started)
			return b, nil
		case <-ctx.Done():
			// The caller went away. End the burst — do not extend it — and
			// report what was done rather than an error, because the
			// transmission did happen.
			b.Duration = s.opt.now().Sub(started)
			return b, nil
		default:
		}

		f, err := modem.ReadFrame(s.port)
		if err != nil {
			if errors.Is(err, modem.ErrTimeout) {
				continue
			}
			b.Duration = s.opt.now().Sub(started)
			return b, fmt.Errorf("cal: read from %s: %w", s.opt.Port, err)
		}
		if f.Type == CmdCalData {
			if lr, err := ParseLevels(f.Payload); err == nil {
				b.Levels = &lr
			}
		}
	}
}

// Transmitting reports whether a burst is running, so a status endpoint can say
// so without racing the burst itself.
func (s *Session) Transmitting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.txStop != nil
}

// Listen watches a receive state and reports what the modem says about the
// signal reaching it — the repeater board's RX workflow.
//
// This is the half of #20's level/invert workflow that is DECIDABLE. A full-size
// MMDVM reports whether the signal it heard was inverted, and that answer settles
// RXInvert outright; the levels it reports alongside are shown so an operator can
// see the waveform is not clipping, but Waypoint does not pretend they let it
// choose an RX level for them (RFC-0021 §6).
//
// A hotspot sends no level replies at all. Rather than spin silently, this
// returns ErrNothingHeard so the caller can say "this board does not report
// levels" instead of showing an empty panel that looks broken.
func (s *Session) Listen(ctx context.Context, d time.Duration) ([]LevelReport, error) {
	if d <= 0 || d > 2*time.Minute {
		d = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	// D-Star's calibration state is the one both firmwares accept for level
	// reporting, and it does not key: entering it only arms the measurement.
	cfg := TXConfig(StateDStarCal, s.opt.ColorCode, s.opt.txLevel())
	if err := s.tune(ctx, s.opt.RXFreqHz, s.opt.TXFreqHz, cfg); err != nil {
		return nil, err
	}

	var out []LevelReport
	for {
		if err := ctx.Err(); err != nil {
			if len(out) == 0 {
				return nil, ErrNothingHeard
			}
			return out, nil
		}
		f, err := modem.ReadFrame(s.port)
		if err != nil {
			if errors.Is(err, modem.ErrTimeout) {
				continue
			}
			return out, fmt.Errorf("cal: read from %s: %w", s.opt.Port, err)
		}
		if f.Type != CmdCalData {
			continue
		}
		lr, err := ParseLevels(f.Payload)
		if err != nil {
			continue
		}
		out = append(out, lr)
	}
}
