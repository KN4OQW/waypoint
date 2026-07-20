package frames

import "encoding/binary"

// DMR network (loopback) frame handling. The bus rides the local DMRGateway
// loopback, which speaks the MMDVM/Homebrew "DMRD" protocol — a 55-byte UDP frame
// wrapping the 33-byte DMR voice payload. Addressing (src/dst 24-bit ids, slot,
// call type, frame kind) is carried in the DMRD header, NOT inside the AMBE, so
// the reframe never touches the codec bits (RFC-0003 §3).
//
// Ground truth: the 55-byte "DMRD" layout is g4klx MMDVMHost DMRNetwork.cpp::write;
// the 33-byte payload's 3-AMBE placement (with AMBE frame 2 straddling the 48-bit
// sync/EMB gap at bytes 13-19) is MMDVM_CM ModeConv.cpp putDMR/getDMR.

const (
	dmrdLen        = 55 // "DMRD" + header + 33-byte payload + BER + RSSI
	dmrPayloadLen  = 33
	dmrPayloadOff  = 20 // payload starts at byte 20 of the DMRD frame
	dmrAMBEPerFrm  = 3
	dmrFlagsOff    = 15
	dmrSeqOff      = 4
	dmrSrcOff      = 5
	dmrDstOff      = 8
	dmrStreamOff   = 16
	dmrRepeaterOff = 11
)

// DMR data types in the flags byte (DMRDefines.h DT_*).
const (
	dmrDTVoiceLCHeader = 0x01
	dmrDTTerminator    = 0x02
)

// dmrAudioSyncGap is BS_SOURCED_AUDIO_SYNC (DMRDefines.h), nibble-aligned to sit
// in the 48-bit gap at payload bytes 13(lo)..19(hi). Bytes [0] and [6] carry only
// the sync nibble; the AMBE nibbles are OR'd in. Bytes [1..5] are pure sync.
var dmrAudioSyncGap = [7]byte{0x07, 0x55, 0xFD, 0x7D, 0xF7, 0x5F, 0x70}

// ParseDMR parses a "DMRD" network frame into a normalized Frame. Voice frames
// yield 3 AMBE codewords; a voice header/terminator yields none (it carries link
// control, not audio). A non-voice data frame returns ErrUnsupported. Malformed
// input returns an error and never panics.
func ParseDMR(buf []byte) (Frame, error) {
	if len(buf) < dmrPayloadOff+dmrPayloadLen {
		return Frame{}, ErrShort
	}
	if string(buf[0:4]) != "DMRD" {
		return Frame{}, ErrBadMagic
	}
	f := Frame{Mode: ModeDMR}
	f.SrcID = readU24(buf[dmrSrcOff:])
	f.DstID = readU24(buf[dmrDstOff:])
	f.Stream.ID = binary.BigEndian.Uint32(buf[dmrStreamOff:])
	f.Stream.Seq = buf[dmrSeqOff]

	flags := buf[dmrFlagsOff]
	payload := buf[dmrPayloadOff : dmrPayloadOff+dmrPayloadLen]

	if flags&0x20 != 0 { // data/control frame (0x20 | dataType)
		switch flags & 0x0F {
		case dmrDTVoiceLCHeader:
			f.Kind = KindHeader
		case dmrDTTerminator:
			f.Kind = KindTerminator
		default:
			return Frame{}, ErrUnsupported
		}
		return f, nil // header/terminator carry no AMBE
	}

	// Voice frame (sync 0x10 or seq 0x00-0x05): extract the 3 AMBE codewords.
	f.Kind = KindVoice
	f.AMBE = make([][]byte, dmrAMBEPerFrm)
	f.AMBE[0] = dmrAMBEToCanonical(payload[0:9])
	f.AMBE[1] = dmrAMBEToCanonical(dmrExtractAMBE2(payload))
	f.AMBE[2] = dmrAMBEToCanonical(payload[24:33])
	return f, nil
}

// ConstructDMR builds a "DMRD" frame from a normalized Frame, applying the DMR
// destination params (slot, default_tg / tg_map) and resolving the source
// callsign->id via the shared lookup when the frame arrived callsign-addressed
// (e.g. from YSF). A voice frame must carry exactly 3 AMBE codewords.
func ConstructDMR(f Frame, p Params, r Resolver) ([]byte, error) {
	res := resolverOrNull(r)
	buf := make([]byte, dmrdLen)
	copy(buf, "DMRD")
	buf[dmrSeqOff] = f.Stream.Seq

	src := f.SrcID
	if src == 0 && f.SrcCallsign != "" {
		src = res.IDForCallsign(f.SrcCallsign)
	}
	dst := dmrDest(f, p)
	writeU24(buf[dmrSrcOff:], src)
	writeU24(buf[dmrDstOff:], dst)
	binary.BigEndian.PutUint32(buf[dmrStreamOff:], f.Stream.ID)

	slotBit := byte(0)
	if p.Slot == 2 {
		slotBit = 0x80
	}
	callBit := byte(0) // group call (default); private would be 0x40

	payload := buf[dmrPayloadOff : dmrPayloadOff+dmrPayloadLen]
	switch f.Kind {
	case KindHeader:
		buf[dmrFlagsOff] = slotBit | callBit | 0x20 | dmrDTVoiceLCHeader
	case KindTerminator:
		buf[dmrFlagsOff] = slotBit | callBit | 0x20 | dmrDTTerminator
	default: // voice
		buf[dmrFlagsOff] = slotBit | callBit | 0x10 // voice sync
		if len(f.AMBE) != dmrAMBEPerFrm {
			return nil, ErrBadFrame
		}
		copy(payload[0:9], dmrAMBEFromCanonical(f.AMBE[0]))
		dmrInsertAMBE2(payload, dmrAMBEFromCanonical(f.AMBE[1]))
		copy(payload[24:33], dmrAMBEFromCanonical(f.AMBE[2]))
		dmrInsertSyncGap(payload)
	}
	return buf, nil
}

// dmrDest maps the frame's destination TG through the attachment params: an
// explicit tg_map entry wins, else the source dst passes through, else default_tg.
func dmrDest(f Frame, p Params) uint32 {
	if mapped, ok := p.TGMap[f.DstID]; ok {
		return mapped
	}
	if f.DstID != 0 {
		return f.DstID
	}
	return p.DefaultTG
}

// dmrExtractAMBE2 reassembles the straddling AMBE frame 2 into a 9-byte codeword
// (ModeConv.cpp putDMR): bytes 9-12, byte 13 hi nibble | byte 19 lo nibble, 20-23.
func dmrExtractAMBE2(payload []byte) []byte {
	sub := make([]byte, dmrAMBEBytes)
	copy(sub[0:4], payload[9:13])
	sub[4] = (payload[13] & 0xF0) | (payload[19] & 0x0F)
	copy(sub[5:9], payload[20:24])
	return sub
}

// dmrInsertAMBE2 writes AMBE frame 2 back into the straddled positions
// (ModeConv.cpp getDMR), preserving the sync/EMB nibbles at 13-lo and 19-hi.
func dmrInsertAMBE2(payload, sub []byte) {
	copy(payload[9:13], sub[0:4])
	payload[13] = (payload[13] & 0x0F) | (sub[4] & 0xF0)
	payload[19] = (payload[19] & 0xF0) | (sub[4] & 0x0F)
	copy(payload[20:24], sub[5:9])
}

// dmrInsertSyncGap writes the audio sync into the 48-bit gap (payload 13-lo..19-hi),
// leaving the AMBE-2 nibbles at 13-hi and 19-lo intact.
func dmrInsertSyncGap(payload []byte) {
	payload[13] = (payload[13] & 0xF0) | (dmrAudioSyncGap[0] & 0x0F)
	copy(payload[14:19], dmrAudioSyncGap[1:6])
	payload[19] = (payload[19] & 0x0F) | (dmrAudioSyncGap[6] & 0xF0)
}

func readU24(b []byte) uint32 { return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]) }

func writeU24(b []byte, v uint32) {
	b[0] = byte(v >> 16)
	b[1] = byte(v >> 8)
	b[2] = byte(v)
}
