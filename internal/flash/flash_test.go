package flash

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/modem"
)

// --- fakes ---------------------------------------------------------------

type fakeSource struct {
	cat            Catalog
	image          []byte
	catErr, artErr error
	fetched        int
}

func (f *fakeSource) Catalog(context.Context) (Catalog, error) {
	if f.catErr != nil {
		return Catalog{}, f.catErr
	}
	return f.cat, nil
}

func (f *fakeSource) Artifact(context.Context, Variant) ([]byte, error) {
	f.fetched++
	if f.artErr != nil {
		return nil, f.artErr
	}
	return f.image, nil
}

type fakeHolder struct {
	active        bool
	stops, starts int
	stopErr       error
}

func (h *fakeHolder) Active(context.Context) (bool, error) { return h.active, nil }
func (h *fakeHolder) Stop(context.Context) error {
	if h.stopErr != nil {
		return h.stopErr
	}
	h.stops++
	h.active = false
	return nil
}
func (h *fakeHolder) Start(context.Context) error {
	h.starts++
	h.active = true
	return nil
}

// testEngine wires an engine to an emulated part, with no hardware and no
// waiting: the reset timings are nanoseconds because what is under test is the
// ORDER of the steps, not the length of the delays between them.
func testEngine(t *testing.T, m *mcu, src *fakeSource, h *fakeHolder) (*Engine, *recordingDriver) {
	t.Helper()
	rec := &recordingDriver{}
	e := &Engine{
		Source: src,
		Holder: h,
		OpenLines: func(LineConfig) (*Lines, error) {
			return &Lines{drv: rec, t: LineTimings{
				ResetHold: time.Nanosecond, BootSettle: time.Nanosecond, AppSettle: time.Nanosecond,
			}}, nil
		},
		OpenPort: func(string, int, time.Duration) (io.ReadWriteCloser, error) { return m, nil },
		Bauds:    []int{115200},
		Timeouts: Timeouts{Sync: 50 * time.Millisecond, ACK: 50 * time.Millisecond, MassErase: time.Second},
	}
	return e, rec
}

func testImage(n int) []byte {
	img := make([]byte, n)
	for i := range img {
		img[i] = byte(i*31 + 7)
	}
	return img
}

func dualHat() modem.Identity {
	return modem.Identity{
		Port: "/dev/ttyAMA0", Transport: "gpio", BoardID: "mmdvm_hs_dual_hat",
		Candidates: []string{"mmdvm_hs_dual_hat"}, TCXOHz: 14_745_600, Duplex: true,
		Firmware: "1.5.1",
	}
}

// --- the whole operation -------------------------------------------------

func TestFlashWritesVerifiesAndGivesTheModemBack(t *testing.T) {
	m := newMCU()
	img := testImage(2048)
	src := &fakeSource{cat: testCatalog(), image: img}
	h := &fakeHolder{active: true}
	e, rec := testEngine(t, m, src, h)

	var stages []Stage
	res, err := e.Flash(context.Background(), Request{Identity: dualHat(), StopHost: true},
		func(p Progress) {
			if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
				stages = append(stages, p.Stage)
			}
		})
	if err != nil {
		t.Fatalf("Flash: %v", err)
	}

	if !bytes.Equal(m.flash[:len(img)], img) {
		t.Error("the modem's flash does not hold the image")
	}
	if res.Variant.ID != "hs_dual_hat-14m7456" {
		t.Errorf("flashed %s, want the dual-hat image", res.Variant.ID)
	}
	if res.Bytes != len(img) {
		t.Errorf("Bytes = %d, want %d", res.Bytes, len(img))
	}
	if res.Before != "1.5.1" {
		t.Errorf("Before = %q, want the firmware version that was there", res.Before)
	}
	if h.stops != 1 || h.starts != 1 {
		t.Errorf("host stops/starts = %d/%d, want 1/1", h.stops, h.starts)
	}

	// Entered the bootloader with BOOT0 high, left it with BOOT0 low — and only
	// once each, since a second exit reset would cost every flash an extra second.
	want := []string{
		"BOOT0=high RESET=low", "BOOT0=high RESET=high",
		"BOOT0=low RESET=low", "BOOT0=low RESET=high",
	}
	if !equalSteps(rec.steps, want) {
		t.Errorf("line steps = %v, want %v", rec.steps, want)
	}

	wantStages := []Stage{StageChoosing, StageFetching, StagePreparing, StageErasing,
		StageWriting, StageVerifying, StageRestarting, StageDone}
	if len(stages) != len(wantStages) {
		t.Fatalf("stages = %v, want %v", stages, wantStages)
	}
	for i := range stages {
		if stages[i] != wantStages[i] {
			t.Fatalf("stages = %v, want %v", stages, wantStages)
		}
	}
}

// Everything that can refuse must refuse BEFORE the node goes off the air. A
// design that stopped the host first would turn an unreachable catalog into a
// minute of dead air for nothing.
func TestNothingIsTakenOffTheAirForARefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  *fakeSource
		req  Request
	}{
		{
			name: "the catalog is unreachable",
			src:  &fakeSource{catErr: errors.New("no route to host")},
			req:  Request{Identity: dualHat(), StopHost: true},
		},
		{
			name: "the image fails verification",
			src:  &fakeSource{cat: testCatalog(), artErr: errors.New("signature verification failed")},
			req:  Request{Identity: dualHat(), StopHost: true},
		},
		{
			name: "the oscillator was only assumed",
			src:  &fakeSource{cat: testCatalog(), image: testImage(64)},
			req: Request{Identity: modem.Identity{
				Port: "/dev/ttyAMA0", Transport: "gpio", BoardID: "mmdvm_hs_hat",
				TCXOHz: 12_288_000, TCXOAssumed: true,
			}, StopHost: true},
		},
		{
			name: "the board attaches over USB",
			src:  &fakeSource{cat: testCatalog(), image: testImage(64)},
			req: Request{Identity: modem.Identity{
				Port: "/dev/ttyACM0", Transport: "usb", BoardID: "zumspot_usb", TCXOHz: 14_745_600,
			}, StopHost: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMCU()
			h := &fakeHolder{active: true}
			e, rec := testEngine(t, m, tc.src, h)

			if _, err := e.Flash(context.Background(), tc.req, nil); err == nil {
				t.Fatal("Flash succeeded where it should have refused")
			}
			if h.stops != 0 {
				t.Error("MMDVM-Host was stopped for a flash that never started")
			}
			if len(rec.steps) != 0 {
				t.Errorf("the modem was reset for a refusal: %v", rec.steps)
			}
			if m.synced {
				t.Error("the bootloader was entered for a refusal")
			}
		})
	}
}

func TestFlashRefusesARunningHostWithoutAuthorisation(t *testing.T) {
	m := newMCU()
	src := &fakeSource{cat: testCatalog(), image: testImage(64)}
	h := &fakeHolder{active: true}
	e, _ := testEngine(t, m, src, h)

	_, err := e.Flash(context.Background(), Request{Identity: dualHat()}, nil)
	if !errors.Is(err, ErrHostRunning) {
		t.Fatalf("err = %v, want ErrHostRunning", err)
	}
	if h.stops != 0 {
		t.Error("stopped a host the caller did not authorise stopping")
	}
}

// The node must come back on the air however the flash ends. This is the
// failure that matters most: a flash that dies mid-write and leaves the modem
// in its bootloader with MMDVM-Host still stopped is a silent node.
func TestTheHostComesBackAfterAFailedFlash(t *testing.T) {
	m := newMCU()
	m.dieAfter = 2 // power cut mid-write
	src := &fakeSource{cat: testCatalog(), image: testImage(2048)}
	h := &fakeHolder{active: true}
	e, rec := testEngine(t, m, src, h)

	if _, err := e.Flash(context.Background(), Request{Identity: dualHat(), StopHost: true}, nil); err == nil {
		t.Fatal("Flash succeeded through a power cut")
	}
	if h.starts != 1 {
		t.Errorf("host starts = %d, want the host restarted after the failure", h.starts)
	}
	// And the board was taken out of its bootloader rather than left parked
	// there waiting for a flash nobody is going to send.
	last := rec.steps[len(rec.steps)-1]
	if last != "BOOT0=low RESET=high" {
		t.Errorf("last line step = %q, want the board released into its firmware", last)
	}
}

// The request that started a flash is frequently gone before it finishes — a
// closed tab, a dropped Wi-Fi link. The modem still has to be given back.
func TestTheHostComesBackWhenTheCallerDisappears(t *testing.T) {
	m := newMCU()
	m.dieAfter = 1
	src := &fakeSource{cat: testCatalog(), image: testImage(2048)}
	h := &fakeHolder{active: true}
	e, _ := testEngine(t, m, src, h)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(5 * time.Millisecond); cancel() }()

	if _, err := e.Flash(ctx, Request{Identity: dualHat(), StopHost: true}, nil); err == nil {
		t.Fatal("Flash succeeded despite a dead part and a cancelled caller")
	}
	if h.starts != 1 {
		t.Errorf("host starts = %d, want 1 — the restore must not inherit the caller's cancellation", h.starts)
	}
}

// The cheap guard against an F1 image reaching an F4 part, checked after the
// connection and before the erase: the last moment at which nothing has been
// destroyed.
func TestAnImageForTheWrongPartIsRefusedBeforeTheErase(t *testing.T) {
	m := newMCU()
	m.id = 0x0419 // an F4, not the F1 the catalog entry names
	m.flash[0] = 0x12
	src := &fakeSource{cat: testCatalog(), image: testImage(64)}
	h := &fakeHolder{active: false}
	e, _ := testEngine(t, m, src, h)

	_, err := e.Flash(context.Background(), Request{Identity: dualHat(), StopHost: true}, nil)
	if err == nil {
		t.Fatal("flashed an image built for a different part")
	}
	if m.flash[0] != 0x12 {
		t.Error("the part was erased before the mismatch was noticed")
	}
}

func TestASecondFlashIsRefusedWhileOneIsRunning(t *testing.T) {
	m := newMCU()
	src := &fakeSource{cat: testCatalog(), image: testImage(4096)}
	h := &fakeHolder{}
	e, _ := testEngine(t, m, src, h)

	started, release := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = e.Flash(context.Background(), Request{Identity: dualHat(), StopHost: true},
			func(p Progress) {
				if p.Stage == StageWriting && p.Done > 0 {
					select {
					case started <- struct{}{}:
						<-release
					default:
					}
				}
			})
	}()
	<-started
	defer close(release)

	if _, err := e.Flash(context.Background(), Request{Identity: dualHat(), StopHost: true}, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if !e.Running() {
		t.Error("Running() = false during a flash")
	}
}

// The bytes are in and verified; a modem that does not answer a re-probe has
// not undone that. Reporting it as a failure would send an operator looking for
// a fault that is not there.
func TestAFailedReprobeDoesNotFailTheFlash(t *testing.T) {
	m := newMCU()
	img := testImage(512)
	src := &fakeSource{cat: testCatalog(), image: img}
	h := &fakeHolder{}
	e, _ := testEngine(t, m, src, h)
	e.Reprobe = func(context.Context) (*modem.Identity, error) {
		return nil, errors.New("no modem answered")
	}

	res, err := e.Flash(context.Background(), Request{Identity: dualHat(), StopHost: true}, nil)
	if err != nil {
		t.Fatalf("Flash: %v", err)
	}
	if res.After != nil {
		t.Error("reported an identity the re-probe never produced")
	}
	if !bytes.Equal(m.flash[:len(img)], img) {
		t.Error("the image is not in flash")
	}
}

func TestASuccessfulReprobeIsReported(t *testing.T) {
	m := newMCU()
	src := &fakeSource{cat: testCatalog(), image: testImage(256)}
	h := &fakeHolder{}
	e, _ := testEngine(t, m, src, h)
	e.Reprobe = func(context.Context) (*modem.Identity, error) {
		return &modem.Identity{Firmware: "1.6.1"}, nil
	}

	res, err := e.Flash(context.Background(), Request{Identity: dualHat(), StopHost: true}, nil)
	if err != nil {
		t.Fatalf("Flash: %v", err)
	}
	if res.After == nil || res.After.Firmware != "1.6.1" {
		t.Errorf("After = %+v, want the new firmware version", res.After)
	}
}

// An operator resolving an ambiguity picks a variant by name, and that choice
// overrides the matcher — including the assumed-oscillator refusal, which is
// the whole point of asking them.
func TestAnExplicitChoiceOverridesTheMatcher(t *testing.T) {
	m := newMCU()
	img := testImage(256)
	src := &fakeSource{cat: testCatalog(), image: img}
	h := &fakeHolder{}
	e, _ := testEngine(t, m, src, h)

	id := modem.Identity{
		Port: "/dev/ttyAMA0", Transport: "gpio", BoardID: "mmdvm_hs_hat",
		TCXOHz: 12_288_000, TCXOAssumed: true,
	}
	res, err := e.Flash(context.Background(),
		Request{Identity: id, VariantID: "hs_hat-14m7456", StopHost: true}, nil)
	if err != nil {
		t.Fatalf("Flash: %v", err)
	}
	if res.Variant.ID != "hs_hat-14m7456" {
		t.Errorf("flashed %s, want the operator's choice", res.Variant.ID)
	}
}

func TestAnUnknownVariantNameIsRefusedWithTheAlternatives(t *testing.T) {
	m := newMCU()
	src := &fakeSource{cat: testCatalog(), image: testImage(64)}
	e, _ := testEngine(t, m, src, &fakeHolder{})

	var me *MatchError
	_, err := e.Flash(context.Background(),
		Request{Identity: dualHat(), VariantID: "no_such_image", StopHost: true}, nil)
	if !errors.As(err, &me) {
		t.Fatalf("err = %v, want a *MatchError", err)
	}
	if len(me.Choices) != len(testCatalog().Variants) {
		t.Errorf("choices = %v, want every variant in the catalog", me.Choices)
	}
}

// Plan is the read-only half: the UI asks what would happen, and gets the
// refusal reason, without anything being stopped or reset.
func TestPlanTouchesNothing(t *testing.T) {
	m := newMCU()
	src := &fakeSource{cat: testCatalog(), image: testImage(64)}
	h := &fakeHolder{active: true}
	e, rec := testEngine(t, m, src, h)

	v, cat, err := e.Plan(context.Background(), Request{Identity: dualHat()})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if v.ID != "hs_dual_hat-14m7456" || cat.Version == "" {
		t.Errorf("Plan = %s from catalog %q", v.ID, cat.Version)
	}
	if h.stops != 0 || len(rec.steps) != 0 || src.fetched != 0 {
		t.Error("Plan stopped, reset or downloaded something")
	}
}

func TestFlashNeedsAPort(t *testing.T) {
	m := newMCU()
	src := &fakeSource{cat: testCatalog(), image: testImage(64)}
	e, _ := testEngine(t, m, src, &fakeHolder{})

	id := dualHat()
	id.Port = ""
	if _, err := e.Flash(context.Background(), Request{Identity: id, StopHost: true}, nil); err == nil {
		t.Fatal("flashed a modem with no port")
	}
}
