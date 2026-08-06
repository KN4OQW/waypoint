package dmrdata

import "errors"

// ErrShortBurst is returned when a slice is not a whole 33-byte burst.
var ErrShortBurst = errors.New("dmrdata: burst must be 33 bytes")

// ErrShortPayload is returned when a payload is not the 12 bytes one burst carries.
var ErrShortPayload = errors.New("dmrdata: payload must be 12 bytes")

// BuildBurst assembles a complete 264-bit burst: the BPTC-coded payload, the
// Golay-coded slot type, and the data sync.
//
// This is the only way to make a burst from nothing. bptcEncode alone leaves the
// other 68 bits as whatever the buffer held, and a zeroed buffer yields a burst
// MMDVM-Host re-transmits as data type 0 — see the package comment.
func BuildBurst(payload []byte, dt DataType, colorCode byte, duplex bool) (Burst, error) {
	if len(payload) != PayloadBytes {
		return Burst{}, ErrShortPayload
	}
	b := Burst{DataType: dt}
	bptcEncode(payload, b.Payload[:])
	setSlotType(b.Payload[:], colorCode, dt)
	addDataSync(b.Payload[:], duplex)
	return b, nil
}

// ReEncodeBurst rewrites a captured burst's payload, keeping every bit the
// payload does not own — sync, and the slot type's colour code.
//
// Re-encoding a capture is how a message gets edited without rebuilding it, and
// it is also the cheapest correctness check there is: ReEncodeBurst(b, Payload(b))
// must return b unchanged. That identity would have caught the encoder bug in
// minutes with no radio involved.
func ReEncodeBurst(burst []byte, payload []byte) ([BurstBytes]byte, error) {
	var out [BurstBytes]byte
	if len(burst) != BurstBytes {
		return out, ErrShortBurst
	}
	if len(payload) != PayloadBytes {
		return out, ErrShortPayload
	}
	copy(out[:], burst)
	bptcEncode(payload, out[:])
	return out, nil
}

// ParseBurst recovers a burst's payload and slot type. corrected counts the bit
// positions the FEC repaired; unfixable means it could not converge, so the
// payload is suspect even though it is still returned for the caller to CRC.
func ParseBurst(burst []byte) (payload []byte, dt DataType, colorCode byte, corrected int, unfixable bool, err error) {
	if len(burst) != BurstBytes {
		return nil, 0, 0, 0, false, ErrShortBurst
	}
	payload, corrected, unfixable = bptcDecode(burst)
	colorCode, dt = getSlotType(burst)
	return payload, dt, colorCode, corrected, unfixable, nil
}
