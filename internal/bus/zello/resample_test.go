package zello

import (
	"math"
	"testing"
)

func TestUpsampleDoublesLengthAndInterpolates(t *testing.T) {
	got := Upsample8to16([]int16{0, 100, 200})
	want := []int16{0, 50, 100, 150, 200, 200}
	if len(got) != len(want) {
		t.Fatalf("length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestDownsampleHalvesLengthAndAverages(t *testing.T) {
	got := Downsample16to8([]int16{0, 50, 100, 150, 200, 200})
	want := []int16{25, 125, 200}
	if len(got) != len(want) {
		t.Fatalf("length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// Two samples near full scale sum to more than an int16 holds. Averaging in
// int16 would wrap the sign and turn a loud passage into a click, which is the
// kind of fault that only appears on the loudest audio and never in a test with
// small numbers.
func TestDownsampleDoesNotOverflowOnLoudAudio(t *testing.T) {
	got := Downsample16to8([]int16{32767, 32767, -32768, -32768})
	if got[0] != 32767 {
		t.Errorf("full-scale positive pair averaged to %d, want 32767", got[0])
	}
	if got[1] != -32768 {
		t.Errorf("full-scale negative pair averaged to %d, want -32768", got[1])
	}
}

// A round trip through both rates must not move the signal's energy much: it is
// the whole audio path for the bridge, and a systematic gain change here is a
// level problem on the air that is hard to trace back.
func TestRoundTripPreservesEnergy(t *testing.T) {
	in := make([]int16, 160)
	for n := range in {
		in[n] = int16(10000 * math.Sin(2*math.Pi*400*float64(n)/8000))
	}
	out := Downsample16to8(Upsample8to16(in))
	if len(out) != len(in) {
		t.Fatalf("round trip changed the frame length: %d -> %d", len(in), len(out))
	}
	rms := func(v []int16) float64 {
		var e float64
		for _, s := range v {
			e += float64(s) * float64(s)
		}
		return math.Sqrt(e / float64(len(v)))
	}
	ratio := rms(out) / rms(in)
	if ratio < 0.9 || ratio > 1.1 {
		t.Errorf("round-trip energy ratio %.3f, want within 10%%", ratio)
	}
}

func TestResamplersHandleEmptyInput(t *testing.T) {
	if got := Upsample8to16(nil); got != nil {
		t.Errorf("Upsample8to16(nil) = %v", got)
	}
	if got := Downsample16to8(nil); len(got) != 0 {
		t.Errorf("Downsample16to8(nil) = %v", got)
	}
}
