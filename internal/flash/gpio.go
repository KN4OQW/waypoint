package flash

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/KN4OQW/waypoint/internal/modem"
)

// The two lines that decide which program an STM32 starts.
//
// BOOT0 is sampled at the instant reset is released: high enters the ROM
// bootloader, low starts whatever is in flash. That single fact is the whole of
// bootloader entry, and it is why the sequences below always set BOOT0 *before*
// releasing nRST — a line changed afterwards has already missed the only moment
// it mattered.
//
// On the MMDVM_HS hat family the Pi drives BOOT0 from BCM20 and nRST from BCM21.
// Those are hardware facts about a board, so they live in the board table with
// these as the family default, not as constants here.
//
// # Why the character device, and what base-512 broke
//
// Linux has two GPIO interfaces. The old sysfs one addresses a line by a global
// number — chip base plus offset — and for years on a Pi that base was 0, so
// every script in the hobby hardcoded "20" and "21" and worked. Kernel 6.6 moved
// dynamic base allocation up to 512 and all of them broke at once, silently
// exporting nothing or the wrong pin.
//
// The character device has no global numbering to get wrong: a chip is opened by
// path, lines are addressed by offset within it, and the chip is chosen by the
// LABEL the driver reports. So this code asks for "the pin controller" rather
// than "GPIO number 532", which is also what keeps it off the Pi's other GPIO
// chip — the raspberrypi-exp-gpio expander, whose lines 20 and 21 exist, are
// wired to entirely different things, and would be found first by a naive scan.
//
// The sysfs path remains as a fallback for a kernel too old or a build too
// stripped for the character device, and there the base is READ from the chip
// rather than assumed. That is the base-512 requirement in issue #19, honoured
// by not having a number to be wrong about in the first place.

// Default header wiring for the MMDVM_HS family.
const (
	DefaultBOOT0Line = 20
	DefaultResetLine = 21
)

// headerChips are the pin-controller labels of the 40-pin header across the Pi
// range. A chip whose label is not one of these is not the header — most
// importantly the raspberrypi-exp-gpio expander on the 3B+/4, which has lines at
// the same offsets wired to power rails and the Ethernet PHY.
var headerChips = []string{
	"pinctrl-bcm2835", // Pi 1 / 2 / 3 / Zero
	"pinctrl-bcm2711", // Pi 4
	"pinctrl-bcm2712", // Pi 5 (SoC bank)
	"pinctrl-rp1",     // Pi 5 (the header lines live here)
}

// LineTimings are the delays in a reset sequence.
//
// They are longer than the STM32 datasheet requires, deliberately. The parts
// need microseconds; the hats put an RC network on nRST, some clone boards put a
// supervisor chip there, and the cost of being generous is a tenth of a second
// on an operation that takes half a minute.
type LineTimings struct {
	ResetHold  time.Duration // nRST held low
	BootSettle time.Duration // after release, before the bootloader is asked anything
	AppSettle  time.Duration // after release into the application, before it is probed
}

// DefaultLineTimings are sized for the launch-tier hats.
func DefaultLineTimings() LineTimings {
	return LineTimings{
		ResetHold:  100 * time.Millisecond,
		BootSettle: 250 * time.Millisecond,
		// Firmware boots, brings up its clocks and starts its USB stack before it
		// will answer anything. Asking too early looks exactly like a failed flash.
		AppSettle: 750 * time.Millisecond,
	}
}

func (t LineTimings) orDefaults() LineTimings {
	d := DefaultLineTimings()
	if t.ResetHold <= 0 {
		t.ResetHold = d.ResetHold
	}
	if t.BootSettle <= 0 {
		t.BootSettle = d.BootSettle
	}
	if t.AppSettle <= 0 {
		t.AppSettle = d.AppSettle
	}
	return t
}

// LineConfig selects and configures the two control lines.
type LineConfig struct {
	BOOT0 int // line offset within the chip; 0 means DefaultBOOT0Line
	Reset int // line offset; 0 means DefaultResetLine

	// ChipLabels are the acceptable pin-controller labels, best first. Empty
	// means the Pi header chips.
	ChipLabels []string

	// DevDir and SysfsRoot are the filesystem roots, overridable so the
	// selection logic can be tested against a fixture tree.
	DevDir    string // default /dev
	SysfsRoot string // default /sys/class/gpio

	Timings LineTimings
}

// WithDefaults fills in the family defaults, so a caller can log or display the
// lines that will actually be driven rather than the zeroes it passed in.
func (c LineConfig) WithDefaults() LineConfig {
	if c.BOOT0 == 0 {
		c.BOOT0 = DefaultBOOT0Line
	}
	if c.Reset == 0 {
		c.Reset = DefaultResetLine
	}
	if len(c.ChipLabels) == 0 {
		c.ChipLabels = headerChips
	}
	if c.DevDir == "" {
		c.DevDir = "/dev"
	}
	if c.SysfsRoot == "" {
		c.SysfsRoot = "/sys/class/gpio"
	}
	c.Timings = c.Timings.orDefaults()
	return c
}

// LinesForBoard reads a board's wiring out of the board table.
//
// Every launch-tier hat uses BCM20 and BCM21, so the table records nothing and
// this returns the family default. A board that wires them elsewhere sets the
// fields there rather than being special-cased here.
func LinesForBoard(b modem.Board) LineConfig {
	return LineConfig{BOOT0: b.BOOT0Line, Reset: b.ResetLine}
}

// driver is the two lines, however they are reached. Both backends present the
// same one-call interface — set both lines at once — because the sequences below
// care about ordering between *steps*, and a backend that could reorder the two
// lines within a step would make the BOOT0-before-release guarantee a lie.
type driver interface {
	set(boot0, reset bool) error // true = line driven high
	describe() string
	Close() error
}

// Lines drives BOOT0 and nRST.
type Lines struct {
	drv   driver
	t     LineTimings
	boot0 bool // last commanded state, so a sequence can hold one line steady
}

// OpenLines claims the control lines, preferring the character device and
// falling back to sysfs.
//
// Both failures are reported together when neither works. They fail for
// different reasons — no /dev/gpiochip* on a stripped kernel, no
// CONFIG_GPIO_SYSFS on a modern one, EACCES on either if the daemon is not root
// — and an operator staring at "cannot open GPIO" needs to know which of those
// they are looking at.
func OpenLines(cfg LineConfig) (*Lines, error) {
	cfg = cfg.WithDefaults()

	drv, cdevErr := openChardevLines(cfg)
	if cdevErr != nil {
		var sysfsErr error
		drv, sysfsErr = openSysfsLines(cfg)
		if sysfsErr != nil {
			return nil, fmt.Errorf("flash: cannot claim the BOOT0/reset lines: "+
				"character device: %v; sysfs: %v", cdevErr, sysfsErr)
		}
	}
	return &Lines{drv: drv, t: cfg.Timings}, nil
}

// Describe names the backend and chip in use, for the flash job's log.
func (l *Lines) Describe() string { return l.drv.describe() }

// EnterBootloader resets the board with BOOT0 high, leaving it in the ROM
// bootloader waiting to be synchronised.
func (l *Lines) EnterBootloader(ctx context.Context) error { return l.resetWithBoot0(ctx, true) }

// EnterApplication resets the board with BOOT0 low, starting whatever is in
// flash — the same thing that happens at the operator's next power-up.
func (l *Lines) EnterApplication(ctx context.Context) error { return l.resetWithBoot0(ctx, false) }

// resetWithBoot0 is the sequence both entries share. The ordering is the
// contract: BOOT0 is settled while the part is held in reset, and only then is
// reset released, because BOOT0 is sampled on that rising edge and nowhere else.
func (l *Lines) resetWithBoot0(ctx context.Context, boot0 bool) error {
	if err := l.drv.set(boot0, false); err != nil { // BOOT0 chosen, reset asserted
		return fmt.Errorf("flash: assert reset: %w", err)
	}
	l.boot0 = boot0
	if err := sleepCtx(ctx, l.t.ResetHold); err != nil {
		return err
	}
	if err := l.drv.set(boot0, true); err != nil { // release reset; BOOT0 sampled here
		return fmt.Errorf("flash: release reset: %w", err)
	}
	settle := l.t.BootSettle
	if !boot0 {
		settle = l.t.AppSettle
	}
	return sleepCtx(ctx, settle)
}

// Close releases the lines.
//
// It deliberately does not park them at a chosen level first: releasing returns
// both pins to inputs, which is the state a Pi that never touched them is in,
// and lets the board's own pull resistors decide. Leaving them driven would mean
// a modem whose reset line is held by a daemon that has finished with it — and
// MMDVM-Host, restarting straight afterwards, would find a board it cannot
// reset.
func (l *Lines) Close() error {
	if l.drv == nil {
		return nil
	}
	err := l.drv.Close()
	l.drv = nil
	return err
}

// pickChip chooses the header chip from what was enumerated.
//
// Labels are tried in the caller's order rather than the filesystem's, so a Pi 5
// (whose header lines are on rp1, with a second controller present) picks the
// right one regardless of which enumerated first. A chip with too few lines is
// skipped rather than opened: it is not the one we want, and requesting an
// out-of-range offset is an error worth never producing.
func pickChip[T any](chips []T, labelOf func(T) string, linesOf func(T) int, want []string, need int) (T, error) {
	var zero T
	for _, label := range want {
		for _, c := range chips {
			if labelOf(c) != label {
				continue
			}
			if linesOf(c) <= need {
				continue
			}
			return c, nil
		}
	}
	if len(chips) == 0 {
		return zero, errors.New("no GPIO chips found")
	}
	var found []string
	for _, c := range chips {
		found = append(found, labelOf(c))
	}
	return zero, fmt.Errorf("no GPIO chip matched %v (found %v)", want, found)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
