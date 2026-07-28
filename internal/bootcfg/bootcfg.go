// Package bootcfg diagnoses and repairs the one piece of Raspberry Pi boot
// configuration a GPIO modem cannot work without: whether the PL011 UART on
// GPIO 14/15 belongs to the modem, or to Bluetooth and a login console.
//
// Waypoint's own image already gets this right — it sets enable_uart=1, adds
// dtoverlay=disable-bt, strips the console token and masks the serial getty
// (image/src/modules/waypoint/start_chroot_script). But Phase-1 distribution is
// a .deb on stock Raspberry Pi OS Lite, and there the defaults are exactly
// wrong: on a Pi 3 and later the PL011 is wired to the onboard Bluetooth
// controller and a getty sits on the serial console. A hat fitted to that node
// is electrically fine and completely mute.
//
// That failure has no symptom an operator can act on. Detection finds nothing;
// MMDVM-Host, if it were started, would exit because its port has no modem; and
// nothing anywhere says the word "Bluetooth". Naming it is most of the value
// here — the repair is three lines in two files, but knowing which three lines
// is the part that costs an evening.
package bootcfg

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Problem IDs. They are stable strings because the UI keys its explanation text
// off them, and a renamed ID would silently lose an explanation.
const (
	ProblemNoBootPartition = "no_boot_partition"
	ProblemUARTDisabled    = "uart_disabled"
	ProblemBluetoothOwns   = "bluetooth_owns_uart"
	ProblemSerialConsole   = "serial_console"
	ProblemGettyEnabled    = "getty_enabled"
)

// Problem is one reason the GPIO UART is not the modem's.
type Problem struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	// Fixable reports whether EnableModemUART can repair this one. Everything
	// except a missing boot partition is fixable; that one means this is not a
	// Raspberry Pi, or its boot partition is not mounted, and Waypoint has
	// nothing useful to offer.
	Fixable bool `json:"fixable"`
}

// Report is the diagnosis.
type Report struct {
	// Applicable is false when this host has no Raspberry Pi boot partition —
	// a dev box, a container, an image built elsewhere. Everything else in the
	// report is meaningless then, and the UI shows nothing rather than
	// inventing a problem the operator cannot have.
	Applicable  bool   `json:"applicable"`
	ConfigPath  string `json:"config_path,omitempty"`
	CmdlinePath string `json:"cmdline_path,omitempty"`

	EnableUART    bool `json:"enable_uart"`
	BluetoothOff  bool `json:"bluetooth_off"`
	SerialConsole bool `json:"serial_console"`
	GettyEnabled  bool `json:"getty_enabled"`

	// OK means a hat on GPIO 14/15 can be talked to. It is not a claim that one
	// is fitted.
	OK       bool      `json:"ok"`
	Problems []Problem `json:"problems,omitempty"`
	// RebootRequired is set after a repair: config.txt and cmdline.txt are read
	// by the firmware and the kernel at boot, so an edit to either changes
	// nothing until the next one.
	RebootRequired bool `json:"reboot_required,omitempty"`
}

// gettyUnits are the serial-console login services that would fight the modem
// for the port. Both names exist because which one the console lands on depends
// on the board and on whether the mini-UART is in play.
var gettyUnits = []string{"serial-getty@ttyAMA0.service", "serial-getty@ttyS0.service"}

// Options are the injectable surroundings, so the diagnosis can be tested
// against a fixture tree instead of the machine running the tests.
type Options struct {
	// BootDir is the boot partition. Empty means the standard search: the
	// current /boot/firmware layout first, then the pre-bookworm /boot.
	BootDir string
	// UnitEnabled reports whether a systemd unit is enabled or running. Nil
	// means "assume not", which is the right default for a host with no
	// systemd — there is no getty to fight over the port there either.
	UnitEnabled func(unit string) bool
}

func (o Options) bootDir() string {
	if o.BootDir != "" {
		return o.BootDir
	}
	for _, d := range []string{"/boot/firmware", "/boot"} {
		if _, err := os.Stat(filepath.Join(d, "config.txt")); err == nil {
			return d
		}
	}
	return ""
}

var (
	// A commented-out or differently-spaced line is not the setting. These match
	// the whole line so `#enable_uart=1` — the state a stock config.txt often
	// ships in — is correctly read as "off".
	reEnableUART = regexp.MustCompile(`(?m)^\s*enable_uart\s*=\s*1\s*$`)
	// Either overlay frees the PL011: disable-bt turns Bluetooth off entirely,
	// miniuart-bt moves it to the mini-UART. Accepting both matters because
	// miniuart-bt is what an operator who wants to keep Bluetooth will have set,
	// and telling them it is wrong would be telling them to undo a correct fix.
	reBTOverlay = regexp.MustCompile(`(?m)^\s*dtoverlay\s*=\s*(disable-bt|miniuart-bt)\b`)
	// The kernel's serial console token, in any of the three names the console
	// answers to.
	reConsole = regexp.MustCompile(`console=(serial0|ttyAMA0|ttyS0)(,\d+)?\s*`)
)

// Inspect reads the boot configuration and reports what stands between a GPIO
// hat and the host.
func Inspect(o Options) Report {
	dir := o.bootDir()
	if dir == "" {
		return Report{Applicable: false}
	}
	r := Report{
		Applicable:  true,
		ConfigPath:  filepath.Join(dir, "config.txt"),
		CmdlinePath: filepath.Join(dir, "cmdline.txt"),
	}
	config, cerr := os.ReadFile(r.ConfigPath)
	if cerr != nil {
		// The directory had a config.txt a moment ago (bootDir found it) or the
		// caller named a directory that has none. Either way there is nothing to
		// diagnose and nothing safe to edit.
		return Report{Applicable: false}
	}
	cmdline, _ := os.ReadFile(r.CmdlinePath)

	r.EnableUART = reEnableUART.Match(config)
	r.BluetoothOff = reBTOverlay.Match(config)
	r.SerialConsole = reConsole.Match(cmdline)
	if o.UnitEnabled != nil {
		for _, u := range gettyUnits {
			if o.UnitEnabled(u) {
				r.GettyEnabled = true
				break
			}
		}
	}

	if !r.EnableUART {
		r.Problems = append(r.Problems, Problem{
			ID: ProblemUARTDisabled, Fixable: true,
			Message: "The GPIO serial port is switched off. Raspberry Pi OS ships it that way, so a hat fitted to a stock install is electrically fine and completely mute (enable_uart=1 in config.txt).",
		})
	}
	if !r.BluetoothOff {
		r.Problems = append(r.Problems, Problem{
			ID: ProblemBluetoothOwns, Fixable: true,
			Message: "The onboard Bluetooth controller owns the UART the modem needs. On a Pi 3 and later this is the default, and it is the single most common reason a working hat is never found (dtoverlay=disable-bt in config.txt).",
		})
	}
	if r.SerialConsole {
		r.Problems = append(r.Problems, Problem{
			ID: ProblemSerialConsole, Fixable: true,
			Message: "A login console is attached to the modem's serial port. It will answer the modem's traffic with login prompts (a console= token in cmdline.txt).",
		})
	}
	if r.GettyEnabled {
		r.Problems = append(r.Problems, Problem{
			ID: ProblemGettyEnabled, Fixable: true,
			Message: "A serial login service is enabled on the modem's port and will hold it open.",
		})
	}
	r.OK = len(r.Problems) == 0
	return r
}

// Applier is the side of a repair that needs systemd. It is an interface so this
// package shells out to nothing and can be tested without one.
type Applier interface {
	// DisableGetty masks the serial login services so they cannot be started by
	// anything later.
	DisableGetty(units []string) error
}

// Apply frees the GPIO UART for the modem, idempotently: enable_uart=1 and a
// Bluetooth overlay appended to config.txt if absent, the console token stripped
// from cmdline.txt, and the serial getty masked.
//
// There is deliberately no way to undo this. Waypoint can free the UART
// faithfully — it knows exactly which three lines that takes — but it cannot
// restore a serial console it never saw, at a baud rate it was never told, and a
// reverse that half-works on a headless node is worse than one that is not
// offered. An operator who moves to a USB modem and wants Bluetooth back edits
// config.txt, which is a file they own.
//
// Returns the report as it stands after the edits, with RebootRequired set if
// anything in the boot files changed.
func Apply(o Options, a Applier) (Report, error) {
	before := Inspect(o)
	if !before.Applicable {
		return before, nil
	}

	changed := false
	if !before.EnableUART || !before.BluetoothOff {
		config, err := os.ReadFile(before.ConfigPath)
		if err != nil {
			return before, err
		}
		out := string(config)
		if !strings.HasSuffix(out, "\n") && out != "" {
			out += "\n"
		}
		if !before.EnableUART {
			out += "\n# Waypoint: free the PL011 UART on GPIO 14/15 for the MMDVM modem.\nenable_uart=1\n"
		}
		if !before.BluetoothOff {
			out += "dtoverlay=disable-bt\n"
		}
		if err := writeFilePreservingMode(before.ConfigPath, []byte(out)); err != nil {
			return before, err
		}
		changed = true
	}

	if before.SerialConsole {
		cmdline, err := os.ReadFile(before.CmdlinePath)
		if err != nil {
			return before, err
		}
		// cmdline.txt is a single line of space-separated tokens, and the
		// firmware is unforgiving about it: a stray newline in the middle
		// truncates the kernel command line. So the console token is removed
		// from the line rather than the file being rewritten.
		stripped := strings.TrimRight(reConsole.ReplaceAllString(string(cmdline), ""), " \t\r\n") + "\n"
		if err := writeFilePreservingMode(before.CmdlinePath, []byte(stripped)); err != nil {
			return before, err
		}
		changed = true
	}

	if before.GettyEnabled && a != nil {
		if err := a.DisableGetty(gettyUnits); err != nil {
			return before, err
		}
	}

	after := Inspect(o)
	after.RebootRequired = changed
	return after, nil
}

// writeFilePreservingMode replaces a file's contents without changing its
// permissions or owner. The boot partition is usually vfat, where the mode is
// synthesised from the mount options, but on a node where it is not, a repair
// that quietly reset config.txt to 0600 would be a nasty thing to leave behind.
func writeFilePreservingMode(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	return os.WriteFile(path, data, mode)
}
