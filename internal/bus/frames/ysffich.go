package frames

// YSF FICH (Frame Information CHannel) codec, ported from
// juribeparada/MMDVM_CM DMR2YSF/YSFFICH.cpp + YSFConvolution.cpp + CRC.cpp. The
// FICH is a 6-byte structure protected by a CCITT-16 CRC, four Golay(24,12)
// blocks, a rate-1/2 K=5 convolutional code (Viterbi on decode), and a 100-dibit
// interleave, laid into the 25 bytes after the YSF sync. The bus needs it only to
// classify a frame (FI = header/comms/terminator) and read/write DT (VD mode 2)
// and FN — but it is ported in full so a constructed frame is byte-valid to a real
// YSF daemon. No audio/vocoder is involved.

// YSF frame geometry (YSFDefines.h).
const (
	ysfSyncBytes = 5
	ysfFICHBytes = 25
	ysfFrameLen  = 120 // sync(5) + FICH(25) + 5x18B VD2 subblocks(90)
)

// ysfSync is YSF_SYNC_BYTES (YSFDefines.h).
var ysfSync = [ysfSyncBytes]byte{0xD4, 0x71, 0xC9, 0x63, 0x4D}

// FI (frame information) values (YSFDefines.h).
const (
	ysfFIHeader     = 0x00
	ysfFICommsChan  = 0x01
	ysfFITerminator = 0x02
)

// DT (data type) — VD mode 2 (DN) is the reframe-tier YSF format (YSFDefines.h).
const ysfDTVDMode2 = 0x02

// fich is the decoded 6-byte FICH (m_fich[0..5]; [4..5] = CRC). Bit fields per
// CYSFFICH getters/setters (YSFFICH.cpp).
type fich [6]byte

func (f *fich) fi() byte     { return (f[0] >> 6) & 0x03 }
func (f *fich) dt() byte     { return f[2] & 0x03 }
func (f *fich) fn() byte     { return (f[1] >> 3) & 0x07 }
func (f *fich) setFI(v byte) { f[0] = (f[0] & 0x3F) | ((v & 0x03) << 6) }
func (f *fich) setDT(v byte) { f[2] = (f[2] & 0xFC) | (v & 0x03) }
func (f *fich) setFN(v byte) { f[1] = (f[1] & 0xC7) | ((v & 0x07) << 3) }
func (f *fich) setFT(v byte) { f[1] = (f[1] & 0xF8) | (v & 0x07) }

// fichDecode decodes the FICH out of a 120-byte YSF frame (CYSFFICH::decode).
// Returns ok=false if the CCITT-16 CRC fails.
func fichDecode(frame []byte) (fich, bool) {
	if len(frame) < ysfSyncBytes+ysfFICHBytes {
		return fich{}, false
	}
	bytes := frame[ysfSyncBytes:] // skip sync

	var v ysfConvolution
	v.start()
	for i := 0; i < 100; i++ {
		n := uint(fichInterleave[i])
		var s0, s1 byte
		if readBit(bytes, n) {
			s0 = 1
		}
		if readBit(bytes, n+1) {
			s1 = 1
		}
		v.decode(s0, s1)
	}
	var output [13]byte
	v.chainback(output[:], 96)

	b0 := golayDecode24128(uint32(output[0])<<16 | uint32(output[1])<<8 | uint32(output[2]))
	b1 := golayDecode24128(uint32(output[3])<<16 | uint32(output[4])<<8 | uint32(output[5]))
	b2 := golayDecode24128(uint32(output[6])<<16 | uint32(output[7])<<8 | uint32(output[8]))
	b3 := golayDecode24128(uint32(output[9])<<16 | uint32(output[10])<<8 | uint32(output[11]))

	var f fich
	f[0] = byte((b0 >> 4) & 0xFF)
	f[1] = byte(((b0 << 4) & 0xF0) | ((b1 >> 8) & 0x0F))
	f[2] = byte(b1 & 0xFF)
	f[3] = byte((b2 >> 4) & 0xFF)
	f[4] = byte(((b2 << 4) & 0xF0) | ((b3 >> 8) & 0x0F))
	f[5] = byte(b3 & 0xFF)

	return f, checkCCITT162(f[:])
}

// fichEncode encodes f into the FICH region of a 120-byte YSF frame
// (CYSFFICH::encode) — Golay-encode 4 blocks, convolutional-encode, interleave.
func (f *fich) encode(frame []byte) {
	addCCITT162(f[:])

	b0 := (uint32(f[0]) << 4 & 0xFF0) | (uint32(f[1]) >> 4 & 0x00F)
	b1 := (uint32(f[1]) << 8 & 0xF00) | (uint32(f[2]) & 0x0FF)
	b2 := (uint32(f[3]) << 4 & 0xFF0) | (uint32(f[4]) >> 4 & 0x00F)
	b3 := (uint32(f[4]) << 8 & 0xF00) | (uint32(f[5]) & 0x0FF)

	c0 := golayEncode24128(b0)
	c1 := golayEncode24128(b1)
	c2 := golayEncode24128(b2)
	c3 := golayEncode24128(b3)

	var conv [13]byte
	conv[0], conv[1], conv[2] = byte(c0>>16), byte(c0>>8), byte(c0)
	conv[3], conv[4], conv[5] = byte(c1>>16), byte(c1>>8), byte(c1)
	conv[6], conv[7], conv[8] = byte(c2>>16), byte(c2>>8), byte(c2)
	conv[9], conv[10], conv[11] = byte(c3>>16), byte(c3>>8), byte(c3)
	conv[12] = 0x00

	var convolved [25]byte
	convEncode(conv[:], convolved[:], 100)

	bytes := frame[ysfSyncBytes:]
	j := uint(0)
	for i := 0; i < 100; i++ {
		n := uint(fichInterleave[i])
		s0 := readBit(convolved[:], j)
		j++
		s1 := readBit(convolved[:], j)
		j++
		writeBit(bytes, n, s0)
		writeBit(bytes, n+1, s1)
	}
}

// --- CCITT-16 CRC (CRC.cpp addCCITT162 / checkCCITT162) ------------------------

// crcCCITT162 computes the (inverted) CCITT-16 over data, matching CCRC's
// table-driven loop with an explicit low/high byte split (no endianness assumption).
func crcCCITT162(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		lo := byte(crc & 0xFF)
		hi := byte(crc >> 8)
		crc = uint16(lo)<<8 ^ ccitt16Table2[hi^b]
	}
	return ^crc
}

// addCCITT162 writes the CRC into the last two bytes of buf (over buf[:len-2]).
func addCCITT162(buf []byte) {
	crc := crcCCITT162(buf[:len(buf)-2])
	buf[len(buf)-1] = byte(crc & 0xFF) // crc8[0]
	buf[len(buf)-2] = byte(crc >> 8)   // crc8[1]
}

// checkCCITT162 verifies the trailing two CRC bytes of buf.
func checkCCITT162(buf []byte) bool {
	if len(buf) < 3 {
		return false
	}
	crc := crcCCITT162(buf[:len(buf)-2])
	return byte(crc&0xFF) == buf[len(buf)-1] && byte(crc>>8) == buf[len(buf)-2]
}

// --- Rate-1/2 K=5 convolutional codec (YSFConvolution.cpp) ---------------------

// ysfConvolution is CYSFConvolution: Viterbi decode + straight encode. Ported
// verbatim (branch tables, 16 states, 180 decisions).
type ysfConvolution struct {
	oldMetrics [16]uint16
	newMetrics [16]uint16
	decisions  [180]uint64
	dp         int
}

var convBranch1 = [8]byte{0, 0, 0, 0, 1, 1, 1, 1}
var convBranch2 = [8]byte{0, 1, 1, 0, 0, 1, 1, 0}

func (v *ysfConvolution) start() {
	v.oldMetrics = [16]uint16{}
	v.newMetrics = [16]uint16{}
	v.dp = 0
}

// decode is CYSFConvolution::decode(s0,s1). M=2.
func (v *ysfConvolution) decode(s0, s1 byte) {
	const numStatesD2 = 8
	const m = 2
	v.decisions[v.dp] = 0
	for i := 0; i < numStatesD2; i++ {
		j := i * 2
		metric := uint16(convBranch1[i]^s0) + uint16(convBranch2[i]^s1)

		m0 := v.oldMetrics[i] + metric
		m1 := v.oldMetrics[i+numStatesD2] + (m - metric)
		var d0 uint64
		if m0 >= m1 {
			d0 = 1
			v.newMetrics[j] = m1
		} else {
			v.newMetrics[j] = m0
		}

		m0 = v.oldMetrics[i] + (m - metric)
		m1 = v.oldMetrics[i+numStatesD2] + metric
		var d1 uint64
		if m0 >= m1 {
			d1 = 1
			v.newMetrics[j+1] = m1
		} else {
			v.newMetrics[j+1] = m0
		}

		v.decisions[v.dp] |= (d1 << uint(j+1)) | (d0 << uint(j))
	}
	v.dp++
	v.oldMetrics, v.newMetrics = v.newMetrics, v.oldMetrics
}

// chainback is CYSFConvolution::chainback (K=5).
func (v *ysfConvolution) chainback(out []byte, nBits uint) {
	const k = 5
	state := uint32(0)
	for nBits > 0 {
		nBits--
		v.dp--
		i := state >> (9 - k)
		bit := byte(v.decisions[v.dp]>>i) & 1
		state = (uint32(bit) << 7) | (state >> 1)
		writeBit(out, nBits, bit != 0)
	}
}

// convEncode is CYSFConvolution::encode (rate-1/2, polys g1=d+d3+d4, g2=d+d1+d2+d4).
func convEncode(in, out []byte, nBits uint) {
	var d1, d2, d3, d4 byte
	k := uint(0)
	for i := uint(0); i < nBits; i++ {
		var d byte
		if readBit(in, i) {
			d = 1
		}
		g1 := (d + d3 + d4) & 1
		g2 := (d + d1 + d2 + d4) & 1
		d4, d3, d2, d1 = d3, d2, d1, d
		writeBit(out, k, g1 != 0)
		k++
		writeBit(out, k, g2 != 0)
		k++
	}
}
