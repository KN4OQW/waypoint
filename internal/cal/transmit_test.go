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

// The transmit tests are the ones worth being pedantic about. Everything else in
// this package produces a number; this produces a carrier.

func TestTransmitRefusesAReceiveState(t *testing.T) {
	m := newFakeModem(438_800_000, 0)
	s := openFake(t, m, nil)

	if _, err := s.Transmit(context.Background(), StateDMR, time.Second); err == nil {
		t.Fatal("Transmit accepted an ordinary receive state")
	}
	if m.isKeyed() {
		t.Fatal("the modem was keyed by a refused request")
	}
}

// TestTransmitRefusesOutsideTheAmateurService covers the failure the band check
// exists for: a frequency that is wrong rather than merely unusual.
func TestTransmitRefusesOutsideTheAmateurService(t *testing.T) {
	for _, tc := range []struct {
		name string
		hz   uint32
	}{
		{"a broadcast frequency", 98_500_000},
		{"a transposed digit landing outside any band", 483_800_000},
		{"the 70 cm satellite segment", 436_500_000},
		{"the 2 m satellite segment", 145_900_000},
		{"no frequency configured at all", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeModem(438_800_000, 0)
			s := openFake(t, m, func(o *Options) { o.TXFreqHz = tc.hz })

			if _, err := s.Transmit(context.Background(), StateDMRCal, 50*time.Millisecond); err == nil {
				t.Fatal("Transmit accepted the frequency")
			}
			if m.isKeyed() {
				t.Fatal("the modem was keyed anyway")
			}
		})
	}
}

func TestTransmitKeysAndThenUnkeys(t *testing.T) {
	m := newFakeModem(438_800_000, 0)
	s := openFake(t, m, nil)

	keyed := make(chan bool, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		keyed <- m.isKeyed()
	}()

	b, err := s.Transmit(context.Background(), StateDMRCal, 120*time.Millisecond)
	if err != nil {
		t.Fatalf("Transmit: %v", err)
	}
	if !<-keyed {
		t.Error("the modem was never keyed during the burst")
	}
	if m.isKeyed() {
		t.Error("the modem was left keyed after the burst")
	}
	if b.State != StateDMRCal {
		t.Errorf("burst reported state %s", b.State)
	}
}

// TestTransmitUnkeysWhenTheCallerGoesAway is the property that matters most: a
// closed browser tab, a cancelled request or a dropped link must end a
// transmission. The unkey deliberately does not run on the caller's context,
// and this is what proves it.
func TestTransmitUnkeysWhenTheCallerGoesAway(t *testing.T) {
	m := newFakeModem(438_800_000, 0)
	s := openFake(t, m, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if _, err := s.Transmit(ctx, StateDMRCal, 10*time.Second); err != nil {
		t.Fatalf("Transmit: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the burst ran for %s after its caller was cancelled", elapsed)
	}
	if m.isKeyed() {
		t.Fatal("the modem was left keyed after the caller went away")
	}
}

// TestBurstIsClampedToTheCeiling checks the dead-man ceiling itself. The clamp
// is tested directly rather than by waiting out a burst, because the ceiling is
// half a minute and a test that waits it out is a test nobody runs.
func TestBurstIsClampedToTheCeiling(t *testing.T) {
	for _, tc := range []struct {
		in, want time.Duration
	}{
		{0, MaxBurst},
		{-time.Second, MaxBurst},
		{MaxBurst + time.Hour, MaxBurst},
		{5 * time.Second, 5 * time.Second},
	} {
		if got := clampBurst(tc.in); got != tc.want {
			t.Errorf("clampBurst(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestTransmitRefusesASecondBurst(t *testing.T) {
	m := newFakeModem(438_800_000, 0)
	s := openFake(t, m, nil)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = s.Transmit(context.Background(), StateDMRCal, 200*time.Millisecond)
	}()
	time.Sleep(40 * time.Millisecond)

	if _, err := s.Transmit(context.Background(), StateDMRCal, 50*time.Millisecond); !errors.Is(err, ErrTransmitting) {
		t.Fatalf("second burst error = %v, want ErrTransmitting", err)
	}
	wg.Wait()
	if m.isKeyed() {
		t.Fatal("the modem was left keyed")
	}
}

// TestCloseUnkeysAndIdlesTheModem covers the exit path. A session that ended —
// cleanly or not — must not leave the board transmitting, and must not leave it
// in a calibration state for MMDVM-Host to inherit when it restarts.
func TestCloseUnkeysAndIdlesTheModem(t *testing.T) {
	m := newFakeModem(438_800_000, 0)
	s := openFake(t, m, nil)

	go func() { _, _ = s.Transmit(context.Background(), StateDMRCal, 5*time.Second) }()
	time.Sleep(40 * time.Millisecond)

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if m.isKeyed() {
		t.Fatal("Close left the modem keyed")
	}
	m.mu.Lock()
	state := m.state
	closed := m.closed
	m.mu.Unlock()
	if state != StateIdle {
		t.Errorf("Close left the modem in %s, not idle", state)
	}
	if !closed {
		t.Error("Close did not close the port")
	}
}

// --- arbitration ---------------------------------------------------------

type fakeHolder struct {
	mu       sync.Mutex
	active   bool
	stops    int
	starts   int
	stopErr  error
	startErr error
}

func (h *fakeHolder) Active(context.Context) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active, nil
}

func (h *fakeHolder) Stop(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopErr != nil {
		return h.stopErr
	}
	h.stops++
	h.active = false
	return nil
}

func (h *fakeHolder) Start(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.startErr != nil {
		return h.startErr
	}
	h.starts++
	h.active = true
	return nil
}

func (h *fakeHolder) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stops, h.starts
}

// TestOpenRefusesARunningHost is RFC-0020's port-ownership rule: taking the
// modem from a node that is on the air is never something to do quietly.
func TestOpenRefusesARunningHost(t *testing.T) {
	m := newFakeModem(438_800_000, 0)
	h := &fakeHolder{active: true}

	_, err := Open(context.Background(), Options{
		Port: "/dev/fake", RXFreqHz: m.baseHz, TXFreqHz: m.baseHz, Holder: h,
		open: func(string, int, time.Duration) (io.ReadWriteCloser, error) { return m, nil },
	})
	if !errors.Is(err, modem.ErrHostRunning) {
		t.Fatalf("error = %v, want ErrHostRunning", err)
	}
	if stops, _ := h.counts(); stops != 0 {
		t.Fatal("the host was stopped without authorisation")
	}
}

func TestSessionRestartsTheHostOnEveryExit(t *testing.T) {
	t.Run("after a clean close", func(t *testing.T) {
		m := newFakeModem(438_800_000, 0)
		h := &fakeHolder{active: true}
		s, err := Open(context.Background(), Options{
			Port: "/dev/fake", RXFreqHz: m.baseHz, TXFreqHz: m.baseHz, Holder: h, StopHost: true,
			open: func(string, int, time.Duration) (io.ReadWriteCloser, error) { return m, nil },
		})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if stops, _ := h.counts(); stops != 1 {
			t.Fatalf("host stopped %d times, want 1", stops)
		}
		s.Close()
		if _, starts := h.counts(); starts != 1 {
			t.Fatalf("host started %d times after close, want 1", starts)
		}
	})

	t.Run("when the modem never answers", func(t *testing.T) {
		m := newFakeModem(438_800_000, 0)
		m.protocol = 9 // a version this package refuses to speak
		h := &fakeHolder{active: true}
		_, err := Open(context.Background(), Options{
			Port: "/dev/fake", RXFreqHz: m.baseHz, TXFreqHz: m.baseHz, Holder: h, StopHost: true,
			open: func(string, int, time.Duration) (io.ReadWriteCloser, error) { return m, nil },
		})
		if err == nil {
			t.Fatal("Open succeeded against a modem speaking an unknown protocol")
		}
		// The node must come back on the air even though the session never
		// existed. This is the path that would otherwise leave a node down.
		if _, starts := h.counts(); starts != 1 {
			t.Fatalf("host started %d times after a failed open, want 1", starts)
		}
	})
}
