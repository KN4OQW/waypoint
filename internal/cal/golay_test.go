package cal

import (
	"math/bits"
	"testing"
)

// These fixtures are spot values lifted from the tables Waypoint deliberately
// does NOT carry: MMDVMCal's Golay24128.cpp and BERCal.cpp. Deriving the tables
// is only worth doing if the derivation is pinned to what every other MMDVM
// implementation actually uses, and this is that pin. A change to the generator
// polynomial or the bit order fails here rather than showing up on the bench as
// a bit error rate that is subtly, plausibly wrong.

var golay23Published = []struct{ data, want uint32 }{
	{0, 0x000000}, {1, 0x0018EA}, {2, 0x00293E}, {3, 0x0031D4}, {7, 0x007B42},
	{15, 0x00F684}, {31, 0x01F5E2}, {63, 0x03F32E}, {127, 0x07FEB6}, {255, 0x0FFD6C},
	{511, 0x1FFAD8}, {1023, 0x3FF5B0}, {2047, 0x7FF38A}, {2048, 0x800C74},
	{3000, 0xBB8F1E}, {4093, 0xFFD6C0}, {4094, 0xFFE714}, {4095, 0xFFFFFE},
}

var golay24Published = []struct{ data, want uint32 }{
	{0, 0x000000}, {1, 0x0018EB}, {2, 0x00293E}, {3, 0x0031D5}, {7, 0x007B42},
	{15, 0x00F684}, {31, 0x01F5E3}, {63, 0x03F32E}, {127, 0x07FEB7}, {255, 0x0FFD6D},
	{511, 0x1FFAD9}, {1023, 0x3FF5B1}, {2047, 0x7FF38A}, {2048, 0x800C75},
	{3000, 0xBB8F1F}, {4093, 0xFFD6C1}, {4094, 0xFFE714}, {4095, 0xFFFFFF},
}

// TestEncode23127MatchesPublished pins the one convention difference between
// this package and upstream: its 23-bit encoding table stores every codeword
// shifted up by a bit, and its callers shift back down. Getting this backwards
// halves every regenerated codeword and inflates BER by roughly a factor of two
// — a number that still looks like a bit error rate, which is exactly what makes
// it worth a test of its own.
func TestEncode23127MatchesPublished(t *testing.T) {
	for _, tc := range golay23Published {
		if got := encode23127(tc.data) << 1; got != tc.want {
			t.Errorf("encode23127(%d)<<1 = 0x%06X, published table has 0x%06X", tc.data, got, tc.want)
		}
	}
}

func TestEncode24128MatchesPublished(t *testing.T) {
	for _, tc := range golay24Published {
		if got := encode24128(tc.data); got != tc.want {
			t.Errorf("encode24128(%d) = 0x%06X, published table has 0x%06X", tc.data, got, tc.want)
		}
	}
}

// TestGolayIsPerfect is the property the whole file rests on. Golay(23,12) has
// 2048 syndromes and exactly 2048 error patterns of weight three or less, in
// one-to-one correspondence — so if every syndrome slot was filled exactly once
// during init, the derived decoder is complete by construction and there is no
// table to have mistyped.
func TestGolayIsPerfect(t *testing.T) {
	seen := make(map[uint32]int)
	count := func(pattern uint32) { seen[syndrome23127(pattern)]++ }
	count(0)
	for i := 0; i < 23; i++ {
		count(1 << i)
		for j := i + 1; j < 23; j++ {
			count(1<<i | 1<<j)
			for k := j + 1; k < 23; k++ {
				count(1<<i | 1<<j | 1<<k)
			}
		}
	}
	if len(seen) != 2048 {
		t.Fatalf("weight-≤3 patterns produced %d distinct syndromes, want 2048", len(seen))
	}
	for syn, n := range seen {
		if n != 1 {
			t.Fatalf("syndrome 0x%03X is produced by %d different patterns; the code is not perfect and the derivation is wrong", syn, n)
		}
	}
}

// TestGolayCorrectsUpToThreeErrors walks every data word and, for a sample of
// error patterns, checks the data survives. Three errors is the design limit;
// four is expected to fail and is not tested, because a decoder that "worked" on
// four errors would mean the syndrome table was wrong.
func TestGolayCorrectsUpToThreeErrors(t *testing.T) {
	patterns := []uint32{0, 1, 1 << 22, 1<<3 | 1<<17, 1<<0 | 1<<11 | 1<<22, 0x7 << 9}
	for data := uint32(0); data < 4096; data++ {
		code := encode23127(data)
		for _, p := range patterns {
			if bits.OnesCount32(p) > 3 {
				continue
			}
			if got := decode23127(code ^ p); got != data {
				t.Fatalf("decode23127(encode23127(%d) ^ 0x%06X) = %d, want %d", data, p, got, data)
			}
		}
	}
}

func TestDecode24128RoundTrips(t *testing.T) {
	for data := uint32(0); data < 4096; data++ {
		if got := decode24128(encode24128(data)); got != data {
			t.Fatalf("decode24128(encode24128(%d)) = %d", data, got)
		}
	}
}
