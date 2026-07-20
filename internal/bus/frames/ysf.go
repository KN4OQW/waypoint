package frames

import "strings"

// YSF System Fusion DN (V/D mode 2) network frame handling. The bus rides the
// MMDVM-Host YSF loopback, which speaks the "YSFD" reflector protocol: a 155-byte
// UDP frame wrapping a 120-byte YSF frame (5-byte sync + 25-byte FICH + five
// 18-byte VD2 subblocks). Each subblock's VCH (bits 40-143) carries one AMBE
// codeword, whitened and bit-interleaved; the DCH (bits 0-39) carries addressing
// which — like DMR — the bus reads from the "YSFD" header instead, so the codec
// bits are never touched.
//
// Ground truth: "YSFD" layout is YSFClients YSFNetwork.cpp; the VD2 subblock VCH
// interleave/whitening + majority-vote FEC is MMDVM_CM ModeConv.cpp putYSF/
// putAMBE2YSF; the FICH is YSFFICH.cpp (see ysffich.go).

const (
	ysfdLen        = 155
	ysfFrameOff    = 35 // 120-byte YSF frame starts here
	ysfGwCallOff   = 4
	ysfSrcCallOff  = 14
	ysfDstCallOff  = 24
	ysfCounterOff  = 34
	ysfCallLen     = 10
	ysfVCHPerFrame = 5

	ysfVCHRegionOff = 30  // sync(5)+FICH(25); VCH region is the trailing 90 bytes
	ysfVCHBitStart  = 40  // DCH occupies bits 0-39 of each 144-bit subblock
	ysfSubblockBits = 144 // 18 bytes
	ysfVCHBits      = 104 // 13 bytes
)

// ParseYSF parses a "YSFD" frame. A communications frame yields 5 AMBE codewords;
// a header/terminator yields none. A non-VD-mode-2 (e.g. VW full-rate) or
// CRC-invalid FICH returns ErrUnsupported/ErrBadFrame. Never panics.
func ParseYSF(buf []byte) (Frame, error) {
	if len(buf) < ysfdLen {
		return Frame{}, ErrShort
	}
	if string(buf[0:4]) != "YSFD" {
		return Frame{}, ErrBadMagic
	}
	frame := buf[ysfFrameOff : ysfFrameOff+ysfFrameLen]

	fi, ok := fichDecode(frame)
	if !ok {
		return Frame{}, ErrBadFrame // FICH CRC failed
	}
	if fi.dt() != ysfDTVDMode2 {
		return Frame{}, ErrUnsupported // only DN (VD mode 2) is reframe-tier
	}

	f := Frame{Mode: ModeYSF}
	f.SrcCallsign = trimCallsign(buf[ysfSrcCallOff : ysfSrcCallOff+ysfCallLen])
	f.DstCallsign = trimCallsign(buf[ysfDstCallOff : ysfDstCallOff+ysfCallLen])
	f.Stream.Seq = fi.fn()

	switch fi.fi() {
	case ysfFIHeader:
		f.Kind = KindHeader
		return f, nil
	case ysfFITerminator:
		f.Kind = KindTerminator
		return f, nil
	default:
		f.Kind = KindVoice
	}

	region := frame[ysfVCHRegionOff:]
	f.AMBE = make([][]byte, ysfVCHPerFrame)
	for j := 0; j < ysfVCHPerFrame; j++ {
		off := uint(ysfSubblockBits*j + ysfVCHBitStart)
		f.AMBE[j] = ysfVCHToCanonical(region, off)
	}
	return f, nil
}

// ConstructYSF builds a "YSFD" frame. A voice frame must carry exactly 5 AMBE
// codewords. The source callsign is taken from the frame, or resolved from its id
// via the shared lookup when it arrived id-addressed (e.g. from DMR/NXDN).
func ConstructYSF(f Frame, p Params, r Resolver) ([]byte, error) {
	res := resolverOrNull(r)
	buf := make([]byte, ysfdLen)
	copy(buf, "YSFD")

	src := f.SrcCallsign
	if src == "" && f.SrcID != 0 {
		src = res.CallsignForID(f.SrcID)
	}
	dst := f.DstCallsign
	if dst == "" {
		dst = "ALL"
	}
	putCallsign(buf[ysfGwCallOff:], src) // gateway callsign := source (no separate identity here)
	putCallsign(buf[ysfSrcCallOff:], src)
	putCallsign(buf[ysfDstCallOff:], dst)
	buf[ysfCounterOff] = (f.Stream.Seq & 0x7F) << 1

	frame := buf[ysfFrameOff : ysfFrameOff+ysfFrameLen]
	copy(frame[0:ysfSyncBytes], ysfSync[:])

	var fi fich
	fi.setDT(ysfDTVDMode2)
	fi.setFN(f.Stream.Seq)
	fi.setFT(ysfVCHPerFrame) // frames total
	switch f.Kind {
	case KindHeader:
		fi.setFI(ysfFIHeader)
		buf[ysfCounterOff] = 0
	case KindTerminator:
		fi.setFI(ysfFITerminator)
		buf[ysfCounterOff] |= 0x01 // end of transmission
	default:
		fi.setFI(ysfFICommsChan)
		if len(f.AMBE) != ysfVCHPerFrame {
			return nil, ErrBadFrame
		}
		region := frame[ysfVCHRegionOff:]
		for j := 0; j < ysfVCHPerFrame; j++ {
			off := uint(ysfSubblockBits*j + ysfVCHBitStart)
			ysfVCHFromCanonical(region, off, f.AMBE[j])
		}
	}
	fi.encode(frame)
	return buf, nil
}

// ysfVCHToCanonical lifts one AMBE codeword out of a VD2 subblock's VCH: read 104
// bits via INTERLEAVE_TABLE_26_4 at the subblock offset, de-whiten, then take the
// middle bit of each rate-1/3 triple (a12,b12) plus c (3 majority + 22 raw).
// ModeConv.cpp putYSF.
func ysfVCHToCanonical(region []byte, off uint) []byte {
	var vch [13]byte
	for i := 0; i < ysfVCHBits; i++ {
		if readBit(region, off+uint(interleave264[i])) {
			writeBit(vch[:], uint(i), true)
		}
	}
	for i := 0; i < 13; i++ {
		vch[i] ^= whitening[i]
	}
	var a, b, c uint32
	for i := 0; i < 12; i++ {
		if readBit(vch[:], uint(3*i+1)) {
			a |= 1 << (11 - uint(i))
		}
	}
	for i := 0; i < 12; i++ {
		if readBit(vch[:], uint(3*(i+12)+1)) {
			b |= 1 << (11 - uint(i))
		}
	}
	for i := 0; i < 3; i++ {
		if readBit(vch[:], uint(3*(i+24)+1)) {
			c |= 1 << (24 - uint(i))
		}
	}
	for i := 0; i < 22; i++ {
		if readBit(vch[:], uint(81+i)) {
			c |= 1 << (21 - uint(i))
		}
	}
	return ambePutABC(a, b, c)
}

// ysfVCHFromCanonical packs one AMBE codeword into a VD2 subblock's VCH: rate-1/3
// triple-repeat a12/b12 and c's top 3 bits, 22 raw c bits, whiten, interleave.
// ModeConv.cpp putAMBE2YSF (fed the canonical 49 bits directly — no Golay/PRNG on
// the YSF side).
func ysfVCHFromCanonical(region []byte, off uint, cw []byte) {
	a, b, c := ambeGetABC(cw)
	var vch [13]byte
	for i := 0; i < 12; i++ {
		bit := a&(1<<(11-uint(i))) != 0
		triple(vch[:], 3*i, bit)
	}
	for i := 0; i < 12; i++ {
		bit := b&(1<<(11-uint(i))) != 0
		triple(vch[:], 3*(i+12), bit)
	}
	for i := 0; i < 3; i++ {
		bit := c&(1<<(24-uint(i))) != 0
		triple(vch[:], 3*(i+24), bit)
	}
	for i := 0; i < 22; i++ {
		writeBit(vch[:], uint(81+i), c&(1<<(21-uint(i))) != 0)
	}
	writeBit(vch[:], 103, false)
	for i := 0; i < 13; i++ {
		vch[i] ^= whitening[i]
	}
	for i := 0; i < ysfVCHBits; i++ {
		writeBit(region, off+uint(interleave264[i]), readBit(vch[:], uint(i)))
	}
}

func triple(p []byte, base int, bit bool) {
	writeBit(p, uint(base), bit)
	writeBit(p, uint(base+1), bit)
	writeBit(p, uint(base+2), bit)
}

// trimCallsign / putCallsign handle the 10-byte space-padded YSF callsign field.
func trimCallsign(b []byte) string { return strings.TrimRight(string(b), " \x00") }

func putCallsign(dst []byte, cs string) {
	cs = strings.ToUpper(cs)
	if len(cs) > ysfCallLen {
		cs = cs[:ysfCallLen]
	}
	for i := 0; i < ysfCallLen; i++ {
		if i < len(cs) {
			dst[i] = cs[i]
		} else {
			dst[i] = ' '
		}
	}
}
