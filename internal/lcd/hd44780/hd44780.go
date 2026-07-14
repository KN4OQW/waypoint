package hd44780

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// conn is the byte transport to the PCF8574 (one I2C slave). It is an interface
// so the protocol/device logic is testable without real hardware.
type conn interface {
	tx([]byte) error
	close() error
}

// Device drives an HD44780 over a PCF8574 backpack. It implements
// internal/lcd.LCDDevice. All writes go out backlight-on.
type Device struct {
	c          conn
	rows, cols int
	offsets    []byte
	sleep      func(time.Duration) // injectable so tests don't wait real init delays
}

// Open opens the I2C bus, selects the backpack address, and probes it. It does
// NOT run the LCD init sequence — the renderer calls Init once it knows the
// geometry. A missing/!ACKing panel fails here so the caller can fall back to a
// headless noop.
func Open(bus, addr string) (*Device, error) {
	a, err := parseAddr(addr)
	if err != nil {
		return nil, err
	}
	c, err := openI2C(bus, a)
	if err != nil {
		return nil, err
	}
	// Probe: set the port to backlight-on (no enable strobe). An absent device
	// makes this write fail (ENXIO), so we don't adopt a panel that isn't there.
	if err := c.tx([]byte{pinBL}); err != nil {
		c.close()
		return nil, fmt.Errorf("probe: %w", err)
	}
	return &Device{c: c, sleep: time.Sleep}, nil
}

// Init records the geometry and runs the HD44780 4-bit init handshake.
func (d *Device) Init(rows, cols int) error {
	d.rows, d.cols = rows, cols
	d.offsets = rowOffsets(rows, cols)
	return d.initLCD()
}

// WriteLine positions the cursor at the row's DDRAM base and writes the text as
// data. text is expected already sized to cols and ASCII (the renderer's job).
// Cursor + characters go out as one I2C transaction to spare the slow bus.
func (d *Device) WriteLine(row int, text string) error {
	if row < 0 || row >= len(d.offsets) {
		return nil
	}
	buf := encode(cmdSetDDRAM|d.offsets[row], false, true)
	for i := 0; i < len(text); i++ {
		buf = append(buf, encode(text[i], true, true)...)
	}
	return d.c.tx(buf)
}

// Clear clears the display and waits for the (slow) clear instruction.
func (d *Device) Clear() error {
	if err := d.c.tx(encode(cmdClear, false, true)); err != nil {
		return err
	}
	d.sleep(2 * time.Millisecond)
	return nil
}

// Close releases the I2C file descriptor.
func (d *Device) Close() error { return d.c.close() }

// command sends one instruction byte (register select low).
func (d *Device) command(b byte) error { return d.c.tx(encode(b, false, true)) }

// initLCD performs the datasheet 4-bit initialization: three 0x3 nibbles to
// force a known state, a 0x2 to enter 4-bit mode, then function-set / display /
// clear / entry-mode / display-on. Delays follow the datasheet minimums.
func (d *Device) initLCD() error {
	d.sleep(50 * time.Millisecond) // power-on settle
	steps := []struct {
		bytes []byte
		wait  time.Duration
	}{
		{nibbleRaw(0x30, true), 5 * time.Millisecond},
		{nibbleRaw(0x30, true), 1 * time.Millisecond},
		{nibbleRaw(0x30, true), 1 * time.Millisecond},
		{nibbleRaw(0x20, true), 1 * time.Millisecond}, // switch to 4-bit
		{encode(cmdFunctionSet, false, true), 0},
		{encode(cmdDisplayOff, false, true), 0},
		{encode(cmdClear, false, true), 2 * time.Millisecond},
		{encode(cmdEntryMode, false, true), 0},
		{encode(cmdDisplayOn, false, true), 0},
	}
	for _, s := range steps {
		if err := d.c.tx(s.bytes); err != nil {
			return err
		}
		if s.wait > 0 {
			d.sleep(s.wait)
		}
	}
	return nil
}

// parseAddr parses a PCF8574 I2C address written in hex, with or without the 0x
// prefix (e.g. "0x27" or "27").
func parseAddr(s string) (uint8, error) {
	t := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x")
	v, err := strconv.ParseUint(t, 16, 8)
	if err != nil {
		return 0, fmt.Errorf("invalid I2C address %q (want hex, e.g. 0x27)", s)
	}
	return uint8(v), nil
}

// i2cSlave is the I2C_SLAVE ioctl request (linux/i2c-dev.h): bind this fd to a
// slave address for subsequent read/write.
const i2cSlave = 0x0703

// i2cConn is the real transport: a /dev/i2c-N file descriptor bound to one slave.
type i2cConn struct{ fd int }

func openI2C(bus string, addr uint8) (conn, error) {
	fd, err := unix.Open(bus, unix.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", bus, err)
	}
	if err := unix.IoctlSetInt(fd, i2cSlave, int(addr)); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("select 0x%02x on %s: %w", addr, bus, err)
	}
	return &i2cConn{fd: fd}, nil
}

func (c *i2cConn) tx(b []byte) error { _, err := unix.Write(c.fd, b); return err }
func (c *i2cConn) close() error      { return unix.Close(c.fd) }
