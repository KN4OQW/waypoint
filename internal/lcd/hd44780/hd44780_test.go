package hd44780

import (
	"bytes"
	"reflect"
	"testing"
	"time"
)

// encode packs a byte into four PCF8574 writes (hi nibble E-pair, lo nibble
// E-pair) with the right control bits. Values are hand-computed from the pin map.
func TestEncode(t *testing.T) {
	cases := []struct {
		name   string
		b      byte
		rs, bl bool
		want   []byte
	}{
		// 'H' = 0x48 as data, backlight on: ctrl = RS|BL = 0x09.
		{"data-H", 0x48, true, true, []byte{0x4D, 0x49, 0x8D, 0x89}},
		// Clear (0x01) as command, backlight on: ctrl = BL = 0x08.
		{"cmd-clear", cmdClear, false, true, []byte{0x0C, 0x08, 0x1C, 0x18}},
		// Set DDRAM to 0x00 (0x80): command, backlight on.
		{"cmd-ddram0", cmdSetDDRAM, false, true, []byte{0x8C, 0x88, 0x0C, 0x08}},
		// Backlight off drops bit 3.
		{"data-H-nobl", 0x48, true, false, []byte{0x45, 0x41, 0x85, 0x81}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := encode(c.b, c.rs, c.bl); !bytes.Equal(got, c.want) {
				t.Errorf("encode(%#02x,%v,%v) = % x, want % x", c.b, c.rs, c.bl, got, c.want)
			}
		})
	}
}

func TestNibbleRaw(t *testing.T) {
	if got := nibbleRaw(0x30, true); !bytes.Equal(got, []byte{0x3C, 0x38}) {
		t.Errorf("nibbleRaw(0x30,true) = % x, want 3c 38", got)
	}
	if got := nibbleRaw(0x20, false); !bytes.Equal(got, []byte{0x24, 0x20}) {
		t.Errorf("nibbleRaw(0x20,false) = % x, want 24 20", got)
	}
}

func TestRowOffsets(t *testing.T) {
	cases := []struct {
		rows, cols int
		want       []byte
	}{
		{2, 16, []byte{0x00, 0x40}},
		{2, 20, []byte{0x00, 0x40}},
		{4, 20, []byte{0x00, 0x40, 0x14, 0x54}},
		{4, 16, []byte{0x00, 0x40, 0x10, 0x50}},
	}
	for _, c := range cases {
		if got := rowOffsets(c.rows, c.cols); !bytes.Equal(got, c.want) {
			t.Errorf("rowOffsets(%d,%d) = % x, want % x", c.rows, c.cols, got, c.want)
		}
	}
}

func TestParseAddr(t *testing.T) {
	ok := map[string]uint8{"0x27": 0x27, "0X3F": 0x3f, "27": 0x27, " 0x20 ": 0x20}
	for in, want := range ok {
		if got, err := parseAddr(in); err != nil || got != want {
			t.Errorf("parseAddr(%q) = %#x,%v; want %#x", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "zz", "0xGG", "0x100", "300"} {
		if _, err := parseAddr(bad); err == nil {
			t.Errorf("parseAddr(%q) should error", bad)
		}
	}
}

// fakeConn captures the byte stream so device output can be asserted.
type fakeConn struct {
	buf    bytes.Buffer
	closed bool
}

func (f *fakeConn) tx(b []byte) error { f.buf.Write(b); return nil }
func (f *fakeConn) close() error      { f.closed = true; return nil }

func newTestDevice() (*Device, *fakeConn) {
	c := &fakeConn{}
	return &Device{c: c, sleep: func(time.Duration) {}}, c
}

// Init emits the datasheet 4-bit handshake: three 0x3 nibbles, one 0x2, then
// function-set and finishing with display-on.
func TestInitSequence(t *testing.T) {
	d, c := newTestDevice()
	if err := d.Init(4, 20); err != nil {
		t.Fatal(err)
	}
	got := c.buf.Bytes()

	wantPrefix := bytes.Join([][]byte{
		nibbleRaw(0x30, true), nibbleRaw(0x30, true), nibbleRaw(0x30, true),
		nibbleRaw(0x20, true),
		encode(cmdFunctionSet, false, true),
	}, nil)
	if !bytes.HasPrefix(got, wantPrefix) {
		t.Errorf("init prefix = % x\nwant       % x", got[:len(wantPrefix)], wantPrefix)
	}
	if wantEnd := encode(cmdDisplayOn, false, true); !bytes.HasSuffix(got, wantEnd) {
		t.Errorf("init should end with display-on % x, got tail % x", wantEnd, got[len(got)-4:])
	}
	// The clear instruction must appear in the init stream.
	if !bytes.Contains(got, encode(cmdClear, false, true)) {
		t.Error("init stream missing clear")
	}
}

// WriteLine addresses the row's DDRAM base then streams the characters, all in
// one transaction. Row 2 of a 20-wide 4-line panel is DDRAM 0x14.
func TestWriteLine(t *testing.T) {
	d, c := newTestDevice()
	if err := d.Init(4, 20); err != nil {
		t.Fatal(err)
	}
	c.buf.Reset()
	if err := d.WriteLine(2, "Hi"); err != nil {
		t.Fatal(err)
	}
	want := bytes.Join([][]byte{
		encode(cmdSetDDRAM|0x14, false, true),
		encode('H', true, true),
		encode('i', true, true),
	}, nil)
	if got := c.buf.Bytes(); !bytes.Equal(got, want) {
		t.Errorf("WriteLine(2,\"Hi\") = % x\nwant                % x", got, want)
	}

	// A row beyond the panel is a no-op (no writes).
	c.buf.Reset()
	if err := d.WriteLine(9, "x"); err != nil {
		t.Fatal(err)
	}
	if c.buf.Len() != 0 {
		t.Errorf("out-of-range WriteLine wrote % x", c.buf.Bytes())
	}
}

func TestClearAndClose(t *testing.T) {
	d, c := newTestDevice()
	if err := d.Clear(); err != nil {
		t.Fatal(err)
	}
	if got := c.buf.Bytes(); !bytes.Equal(got, encode(cmdClear, false, true)) {
		t.Errorf("Clear wrote % x", got)
	}
	if err := d.Close(); err != nil || !c.closed {
		t.Errorf("Close err=%v closed=%v", err, c.closed)
	}
}

// rowOffsets returns a fresh slice each call (no aliasing surprises for callers).
func TestRowOffsetsDistinct(t *testing.T) {
	a, b := rowOffsets(4, 20), rowOffsets(4, 20)
	a[0] = 0xFF
	if reflect.DeepEqual(a, b) {
		t.Error("rowOffsets returned an aliased slice")
	}
}
