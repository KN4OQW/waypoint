package hd44780

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Detection exists because the panel is most useful exactly when the operator
// has had no chance to configure it.
//
// The LCD is normally an operator-configured device: bus and address come from
// the config, and nothing opens a panel that has not been declared. On a node
// that has never been set up there is no config and nobody to write one — which
// is the moment a two-line display is worth most, because a freshly flashed
// headless board with no access point has no other way to say anything at all.
// So the setup path probes for a panel instead of being told about one.

// DefaultBuses are the I2C buses to search, in order. /dev/i2c-1 is the header
// bus on every Pi since the B+; /dev/i2c-0 is the early-model header and is
// checked second so a rev-1 board is not silently unsupported.
var DefaultBuses = []string{"/dev/i2c-1", "/dev/i2c-0"}

// CandidateAddrs are the addresses a PCF8574 backpack can occupy, and NOTHING
// else is probed.
//
// The PCF8574 answers on 0x20–0x27 and the PCF8574A on 0x38–0x3F, both selected
// by three address-strap pins; 0x27 and 0x3F are the two the cheap eBay modules
// ship strapped to. Confining the sweep to these ranges is the whole safety
// argument for probing at all — a hotspot's I2C bus may also carry a real-time
// clock (0x68), an EEPROM (0x50–0x57) or a sensor, and walking the entire bus
// would mean poking chips this code knows nothing about.
var CandidateAddrs = []uint8{
	0x27, 0x3F, // strapped-default backpacks: overwhelmingly the ones in the field
	0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26,
	0x38, 0x39, 0x3A, 0x3B, 0x3C, 0x3D, 0x3E,
}

// ErrNoPanel is returned when no bus exists or nothing answered on one.
var ErrNoPanel = errors.New("no HD44780 panel found on any I2C bus")

// Found describes a detected panel.
type Found struct {
	Bus  string // e.g. /dev/i2c-1
	Addr string // e.g. 0x27
}

func (f Found) String() string { return f.Bus + "@" + f.Addr }

// Detect sweeps the candidate addresses on each bus and returns the first that
// answers. buses may be nil, meaning DefaultBuses.
//
// A bus that does not exist is skipped rather than reported: I2C is off by
// default on Raspberry Pi OS, so "no /dev/i2c-1" is a configuration state, not a
// fault, and it is the common case on a board with no panel wired.
func Detect(buses []string) (Found, error) {
	if len(buses) == 0 {
		buses = DefaultBuses
	}
	for _, bus := range buses {
		if _, err := os.Stat(bus); err != nil {
			continue
		}
		for _, a := range CandidateAddrs {
			if probe(bus, a) {
				return Found{Bus: bus, Addr: fmt.Sprintf("0x%02x", a)}, nil
			}
		}
	}
	return Found{}, ErrNoPanel
}

// probe reports whether a device acknowledges at addr.
//
// It READS one byte rather than writing one. A write is what Open does once a
// panel is known, but detection is speculative by nature: it addresses chips
// that may not be backpacks, and a stray write to an unknown device can latch
// outputs or start a state machine. A one-byte read is the gentlest transaction
// that still requires the slave to ACK its address — for a PCF8574 it returns
// the current port state and changes nothing. Anything that does not ACK fails
// with ENXIO/EREMOTEIO and is not adopted.
func probe(bus string, addr uint8) bool {
	fd, err := unix.Open(bus, unix.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.IoctlSetInt(fd, i2cSlave, int(addr)); err != nil {
		return false
	}
	b := make([]byte, 1)
	_, err = unix.Read(fd, b)
	return err == nil
}
