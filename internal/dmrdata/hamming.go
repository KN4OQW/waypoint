package dmrdata

// The two shortened Hamming codes that make up BPTC(196,96): rows are protected
// by Hamming(15,11,3), columns by Hamming(13,9,3). Ported from g4klx/MMDVMHost
// Hamming.cpp (decode15113_2/encode15113_2, decode1393/encode1393).
//
// Both correct exactly one bit. The syndrome-to-position maps below are the
// upstream switch statements transcribed as tables — the same values in the same
// order, laid out so a reader can check them against Hamming.cpp at a glance.

// hammingEncode15113 writes the four parity bits of a 15-bit row in place.
func hammingEncode15113(d []bool) {
	d[11] = d[0] != d[1] != d[2] != d[3] != d[5] != d[7] != d[8]
	d[12] = d[1] != d[2] != d[3] != d[4] != d[6] != d[8] != d[9]
	d[13] = d[2] != d[3] != d[4] != d[5] != d[7] != d[9] != d[10]
	d[14] = d[0] != d[1] != d[2] != d[4] != d[6] != d[7] != d[10]
}

// hamming15113Syndrome maps a non-zero syndrome to the bit it indicts. Index is
// the syndrome; value is the position, or -1 for a syndrome no single-bit error
// produces (which means more than one bit flipped and this code cannot fix it).
var hamming15113Syndrome = [16]int8{
	0x00: -1,
	0x01: 11, 0x02: 12, 0x04: 13, 0x08: 14, // parity bits
	0x09: 0, 0x0B: 1, 0x0F: 2, 0x07: 3, 0x0E: 4, 0x05: 5,
	0x0A: 6, 0x0D: 7, 0x03: 8, 0x06: 9, 0x0C: 10, // data bits
}

// hammingDecode15113 corrects at most one bit of a 15-bit row, reporting whether
// it changed anything.
func hammingDecode15113(d []bool) bool {
	var n byte
	if (d[0] != d[1] != d[2] != d[3] != d[5] != d[7] != d[8]) != d[11] {
		n |= 0x01
	}
	if (d[1] != d[2] != d[3] != d[4] != d[6] != d[8] != d[9]) != d[12] {
		n |= 0x02
	}
	if (d[2] != d[3] != d[4] != d[5] != d[7] != d[9] != d[10]) != d[13] {
		n |= 0x04
	}
	if (d[0] != d[1] != d[2] != d[4] != d[6] != d[7] != d[10]) != d[14] {
		n |= 0x08
	}
	if pos := hamming15113Syndrome[n]; pos >= 0 {
		d[pos] = !d[pos]
		return true
	}
	return false
}

// hammingEncode1393 writes the four parity bits of a 13-bit column in place.
func hammingEncode1393(d []bool) {
	d[9] = d[0] != d[1] != d[3] != d[5] != d[6]
	d[10] = d[0] != d[1] != d[2] != d[4] != d[6] != d[7]
	d[11] = d[0] != d[1] != d[2] != d[3] != d[5] != d[7] != d[8]
	d[12] = d[0] != d[2] != d[4] != d[5] != d[8]
}

// hamming1393Syndrome is the column code's syndrome map; see hamming15113Syndrome.
var hamming1393Syndrome = [16]int8{
	0x00: -1,
	0x01: 9, 0x02: 10, 0x04: 11, 0x08: 12, // parity bits
	0x0F: 0, 0x07: 1, 0x0E: 2, 0x05: 3, 0x0A: 4,
	0x0D: 5, 0x03: 6, 0x06: 7, 0x0C: 8, // data bits
	0x09: -1, 0x0B: -1,
}

// hammingDecode1393 corrects at most one bit of a 13-bit column, reporting
// whether it changed anything.
func hammingDecode1393(d []bool) bool {
	var n byte
	if (d[0] != d[1] != d[3] != d[5] != d[6]) != d[9] {
		n |= 0x01
	}
	if (d[0] != d[1] != d[2] != d[4] != d[6] != d[7]) != d[10] {
		n |= 0x02
	}
	if (d[0] != d[1] != d[2] != d[3] != d[5] != d[7] != d[8]) != d[11] {
		n |= 0x04
	}
	if (d[0] != d[2] != d[4] != d[5] != d[8]) != d[12] {
		n |= 0x08
	}
	if pos := hamming1393Syndrome[n]; pos >= 0 {
		d[pos] = !d[pos]
		return true
	}
	return false
}
