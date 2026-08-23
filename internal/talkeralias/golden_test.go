package talkeralias

import (
	"encoding/hex"
	"testing"
)

// Golden vectors verified against MMDVM-Host's OWN decoder.
//
// A Go encoder checked only against a Go decoder proves the two agree, not that
// either is right. So these byte strings were produced by Encode and then fed to
// g4klx's CDMRTA::decodeTA — compiled from the fork at the pinned SHA, driven by
// a harness, and asked what it made of them. All eight came back exactly equal to
// the `want` column, including the 7-bit stream that starts at bit 7 and the
// UTF-16 pairs that are big-endian.
//
// The bytes are frozen here so that evidence survives without a C++ toolchain in
// the loop. If Encode's output ever stops matching them, either the wire format
// changed — in which case the fork's parser and MMDVM-Host's own emitter both
// need looking at — or the encoder broke. It is not a test to update casually.
//
// Each vector is the 28-byte alias buffer: the four blocks' 7-byte payloads
// concatenated, i.e. bytes [8:15] of each DMRA frame in block order.
var goldenVectors = []struct {
	format Format
	alias  string
	hex    string
}{
	{Format7Bit, "KN4OQW", "0d2e7349f46b80000000000000000000000000000000000000000000"},
	{Format7Bit, "KN4OQW Clint Chance", "272e7349f46ba087b34eee8821e8c3bb1e5000000000000000000000"},
	{Format7Bit, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "3f060c183060c183060c183060c183060c183060c183060c183060c1"},
	{Format7Bit, "Clint Chance", "190f669ddd1043d187763ca000000000000000000000000000000000"},
	{FormatISO8859_1, "Jose Munoz", "544a6f7365204d756e6f7a0000000000000000000000000000000000"},
	{FormatISO8859_1, "bbbbbbbbbbbbbbbbbbbbbbbbbbb", "76626262626262626262626262626262626262626262626262626262"},
	{FormatUTF16BE, "KN4OQW", "cc004b004e0034004f00510057000000000000000000000000000000"},
	{FormatUTF16BE, "ccccccccccccc", "da006300630063006300630063006300630063006300630063006300"},
}

func TestGoldenVectorsMatchUpstreamDecoder(t *testing.T) {
	for _, v := range goldenVectors {
		t.Run(v.alias[:min(len(v.alias), 12)], func(t *testing.T) {
			frames, err := Encode(3180202, v.alias, v.format)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			var got []byte
			for _, fr := range frames {
				got = append(got, fr[8:]...)
			}
			want, err := hex.DecodeString(v.hex)
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(got) != hex.EncodeToString(want) {
				t.Errorf("encoded bytes differ from the vector upstream's decoder accepted:\n got %x\nwant %x", got, want)
			}
			// And our own decoder reads the same bytes back the same way.
			_, back, f, err := Decode(frames)
			if err != nil {
				t.Fatal(err)
			}
			if back != v.alias || f != v.format {
				t.Errorf("round trip = %q/%d, want %q/%d", back, f, v.alias, v.format)
			}
		})
	}
}
