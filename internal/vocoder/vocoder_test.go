package vocoder

import (
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
)

// The whole "nothing repacks anything" claim rests on this. md380_vocoder's
// plain entry points take and return the 49-bit codeword packed into seven bytes
// MSB-first, and that is exactly what internal/bus/frames calls canonical — so
// Encode's output goes straight to the frame layer with no conversion.
//
// If frames ever changed its canonical width this test fails here rather than in
// the audio, which is the only place the mistake would otherwise show up.
func TestCodewordWidthMatchesTheFrameLayer(t *testing.T) {
	if CodewordBytes != frames.AMBEBytes {
		t.Fatalf("CodewordBytes = %d but frames.AMBEBytes = %d; "+
			"the vocoder and the bus no longer agree on a codeword",
			CodewordBytes, frames.AMBEBytes)
	}
}

// One frame is 20 ms of 8 kHz audio and there is no such thing as a partial one.
// These three constants have to stay consistent or every length check in the
// package is checking the wrong thing.
func TestFrameGeometryIsSelfConsistent(t *testing.T) {
	if PCMBytes != SamplesPerFrame*2 {
		t.Errorf("PCMBytes = %d, want %d", PCMBytes, SamplesPerFrame*2)
	}
	if got := time.Duration(SamplesPerFrame) * time.Second / 8000; got != FrameDuration {
		t.Errorf("%d samples at 8 kHz is %v, but FrameDuration is %v", SamplesPerFrame, got, FrameDuration)
	}
}

func TestOpenRequiresBothImagePaths(t *testing.T) {
	for _, cfg := range []Config{
		{},
		{FirmwarePath: "/tmp/fw.img"},
		{RAMPath: "/tmp/ram.img"},
	} {
		if _, err := Open(cfg); err == nil {
			t.Errorf("Open(%+v) succeeded; both images are required", cfg)
		}
	}
}
