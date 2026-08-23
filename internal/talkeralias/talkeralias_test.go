package talkeralias

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestRoundTrip is the canonical check: everything that goes in comes back.
//
// The table is chosen around the boundaries the format actually has rather than
// around round numbers — the ceilings, one past them, and the strings a real node
// would send.
func TestRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alias string
		f     Format
		want  string // "" means unchanged
	}{
		{"callsign only", "KN4OQW", Format7Bit, ""},
		{"callsign and name", "KN4OQW Clint Chance", Format7Bit, ""},
		{"name only", "Clint Chance", Format7Bit, ""},
		{"one character", "K", Format7Bit, ""},
		{"at the 7-bit ceiling", strings.Repeat("A", Max7Bit), Format7Bit, ""},
		{"over the 7-bit ceiling", strings.Repeat("A", Max7Bit+9), Format7Bit, strings.Repeat("A", Max7Bit)},
		{"iso-8859-1", "José Muñoz", FormatISO8859_1, ""},
		{"at the 8-bit ceiling", strings.Repeat("b", Max8Bit), FormatISO8859_1, ""},
		{"over the 8-bit ceiling", strings.Repeat("b", Max8Bit+5), FormatISO8859_1, strings.Repeat("b", Max8Bit)},
		{"utf-8", "Škoda", FormatUTF8, ""},
		{"utf-16be", "KN4OQW", FormatUTF16BE, ""},
		{"at the utf-16 ceiling", strings.Repeat("c", MaxUTF16), FormatUTF16BE, ""},
		{"over the utf-16 ceiling", strings.Repeat("c", MaxUTF16+4), FormatUTF16BE, strings.Repeat("c", MaxUTF16)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == "" {
				want = tc.alias
			}
			frames, err := Encode(3180202, tc.alias, tc.f)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if len(frames) != 4 {
				t.Fatalf("got %d frames, want 4", len(frames))
			}
			id, got, gotF, err := Decode(frames)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if id != 3180202 {
				t.Errorf("id = %d, want 3180202", id)
			}
			if gotF != tc.f {
				t.Errorf("format = %d, want %d", gotF, tc.f)
			}
			if got != want {
				t.Errorf("round trip:\n got %q\nwant %q", got, want)
			}
		})
	}
}

// TestEncodeDecodeIsIdempotent is the prompt's canonical form stated literally:
// re-encoding what was decoded reproduces the same bytes. It catches an encoder
// and decoder that disagree about padding or about where the text begins, which a
// value-only comparison can miss.
func TestEncodeDecodeIsIdempotent(t *testing.T) {
	for _, f := range []Format{Format7Bit, FormatISO8859_1, FormatUTF8, FormatUTF16BE} {
		first, err := Encode(3180202, "KN4OQW Clint", f)
		if err != nil {
			t.Fatal(err)
		}
		id, alias, gotF, err := Decode(first)
		if err != nil {
			t.Fatal(err)
		}
		second, err := Encode(id, alias, gotF)
		if err != nil {
			t.Fatal(err)
		}
		for i := range first {
			if !bytes.Equal(first[i], second[i]) {
				t.Errorf("format %d block %d: encode(decode(x)) != x\n got %x\nwant %x", f, i, second[i], first[i])
			}
		}
	}
}

// TestFrameLayout pins the wire format against MMDVM-Host's writeTalkerAlias,
// which is what the fork's parser mirrors. A change here is a protocol change.
func TestFrameLayout(t *testing.T) {
	frames, err := Encode(0xAABBCC, "KN4OQW", Format7Bit)
	if err != nil {
		t.Fatal(err)
	}
	for i, fr := range frames {
		if len(fr) != 15 {
			t.Errorf("block %d is %d bytes, want 15", i, len(fr))
		}
		if string(fr[0:4]) != "DMRA" {
			t.Errorf("block %d magic = %q, want DMRA", i, fr[0:4])
		}
		// 24-bit big-endian id, as writeTalkerAlias writes it.
		if fr[4] != 0xAA || fr[5] != 0xBB || fr[6] != 0xCC {
			t.Errorf("block %d id bytes = %02X %02X %02X, want AA BB CC", i, fr[4], fr[5], fr[6])
		}
		if int(fr[7]) != i {
			t.Errorf("block %d carries type %d", i, fr[7])
		}
	}
}

// TestHeaderBits pins the header byte, including the part that is easy to get
// wrong: in 7-bit mode bit 0 is not padding, it is the first character's top bit.
func TestHeaderBits(t *testing.T) {
	frames, err := Encode(3180202, "KN4OQW", Format7Bit)
	if err != nil {
		t.Fatal(err)
	}
	h := frames[0][8]
	if got := Format(h >> 6); got != Format7Bit {
		t.Errorf("format bits = %d, want 0", got)
	}
	if got := int(h >> 1 & 0x1F); got != 6 {
		t.Errorf("length bits = %d, want 6", got)
	}
	// 'K' is 0x4B = 1001011; its top bit is 1, so bit 0 of the header must be set.
	if h&0x01 != 1 {
		t.Errorf("header bit 0 = 0; in 7-bit mode it carries the first character's "+
			"top bit, and 'K' (0x%02X) starts with a 1", 'K')
	}
}

// TestChooseFormat: the narrowest encoding that carries the string, and never
// UTF-16 automatically.
func TestChooseFormat(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Format
	}{
		{"KN4OQW", Format7Bit},
		{"KN4OQW Clint Chance", Format7Bit},
		{"José", FormatISO8859_1},
		{"Muñoz", FormatISO8859_1},
		{"Škoda", FormatUTF8},
		{"日本", FormatUTF8},
	} {
		if got := ChooseFormat(tc.in); got != tc.want {
			t.Errorf("ChooseFormat(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestTruncationNeverSplitsARune: a name cut mid-sequence decodes as a
// replacement glyph, which reads as a fault rather than as a long name.
func TestTruncationNeverSplitsARune(t *testing.T) {
	long := strings.Repeat("é", 40) // 2 bytes each in UTF-8
	frames, err := Encode(3180202, long, FormatUTF8)
	if err != nil {
		t.Fatal(err)
	}
	_, got, _, err := Decode(frames)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(long, got) {
		t.Errorf("truncated to %q, which is not a prefix of the input", got)
	}
	if strings.ContainsRune(got, '�') {
		t.Errorf("truncation split a rune: %q", got)
	}
	if len(got) > Max8Bit {
		t.Errorf("truncated to %d bytes, over the %d ceiling", len(got), Max8Bit)
	}
}

// TestNoAlias: nothing to say means no frames, and it is not an error the caller
// should log. A node whose phonebook has no row for a caller is the ordinary case.
func TestNoAlias(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n"} {
		if _, err := Encode(3180202, in, Format7Bit); !errors.Is(err, ErrNoAlias) {
			t.Errorf("Encode(%q) = %v, want ErrNoAlias", in, err)
		}
	}
}

// TestBadID: the id field is three bytes, and zero is not an issued DMR ID.
func TestBadID(t *testing.T) {
	for _, id := range []uint32{0, MaxID + 1, 0xFFFFFFFF} {
		if _, err := Encode(id, "KN4OQW", Format7Bit); !errors.Is(err, ErrBadID) {
			t.Errorf("Encode(id=%d) = %v, want ErrBadID", id, err)
		}
	}
	if _, err := Encode(MaxID, "KN4OQW", Format7Bit); err != nil {
		t.Errorf("Encode at the ceiling id: %v", err)
	}
}

// TestDecodeRejectsMalformedSets covers what a parser on the other end has to
// survive: short sets, duplicate blocks, mixed sources, and non-DMRA bytes.
func TestDecodeRejectsMalformedSets(t *testing.T) {
	good, err := Encode(3180202, "KN4OQW", Format7Bit)
	if err != nil {
		t.Fatal(err)
	}
	clone := func() [][]byte {
		out := make([][]byte, 4)
		for i, f := range good {
			out[i] = append([]byte(nil), f...)
		}
		return out
	}

	if _, _, _, err := Decode(good[:3]); !errors.Is(err, ErrIncomplete) {
		t.Errorf("three blocks = %v, want ErrIncomplete", err)
	}
	dup := clone()
	dup[3][7] = 0 // two headers, no block 3
	if _, _, _, err := Decode(dup); !errors.Is(err, ErrIncomplete) {
		t.Errorf("duplicate block = %v, want ErrIncomplete", err)
	}
	mixed := clone()
	mixed[2][6] ^= 0xFF // a different source id
	if _, _, _, err := Decode(mixed); !errors.Is(err, ErrIncomplete) {
		t.Errorf("mixed sources = %v, want ErrIncomplete", err)
	}
	bad := clone()
	copy(bad[1], "DMRD")
	if _, _, _, err := Decode(bad); !errors.Is(err, ErrBadFrame) {
		t.Errorf("wrong magic = %v, want ErrBadFrame", err)
	}
}

// TestBlocksAreOrderIndependent: the wire has no ordering guarantee and each
// frame names its own block, so a receiver must reassemble from any order.
func TestBlocksAreOrderIndependent(t *testing.T) {
	frames, err := Encode(3180202, "KN4OQW Clint Chance", Format7Bit)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := [][]byte{frames[3], frames[1], frames[0], frames[2]}
	_, got, _, err := Decode(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if got != "KN4OQW Clint Chance" {
		t.Errorf("out-of-order reassembly = %q", got)
	}
}
