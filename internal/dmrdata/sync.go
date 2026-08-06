package dmrdata

// The 48-bit sync pattern in the middle of a burst, nibble-aligned across bytes
// 13-19. Ported from g4klx/MMDVMHost Sync.cpp and DMRDefines.h.
//
// Bytes 13 and 19 are shared with the slot type, so the pattern is masked in
// rather than copied: SYNC_MASK leaves the low nibble of 13 and the high nibble
// of 19 alone.

var (
	syncMask           = [7]byte{0x0F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xF0}
	bsSourcedDataSync  = [7]byte{0x0D, 0xFF, 0x57, 0xD7, 0x5D, 0xF5, 0xD0}
	msSourcedDataSync  = [7]byte{0x0D, 0x5D, 0x7F, 0x77, 0xFD, 0x75, 0x70}
	bsSourcedAudioSync = [7]byte{0x07, 0x55, 0xFD, 0x7D, 0xF7, 0x5F, 0x70}
	msSourcedAudioSync = [7]byte{0x07, 0xF7, 0xD5, 0xDD, 0x57, 0xDF, 0xD0}
)

// addDataSync writes the data sync pattern into burst. duplex selects the
// base-station pattern; a simplex node sources the mobile-station one.
//
// MMDVM-Host rewrites this for the node's own duplex setting on the way out, so
// getting it wrong costs nothing on air — but a burst with NO sync is a burst
// with no sync, and that is the failure this package exists to prevent.
func addDataSync(burst []byte, duplex bool) {
	pattern := &msSourcedDataSync
	if duplex {
		pattern = &bsSourcedDataSync
	}
	for i := 0; i < 7; i++ {
		burst[13+i] = burst[13+i]&^syncMask[i] | pattern[i]
	}
}

// hasDataSync reports whether burst carries either data sync pattern, ignoring
// the nibbles the slot type shares with it.
func hasDataSync(burst []byte) bool {
	return matchesSync(burst, &bsSourcedDataSync) || matchesSync(burst, &msSourcedDataSync)
}

// hasAnySync reports whether burst carries any of the four sync patterns.
//
// A burst with none of them is not something a radio transmitted. It is what a
// buffer looks like when only the 196 BPTC bits were written into it, and
// recognising that shape is the difference between "nobody sent a message" and
// "we sent one wrong". Feed counts it, so the failure is visible at runtime and
// not only in tests.
func hasAnySync(burst []byte) bool {
	return hasDataSync(burst) ||
		matchesSync(burst, &bsSourcedAudioSync) || matchesSync(burst, &msSourcedAudioSync)
}

func matchesSync(burst []byte, pattern *[7]byte) bool {
	for i := 0; i < 7; i++ {
		if burst[13+i]&syncMask[i] != pattern[i] {
			return false
		}
	}
	return true
}
