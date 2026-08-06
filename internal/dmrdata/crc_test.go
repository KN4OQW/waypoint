package dmrdata

import (
	"encoding/hex"
	"testing"
)

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex in test: %v", err)
	}
	return b
}

// CRC-CCITT-16 against PDUs recorded off the air, not against values this package
// computed. A checksum test that checks a checksum against itself passes for any
// polynomial.
//
// Worked example, the first vector below. The PDU is a preamble CSBK from
// BrandMeister; its first ten octets are bd 00 80 14 30 86 aa 04 03 53 and the
// last two are the checksum, 31 f3. Running addCCITT16 over the ten with the CSBK
// mask must reproduce those two — and running it with the DATA HEADER mask must
// not, which is the entire reason the masks exist: a receiver that tries to read a
// CSBK as a data header fails the checksum instead of half-believing the fields.
func TestCCITT16AgainstRecordedPDUs(t *testing.T) {
	for _, tc := range []struct {
		name string
		pdu  string
		mask [2]byte
	}{
		{"preamble CSBK from BrandMeister", "bd0080143086aa04035331f3", csbkCRCMask},
		{"preamble CSBK from the radio", "bd0080083086aa3086aad7dd", csbkCRCMask},
		{"unconfirmed data header from the radio", "02443086aa3086aa84005eeb", dataHeaderCRCMask},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := mustHex(t, tc.pdu)
			got := append([]byte(nil), want...)
			got[10], got[11] = 0, 0
			addCCITT16(got, tc.mask)
			if got[10] != want[10] || got[11] != want[11] {
				t.Errorf("crc = %02x%02x, want %02x%02x", got[10], got[11], want[10], want[11])
			}
			if !checkCCITT16(want, tc.mask) {
				t.Error("checkCCITT16 rejected a PDU recorded off the air")
			}

			other := csbkCRCMask
			if tc.mask == csbkCRCMask {
				other = dataHeaderCRCMask
			}
			if checkCCITT16(want, other) {
				t.Error("the PDU also passes under the wrong mask; the masks are not separating anything")
			}
		})
	}
}

func TestCCITT16DetectsEverySingleBitError(t *testing.T) {
	pdu := mustHex(t, "02443086aa3086aa84005eeb")
	for bit := 0; bit < len(pdu)*8; bit++ {
		damaged := append([]byte(nil), pdu...)
		damaged[bit/8] ^= bitMaskBE[bit%8]
		if checkCCITT16(damaged, dataHeaderCRCMask) {
			t.Errorf("bit %d flipped and the checksum still passed", bit)
		}
	}
}

// The ITU CRC-32 is the one that guards the message body, and it is not a standard
// CRC-32: bytes are fed in pairwise-swapped, the polynomial is not reflected, the
// register starts at zero and is not inverted at the end. Each of those three is
// checked here by asserting the value that a real message carried — the only
// authority available, since no library computes this.
func TestITUCRC32AgainstRecordedBodies(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string // the whole block-aligned body, checksum included
		want uint32
	}{
		{
			// Radio -> network, "HELLO". 44 octets of datagram and padding, then
			// the checksum little-endian: 35 5e 8f ef.
			name: "radio-originated HELLO",
			body: "450000" + "2a00020000011194" + "0d0c3086aa0c3086aa1398139800163ec5" +
				"000d000a480045004c004c004f00000035" + "5e8fef",
			want: 0xEF8F5E35,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := mustHex(t, tc.body)
			if got := ituCRC32(body[:len(body)-4]); got != tc.want {
				t.Errorf("ituCRC32 = %08X, want %08X", got, tc.want)
			}
		})
	}
}

// A standard CRC-32 would be a plausible-looking wrong answer, so pin that this
// one is genuinely different: swapping a pair of bytes must change the result. If
// it did not, the pairwise feed would be doing nothing and the implementation
// could quietly be replaced by hash/crc32 with the wrong polynomial.
func TestITUCRC32IsByteOrderSensitive(t *testing.T) {
	a := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	b := []byte{0x02, 0x01, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	if ituCRC32(a) == ituCRC32(b) {
		t.Error("swapping the first pair did not change the checksum")
	}
	if ituCRC32(nil) != 0 {
		t.Error("the empty message should leave the register at its zero init")
	}
}
