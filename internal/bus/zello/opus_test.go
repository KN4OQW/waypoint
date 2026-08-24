//go:build zello

package zello

import (
	"math"
	"testing"
)

func tonePCM(rate, ms int, hz float64) []int16 {
	n := rate * ms / 1000
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = int16(10000 * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
	}
	return pcm
}

func rms(v []int16) float64 {
	if len(v) == 0 {
		return 0
	}
	var e float64
	for _, s := range v {
		e += float64(s) * float64(s)
	}
	return math.Sqrt(e / float64(len(v)))
}

// The defaults must produce Zello's own documented header, because that string
// is the one value in the protocol we can check against the vendor rather than
// against ourselves.
func TestDefaultEncoderProducesTheDocumentedCodecHeader(t *testing.T) {
	e, err := NewEncoder(DefaultSampleRate, DefaultFrameMS)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	if got := e.CodecHeader().Encode(); got != "gD4BPA==" {
		t.Errorf("codec_header = %q, want gD4BPA==", got)
	}
	if got, want := e.FrameSamples(), 960; got != want {
		t.Errorf("FrameSamples() = %d, want %d (60 ms at 16 kHz)", got, want)
	}
}

// Opus is lossy and a round trip will not be sample-identical, but speech that
// goes in has to come out carrying the same energy. This is the audio path the
// bridge depends on.
func TestOpusRoundTripPreservesTheSignal(t *testing.T) {
	for _, frameMS := range []int{20, 60} {
		enc, err := NewEncoder(DefaultSampleRate, frameMS)
		if err != nil {
			t.Fatalf("NewEncoder(%d): %v", frameMS, err)
		}
		dec, err := NewDecoder(DefaultSampleRate)
		if err != nil {
			t.Fatalf("NewDecoder: %v", err)
		}

		// Several frames: Opus carries prediction state, and the first packet
		// out of a fresh encoder is not representative.
		var last []int16
		for i := 0; i < 10; i++ {
			pkt, err := enc.Encode(tonePCM(DefaultSampleRate, frameMS, 440))
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if len(pkt) == 0 {
				t.Fatal("Encode produced an empty packet")
			}
			last, err = dec.Decode(pkt)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
		}
		want := DefaultSampleRate * frameMS / 1000
		if len(last) != want {
			t.Errorf("%d ms frame decoded to %d samples, want %d", frameMS, len(last), want)
		}
		ratio := rms(last) / rms(tonePCM(DefaultSampleRate, frameMS, 440))
		if ratio < 0.5 || ratio > 2.0 {
			t.Errorf("%d ms round-trip energy ratio %.2f is outside 0.5-2.0", frameMS, ratio)
		}
	}
}

// A duration inside Zello's 2.5-60 ms range but not an Opus frame size is
// refused here. libopus would reject it later with an error that does not say
// which value was wrong.
func TestEncoderRefusesADurationOpusDoesNotHave(t *testing.T) {
	for _, ms := range []int{0, 3, 30, 45, 61} {
		if _, err := NewEncoder(DefaultSampleRate, ms); err == nil {
			t.Errorf("NewEncoder accepted %d ms", ms)
		}
	}
}

func TestEncodeRefusesTheWrongFrameLength(t *testing.T) {
	e, err := NewEncoder(DefaultSampleRate, 20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Encode(make([]int16, e.FrameSamples()-1)); err == nil {
		t.Error("a short frame was accepted")
	}
	if _, err := e.Encode(make([]int16, e.FrameSamples()+1)); err == nil {
		t.Error("an oversized frame was accepted")
	}
}

func TestDecodeRefusesAnEmptyPacket(t *testing.T) {
	d, err := NewDecoder(DefaultSampleRate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Decode(nil); err == nil {
		t.Error("an empty packet was accepted")
	}
}

// The whole RF-to-Zello chain in one test: 8 kHz frames from the vocoder,
// upsampled, Opus-encoded, decoded, and downsampled back. Three 20 ms vocoder
// frames make one 60 ms Opus packet, which is the default framing.
func TestTheFullAudioPathSurvivesAtDefaultFraming(t *testing.T) {
	enc, err := NewEncoder(DefaultSampleRate, DefaultFrameMS)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewDecoder(DefaultSampleRate)
	if err != nil {
		t.Fatal(err)
	}

	// 60 ms at 8 kHz, the three 20 ms vocoder frames a Zello packet covers.
	src := tonePCM(8000, DefaultFrameMS, 440)
	var out []int16
	for i := 0; i < 10; i++ {
		pkt, err := enc.Encode(Upsample8to16(src))
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		wide, err := dec.Decode(pkt)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		out = Downsample16to8(wide)
	}
	if len(out) != len(src) {
		t.Fatalf("the path changed the frame length: %d -> %d", len(src), len(out))
	}
	if ratio := rms(out) / rms(src); ratio < 0.5 || ratio > 2.0 {
		t.Errorf("end-to-end energy ratio %.2f is outside 0.5-2.0", ratio)
	}
}
