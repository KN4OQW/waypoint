package modem

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Which ports get probed, and why the sweep is confined.
//
// internal/lcd/hd44780 makes the same argument about I2C and it applies here
// with more force: a hotspot's USB may also carry a GPS, a Nextion display, a
// radio's CAT interface or a 3D printer, and detection is speculative by
// nature — it addresses devices this code knows nothing about. Three things
// keep that honest:
//
//   - Only tty devices that could plausibly be a modem are enumerated at all.
//   - The one frame Waypoint ever writes is GET_VERSION, three bytes with no
//     side effect on anything that understands them and no meaning to anything
//     that does not.
//   - Ports the operator has already claimed for something else — the display
//     port, above all, because a Nextion is a serial device sitting on exactly
//     the kind of port this sweep would otherwise walk into — are excluded by
//     the caller, not discovered and hoped about.
//
// Ordering is evidence-ranked. A known modem VID/PID is positive evidence that
// something is a modem; a /dev/ttyAMA0 that exists is not (it exists on every
// Pi whether or not a hat is fitted). So known USB IDs are probed first, the
// GPIO UART next, and unrecognised serial devices last.

// usbIDs are the USB vendor/product pairs the launch-tier boards appear as.
// A match is a ranking hint and nothing more: an unlisted ID is still probed,
// just later, because clone vendors reuse and invent IDs freely.
var usbIDs = map[string]string{
	"1eaf:0004": "Maple serial (STM32F103 CDC) — ZUMspot USB, JumboSpot USB and kin",
	"1eaf:0003": "Maple DFU bootloader — the board is in its bootloader, not running firmware",
	"0483:5740": "ST-Micro Virtual COM Port",
	"10c4:ea60": "Silicon Labs CP210x bridge — Nano hotSPOT and kin",
	"0403:6001": "FTDI FT232 bridge",
}

// Port is one candidate serial device.
type Port struct {
	Path      string
	Transport Transport
	VID, PID  string // "1eaf", "0004"; empty for GPIO
	Known     string // why this ID is recognised, when it is
	rank      int
}

// ID is the "vid:pid" key, or "" for a port with no USB identity.
func (p Port) ID() string {
	if p.VID == "" || p.PID == "" {
		return ""
	}
	return p.VID + ":" + p.PID
}

// Bootloader reports a board sitting in its DFU bootloader rather than running
// firmware. It cannot answer GET_VERSION, so probing it is pointless — but
// saying so is far better than "no modem found", because the operator is one
// firmware flash (#19) away from a working node and needs to be told that.
func (p Port) Bootloader() bool { return p.ID() == "1eaf:0003" }

// Scanner enumerates candidate ports. The filesystem roots are fields so the
// enumeration can be tested against a fixture tree rather than the machine
// running the tests.
type Scanner struct {
	DevDir  string   // default /dev
	SysTTY  string   // default /sys/class/tty
	Exclude []string // ports the operator has claimed for something else
}

// Scan lists candidate ports, best evidence first.
func (s Scanner) Scan() ([]Port, error) {
	devDir, sysTTY := s.DevDir, s.SysTTY
	if devDir == "" {
		devDir = "/dev"
	}
	if sysTTY == "" {
		sysTTY = "/sys/class/tty"
	}

	entries, err := os.ReadDir(devDir)
	if err != nil {
		return nil, err
	}
	excluded := map[string]bool{}
	for _, e := range s.Exclude {
		if e = strings.TrimSpace(e); e != "" {
			excluded[e] = true
		}
	}

	seen := map[string]bool{}
	var ports []Port
	for _, e := range entries {
		name := e.Name()
		transport, ok := classify(name)
		if !ok {
			continue
		}
		path := filepath.Join(devDir, name)
		// /dev/serial0 and /dev/ttyAMA0 are the same UART under two names.
		// Probing a port twice wastes the operator's time on the one screen
		// where they are watching a spinner.
		real := path
		if r, err := filepath.EvalSymlinks(path); err == nil {
			real = r
		}
		if seen[real] || excluded[path] || excluded[real] {
			continue
		}
		seen[real] = true

		p := Port{Path: path, Transport: transport}
		if transport == TransportUSB {
			p.VID, p.PID = usbIDOf(sysTTY, name)
			p.Known = usbIDs[p.ID()]
		}
		p.rank = rank(p, name)
		ports = append(ports, p)
	}
	sort.SliceStable(ports, func(i, j int) bool {
		if ports[i].rank != ports[j].rank {
			return ports[i].rank < ports[j].rank
		}
		return ports[i].Path < ports[j].Path
	})
	return ports, nil
}

// classify decides whether a /dev entry could be a modem at all.
func classify(name string) (Transport, bool) {
	switch {
	case strings.HasPrefix(name, "ttyAMA"), name == "serial0", name == "serial1":
		return TransportGPIO, true
	case name == "ttyS0":
		// Only ttyS0. On a Pi 4 with the mini-UART routed to the header this is
		// a real modem port, but on any x86 host /dev/ttyS0..31 all exist and
		// all open successfully, and probing 32 dead ports at a second and a
		// half apiece is a minute of spinner for nothing.
		return TransportGPIO, true
	case strings.HasPrefix(name, "ttyACM"), strings.HasPrefix(name, "ttyUSB"):
		return TransportUSB, true
	}
	return 0, false
}

func rank(p Port, name string) int {
	switch {
	case p.Known != "" && !p.Bootloader():
		return 0 // a recognised modem ID is positive evidence
	case p.Transport == TransportGPIO:
		return 1
	case strings.HasPrefix(name, "ttyACM"):
		return 2 // a CDC device is more likely a modem than a bridge chip is
	default:
		return 3
	}
}

// usbIDOf reads the vendor/product IDs for a tty out of sysfs.
//
// The device symlink points at different things for the two USB paths — a CDC
// interface for ttyACM, a usb-serial port several levels down for ttyUSB — so
// rather than encode either shape, this walks up parents until it finds the
// directory that carries both ID files. Missing IDs are not an error: a tty
// with no USB ancestry is simply not a USB device.
func usbIDOf(sysTTY, name string) (vid, pid string) {
	dir, err := filepath.EvalSymlinks(filepath.Join(sysTTY, name, "device"))
	if err != nil {
		return "", ""
	}
	for i := 0; i < 8; i++ {
		v, verr := os.ReadFile(filepath.Join(dir, "idVendor"))
		p, perr := os.ReadFile(filepath.Join(dir, "idProduct"))
		if verr == nil && perr == nil {
			return strings.ToLower(strings.TrimSpace(string(v))), strings.ToLower(strings.TrimSpace(string(p)))
		}
		parent := filepath.Dir(dir)
		if parent == dir || parent == "/" {
			break
		}
		dir = parent
	}
	return "", ""
}
