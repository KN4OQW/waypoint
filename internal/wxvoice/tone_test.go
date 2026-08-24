package wxvoice

import (
	"math"
	"testing"
)

func TestGenerateToneLengthAndLevel(t *testing.T) {
	pcm := GenerateTone(ToneOptions{HzA: 1050, Millis: 1000, Amplitude: 0.5})
	if len(pcm) != DMRSampleRate {
		t.Fatalf("one second of tone is %d samples, want %d", len(pcm), DMRSampleRate)
	}
	var peak int16
	for _, s := range pcm {
		if s > peak {
			peak = s
		}
	}
	// Around half full scale, and definitely not clipping.
	if peak < 12000 || peak > 17000 {
		t.Errorf("peak = %d, want roughly half of full scale", peak)
	}
}

// No tone configured must produce nothing, so a caller can prepend blindly.
func TestGenerateToneOffProducesNothing(t *testing.T) {
	for _, o := range []ToneOptions{{}, {HzA: 0, Millis: 500}, {HzA: 1050, Millis: 0}} {
		if got := GenerateTone(o); got != nil {
			t.Errorf("%+v produced %d samples, want none", o, len(got))
		}
	}
}

// Two tones must share the amplitude budget, or the sum clips and a clipped
// tone through a vocoder is far worse than a quiet one.
func TestTwoToneDoesNotClip(t *testing.T) {
	pcm := GenerateTone(ToneOptions{HzA: 853, HzB: 960, Millis: 500, Amplitude: 1})
	for i, s := range pcm {
		if s == math.MaxInt16 || s == math.MinInt16 {
			t.Fatalf("sample %d is at the rail (%d); the two tones clipped", i, s)
		}
	}
}

// A tone that starts at full amplitude clicks, and a click through a vocoder
// splatters. The envelope should ramp both ends.
func TestToneRampsInAndOut(t *testing.T) {
	pcm := GenerateTone(ToneOptions{HzA: 1000, Millis: 500, Amplitude: 0.8})
	if pcm[0] > 200 || pcm[0] < -200 {
		t.Errorf("tone starts at %d, want near zero", pcm[0])
	}
	if last := pcm[len(pcm)-1]; last > 200 || last < -200 {
		t.Errorf("tone ends at %d, want near zero", last)
	}
}

func TestPrependTone(t *testing.T) {
	speech := make([]int16, 800)
	for i := range speech {
		speech[i] = 1000
	}
	tone := GenerateTone(ToneOptions{HzA: 1050, Millis: 250, Amplitude: 0.5})

	got := PrependTone(tone, speech)
	gap := DMRSampleRate * 150 / 1000
	if want := len(tone) + gap + len(speech); len(got) != want {
		t.Errorf("combined length %d, want %d (tone + gap + speech)", len(got), want)
	}
	// The speech must survive intact at the end.
	if got[len(got)-1] != 1000 {
		t.Errorf("speech was altered: last sample %d", got[len(got)-1])
	}
	// And a silent gap must actually separate them.
	mid := len(tone) + gap/2
	if got[mid] != 0 {
		t.Errorf("no silence between tone and speech at %d: %d", mid, got[mid])
	}
	// No tone means speech unchanged.
	if same := PrependTone(nil, speech); len(same) != len(speech) {
		t.Error("PrependTone with no tone changed the speech")
	}
}
