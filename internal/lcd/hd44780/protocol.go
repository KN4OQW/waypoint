// Package hd44780 drives an HD44780 character LCD over a PCF8574 I2C backpack —
// the real device behind internal/lcd.LCDDevice (docs/design/lcd.md §7). The
// wire protocol (nibble packing, init sequence, DDRAM addressing) is pure and
// unit-tested here; the I2C file-descriptor I/O is a thin separate layer.
//
// HARDWARE-GATED: the byte protocol below is transcribed from the HD44780 and
// PCF8574 datasheets and unit-tested for exact output, but it has NOT been
// validated against a physical panel in CI — that requires a 20x4 LCD on a
// PCF8574 backpack on the test box. Treat on-hardware behaviour as unverified
// until then.
package hd44780

// PCF8574 → HD44780 pin mapping used by the ubiquitous "LCD1602/2004 I2C"
// backpacks: the low nibble carries the control lines, the high nibble the 4-bit
// data bus (D4..D7).
const (
	pinRS byte = 0x01 // register select: 0 = command, 1 = data
	pinRW byte = 0x02 // read/write: always 0 (write-only) here
	pinEN byte = 0x04 // enable strobe: the chip latches on its falling edge
	pinBL byte = 0x08 // backlight (always on in v1)
)

// encode packs one command/data byte into the four PCF8574 writes that clock it
// into the HD44780 in 4-bit mode: high nibble then low nibble, each sent as a
// pair (enable high to set up, enable low to latch). rs selects the data vs
// command register; bl holds the backlight state across all four writes.
func encode(b byte, rs, bl bool) []byte {
	var ctrl byte
	if rs {
		ctrl |= pinRS
	}
	if bl {
		ctrl |= pinBL
	}
	hi := (b & 0xF0) | ctrl
	lo := ((b << 4) & 0xF0) | ctrl
	return []byte{hi | pinEN, hi, lo | pinEN, lo}
}

// nibbleRaw is a single high-nibble write, used only by the 4-bit init handshake
// (before the controller is in 4-bit mode, only the high nibble is meaningful).
// hi already carries the nibble in its high bits (e.g. 0x30, 0x20).
func nibbleRaw(hi byte, bl bool) []byte {
	b := hi
	if bl {
		b |= pinBL
	}
	return []byte{b | pinEN, b}
}

// HD44780 instruction opcodes used by the init sequence and rendering.
const (
	cmdClear       byte = 0x01
	cmdEntryMode   byte = 0x06 // increment cursor, no display shift
	cmdDisplayOff  byte = 0x08
	cmdDisplayOn   byte = 0x0C // display on, cursor off, blink off
	cmdFunctionSet byte = 0x28 // 4-bit bus, 2-line mode (also drives 4-line panels), 5x8 font
	cmdSetDDRAM    byte = 0x80 // OR with the target address
)

// rowOffsets returns the DDRAM base address of each row. On HD44780 panels line 1
// and line 2 live at 0x00 and 0x40; four-line panels wrap line 3 onto line 1 and
// line 4 onto line 2 offset by the column count — hence the interleaved
// 0x00,0x40,cols,0x40+cols order (0x14/0x54 on a 20-wide panel).
func rowOffsets(rows, cols int) []byte {
	if rows >= 4 {
		return []byte{0x00, 0x40, byte(cols), byte(0x40 + cols)}
	}
	return []byte{0x00, 0x40}
}
