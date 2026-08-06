package dmrdata

import (
	"bytes"
	"testing"
)

// TestBurstWritesEvery264Bits is the guard the package exists for.
//
// A burst is 264 bits and they are owned by exactly three things: 196 by the BPTC
// payload, 20 by the Golay-coded slot type, 48 by the sync. Reading all three back
// and getting what was put in accounts for every bit — there is nowhere else for
// one to hide. If a future change stops writing any of them, one of these three
// assertions fails.
//
// The bug this pins cost two days on the bench: an encoder that wrote the payload
// into a zeroed buffer, leaving slot type 0 and no sync. MMDVM-Host read that slot
// type back and re-transmitted the burst as data type 0, so the message went out,
// the radio's LED lit, and nothing was ever displayed.
func TestBurstWritesEvery264Bits(t *testing.T) {
	payload := []byte{0x02, 0x44, 0x30, 0x86, 0xAA, 0x30, 0x86, 0xAA, 0x84, 0x00, 0x5E, 0xEB}

	for _, tc := range []struct {
		name   string
		dt     DataType
		cc     byte
		duplex bool
	}{
		{"csbk, base station", DataTypeCSBK, 1, true},
		{"data header, mobile station", DataTypeDataHeader, 1, false},
		{"rate-1/2, colour code 15", DataTypeRate12, 15, true},
		{"rate-1/2, colour code 0", DataTypeRate12, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := BuildBurst(payload, tc.dt, tc.cc, tc.duplex)
			if err != nil {
				t.Fatalf("BuildBurst: %v", err)
			}
			got, dt, cc, _, unfixable, err := ParseBurst(b.Payload[:])
			if err != nil {
				t.Fatalf("ParseBurst: %v", err)
			}
			if unfixable {
				t.Error("the FEC could not converge on a burst this package just built")
			}
			// 196 bits.
			if !bytes.Equal(got, payload) {
				t.Errorf("payload = %x, want %x", got, payload)
			}
			// 20 bits. The data type is the one that matters: MMDVM-Host reads it
			// back off the wire and re-transmits accordingly.
			if dt != tc.dt {
				t.Errorf("data type = %v, want %v", dt, tc.dt)
			}
			if cc != tc.cc {
				t.Errorf("colour code = %d, want %d", cc, tc.cc)
			}
			// 48 bits.
			if !hasDataSync(b.Payload[:]) {
				t.Errorf("no data sync in %x", b.Payload)
			}
		})
	}
}

// The same three assertions against a burst built the broken way, so the test
// above is known to be capable of failing. A test for a historical bug that
// cannot detect the historical bug is decoration.
func TestBPTCEncodeAloneLeavesABurstUnusable(t *testing.T) {
	payload := []byte{0x02, 0x44, 0x30, 0x86, 0xAA, 0x30, 0x86, 0xAA, 0x84, 0x00, 0x5E, 0xEB}
	var broken [BurstBytes]byte
	bptcEncode(payload, broken[:]) // what the harness did until 2026-08-06

	if _, dt := getSlotType(broken[:]); dt == DataTypeRate12 {
		t.Error("a zeroed buffer somehow carries the right data type; the test is not testing anything")
	}
	if hasDataSync(broken[:]) {
		t.Error("a zeroed buffer somehow carries a sync pattern")
	}
	// The payload itself is fine — which is exactly why this was so hard to see.
	// Every debugging tool that decoded the payload showed a perfect message.
	got, _, _ := bptcDecode(broken[:])
	if !bytes.Equal(got, payload) {
		t.Errorf("payload = %x, want %x", got, payload)
	}
}

// TestReEncodeIsIdentity is the check that would have found the bug in minutes,
// with no radio, no hotspot and no BrandMeister: decode a burst, re-encode it, and
// require the bytes back. It failed for every re-encoded capture at the time and
// nobody ran it.
func TestReEncodeIsIdentity(t *testing.T) {
	for _, name := range allFixtures {
		t.Run(name, func(t *testing.T) {
			for i, b := range loadFixture(t, name) {
				payload, _, _, _, unfixable, err := ParseBurst(b.Payload)
				if err != nil {
					t.Fatalf("burst %d: ParseBurst: %v", i, err)
				}
				if unfixable {
					t.Errorf("burst %d: FEC did not converge on a recorded burst", i)
				}
				out, err := ReEncodeBurst(b.Payload, payload)
				if err != nil {
					t.Fatalf("burst %d: ReEncodeBurst: %v", i, err)
				}
				if !bytes.Equal(out[:], b.Payload) {
					t.Errorf("burst %d: re-encode differs\n got %x\nwant %x", i, out, b.Payload)
				}
			}
		})
	}
}

// A recorded burst's slot type must agree with the data type its network frame
// declared. Where it does not, the transmitter built the burst wrong — which is
// what capture-nosync.txt records, so it is excluded and checked separately.
func TestFixtureSlotTypesMatchTheirFrames(t *testing.T) {
	for _, name := range allFixtures {
		if name == "capture-nosync.txt" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for i, b := range loadFixture(t, name) {
				_, dt := getSlotType(b.Payload)
				if dt != b.DataType {
					t.Errorf("burst %d: slot type says %v, frame said %v", i, dt, b.DataType)
				}
				if !hasDataSync(b.Payload) {
					t.Errorf("burst %d: no data sync", i)
				}
			}
		})
	}
}

// The broken fixture, checked for the shape of its brokenness rather than merely
// failing to parse: every burst claims a data type in its frame and carries slot
// type 0 with no sync.
func TestNoSyncFixtureIsTheHistoricalFailure(t *testing.T) {
	for i, b := range loadFixture(t, "capture-nosync.txt") {
		if b.DataType == 0 {
			t.Fatalf("burst %d: fixture frame declares data type 0; it should declare a real one", i)
		}
		if _, dt := getSlotType(b.Payload); dt != 0 {
			t.Errorf("burst %d: slot type = %v, want 0", i, dt)
		}
		if hasAnySync(b.Payload) {
			t.Errorf("burst %d: carries a sync pattern; the fixture is not the broken artefact", i)
		}
	}
}

// Single-bit errors are what the two Hamming codes exist to fix. Every one of the
// 264 bit positions gets flipped in turn; the payload has to survive all of them.
func TestBPTCCorrectsAnySingleBitError(t *testing.T) {
	payload := []byte{0x45, 0x00, 0x00, 0x2A, 0x00, 0x02, 0x00, 0x00, 0x01, 0x11, 0x94, 0x0D}
	b, err := BuildBurst(payload, DataTypeRate12, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	for bit := 0; bit < BurstBytes*8; bit++ {
		var damaged [BurstBytes]byte
		copy(damaged[:], b.Payload[:])
		damaged[bit/8] ^= bitMaskBE[bit%8]

		got, _, _ := bptcDecode(damaged[:])
		if !bytes.Equal(got, payload) {
			t.Fatalf("bit %d: payload = %x, want %x", bit, got, payload)
		}
	}
}

func TestBurstLengthErrors(t *testing.T) {
	if _, err := BuildBurst(make([]byte, 11), DataTypeRate12, 1, true); err != ErrShortPayload {
		t.Errorf("BuildBurst(11 bytes) = %v, want ErrShortPayload", err)
	}
	if _, err := ReEncodeBurst(make([]byte, 32), make([]byte, PayloadBytes)); err != ErrShortBurst {
		t.Errorf("ReEncodeBurst(32 bytes) = %v, want ErrShortBurst", err)
	}
	if _, _, _, _, _, err := ParseBurst(nil); err != ErrShortBurst {
		t.Errorf("ParseBurst(nil) = %v, want ErrShortBurst", err)
	}
}

// The slot type has to survive its own Golay code over the whole 8-bit space, or
// a colour code the node happens to use would corrupt the data type beside it.
func TestSlotTypeRoundTripsEveryValue(t *testing.T) {
	for cc := 0; cc < 16; cc++ {
		for dt := 0; dt < 16; dt++ {
			var burst [BurstBytes]byte
			setSlotType(burst[:], byte(cc), DataType(dt))
			gotCC, gotDT := getSlotType(burst[:])
			if gotCC != byte(cc) || gotDT != DataType(dt) {
				t.Fatalf("cc=%d dt=%d round-tripped as cc=%d dt=%d", cc, dt, gotCC, gotDT)
			}
		}
	}
}
