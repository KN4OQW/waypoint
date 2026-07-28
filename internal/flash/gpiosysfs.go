package flash

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// The sysfs GPIO fallback, and the base-512 problem in full.
//
// sysfs addresses a line by a GLOBAL number: the owning chip's base plus the
// line's offset within it. On a Raspberry Pi that base was 0 for years, so
// "GPIO 20" and "the twentieth line of the pin controller" were the same number
// and every flashing script in the hobby hardcoded it. Linux 6.6 changed
// dynamic GPIO base allocation to start at 512, the header controller's base
// became 512, and those scripts began exporting line 20 of whatever chip
// happened to own the number — or nothing at all.
//
// The fix is not to hardcode 512 either, because the base is dynamic by
// definition and a Pi 5, a different kernel or another expander loading first
// will produce a different one. It is READ, from the chip whose label says it is
// the header. The character device is preferred precisely because it has no such
// number to get wrong; this path exists for kernels that do not offer it.

type sysfsLines struct {
	root         string
	chip         string
	boot0, reset int  // global line numbers
	ownBoot0     bool // exported by us, so ours to unexport
	ownReset     bool
}

type sysfsChip struct {
	dir   string
	label string
	base  int
	lines int
}

// readSysfsChips enumerates the controllers sysfs knows about.
func readSysfsChips(root string) ([]sysfsChip, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var chips []sysfsChip
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "gpiochip") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		label, err := readTrimmed(filepath.Join(dir, "label"))
		if err != nil {
			continue
		}
		base, err := readInt(filepath.Join(dir, "base"))
		if err != nil {
			continue
		}
		lines, err := readInt(filepath.Join(dir, "ngpio"))
		if err != nil {
			continue
		}
		chips = append(chips, sysfsChip{dir: dir, label: label, base: base, lines: lines})
	}
	return chips, nil
}

func openSysfsLines(cfg LineConfig) (driver, error) {
	chips, err := readSysfsChips(cfg.SysfsRoot)
	if err != nil {
		return nil, err
	}
	need := cfg.BOOT0
	if cfg.Reset > need {
		need = cfg.Reset
	}
	chip, err := pickChip(chips,
		func(c sysfsChip) string { return c.label },
		func(c sysfsChip) int { return c.lines },
		cfg.ChipLabels, need)
	if err != nil {
		return nil, err
	}

	l := &sysfsLines{
		root:  cfg.SysfsRoot,
		chip:  fmt.Sprintf("%s (%s), base %d", filepath.Base(chip.dir), chip.label, chip.base),
		boot0: chip.base + cfg.BOOT0,
		reset: chip.base + cfg.Reset,
	}

	// Direction is written as "low"/"high" rather than "out": those set the
	// direction AND the initial level in one write, so the pin never spends a
	// moment driving the wrong way. Reset starts released for the same reason the
	// character-device path sets initial values — claiming the lines must not
	// reset a running modem.
	var err2 error
	if l.ownBoot0, err2 = l.claim(l.boot0, "low"); err2 != nil {
		return nil, err2
	}
	if l.ownReset, err2 = l.claim(l.reset, "high"); err2 != nil {
		_ = l.Close()
		return nil, err2
	}
	return l, nil
}

// exportLine writes a line number to the export or unexport file. It is a
// variable so the already-exported case — which only the kernel can produce —
// can be exercised in tests.
var exportLine = func(root, file string, line int) error {
	return os.WriteFile(filepath.Join(root, file), []byte(strconv.Itoa(line)), 0o200)
}

// claim exports a line and sets its direction, reporting whether this call is
// what exported it.
//
// Ownership is decided by what the export write returns, not by whether the
// line's directory already exists: the directory does not appear until after a
// successful export, so checking for it first would mean never exporting
// anything. The kernel answers EBUSY when a line is already exported, and a line
// somebody else exported is one we must not unexport out from under them — that
// would be Waypoint reaching into another program's hardware on the way out.
func (l *sysfsLines) claim(line int, direction string) (owned bool, err error) {
	dir := l.lineDir(line)
	switch err := exportLine(l.root, "export", line); {
	case err == nil:
		owned = true
	case errors.Is(err, unix.EBUSY), errors.Is(err, os.ErrExist):
		owned = false
	default:
		return false, fmt.Errorf("export GPIO %d: %w", line, err)
	}
	// udev retitles the new files after the kernel creates them, and the window
	// is small but real: a write straight after the export can land before the
	// daemon's own permissions apply. Retrying briefly is cheaper than failing a
	// flash over a race with udev.
	if err := writeWithRetry(filepath.Join(dir, "direction"), direction); err != nil {
		return owned, fmt.Errorf("set GPIO %d direction: %w", line, err)
	}
	return owned, nil
}

func (l *sysfsLines) lineDir(line int) string {
	return filepath.Join(l.root, "gpio"+strconv.Itoa(line))
}

func (l *sysfsLines) set(boot0, reset bool) error {
	if err := l.write(l.boot0, boot0); err != nil {
		return err
	}
	return l.write(l.reset, reset)
}

func (l *sysfsLines) write(line int, high bool) error {
	v := "0"
	if high {
		v = "1"
	}
	if err := os.WriteFile(filepath.Join(l.lineDir(line), "value"), []byte(v), 0o200); err != nil {
		return fmt.Errorf("set GPIO %d: %w", line, err)
	}
	return nil
}

func (l *sysfsLines) describe() string {
	return fmt.Sprintf("%s, sysfs (BOOT0 %d, reset %d)", l.chip, l.boot0, l.reset)
}

// Close unexports the lines this instance exported, returning them to inputs so
// the board's own pull resistors hold them — see Lines.Close.
func (l *sysfsLines) Close() error {
	var firstErr error
	unexport := func(line int) {
		if err := exportLine(l.root, "unexport", line); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("unexport GPIO %d: %w", line, err)
		}
	}
	if l.ownBoot0 {
		unexport(l.boot0)
		l.ownBoot0 = false
	}
	if l.ownReset {
		unexport(l.reset)
		l.ownReset = false
	}
	return firstErr
}

func writeWithRetry(path, content string) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.WriteFile(path, []byte(content), 0o200); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return err
}

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func readInt(path string) (int, error) {
	s, err := readTrimmed(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}
