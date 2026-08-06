package dmrdata

import (
	"bytes"
	"errors"
	"testing"
)

func TestDataHeaderRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    DataHeader
	}{
		{"individual, one block", DataHeader{Src: 3180202, Dst: 262995, SAP: SAPIPData, Blocks: 1}},
		{"group", DataHeader{Group: true, Src: 3180202, Dst: 91, SAP: SAPIPData, Blocks: 4, Pad: 8}},
		{"the id extremes", DataHeader{Src: 1, Dst: 0xFFFFFF, SAP: SAPIPData, Blocks: 127}},
		// Pad is a 5-bit value split across two octets: bit 4 lives in octet 0 and
		// the low nibble in octet 1. A value with both parts set proves the split
		// is symmetric between build and parse rather than merely self-consistent
		// for small numbers.
		{"pad spanning both of its fields", DataHeader{Src: 7, Dst: 8, SAP: SAPIPData, Blocks: 3, Pad: 0x1B}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdu := buildDataHeader(tc.h)
			if len(pdu) != 12 {
				t.Fatalf("header is %d bytes, want 12", len(pdu))
			}
			got, err := parseDataHeader(pdu)
			if err != nil {
				t.Fatalf("parseDataHeader: %v", err)
			}
			if got != tc.h {
				t.Errorf("round trip = %+v, want %+v", got, tc.h)
			}
		})
	}
}

// The recorded header, parsed field by field against what the bench saw: a
// 4-block unconfirmed transfer from 3180202 to itself with two padding octets.
func TestParseRecordedDataHeader(t *testing.T) {
	got, err := parseDataHeader(mustHex(t, "02423086aa3086aa84000463"))
	if err != nil {
		t.Fatalf("parseDataHeader: %v", err)
	}
	want := DataHeader{Src: 3180202, Dst: 3180202, SAP: SAPIPData, Blocks: 4, Pad: 2}
	if got != want {
		t.Errorf("= %+v, want %+v", got, want)
	}
}

func TestParseDataHeaderRejections(t *testing.T) {
	good := buildDataHeader(DataHeader{Src: 1, Dst: 2, SAP: SAPIPData, Blocks: 1})

	t.Run("a flipped bit fails the checksum", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		bad[3] ^= 0x01
		if _, err := parseDataHeader(bad); !errors.Is(err, ErrBadCRC) {
			t.Errorf("err = %v, want ErrBadCRC", err)
		}
	})

	t.Run("a confirmed-data header is declined, not misread", func(t *testing.T) {
		// Confirmed data needs per-block CRC-9 and an ARQ exchange. Reading one as
		// unconfirmed would produce a message whose blocks are silently wrong.
		bad := append([]byte(nil), good...)
		bad[0] = bad[0]&0xF0 | byte(DPFConfirmedData)
		addCCITT16(bad, dataHeaderCRCMask)
		if _, err := parseDataHeader(bad); !errors.Is(err, ErrUnsupportedPDU) {
			t.Errorf("err = %v, want ErrUnsupportedPDU", err)
		}
	})

	t.Run("a short PDU is not read at all", func(t *testing.T) {
		if _, err := parseDataHeader(good[:11]); !errors.Is(err, ErrShortPayload) {
			t.Errorf("err = %v, want ErrShortPayload", err)
		}
	})
}

func TestPreambleCSBK(t *testing.T) {
	// The recorded BrandMeister preamble, rebuilt from its fields. Matching it
	// byte for byte says this package's preamble is the one BrandMeister sends.
	want := mustHex(t, "bd0080143086aa04035331f3")
	got := buildPreambleCSBK(262995, 3180202, 20, false)
	if !bytes.Equal(got, want) {
		t.Errorf("preamble = %x, want %x (BrandMeister's own)", got, want)
	}
	if !isPreambleCSBK(got) {
		t.Error("isPreambleCSBK rejected a preamble this package built")
	}
	if isPreambleCSBK(buildDataHeader(DataHeader{Src: 1, Dst: 2, SAP: SAPIPData, Blocks: 1})) {
		t.Error("a data header was accepted as a preamble CSBK")
	}
}

func TestPreambleCSBKGroupBit(t *testing.T) {
	individual := buildPreambleCSBK(1, 2, 3, false)
	group := buildPreambleCSBK(1, 2, 3, true)
	if individual[2] != 0x80 {
		t.Errorf("individual octet 2 = %02x, want 80", individual[2])
	}
	if group[2] != 0xC0 {
		t.Errorf("group octet 2 = %02x, want c0", group[2])
	}
}
