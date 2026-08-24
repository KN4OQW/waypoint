//go:build zello && linux && arm

package vocoder

import (
	"encoding/hex"
	"errors"
	"math"
	"os"
	"testing"
	"time"
)

// These run only where the vocoder exists: a tagged build on 32-bit ARM, with
// the operator's firmware images present. They are the tests CI cannot run, and
// they are the reason the vocoder-touching ones gate on hardware.
//
//	WAYPOINT_MD380_FW=/path/md380fw.img \
//	WAYPOINT_MD380_RAM=/path/md380ram.img \
//	  ./vocoder.test -test.v
func testVocoder(t *testing.T) *Vocoder {
	t.Helper()
	fw, ram := os.Getenv("WAYPOINT_MD380_FW"), os.Getenv("WAYPOINT_MD380_RAM")
	if fw == "" || ram == "" {
		t.Skip("set WAYPOINT_MD380_FW and WAYPOINT_MD380_RAM to run the vocoder tests")
	}
	v, err := Open(Config{FirmwarePath: fw, RAMPath: ram})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

// tone builds frame f of a continuous 1 kHz sine at 8 kHz, so successive frames
// are phase-continuous the way real audio is.
func tone(f int) []int16 {
	pcm := make([]int16, SamplesPerFrame)
	for n := range pcm {
		pcm[n] = int16(12000 * math.Sin(2*math.Pi*1000*float64(f*SamplesPerFrame+n)/8000))
	}
	return pcm
}

// The value below was produced on the bench Pi 3 by the vocoder itself, through
// two independent paths that agree: the linked-blob wpambe build, and the
// runtime-mapped build this package uses. Pinning it means a change of firmware
// image, of library, or of the packing convention is caught here rather than as
// unintelligible audio on the air.
func TestEncodingAKnownToneGivesTheKnownCodeword(t *testing.T) {
	v := testVocoder(t)

	got, err := v.Encode(tone(0))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	const want = "f8f011044ca880"
	if hex.EncodeToString(got) != want {
		t.Errorf("codeword = %s, want %s", hex.EncodeToString(got), want)
	}
	if got[6] != 0x80 {
		t.Errorf("byte 6 = %#x, want 0x80 — bit 48 belongs in the MSB in canonical form", got[6])
	}
}

// The encoder carries model state across frames, exactly as the decoder does.
// Encoding the same input twice does not give the same codeword: the second one
// is the steady-state answer for a continuous tone, because by then the model
// has adapted to the first.
//
// Measured: frame 0 encodes to f8f011044ca880 as the first operation after Open,
// and to ff920202020800 immediately after — and ff920202020800 is precisely what
// a continuous 1 kHz tone settles to from frame 2 onwards.
//
// Three consequences for anything built on this package. A codeword cannot be
// cached and replayed. Frames must be fed in order. And two streams cannot share
// a vocoder even sequentially without the first corrupting the second's model —
// which, with one vocoder per process, is another reason a bus transcodes one
// talker at a time and RFC-0003 §5's arbitration is doing real work here.
func TestTheEncoderIsStatefulAcrossFrames(t *testing.T) {
	v := testVocoder(t)

	first, err := v.Encode(tone(0))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	second, err := v.Encode(tone(0))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if hex.EncodeToString(first) == hex.EncodeToString(second) {
		t.Errorf("the same frame encoded identically twice (%s); the encoder is no longer "+
			"stateful, so the ordering rules this package documents may no longer apply",
			hex.EncodeToString(first))
	}
	// Whatever the model does, the OR hazard must never show through: Encode
	// allocates a zeroed codeword every call, so no byte of a previous answer
	// can survive into this one.
	if len(second) != CodewordBytes {
		t.Fatalf("codeword length %d", len(second))
	}
}

// The decisive test: audio that goes in comes back out as audio. Not
// bit-identical — this is a lossy model-based codec — but carrying the energy
// and the shape of what was sent.
func TestRoundTripPreservesTheSignal(t *testing.T) {
	v := testVocoder(t)

	const frames = 50
	var in, out [][]int16
	for f := 0; f < frames; f++ {
		src := tone(f)
		cw, err := v.Encode(src)
		if err != nil {
			t.Fatalf("encode frame %d: %v", f, err)
		}
		dst, err := v.Decode(cw)
		if err != nil {
			t.Fatalf("decode frame %d: %v", f, err)
		}
		in, out = append(in, src), append(out, dst)
	}

	// Frame 0 is the codec's model converging and is expected to be near-silent;
	// measure from the second half, which is steady state.
	var ein, eout float64
	for f := frames / 2; f < frames; f++ {
		for n := 0; n < SamplesPerFrame; n++ {
			ein += float64(in[f][n]) * float64(in[f][n])
			eout += float64(out[f][n]) * float64(out[f][n])
		}
	}
	ratio := math.Sqrt(eout / ein)
	if ratio < 0.5 || ratio > 2.0 {
		t.Errorf("steady-state energy ratio %.2f is outside 0.5-2.0; the audio did not survive", ratio)
	}
}

// Recorded as a property because it changes how a caller must behave: the first
// frame out of the decoder is not usable audio. A bridge that ignores this clips
// the first syllable of every transmission, which sounds like a radio problem
// and is not one.
func TestTheFirstDecodedFrameIsNearSilentThenRecovers(t *testing.T) {
	v := testVocoder(t)

	rms := func(pcm []int16) float64 {
		var e float64
		for _, s := range pcm {
			e += float64(s) * float64(s)
		}
		return math.Sqrt(e / float64(len(pcm)))
	}

	var first, later float64
	for f := 0; f < 5; f++ {
		cw, err := v.Encode(tone(f))
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		pcm, err := v.Decode(cw)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if f == 0 {
			first = rms(pcm)
		}
		if f == 4 {
			later = rms(pcm)
		}
	}
	if first > later/10 {
		t.Errorf("first frame rms %.0f vs settled %.0f — the warm-up this documents is gone; "+
			"check whether the caller still needs to prime the decoder", first, later)
	}
}

// The firmware sits at fixed addresses and the library computes through fixed
// buffers inside it, so a second Vocoder would share one codec with the first and
// interleave its audio. Refusing is the only safe answer.
func TestASecondVocoderIsRefused(t *testing.T) {
	fw, ram := os.Getenv("WAYPOINT_MD380_FW"), os.Getenv("WAYPOINT_MD380_RAM")
	if fw == "" || ram == "" {
		t.Skip("set WAYPOINT_MD380_FW and WAYPOINT_MD380_RAM to run the vocoder tests")
	}
	first, err := Open(Config{FirmwarePath: fw, RAMPath: ram})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer first.Close()

	if _, err := Open(Config{FirmwarePath: fw, RAMPath: ram}); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("second Open err = %v, want ErrAlreadyOpen", err)
	}
}

// Real time is the whole question for a bridge. A node that cannot service a
// 20 ms frame in under 20 ms cannot carry audio, and this is the floor of the
// supported hardware.
func TestAFrameIsServicedFasterThanRealTime(t *testing.T) {
	v := testVocoder(t)

	const frames = 100
	start := time.Now()
	for f := 0; f < frames; f++ {
		cw, err := v.Encode(tone(f))
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if _, err := v.Decode(cw); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	per := time.Since(start) / frames
	t.Logf("encode+decode per 20 ms frame: %v", per)
	if per >= FrameDuration {
		t.Errorf("%v per frame is not real time; the budget is %v", per, FrameDuration)
	}
}
