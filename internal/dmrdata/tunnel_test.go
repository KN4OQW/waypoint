package dmrdata

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestBodyRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		group bool
	}{
		{"empty", "", false},
		{"one character", "A", false},
		{"a short message", "hello", false},
		{"exactly one block of text", "123456", false},
		{"a talkgroup message", "net starts at 8pm", true},
		{"non-ASCII inside the BMP", "café 73 — MHz", false},
		{"outside the BMP, so surrogate pairs", "ok 👍", false},
		{"the longest message allowed", strings.Repeat("A", MaxTextUnits), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, blocks, pad, err := buildBody(3180202, 262995, tc.text, 42, tc.group)
			if err != nil {
				t.Fatalf("buildBody: %v", err)
			}
			if len(body) != blocks*blockOctets {
				t.Fatalf("body is %d octets, not %d whole blocks", len(body), blocks)
			}
			if pad < 0 || pad >= blockOctets {
				t.Fatalf("pad = %d, want 0..%d", pad, blockOctets-1)
			}
			msg, err := parseBody(body)
			if err != nil {
				t.Fatalf("parseBody: %v", err)
			}
			if msg.Text != tc.text {
				t.Errorf("text = %q, want %q", msg.Text, tc.text)
			}
			if msg.Src != 3180202 || msg.Dst != 262995 {
				t.Errorf("addresses = %d -> %d, want 3180202 -> 262995", msg.Src, msg.Dst)
			}
			if msg.Group != tc.group {
				t.Errorf("group = %v, want %v", msg.Group, tc.group)
			}
			if msg.Seq != 42 {
				t.Errorf("seq = %d, want 42", msg.Seq)
			}
		})
	}
}

// A surrogate pair costs two units, so the rune limit and the unit limit are not
// the same number. The bound that matters is the one the length field imposes.
func TestTextLengthLimit(t *testing.T) {
	if _, _, _, err := buildBody(1, 2, strings.Repeat("A", MaxTextUnits+1), 0, false); !errors.Is(err, ErrTextTooLong) {
		t.Errorf("one unit over: err = %v, want ErrTextTooLong", err)
	}
	// Sixty-two emoji are 124 units — over the limit despite being only 62 runes.
	if _, _, _, err := buildBody(1, 2, strings.Repeat("👍", 62), 0, false); !errors.Is(err, ErrTextTooLong) {
		t.Errorf("62 surrogate pairs: err = %v, want ErrTextTooLong", err)
	}
	if _, _, _, err := buildBody(1, 2, strings.Repeat("👍", 61), 0, false); err != nil {
		t.Errorf("61 surrogate pairs (122 units): err = %v, want nil", err)
	}
}

// Invalid UTF-8 is refused rather than quietly turned into replacement characters.
// Go's []rune conversion substitutes U+FFFD for every bad byte, so without the
// check the radio would display a row of question marks and the caller would be
// told the message went fine. FuzzBuildMessage found this.
func TestInvalidUTF8IsRefused(t *testing.T) {
	for _, bad := range []string{"\xc3", "hello \xff world", "\xed\xa0\x80"} {
		if _, _, _, err := buildBody(1, 2, bad, 0, false); !errors.Is(err, ErrInvalidText) {
			t.Errorf("%q: err = %v, want ErrInvalidText", bad, err)
		}
	}
}

// MaxTextUnits exists because the TMS length field is written as a single octet.
// Prove that at the limit it is still a single octet, so the bound is derived from
// the format and not from taste.
func TestMaxTextUnitsKeepsTheLengthFieldInRange(t *testing.T) {
	body, _, _, err := buildBody(1, 2, strings.Repeat("A", MaxTextUnits), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	udpLen := int(binary.BigEndian.Uint16(body[24:]))
	if v := udpLen - 10; v > 0xFF {
		t.Errorf("TMS length field would be %d, which does not fit an octet", v)
	}
	if body[28] != 0 {
		t.Errorf("the TMS length high octet is %02x; the proven construction leaves it zero", body[28])
	}
}

// The ETSI framing, checked against a real recorded datagram: no TMS header at
// all, so the CRLF starts immediately after the UDP header. A receiver that
// assumed the TMS header would read six octets of text that are not there and
// stop six octets early.
//
// The TMS framing is covered end to end by TestReassembleRecordedCaptures, which
// runs a whole recorded BrandMeister message through the reassembler.
func TestParseRecordedETSIBody(t *testing.T) {
	got, err := parseBody(mustHex(t, "450000"+"2a00020000011194"+
		"0d0c3086aa0c3086aa1398139800163ec5"+"000d000a480045004c004c004f00000035"+"5e8fef"))
	if err != nil {
		t.Fatalf("parseBody: %v", err)
	}
	want := Message{Src: 3180202, Dst: 3180202, Seq: 2, Text: "HELLO"}
	if got != want {
		t.Errorf("= %+v\nwant %+v", got, want)
	}
}

func TestParseBodyRejections(t *testing.T) {
	good, _, _, err := buildBody(3180202, 262995, "hello", 1, false)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a flipped bit fails the CRC", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		bad[40] ^= 0x01
		if _, err := parseBody(bad); !errors.Is(err, ErrBadCRC) {
			t.Errorf("err = %v, want ErrBadCRC", err)
		}
	})

	t.Run("a port nobody speaks is declined", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		binary.BigEndian.PutUint16(bad[22:], 1234)
		binary.LittleEndian.PutUint32(bad[len(bad)-4:], ituCRC32(bad[:len(bad)-4]))
		if _, err := parseBody(bad); !errors.Is(err, ErrMalformedBody) {
			t.Errorf("err = %v, want ErrMalformedBody", err)
		}
	})

	// A length field that overstates the datagram must be refused rather than
	// used to index past what actually arrived. This is the one that turns a
	// malformed packet from somebody else's radio into a crash.
	t.Run("a length field longer than the blocks that carried it", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		binary.BigEndian.PutUint16(bad[24:], 0xFFFF)
		binary.LittleEndian.PutUint32(bad[len(bad)-4:], ituCRC32(bad[:len(bad)-4]))
		if _, err := parseBody(bad); !errors.Is(err, ErrMalformedBody) {
			t.Errorf("err = %v, want ErrMalformedBody", err)
		}
	})

	t.Run("not IPv4, not UDP", func(t *testing.T) {
		for _, mut := range []struct {
			name string
			at   int
			to   byte
		}{{"version 6", 0, 0x65}, {"protocol TCP", 9, 6}} {
			bad := append([]byte(nil), good...)
			bad[mut.at] = mut.to
			binary.LittleEndian.PutUint32(bad[len(bad)-4:], ituCRC32(bad[:len(bad)-4]))
			if _, err := parseBody(bad); !errors.Is(err, ErrMalformedBody) {
				t.Errorf("%s: err = %v, want ErrMalformedBody", mut.name, err)
			}
		}
	})

	t.Run("shorter than a datagram, or not block-aligned", func(t *testing.T) {
		for _, b := range [][]byte{nil, make([]byte, 12), good[:len(good)-1]} {
			if _, err := parseBody(b); !errors.Is(err, ErrMalformedBody) {
				t.Errorf("len %d: err = %v, want ErrMalformedBody", len(b), err)
			}
		}
	})
}

// The internet checksum against a recorded datagram, which is the only way to know
// the pseudo-header is laid out the way the radio lays it out.
func TestChecksumsMatchARecordedDatagram(t *testing.T) {
	body := mustHex(t, "450000"+"2a00020000011194"+"0d0c3086aa0c3086aa1398139800163ec5"+
		"000d000a480045004c004c004f00000035"+"5e8fef")

	ip := append([]byte(nil), body[:ipHeaderLen]...)
	want := binary.BigEndian.Uint16(ip[10:])
	ip[10], ip[11] = 0, 0
	if got := onesComplementSum(ip); got != want {
		t.Errorf("IP header checksum = %04x, want %04x", got, want)
	}

	udpLen := int(binary.BigEndian.Uint16(body[24:]))
	zeroed := append([]byte(nil), body...)
	wantUDP := binary.BigEndian.Uint16(body[26:])
	zeroed[26], zeroed[27] = 0, 0
	if got := udpChecksum(zeroed, udpLen); got != wantUDP {
		t.Errorf("UDP checksum = %04x, want %04x", got, wantUDP)
	}
}

// The reference implementation folds the checksum's carries once. Once is enough
// only while the carry stays inside 16 bits. Folding to a fixed point is what a
// correct implementation does; check it agrees on a sum that needs two folds.
func TestOnesComplementSumFoldsRepeatedly(t *testing.T) {
	data := make([]byte, 512)
	for i := range data {
		data[i] = 0xFF
	}
	// 256 words of 0xFFFF sum to 0xFF_FF00; one fold gives 0xFF00+0xFF = 0xFFFF,
	// which needs no second fold but does need the first to be applied to the
	// folded value rather than the raw one.
	if got := onesComplementSum(data); got != 0 {
		t.Errorf("sum = %04x, want 0000", got)
	}
	// An odd trailing octet is padded high, not low.
	if onesComplementSum([]byte{0x12}) != onesComplementSum([]byte{0x12, 0x00}) {
		t.Error("a trailing octet was not padded into the high half of its word")
	}
}
