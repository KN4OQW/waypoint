package flash

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The kernel encodes an ioctl argument's size in the ioctl number itself, so a
// struct laid out wrongly is an immediate EINVAL rather than a subtle bug. These
// are the sizes the numbers in x/sys encode, and this is the only check that
// catches a transcription slip on a machine with no GPIO controller to try.
func TestGPIOStructsMatchTheKernelABI(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"gpiochip_info", unsafe.Sizeof(gpiochipInfo{}), 68},                 // 0x8044b401
		{"gpio_v2_line_attribute", unsafe.Sizeof(gpioV2LineAttribute{}), 16}, //
		{"gpio_v2_line_config_attribute", unsafe.Sizeof(gpioV2LineConfigAttribute{}), 24},
		{"gpio_v2_line_config", unsafe.Sizeof(gpioV2LineConfig{}), 272},
		{"gpio_v2_line_request", unsafe.Sizeof(gpioV2LineRequest{}), 592}, // 0xc250b407
		{"gpio_v2_line_values", unsafe.Sizeof(gpioV2LineValues{}), 16},    // 0xc010b40f
	} {
		if tc.got != tc.want {
			t.Errorf("sizeof(%s) = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
	// The union member must sit at offset 8: the kernel's __aligned_u64 forces it
	// there even on 32-bit, where Go would otherwise be free to pack it at 4.
	if off := unsafe.Offsetof(gpioV2LineAttribute{}.Value); off != 8 {
		t.Errorf("gpio_v2_line_attribute.value at offset %d, want 8", off)
	}
	if off := unsafe.Offsetof(gpioV2LineRequest{}.Config); off != 288 {
		t.Errorf("gpio_v2_line_request.config at offset %d, want 288", off)
	}
}

// --- chip selection ------------------------------------------------------

type fakeChip struct {
	label string
	lines int
}

func TestPickChipPrefersTheCallersLabelOrder(t *testing.T) {
	// A Pi 5 presents both controllers; the header lines are on rp1, and which
	// enumerates first is not something to depend on.
	chips := []fakeChip{{"pinctrl-bcm2712", 54}, {"pinctrl-rp1", 54}}
	got, err := pickChip(chips,
		func(c fakeChip) string { return c.label },
		func(c fakeChip) int { return c.lines },
		[]string{"pinctrl-rp1", "pinctrl-bcm2712"}, 21)
	if err != nil {
		t.Fatalf("pickChip: %v", err)
	}
	if got.label != "pinctrl-rp1" {
		t.Errorf("chose %q, want pinctrl-rp1", got.label)
	}
}

// The expander is the trap this exists to avoid: it is a real GPIO chip, it has
// lines at offsets 20 and 21, and they are wired to things that are not a modem.
func TestPickChipIgnoresTheGPIOExpander(t *testing.T) {
	chips := []fakeChip{{"raspberrypi-exp-gpio", 8}, {"pinctrl-bcm2835", 54}}
	got, err := pickChip(chips,
		func(c fakeChip) string { return c.label },
		func(c fakeChip) int { return c.lines },
		headerChips, 21)
	if err != nil {
		t.Fatalf("pickChip: %v", err)
	}
	if got.label != "pinctrl-bcm2835" {
		t.Errorf("chose %q, want pinctrl-bcm2835", got.label)
	}
}

func TestPickChipRejectsAChipWithTooFewLines(t *testing.T) {
	chips := []fakeChip{{"pinctrl-bcm2835", 8}} // right label, not enough lines
	if _, err := pickChip(chips,
		func(c fakeChip) string { return c.label },
		func(c fakeChip) int { return c.lines },
		headerChips, 21); err == nil {
		t.Fatal("accepted a chip with too few lines")
	}
}

func TestPickChipNamesWhatItFound(t *testing.T) {
	chips := []fakeChip{{"gpio-brcmstb", 32}}
	_, err := pickChip(chips,
		func(c fakeChip) string { return c.label },
		func(c fakeChip) int { return c.lines },
		headerChips, 21)
	if err == nil || !strings.Contains(err.Error(), "gpio-brcmstb") {
		t.Fatalf("err = %v, want it to name the chips that were found", err)
	}
}

// --- the sysfs fallback --------------------------------------------------

// sysfsFixture builds a /sys/class/gpio tree. Line directories are pre-created
// the way the kernel creates them on export.
func sysfsFixture(t *testing.T, chips []sysfsChip, lines ...int) string {
	t.Helper()
	root := t.TempDir()
	for _, c := range chips {
		dir := filepath.Join(root, filepath.Base(c.dir))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		write := func(name, v string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("label", c.label)
		write("base", itoa(c.base))
		write("ngpio", itoa(c.lines))
	}
	for _, f := range []string{"export", "unexport"} {
		if err := os.WriteFile(filepath.Join(root, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range lines {
		dir := filepath.Join(root, "gpio"+itoa(n))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"direction", "value"} {
			if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// The heart of it: the global line number is the chip's base plus the offset,
// and the base is read rather than assumed. Both the pre-6.6 and post-6.6
// numberings must produce the right pin from the same configuration.
func TestSysfsComputesLineNumbersFromTheChipsBase(t *testing.T) {
	for _, tc := range []struct {
		name         string
		base         int
		boot0, reset int
	}{
		{"kernel before 6.6 (base 0)", 0, 20, 21},
		{"kernel 6.6 and later (base 512)", 512, 532, 533},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := sysfsFixture(t,
				[]sysfsChip{{dir: "gpiochip" + itoa(tc.base), label: "pinctrl-bcm2711", base: tc.base, lines: 54}},
				tc.boot0, tc.reset)

			drv, err := openSysfsLines(LineConfig{SysfsRoot: root}.withDefaults())
			if err != nil {
				t.Fatalf("openSysfsLines: %v", err)
			}
			defer drv.Close()

			if got := readFile(t, filepath.Join(root, "export")); got != itoa(tc.reset) {
				// Each export is its own write, so the file holds the last one.
				t.Errorf("last export = %q, want %q", got, itoa(tc.reset))
			}
			// Reset is claimed released and BOOT0 low, so claiming the lines cannot
			// reset a modem that is on the air.
			if got := readFile(t, filepath.Join(root, "gpio"+itoa(tc.boot0), "direction")); got != "low" {
				t.Errorf("BOOT0 direction = %q, want low", got)
			}
			if got := readFile(t, filepath.Join(root, "gpio"+itoa(tc.reset), "direction")); got != "high" {
				t.Errorf("reset direction = %q, want high", got)
			}

			if err := drv.set(true, false); err != nil {
				t.Fatalf("set: %v", err)
			}
			if got := readFile(t, filepath.Join(root, "gpio"+itoa(tc.boot0), "value")); got != "1" {
				t.Errorf("BOOT0 value = %q, want 1", got)
			}
			if got := readFile(t, filepath.Join(root, "gpio"+itoa(tc.reset), "value")); got != "0" {
				t.Errorf("reset value = %q, want 0", got)
			}
			if !strings.Contains(drv.describe(), "base "+itoa(tc.base)) {
				t.Errorf("describe = %q, want it to name the base", drv.describe())
			}
		})
	}
}

func TestSysfsPicksTheHeaderChipNotTheExpander(t *testing.T) {
	root := sysfsFixture(t, []sysfsChip{
		{dir: "gpiochip504", label: "raspberrypi-exp-gpio", base: 504, lines: 8},
		{dir: "gpiochip512", label: "pinctrl-bcm2835", base: 512, lines: 54},
	}, 532, 533)

	drv, err := openSysfsLines(LineConfig{SysfsRoot: root}.withDefaults())
	if err != nil {
		t.Fatalf("openSysfsLines: %v", err)
	}
	defer drv.Close()

	if !strings.Contains(drv.describe(), "pinctrl-bcm2835") {
		t.Errorf("describe = %q, want the header controller", drv.describe())
	}
}

func TestSysfsUnexportsWhatItExported(t *testing.T) {
	root := sysfsFixture(t,
		[]sysfsChip{{dir: "gpiochip512", label: "pinctrl-bcm2711", base: 512, lines: 54}},
		532, 533)

	drv, err := openSysfsLines(LineConfig{SysfsRoot: root}.withDefaults())
	if err != nil {
		t.Fatalf("openSysfsLines: %v", err)
	}
	if err := drv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "unexport")); got != "533" {
		t.Errorf("last unexport = %q, want 533", got)
	}
}

// A line somebody else exported is not ours to take away from them.
func TestSysfsDoesNotUnexportALineItDidNotExport(t *testing.T) {
	root := sysfsFixture(t,
		[]sysfsChip{{dir: "gpiochip512", label: "pinctrl-bcm2711", base: 512, lines: 54}},
		532, 533)

	// The kernel answers EBUSY when a line is already exported, which no plain
	// file can reproduce — so the export write itself is stubbed for BOOT0.
	real := exportLine
	t.Cleanup(func() { exportLine = real })
	exportLine = func(r, file string, line int) error {
		if file == "export" && line == 532 {
			return unix.EBUSY
		}
		return real(r, file, line)
	}

	drv, err := openSysfsLines(LineConfig{SysfsRoot: root}.withDefaults())
	if err != nil {
		t.Fatalf("openSysfsLines: %v", err)
	}
	if err := drv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "unexport")); got == "532" {
		t.Error("unexported GPIO 532, which this process did not export")
	}
}

func TestSysfsReportsNoMatchingChip(t *testing.T) {
	root := sysfsFixture(t, []sysfsChip{
		{dir: "gpiochip0", label: "some-other-controller", base: 0, lines: 32},
	})
	if _, err := openSysfsLines(LineConfig{SysfsRoot: root}.withDefaults()); err == nil {
		t.Fatal("openSysfsLines succeeded with no header controller present")
	}
}

// --- the reset sequences -------------------------------------------------

type recordingDriver struct{ steps []string }

func (r *recordingDriver) set(boot0, reset bool) error {
	r.steps = append(r.steps, name(boot0, "BOOT0")+" "+name(reset, "RESET"))
	return nil
}
func (r *recordingDriver) describe() string { return "recording" }
func (r *recordingDriver) Close() error     { return nil }

func name(high bool, what string) string {
	if high {
		return what + "=high"
	}
	return what + "=low"
}

func fastLines(drv driver) *Lines {
	return &Lines{drv: drv, t: LineTimings{ResetHold: time.Nanosecond, BootSettle: time.Nanosecond, AppSettle: time.Nanosecond}}
}

// BOOT0 is sampled on the rising edge of reset and at no other time, so it must
// be settled while the part is held in reset. If these two steps were ever
// reordered the board would start the wrong program — the bug this ordering
// exists to prevent.
func TestEnterBootloaderSettlesBOOT0BeforeReleasingReset(t *testing.T) {
	r := &recordingDriver{}
	if err := fastLines(r).EnterBootloader(context.Background()); err != nil {
		t.Fatalf("EnterBootloader: %v", err)
	}
	want := []string{"BOOT0=high RESET=low", "BOOT0=high RESET=high"}
	if !equalSteps(r.steps, want) {
		t.Errorf("steps = %v, want %v", r.steps, want)
	}
}

func TestEnterApplicationHoldsBOOT0LowAcrossTheReset(t *testing.T) {
	r := &recordingDriver{}
	if err := fastLines(r).EnterApplication(context.Background()); err != nil {
		t.Fatalf("EnterApplication: %v", err)
	}
	want := []string{"BOOT0=low RESET=low", "BOOT0=low RESET=high"}
	if !equalSteps(r.steps, want) {
		t.Errorf("steps = %v, want %v", r.steps, want)
	}
}

func TestResetSequenceIsAbandonedWhenTheCallerGivesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	l := &Lines{drv: &recordingDriver{}, t: DefaultLineTimings()}
	if err := l.EnterBootloader(ctx); err == nil {
		t.Fatal("EnterBootloader ignored a cancelled context")
	}
}

func equalSteps(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
