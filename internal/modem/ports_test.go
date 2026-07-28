package modem

import (
	"os"
	"path/filepath"
	"testing"
)

// devTree builds a fake /dev and /sys/class/tty so enumeration is tested against
// a fixture rather than against whatever the machine running the tests happens
// to have plugged in.
type devTree struct{ dev, sys string }

func newDevTree(t *testing.T) devTree {
	t.Helper()
	root := t.TempDir()
	d := devTree{dev: filepath.Join(root, "dev"), sys: filepath.Join(root, "sys", "class", "tty")}
	for _, p := range []string{d.dev, d.sys} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func (d devTree) tty(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(d.dev, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (d devTree) symlink(t *testing.T, name, target string) {
	t.Helper()
	if err := os.Symlink(filepath.Join(d.dev, target), filepath.Join(d.dev, name)); err != nil {
		t.Fatal(err)
	}
}

// usb wires a tty to a USB device node in the fake sysfs, at depth levels below
// the interface the tty's device symlink points at — which is how the two USB
// paths actually differ on a real machine.
func (d devTree) usb(t *testing.T, name, vid, pid string, depth int) {
	t.Helper()
	devPath := filepath.Join(d.sys, name)
	usbRoot := filepath.Join(devPath, "usbdev")
	leaf := usbRoot
	for i := 0; i < depth; i++ {
		leaf = filepath.Join(leaf, "child")
	}
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, v := range map[string]string{"idVendor": vid, "idProduct": pid} {
		if err := os.WriteFile(filepath.Join(usbRoot, f), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(leaf, filepath.Join(devPath, "device")); err != nil {
		t.Fatal(err)
	}
}

func TestScanOnlyEnumeratesPlausibleModemPorts(t *testing.T) {
	d := newDevTree(t)
	for _, n := range []string{"ttyAMA0", "ttyACM0", "ttyUSB0", "null", "i2c-1", "ttyprintk", "sda"} {
		d.tty(t, n)
	}
	ports, err := Scanner{DevDir: d.dev, SysTTY: d.sys}.Scan()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range ports {
		got[filepath.Base(p.Path)] = true
	}
	for _, want := range []string{"ttyAMA0", "ttyACM0", "ttyUSB0"} {
		if !got[want] {
			t.Errorf("Scan missed %s", want)
		}
	}
	for _, never := range []string{"null", "i2c-1", "ttyprintk", "sda"} {
		if got[never] {
			t.Errorf("Scan enumerated %s — the sweep must not walk devices it knows nothing about", never)
		}
	}
}

func TestScanDoesNotEnumerateTheSameUARTTwice(t *testing.T) {
	// /dev/serial0 is a symlink to /dev/ttyAMA0 on every Pi. Probing one UART
	// twice is a second and a half of spinner for nothing.
	d := newDevTree(t)
	d.tty(t, "ttyAMA0")
	d.symlink(t, "serial0", "ttyAMA0")
	ports, err := Scanner{DevDir: d.dev, SysTTY: d.sys}.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 {
		t.Fatalf("Scan returned %d ports for one UART: %+v", len(ports), ports)
	}
}

func TestScanSkipsTheSerialConsoleClones(t *testing.T) {
	// Only ttyS0. On x86 every ttyS0..31 exists and opens successfully, and
	// probing 32 dead ports is a minute of spinner.
	d := newDevTree(t)
	for _, n := range []string{"ttyS0", "ttyS1", "ttyS4", "ttyS31"} {
		d.tty(t, n)
	}
	ports, err := Scanner{DevDir: d.dev, SysTTY: d.sys}.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || filepath.Base(ports[0].Path) != "ttyS0" {
		t.Fatalf("Scan returned %+v, want only ttyS0", ports)
	}
}

func TestScanExcludesPortsTheOperatorHasClaimed(t *testing.T) {
	// The display port is the one that matters: a Nextion is a serial device on
	// exactly the kind of port this sweep would otherwise walk into.
	d := newDevTree(t)
	d.tty(t, "ttyAMA0")
	d.tty(t, "ttyUSB0")
	nextion := filepath.Join(d.dev, "ttyUSB0")
	ports, err := Scanner{DevDir: d.dev, SysTTY: d.sys, Exclude: []string{nextion}}.Scan()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ports {
		if p.Path == nextion {
			t.Fatal("Scan probed a port the operator had already claimed for a display")
		}
	}
	if len(ports) != 1 {
		t.Fatalf("Scan returned %+v, want only the GPIO UART", ports)
	}
}

func TestScanReadsUSBIDsFromEitherSysfsShape(t *testing.T) {
	// ttyACM's device symlink lands on a CDC interface; ttyUSB's lands several
	// levels below the USB device. Walking up beats encoding either shape.
	d := newDevTree(t)
	d.tty(t, "ttyACM0")
	d.usb(t, "ttyACM0", "1eaf", "0004", 1)
	d.tty(t, "ttyUSB0")
	d.usb(t, "ttyUSB0", "10c4", "ea60", 3)

	ports, err := Scanner{DevDir: d.dev, SysTTY: d.sys}.Scan()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, p := range ports {
		ids[filepath.Base(p.Path)] = p.ID()
	}
	if ids["ttyACM0"] != "1eaf:0004" {
		t.Errorf("ttyACM0 USB ID = %q, want 1eaf:0004", ids["ttyACM0"])
	}
	if ids["ttyUSB0"] != "10c4:ea60" {
		t.Errorf("ttyUSB0 USB ID = %q, want 10c4:ea60", ids["ttyUSB0"])
	}
}

func TestScanRanksRecognisedModemsAheadOfTheGPIOUART(t *testing.T) {
	// A known VID/PID is positive evidence that something is a modem. A
	// /dev/ttyAMA0 that exists is not: it exists on every Pi, hat or no hat.
	d := newDevTree(t)
	d.tty(t, "ttyAMA0")
	d.tty(t, "ttyUSB0")
	d.tty(t, "ttyACM0")
	d.usb(t, "ttyACM0", "1eaf", "0004", 1)

	ports, err := Scanner{DevDir: d.dev, SysTTY: d.sys}.Scan()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ttyACM0", "ttyAMA0", "ttyUSB0"}
	for i, w := range want {
		if i >= len(ports) || filepath.Base(ports[i].Path) != w {
			t.Fatalf("probe order = %+v, want %v", ports, want)
		}
	}
	if ports[0].Known == "" {
		t.Error("a recognised USB ID must carry the reason it is recognised, for the report")
	}
}

func TestBootloaderPortIsRecognised(t *testing.T) {
	// A board in DFU cannot answer GET_VERSION. Saying so is far more use than
	// "no modem found" — the operator is one flash away from a working node.
	d := newDevTree(t)
	d.tty(t, "ttyACM0")
	d.usb(t, "ttyACM0", "1eaf", "0003", 1)
	ports, err := Scanner{DevDir: d.dev, SysTTY: d.sys}.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || !ports[0].Bootloader() {
		t.Fatalf("ports = %+v; the DFU bootloader ID must be recognised as such", ports)
	}
}

func TestScanMissingDevDirIsAnError(t *testing.T) {
	if _, err := (Scanner{DevDir: filepath.Join(t.TempDir(), "nope")}).Scan(); err == nil {
		t.Fatal("Scan silently succeeded with no /dev")
	}
}
