package modem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakePort is a modem on the other end of a wire: it answers GET_VERSION with
// desc and says nothing to anything else.
type fakePort struct {
	desc    string
	proto   uint8
	out     bytes.Buffer
	silent  bool // opens fine, never answers — the commonest real outcome
	garbage []byte
}

func (f *fakePort) Write(b []byte) (int, error) {
	if f.silent || !bytes.Equal(b, VersionRequest()) {
		return len(b), nil
	}
	f.out.Write(f.garbage)
	if f.proto == 2 {
		f.out.Write(versionFrameV2(CapDStar|CapDMR|CapYSF, CapPOCSAG, byte(CPUST), []byte{1, 2, 3}, f.desc))
	} else {
		f.out.Write(versionFrameV1(f.desc))
	}
	return len(b), nil
}

func (f *fakePort) Read(b []byte) (int, error) {
	if f.out.Len() == 0 {
		return 0, ErrTimeout
	}
	return f.out.Read(b)
}

func (f *fakePort) Close() error { return nil }

// newDetector wires a Detector to a fixture /dev and a map of port path to the
// thing on the end of it. A path with no entry opens but stays silent.
func newDetector(t *testing.T, tree devTree, ports map[string]*fakePort) *Detector {
	t.Helper()
	return &Detector{
		Scanner:     Scanner{DevDir: tree.dev, SysTTY: tree.sys},
		ReadTimeout: time.Millisecond,
		Attempts:    2,
		Settle:      time.Millisecond,
		Now:         func() time.Time { return time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC) },
		open: func(path string, baud int, _ time.Duration) (io.ReadWriteCloser, error) {
			if p, ok := ports[filepath.Base(path)]; ok {
				return p, nil
			}
			return &fakePort{silent: true}, nil
		},
	}
}

// The acceptance criterion in issue #18, end to end: the bench Dual Hat on the
// GPIO UART, detected and identified.
func TestDetectBenchDualHat(t *testing.T) {
	tree := newDevTree(t)
	tree.tty(t, "ttyAMA0")
	d := newDetector(t, tree, map[string]*fakePort{
		"ttyAMA0": {desc: "MMDVM_HS_Dual_Hat-v1.6.1 20230526 14.7456MHz dual ADF7021 FW by CA6JAU GitID #899fc2a", proto: 1},
	})
	d.Bauds = []int{115200}

	res, err := d.Detect(context.Background(), false)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	id := res.Identity
	if id == nil {
		t.Fatal("Detect found no modem")
	}
	if filepath.Base(id.Port) != "ttyAMA0" || id.Transport != "gpio" || id.Baud != 115200 {
		t.Errorf("found at %s/%s/%d, want ttyAMA0/gpio/115200", id.Port, id.Transport, id.Baud)
	}
	if id.HWType != "MMDVM_HS_Dual_Hat" || id.Firmware != "1.6.1" {
		t.Errorf("identity = %+v", id)
	}
	if id.TCXOHz != 14_745_600 || id.TCXOAssumed {
		t.Errorf("TCXO = %d assumed=%v, want 14745600 reported", id.TCXOHz, id.TCXOAssumed)
	}
	if !id.Duplex {
		t.Error("Duplex = false for a dual-ADF7021 board")
	}
	if !contains(id.Candidates, "mmdvm_hs_dual_hat") {
		t.Errorf("Candidates = %v, missing the bench board", id.Candidates)
	}
	if id.DetectedAt != "2026-07-27T12:00:00Z" {
		t.Errorf("DetectedAt = %q", id.DetectedAt)
	}
}

func TestDetectReportsEveryPortItTried(t *testing.T) {
	// "Nothing found" is the outcome an operator most needs explained.
	tree := newDevTree(t)
	tree.tty(t, "ttyAMA0")
	tree.tty(t, "ttyACM0")
	d := newDetector(t, tree, nil)
	d.Bauds = []int{115200}

	res, err := d.Detect(context.Background(), false)
	if !errors.Is(err, ErrNoModem) {
		t.Fatalf("err = %v, want ErrNoModem", err)
	}
	if len(res.Scanned) != 2 {
		t.Fatalf("Scanned = %+v, want both ports reported", res.Scanned)
	}
	for _, s := range res.Scanned {
		if s.Outcome != OutcomeSilent {
			t.Errorf("%s outcome = %q, want silent", s.Port, s.Outcome)
		}
	}
}

func TestDetectReportsABoardStuckInItsBootloader(t *testing.T) {
	tree := newDevTree(t)
	tree.tty(t, "ttyACM0")
	tree.usb(t, "ttyACM0", "1eaf", "0003", 1)
	d := newDetector(t, tree, nil)

	res, err := d.Detect(context.Background(), false)
	if !errors.Is(err, ErrNoModem) {
		t.Fatalf("err = %v, want ErrNoModem", err)
	}
	if res.Bootloader == "" {
		t.Fatal("a board in DFU was not called out; the operator is one flash from a working node")
	}
	if res.Scanned[0].Outcome != OutcomeBootloader {
		t.Errorf("outcome = %q, want bootloader", res.Scanned[0].Outcome)
	}
}

func TestDetectSkipsPastTrafficLeftInTheBuffer(t *testing.T) {
	// A port MMDVM-Host only just let go of still has its frames in flight.
	tree := newDevTree(t)
	tree.tty(t, "ttyAMA0")
	other := []byte{frameStart, 0x04, 0x01, 0x00} // a GET_STATUS reply
	d := newDetector(t, tree, map[string]*fakePort{
		"ttyAMA0": {desc: "ZUMspot-v1.5.2 20210227 14.7456MHz ADF7021 FW by CA6JAU", proto: 1, garbage: other},
	})
	res, err := d.Detect(context.Background(), false)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if res.Identity == nil || res.Identity.HWType != "ZUMspot" {
		t.Fatalf("identity = %+v; the answer was behind someone else's frame", res.Identity)
	}
}

func TestDetectTriesTheSecondBaudBeforeGivingUp(t *testing.T) {
	// Silence is exactly what the wrong line speed sounds like.
	tree := newDevTree(t)
	tree.tty(t, "ttyAMA0")
	answered := false
	d := newDetector(t, tree, nil)
	d.Bauds = []int{115200, 460800}
	d.open = func(path string, baud int, _ time.Duration) (io.ReadWriteCloser, error) {
		if baud != 460800 {
			return &fakePort{silent: true}, nil
		}
		answered = true
		return &fakePort{desc: "MMDVM_HS_Hat-v1.5.1 20210423 12.288MHz ADF7021 FW by CA6JAU", proto: 1}, nil
	}
	res, err := d.Detect(context.Background(), false)
	if err != nil || !answered {
		t.Fatalf("Detect: %v (second baud tried: %v)", err, answered)
	}
	if res.Identity.Baud != 460800 {
		t.Errorf("Baud = %d, want the speed that actually answered", res.Identity.Baud)
	}
}

// fakeHolder stands in for MMDVM-Host under systemd.
type fakeHolder struct {
	mu            sync.Mutex
	active        bool
	stops, starts int
	stopErr       error
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
	h.stops, h.active = h.stops+1, false
	return nil
}

func (h *fakeHolder) Start(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.starts, h.active = h.starts+1, true
	return nil
}

func TestDetectRefusesToStealThePortFromARunningHost(t *testing.T) {
	// A probe that grabs the port mid-update looks exactly like a bad update to
	// internal/stackupdate's health gate, and triggers a rollback.
	tree := newDevTree(t)
	tree.tty(t, "ttyAMA0")
	d := newDetector(t, tree, nil)
	d.Holder = &fakeHolder{active: true}

	if _, err := d.Detect(context.Background(), false); !errors.Is(err, ErrHostRunning) {
		t.Fatalf("err = %v, want ErrHostRunning", err)
	}
	if d.Holder.(*fakeHolder).stops != 0 {
		t.Error("Detect stopped MMDVM-Host without being authorised to")
	}
}

func TestDetectRestartsTheHostItStopped(t *testing.T) {
	tree := newDevTree(t)
	tree.tty(t, "ttyAMA0")
	h := &fakeHolder{active: true}
	d := newDetector(t, tree, map[string]*fakePort{
		"ttyAMA0": {desc: "MMDVM_HS_Hat-v1.5.1 20210423 12.288MHz ADF7021 FW by CA6JAU", proto: 1},
	})
	d.Holder = h

	if _, err := d.Detect(context.Background(), true); err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if h.stops != 1 || h.starts != 1 || !h.active {
		t.Fatalf("holder stops=%d starts=%d active=%v; the node must come back on the air", h.stops, h.starts, h.active)
	}
}

func TestDetectRestartsTheHostEvenWhenNothingIsFound(t *testing.T) {
	tree := newDevTree(t)
	tree.tty(t, "ttyAMA0")
	h := &fakeHolder{active: true}
	d := newDetector(t, tree, nil)
	d.Holder = h

	if _, err := d.Detect(context.Background(), true); !errors.Is(err, ErrNoModem) {
		t.Fatalf("err = %v, want ErrNoModem", err)
	}
	if h.starts != 1 || !h.active {
		t.Fatal("a failed detection left MMDVM-Host stopped")
	}
}

func TestDetectRestartsTheHostAfterACancelledRequest(t *testing.T) {
	// The browser tab that started a detection may be gone before it ends. The
	// node must not be the thing that pays for that.
	tree := newDevTree(t)
	tree.tty(t, "ttyAMA0")
	h := &fakeHolder{active: true}
	d := newDetector(t, tree, nil)
	d.Holder = h

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Detect(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if h.starts != 1 || !h.active {
		t.Fatalf("cancelled detection left MMDVM-Host stopped (starts=%d active=%v)", h.starts, h.active)
	}
}

func TestDetectDoesNotTouchAHostThatIsAlreadyStopped(t *testing.T) {
	tree := newDevTree(t)
	tree.tty(t, "ttyAMA0")
	h := &fakeHolder{active: false}
	d := newDetector(t, tree, nil)
	d.Holder = h

	_, _ = d.Detect(context.Background(), true)
	if h.stops != 0 || h.starts != 0 {
		t.Errorf("holder was cycled for nothing: stops=%d starts=%d", h.stops, h.starts)
	}
}

func TestDetectSurfacesAStopFailureRatherThanProbingAnyway(t *testing.T) {
	tree := newDevTree(t)
	tree.tty(t, "ttyAMA0")
	h := &fakeHolder{active: true, stopErr: errors.New("unit is masked")}
	d := newDetector(t, tree, nil)
	d.Holder = h

	_, err := d.Detect(context.Background(), true)
	if err == nil {
		t.Fatal("Detect probed a port it had failed to free")
	}
	if h.starts != 0 {
		t.Error("Detect restarted a host it never managed to stop")
	}
}

func TestDeciseconds(t *testing.T) {
	// VTIME is a byte of tenths, and zero means "block forever" — never what a
	// probe wants.
	for d, want := range map[time.Duration]uint8{
		0:                      1,
		time.Millisecond:       1,
		400 * time.Millisecond: 4,
		time.Second:            10,
		time.Hour:              255,
	} {
		if got := deciseconds(d); got != want {
			t.Errorf("deciseconds(%v) = %d, want %d", d, got, want)
		}
	}
}
