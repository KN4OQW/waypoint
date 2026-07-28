package bootcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bootDir builds a fixture boot partition.
func bootDir(t *testing.T, config, cmdline string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.txt"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline.txt"), []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// stockPiOS is what a fresh Raspberry Pi OS Lite install looks like: the UART
// off, Bluetooth holding the PL011, and a login console on the serial port.
const stockPiOS = `# For more options and information see rpi-software-config
[all]
dtparam=audio=on
camera_auto_detect=1
#enable_uart=1
`

const stockCmdline = "console=serial0,115200 console=tty1 root=PARTUUID=abcd1234-02 rootfstype=ext4 fsck.repair=yes rootwait\n"

func TestStockPiOSIsDiagnosedInFull(t *testing.T) {
	// The whole point: a hat fitted to a stock install is electrically fine and
	// completely mute, and nothing anywhere says the word "Bluetooth".
	r := Inspect(Options{
		BootDir:     bootDir(t, stockPiOS, stockCmdline),
		UnitEnabled: func(string) bool { return true },
	})
	if !r.Applicable || r.OK {
		t.Fatalf("stock Pi OS reported as ready for a GPIO hat: %+v", r)
	}
	want := map[string]bool{
		ProblemUARTDisabled: true, ProblemBluetoothOwns: true,
		ProblemSerialConsole: true, ProblemGettyEnabled: true,
	}
	got := map[string]bool{}
	for _, p := range r.Problems {
		got[p.ID] = true
		if !p.Fixable {
			t.Errorf("problem %q reported as unfixable", p.ID)
		}
		if p.Message == "" {
			t.Errorf("problem %q has no explanation, which is most of the value here", p.ID)
		}
	}
	for id := range want {
		if !got[id] {
			t.Errorf("stock Pi OS: %q not diagnosed", id)
		}
	}
}

// A commented-out setting is not the setting, and this is the exact state a
// stock config.txt ships in.
func TestCommentedEnableUARTIsNotEnabled(t *testing.T) {
	r := Inspect(Options{BootDir: bootDir(t, "#enable_uart=1\ndtoverlay=disable-bt\n", "root=/dev/mmcblk0p2\n")})
	if r.EnableUART {
		t.Fatal("a commented-out enable_uart was read as switched on")
	}
}

func TestMiniUARTBluetoothIsAlsoAValidFix(t *testing.T) {
	// An operator who wanted to keep Bluetooth will have set miniuart-bt.
	// Telling them that is wrong would be telling them to undo a correct fix.
	r := Inspect(Options{BootDir: bootDir(t, "enable_uart=1\ndtoverlay=miniuart-bt\n", "root=/dev/mmcblk0p2\n")})
	if !r.BluetoothOff || !r.OK {
		t.Fatalf("miniuart-bt was not accepted as freeing the PL011: %+v", r)
	}
}

func TestWaypointImageIsAlreadyCorrect(t *testing.T) {
	// The state image/src/modules/waypoint/start_chroot_script leaves behind.
	r := Inspect(Options{
		BootDir:     bootDir(t, "enable_uart=1\ndtoverlay=disable-bt\ndtparam=i2c_arm=on\n", "root=/dev/mmcblk0p2 rootwait\n"),
		UnitEnabled: func(string) bool { return false },
	})
	if !r.OK || len(r.Problems) != 0 {
		t.Fatalf("the Waypoint image's own boot config was reported as broken: %+v", r)
	}
}

func TestNotARaspberryPiIsNotAProblem(t *testing.T) {
	// A dev box, a container, an image built elsewhere. Inventing a problem the
	// operator cannot have is worse than saying nothing.
	r := Inspect(Options{BootDir: t.TempDir()})
	if r.Applicable {
		t.Fatalf("a directory with no config.txt was diagnosed as a Pi boot partition: %+v", r)
	}
	if len(r.Problems) != 0 {
		t.Errorf("problems reported for a host this cannot apply to: %+v", r.Problems)
	}
}

type fakeApplier struct{ masked []string }

func (f *fakeApplier) DisableGetty(units []string) error {
	f.masked = append(f.masked, units...)
	return nil
}

func TestApplyFreesTheUARTOnAStockInstall(t *testing.T) {
	dir := bootDir(t, stockPiOS, stockCmdline)
	getty := true
	opts := Options{BootDir: dir, UnitEnabled: func(string) bool { return getty }}
	fa := &fakeApplier{}

	r, err := Apply(opts, fa)
	if err != nil {
		t.Fatal(err)
	}
	config := readFile(t, filepath.Join(dir, "config.txt"))
	if !strings.Contains(config, "\nenable_uart=1\n") {
		t.Errorf("enable_uart not set:\n%s", config)
	}
	if !strings.Contains(config, "dtoverlay=disable-bt") {
		t.Errorf("Bluetooth still owns the UART:\n%s", config)
	}
	// The operator's own settings must survive a repair untouched.
	if !strings.Contains(config, "dtparam=audio=on") || !strings.Contains(config, "camera_auto_detect=1") {
		t.Errorf("the repair ate the operator's own config.txt lines:\n%s", config)
	}

	cmdline := readFile(t, filepath.Join(dir, "cmdline.txt"))
	if strings.Contains(cmdline, "console=serial0") {
		t.Errorf("serial console still on the kernel command line: %q", cmdline)
	}
	if !strings.Contains(cmdline, "root=PARTUUID=abcd1234-02") || !strings.Contains(cmdline, "console=tty1") {
		t.Errorf("the repair removed more than the serial console: %q", cmdline)
	}
	// The firmware truncates the kernel command line at a newline, so it must
	// stay exactly one line.
	if strings.Count(strings.TrimRight(cmdline, "\n"), "\n") != 0 {
		t.Errorf("cmdline.txt is no longer a single line: %q", cmdline)
	}

	if len(fa.masked) == 0 {
		t.Error("the serial login service was left enabled to fight the modem for the port")
	}
	if !r.RebootRequired {
		t.Error("config.txt and cmdline.txt are read at boot; a repair to either needs a reboot to matter")
	}
	getty = false
	if after := Inspect(opts); !after.OK {
		t.Errorf("after the repair, the node still reports problems: %+v", after.Problems)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	dir := bootDir(t, "enable_uart=1\ndtoverlay=disable-bt\n", "root=/dev/mmcblk0p2 rootwait\n")
	opts := Options{BootDir: dir, UnitEnabled: func(string) bool { return false }}
	before := readFile(t, filepath.Join(dir, "config.txt"))

	r, err := Apply(opts, &fakeApplier{})
	if err != nil {
		t.Fatal(err)
	}
	if after := readFile(t, filepath.Join(dir, "config.txt")); after != before {
		t.Errorf("a repair on an already-correct node rewrote config.txt:\n%s", after)
	}
	if r.RebootRequired {
		t.Error("a no-op repair asked for a reboot")
	}
}

func TestApplyOnANonPiDoesNothing(t *testing.T) {
	dir := t.TempDir()
	r, err := Apply(Options{BootDir: dir}, &fakeApplier{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Applicable || r.RebootRequired {
		t.Fatalf("a host with no boot partition was repaired: %+v", r)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Error("the repair created boot files on a host that has none")
	}
}

func TestApplyPreservesFileMode(t *testing.T) {
	// A repair that quietly reset config.txt to 0600 would be a nasty thing to
	// leave behind on a node whose boot partition is not vfat.
	dir := bootDir(t, stockPiOS, stockCmdline)
	path := filepath.Join(dir, "config.txt")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Options{BootDir: dir}, &fakeApplier{}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("config.txt mode = %v, want 0640 preserved", fi.Mode().Perm())
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
