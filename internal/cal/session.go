package cal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/modem"
)

// A calibration session owns the modem port for as long as it runs, under the
// same arbitration rule detection and flashing use (RFC-0020): MMDVM-Host holds
// the UART in normal operation, so a session either finds it stopped or stops it
// with explicit authorisation — and restarts it on every exit path, including
// the ones nobody plans for.
//
// The session is also where transmitting is bounded. Nothing outside this file
// can build the frame that keys a radio; Transmit is the only door, and it is
// the door with the timer on it.

var (
	// ErrNoAnswer means the port opened but nothing on it spoke MMDVM.
	ErrNoAnswer = errors.New("cal: the modem did not answer on this port")
	// ErrClosed is a session used after Close.
	ErrClosed = errors.New("cal: the calibration session is closed")
	// ErrNothingHeard means a sweep ran its course without ever decoding a
	// frame. It is a distinct error because it is the single most likely
	// outcome of a first attempt, and it is not a fault — it means the radio
	// was not keyed, was on the wrong frequency, or was not in DMR.
	ErrNothingHeard = errors.New("cal: nothing was heard on any frequency in the sweep")
	// ErrNotEnoughSignal means frames WERE decoded, but no candidate ever got
	// enough of them to be scored — an operator keying in bursts too short to
	// measure. The difference from ErrNothingHeard is the difference between
	// "check your radio" and "hold the button a little longer".
	ErrNotEnoughSignal = errors.New("cal: a signal was heard, but no frequency was measured on enough frames to be trusted")
)

// Options configures a session. The frequencies come from the node's own
// configuration; there is deliberately no way to pass one in from a request
// body (RFC-0021 §2).
type Options struct {
	Port     string
	Baud     int
	Protocol uint8 // 1 or 2, from detection; a session re-reads it anyway

	RXFreqHz  uint32
	TXFreqHz  uint32
	ColorCode uint8
	TXLevel   float32 // per-mode TX level / deviation, percent
	RFLevel   float32 // RF power on a hotspot, percent

	// Holder is MMDVM-Host. Nil means nothing holds the port — tests, and a
	// node whose stack has never started.
	Holder   modem.Holder
	StopHost bool

	ReadTimeout time.Duration
	Now         func() time.Time

	// open is the serial layer, injectable so the whole session can be tested
	// against a scripted modem with no hardware present.
	open func(path string, baud int, to time.Duration) (io.ReadWriteCloser, error)
}

func (o Options) readTimeout() time.Duration {
	if o.ReadTimeout > 0 {
		return o.ReadTimeout
	}
	// Long enough that an idle gap between a radio's transmissions does not
	// look like a dead port, short enough that a sweep stays responsive to
	// cancellation.
	return 200 * time.Millisecond
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o Options) rfLevel() float32 {
	if o.RFLevel <= 0 {
		return 100
	}
	return o.RFLevel
}

func (o Options) txLevel() float32 {
	if o.TXLevel <= 0 {
		return 50
	}
	return o.TXLevel
}

// Session is an open calibration session.
type Session struct {
	opt      Options
	port     io.ReadWriteCloser
	protocol uint8
	identity modem.Version
	restore  func()

	mu     sync.Mutex
	closed bool
	txStop func() // cancels the running transmit burst, if any

	// lastFrame is when a voice frame was last decoded on ANY candidate
	// frequency. It is what lets the sweep tell an unkeyed radio from a
	// candidate that is simply off frequency (sweep.go, measure). It is only
	// touched from the sweep goroutine, which is the only thing that reads
	// traffic; transmit bursts do not decode voice.
	lastFrame time.Time
}

// Open takes the port and confirms something on it speaks MMDVM.
//
// The version exchange is not ceremony: a session that assumed the port was a
// modem would happily write a SET_CONFIG at a GPS receiver, and the protocol
// version it returns decides which of the two incompatible SET_CONFIG layouts
// every later frame uses.
func Open(ctx context.Context, opt Options) (*Session, error) {
	restore, err := arbitrate(ctx, opt.Holder, opt.StopHost)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			restore()
		}
	}()

	openFn := opt.open
	if openFn == nil {
		openFn = func(path string, baud int, to time.Duration) (io.ReadWriteCloser, error) {
			return modem.OpenPort(path, baud, modem.ParityNone, to)
		}
	}
	baud := opt.Baud
	if baud == 0 {
		baud = modem.Bauds[0]
	}
	port, err := openFn(opt.Port, baud, opt.readTimeout())
	if err != nil {
		return nil, fmt.Errorf("cal: open %s: %w", opt.Port, err)
	}

	s := &Session{opt: opt, port: port, protocol: opt.Protocol, restore: restore}
	v, err := s.version(ctx)
	if err != nil {
		port.Close()
		return nil, err
	}
	s.identity, s.protocol = v, v.Protocol
	ok = true
	return s, nil
}

// arbitrate applies RFC-0020's port-ownership rule. It is a copy of detection's
// in shape and not in code, because the restore step differs: a calibration
// session holds the port for minutes rather than seconds, and its restore has to
// survive the request that started it going away.
func arbitrate(ctx context.Context, h modem.Holder, stopHost bool) (func(), error) {
	noop := func() {}
	if h == nil {
		return noop, nil
	}
	active, err := h.Active(ctx)
	if err != nil {
		return nil, fmt.Errorf("cal: check MMDVM-Host: %w", err)
	}
	if !active {
		return noop, nil
	}
	if !stopHost {
		return nil, modem.ErrHostRunning
	}
	if err := h.Stop(ctx); err != nil {
		return nil, fmt.Errorf("cal: stop MMDVM-Host: %w", err)
	}
	return func() {
		restart, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = h.Start(restart)
	}, nil
}

// Identity is what the modem said about itself when the session opened.
func (s *Session) Identity() modem.Version { return s.identity }

// Close returns the modem and the node to a usable state.
//
// The order matters and every step is best-effort: stop transmitting, put the
// modem back in idle so it is not left in a calibration state that MMDVM-Host
// would inherit, close the port, restart the host. An error in any one of them
// must not skip the ones after it — this runs on the failure path, which is
// exactly when a node most needs to come back on the air.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	stop := s.txStop
	s.mu.Unlock()

	if stop != nil {
		stop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.writeFrame(setTransmit(false))
	idle := Config{State: StateIdle, Simplex: true}
	_ = s.setConfig(ctx, idle)

	err := s.port.Close()
	s.restore()
	return err
}

// version asks the modem to identify itself, reusing internal/modem's codec.
func (s *Session) version(ctx context.Context) (modem.Version, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return modem.Version{}, err
		}
		if _, err := s.port.Write(modem.VersionRequest()); err != nil {
			return modem.Version{}, fmt.Errorf("cal: write to %s: %w", s.opt.Port, err)
		}
		for frames := 0; frames < 6; frames++ {
			f, err := modem.ReadFrame(s.port)
			if err != nil {
				break
			}
			v, err := modem.ParseVersion(f)
			if errors.Is(err, modem.ErrNotVersion) {
				continue
			}
			if err != nil {
				break
			}
			if v.Protocol != 1 && v.Protocol != 2 {
				return modem.Version{}, fmt.Errorf("cal: this modem reports protocol %d, which has no calibration layout Waypoint knows", v.Protocol)
			}
			return v, nil
		}
	}
	return modem.Version{}, ErrNoAnswer
}

func (s *Session) writeFrame(b []byte) error {
	_, err := s.port.Write(b)
	return err
}

// command writes a frame and waits for the modem to accept or refuse it.
//
// Frames that are neither are skipped rather than treated as failures: during a
// sweep the modem is receiving, and an ACK can easily arrive behind a voice
// frame that was already in flight.
func (s *Session) command(ctx context.Context, frame []byte, cmd byte) error {
	if err := s.writeFrame(frame); err != nil {
		return fmt.Errorf("cal: write to %s: %w", s.opt.Port, err)
	}
	const maxFrames = 30
	for i := 0; i < maxFrames; i++ {
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
		switch f.Type {
		case CmdACK:
			if len(f.Payload) > 0 && f.Payload[0] != cmd {
				continue // an ACK for something else; keep looking
			}
			return nil
		case CmdNAK:
			e := &NAKError{Command: cmd}
			if len(f.Payload) > 1 {
				e.Command, e.Reason = f.Payload[0], f.Payload[1]
			}
			return e
		}
	}
	return fmt.Errorf("cal: the modem did not answer command 0x%02X", cmd)
}

// setConfig sends the SET_CONFIG layout this modem's protocol version uses.
func (s *Session) setConfig(ctx context.Context, c Config) error {
	var (
		frame []byte
		err   error
	)
	if s.protocol == 2 {
		frame, err = c.FrameV2()
	} else {
		frame, err = c.FrameV1()
	}
	if err != nil {
		return err
	}
	return s.command(ctx, frame, CmdSetConfig)
}

// tune points the modem at a frequency and makes the change take effect.
//
// Both frames are required, and the reason is not obvious from either firmware's
// command table: on a hotspot, SET_FREQ only STORES the value (CIO::setFreq
// assigns m_frequency_rx and returns), and the synthesiser is not reprogrammed
// until a SET_CONFIG runs the interface configuration again. A sweep that sent
// SET_FREQ alone would step through frequencies the radio never actually moved
// to and draw a flat, meaningless curve — which would look like a board with no
// oscillator error at all.
func (s *Session) tune(ctx context.Context, rx, tx uint32, cfg Config) error {
	if err := s.command(ctx, SetFreq(rx, tx, s.opt.rfLevel()), CmdSetFreq); err != nil {
		return err
	}
	return s.setConfig(ctx, cfg)
}
