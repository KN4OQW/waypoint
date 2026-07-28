package flash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/modem"
)

// The engine: everything that happens between an operator pressing a button and
// a modem running different firmware.
//
// The ordering below is the safety argument, and it is deliberately front-
// loaded — every check that can refuse does so BEFORE the modem is taken off
// the air. Choosing the image, fetching it and verifying its signature all
// happen while the node is still working; only then is MMDVM-Host stopped. A
// design that stopped the host first would turn "your catalog is unreachable"
// into a minute of dead air.
//
// Once the host IS stopped, the guarantee flips to the other kind: whatever
// happens next — a NACK, a cancelled request, a panic, an operator closing the
// browser tab — the host is started again and the lines are released. A node
// that ends a firmware flash off the air because somebody navigated away is a
// worse bug than one that never offered flashing.

// Stage names the step a flash has reached.
type Stage string

const (
	StageChoosing   Stage = "choosing"   // matching the catalog against the modem
	StageFetching   Stage = "fetching"   // downloading and verifying the image
	StagePreparing  Stage = "preparing"  // stopping the host, claiming lines, entering the bootloader
	StageErasing    Stage = "erasing"    //
	StageWriting    Stage = "writing"    //
	StageVerifying  Stage = "verifying"  // reading flash back
	StageRestarting Stage = "restarting" // leaving the bootloader, re-probing, restarting the host
	StageDone       Stage = "done"
)

// Progress is one update from a running flash.
//
// Total is zero on stages that have no measurable size. Clients render a
// spinner for those and a bar for the rest; nothing has to guess which.
type Progress struct {
	Stage  Stage  `json:"stage"`
	Done   int    `json:"done,omitempty"`
	Total  int    `json:"total,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Request is one flash.
type Request struct {
	// Identity is what detection found. It supplies the port, the transport and
	// the board — a flash is never asked to go looking for a modem itself,
	// because detection is the thing that already knows how to arbitrate for the
	// port and how to be honest about ambiguity.
	Identity modem.Identity

	// VariantID overrides the catalog match. It is how an operator resolves the
	// ambiguity a MatchError describes, and the only way a flash proceeds when
	// the oscillator was assumed rather than reported.
	VariantID string

	// StopHost authorises taking the modem away from a running MMDVM-Host.
	// Without it a running host is a refusal, exactly as it is for detection.
	StopHost bool
}

// Result is a completed flash.
type Result struct {
	Variant  Variant         `json:"variant"`
	Bytes    int             `json:"bytes"`
	Duration time.Duration   `json:"duration"`
	Before   string          `json:"before,omitempty"` // firmware version before
	After    *modem.Identity `json:"after,omitempty"`  // what the modem says now
}

var (
	// ErrBusy reports a second flash while one is running.
	ErrBusy = errors.New("flash: a firmware flash is already running")
	// ErrHostRunning reports a modem held by MMDVM-Host without authorisation
	// to stop it.
	ErrHostRunning = errors.New("flash: MMDVM-Host is running and holds the modem port")
	// ErrUSBNotSupported reports a USB board, whose DFU path is a separate
	// engine (RFC-0019 §6) that does not ship yet.
	ErrUSBNotSupported = errors.New("flash: this board attaches over USB, and the USB firmware path is not available in this version")
)

// Engine flashes firmware. Every side effect is a field, so the whole
// orchestration is tested against an emulated part with no hardware present.
type Engine struct {
	Source Source
	Holder modem.Holder // nil means nothing holds the port

	// OpenLines claims BOOT0/nRST. Defaults to the real GPIO backends.
	OpenLines func(LineConfig) (*Lines, error)
	// LineConfig selects the chip and offsets; the board table supplies these.
	LineConfig LineConfig

	// OpenPort opens the modem's serial port. Defaults to modem.OpenPort, which
	// the bootloader needs at even parity.
	OpenPort func(path string, baud int, readTimeout time.Duration) (io.ReadWriteCloser, error)
	// Bauds are tried in order when synchronising. 115200 first, then the speed
	// upstream's tooling effectively uses — a failed autobaud and an absent board
	// are indistinguishable from one attempt.
	Bauds []int

	// Reprobe asks detection what the modem says now. Optional: without it a
	// flash still succeeds, it just cannot show the operator the new version
	// string, which is the most convincing evidence a flash actually took.
	Reprobe func(ctx context.Context) (*modem.Identity, error)

	Timeouts Timeouts
	Now      func() time.Time

	mu      sync.Mutex
	running bool
}

// Running reports whether a flash is in progress.
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) bauds() []int {
	if len(e.Bauds) > 0 {
		return e.Bauds
	}
	return []int{115200, 57600}
}

func (e *Engine) openPort(path string, baud int, timeout time.Duration) (io.ReadWriteCloser, error) {
	if e.OpenPort != nil {
		return e.OpenPort(path, baud, timeout)
	}
	return modem.OpenPort(path, baud, modem.ParityEven, timeout)
}

func (e *Engine) openLines(cfg LineConfig) (*Lines, error) {
	if e.OpenLines != nil {
		return e.OpenLines(cfg)
	}
	return OpenLines(cfg)
}

// Plan chooses the firmware for a request without touching the modem.
//
// It is the read-only half of Flash, so the UI can show what would happen —
// and, more usefully, show the reason nothing can happen — without a button
// press and without stopping anything.
func (e *Engine) Plan(ctx context.Context, req Request) (Variant, Catalog, error) {
	cat, err := e.Source.Catalog(ctx)
	if err != nil {
		return Variant{}, Catalog{}, err
	}
	v, err := choose(cat, req)
	return v, cat, err
}

func choose(cat Catalog, req Request) (Variant, error) {
	if req.VariantID != "" {
		v, ok := cat.ByID(req.VariantID)
		if !ok {
			return Variant{}, &MatchError{
				Reason:  fmt.Sprintf("catalog %s has no firmware called %q", cat.Version, req.VariantID),
				Choices: cat.variantIDs(),
			}
		}
		return v, nil
	}
	return cat.MatchFor(req.Identity)
}

// Flash writes firmware to the modem and reports what it did.
func (e *Engine) Flash(ctx context.Context, req Request, progress func(Progress)) (Result, error) {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return Result{}, ErrBusy
	}
	e.running = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.running = false; e.mu.Unlock() }()

	if progress == nil {
		progress = func(Progress) {}
	}
	started := e.now()

	// --- everything refusable, before the node goes off the air -------------

	progress(Progress{Stage: StageChoosing})
	cat, err := e.Source.Catalog(ctx)
	if err != nil {
		return Result{}, err
	}
	variant, err := choose(cat, req)
	if err != nil {
		return Result{}, err
	}
	if req.Identity.Transport == modem.TransportUSB.String() || variant.Transport == "usb" {
		return Result{}, ErrUSBNotSupported
	}
	if req.Identity.Port == "" {
		return Result{}, errors.New("flash: no modem port; run detection first")
	}

	progress(Progress{Stage: StageFetching, Detail: variant.Describe()})
	image, err := e.Source.Artifact(ctx, variant)
	if err != nil {
		return Result{}, err
	}
	if len(image) == 0 {
		return Result{}, fmt.Errorf("flash: firmware %s is empty", variant.ID)
	}

	// --- from here the modem is ours, and must be given back ---------------

	progress(Progress{Stage: StagePreparing, Detail: "stopping MMDVM-Host"})
	restore, err := e.arbitrate(ctx, req.StopHost)
	if err != nil {
		return Result{}, err
	}
	defer restore()

	lines, err := e.openLines(e.LineConfig)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		// Released before the host restarts: a driven reset line is a modem
		// MMDVM-Host cannot reset.
		_ = lines.Close()
	}()
	progress(Progress{Stage: StagePreparing, Detail: lines.Describe()})

	if err := lines.EnterBootloader(ctx); err != nil {
		return Result{}, err
	}
	// However this ends, the board is left running its firmware rather than
	// parked in a bootloader waiting for a flash nobody is going to send. The
	// flag keeps the success path from resetting twice: the happy path parks the
	// board itself, on the caller's context, and only a failure needs this.
	parked := false
	defer func() {
		if parked {
			return
		}
		exit, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_ = lines.EnterApplication(exit)
	}()

	bl, port, err := e.connect(ctx, req.Identity.Port)
	if err != nil {
		return Result{}, err
	}
	// Closed explicitly on the success path, where the error matters because a
	// port still open is a port MMDVM-Host cannot have back. This is the net for
	// every other exit; closing twice is a no-op.
	defer func() { _ = port.Close() }()
	progress(Progress{Stage: StagePreparing, Detail: fmt.Sprintf(
		"bootloader %s, part 0x%04X", bl.Info().VersionString(), bl.Info().ProductID)})

	// The cheap guard against an F1 image reaching an F4 part: the die the
	// bootloader reports against the die the image was built for. Checked here,
	// after the connection and before the erase — the last moment at which
	// nothing has been destroyed.
	if variant.ProductID != 0 && bl.Info().ProductID != 0 && uint16(variant.ProductID) != bl.Info().ProductID {
		return Result{}, fmt.Errorf("flash: %s was built for part 0x%04X but this modem reports 0x%04X — "+
			"this is the wrong firmware for this hardware", variant.ID, uint16(variant.ProductID), bl.Info().ProductID)
	}

	progress(Progress{Stage: StageErasing})
	if err := bl.MassErase(); err != nil {
		return Result{}, err
	}

	progress(Progress{Stage: StageWriting, Total: len(image)})
	if err := bl.Write(uint32(variant.LoadAddress), image, func(done, total int) {
		progress(Progress{Stage: StageWriting, Done: done, Total: total})
	}); err != nil {
		return Result{}, err
	}

	progress(Progress{Stage: StageVerifying, Total: len(image)})
	if err := bl.Verify(uint32(variant.LoadAddress), image, func(done, total int) {
		progress(Progress{Stage: StageVerifying, Done: done, Total: total})
	}); err != nil {
		return Result{}, err
	}

	// --- give it back ------------------------------------------------------

	progress(Progress{Stage: StageRestarting})
	if err := port.Close(); err != nil {
		return Result{}, fmt.Errorf("flash: close modem port: %w", err)
	}
	if err := lines.EnterApplication(ctx); err != nil {
		return Result{}, err
	}
	parked = true

	res := Result{
		Variant:  variant,
		Bytes:    len(image),
		Duration: e.now().Sub(started),
		Before:   req.Identity.Firmware,
	}
	if e.Reprobe != nil {
		// A failed re-probe is not a failed flash. The bytes are in and verified;
		// what is missing is only the confirmation the operator would like to
		// read, and reporting a successful flash as an error would send them
		// looking for a fault that is not there.
		if id, err := e.Reprobe(ctx); err == nil {
			res.After = id
		} else {
			progress(Progress{Stage: StageRestarting, Detail: "flashed, but the modem did not answer a re-probe: " + err.Error()})
		}
	}
	progress(Progress{Stage: StageDone, Done: len(image), Total: len(image), Detail: variant.Describe()})
	return res, nil
}

// connect synchronises with the bootloader, trying each line speed.
func (e *Engine) connect(ctx context.Context, portPath string) (*Bootloader, io.ReadWriteCloser, error) {
	timeouts := e.Timeouts.orDefaults()
	var lastErr error
	for _, baud := range e.bauds() {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		port, err := e.openPort(portPath, baud, 500*time.Millisecond)
		if err != nil {
			return nil, nil, fmt.Errorf("flash: open %s: %w", portPath, err)
		}
		bl, err := Connect(silenceAdapter{port}, timeouts)
		if err == nil {
			return bl, port, nil
		}
		_ = port.Close()
		lastErr = err
		if !errors.Is(err, ErrNotResponding) {
			// A board that answered something unintelligible will answer the same
			// unintelligible thing at every speed. Only silence is worth retrying.
			break
		}
	}
	return nil, nil, fmt.Errorf("flash: no bootloader on %s: %w", portPath, lastErr)
}

// arbitrate applies the port-ownership rule and returns the restore step.
//
// This mirrors internal/modem's detection arbitration deliberately rather than
// sharing it: the rule is identical but the stakes are not — detection holds the
// port for a second, a flash for a minute — and the restore here must survive
// the caller's context being cancelled, because the request that started a flash
// is frequently gone before it finishes.
func (e *Engine) arbitrate(ctx context.Context, stopHost bool) (func(), error) {
	noop := func() {}
	if e.Holder == nil {
		return noop, nil
	}
	active, err := e.Holder.Active(ctx)
	if err != nil {
		return nil, fmt.Errorf("flash: check MMDVM-Host: %w", err)
	}
	if !active {
		return noop, nil
	}
	if !stopHost {
		return nil, ErrHostRunning
	}
	if err := e.Holder.Stop(ctx); err != nil {
		return nil, fmt.Errorf("flash: stop MMDVM-Host: %w", err)
	}
	return func() {
		restart, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = e.Holder.Start(restart)
	}, nil
}

// silenceAdapter maps the serial layer's silence onto this package's, so the
// protocol code can treat "nothing yet" as a normal condition without importing
// the modem package's error.
type silenceAdapter struct{ rw io.ReadWriter }

func (a silenceAdapter) Read(p []byte) (int, error) {
	n, err := a.rw.Read(p)
	if errors.Is(err, modem.ErrTimeout) {
		return n, ErrSilent
	}
	return n, err
}

func (a silenceAdapter) Write(p []byte) (int, error) { return a.rw.Write(p) }
