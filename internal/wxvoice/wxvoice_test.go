package wxvoice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
)

// buildWAV makes the minimal RIFF a synthesiser emits, so decodeWAV is tested
// against the shape it will actually meet.
func buildWAV(t *testing.T, pcm []int16, rate, channels int) []byte {
	t.Helper()
	var data bytes.Buffer
	for _, s := range pcm {
		for c := 0; c < channels; c++ {
			if err := binary.Write(&data, binary.LittleEndian, s); err != nil {
				t.Fatal(err)
			}
		}
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+data.Len()))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))        // PCM
	binary.Write(&b, binary.LittleEndian, uint16(channels)) //
	binary.Write(&b, binary.LittleEndian, uint32(rate))     //
	binary.Write(&b, binary.LittleEndian, uint32(rate*2*channels))
	binary.Write(&b, binary.LittleEndian, uint16(2*channels))
	binary.Write(&b, binary.LittleEndian, uint16(16)) // bits
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(data.Len()))
	b.Write(data.Bytes())
	return b.Bytes()
}

func TestDecodeWAV(t *testing.T) {
	want := []int16{0, 1000, -1000, 32767, -32768}
	got, rate, err := decodeWAV(buildWAV(t, want, 22050, 1))
	if err != nil {
		t.Fatalf("decodeWAV: %v", err)
	}
	if rate != 22050 {
		t.Errorf("rate = %d, want 22050", rate)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestDecodeWAVTakesTheFirstChannelOfStereo(t *testing.T) {
	want := []int16{5, 6, 7}
	got, _, err := decodeWAV(buildWAV(t, want, 22050, 2))
	if err != nil {
		t.Fatalf("decodeWAV: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("stereo decoded to %d samples, want %d", len(got), len(want))
	}
}

func TestDecodeWAVRefusesWhatItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
	}{
		{"not a wav", []byte("hello there, this is not a riff file at all!!")},
		{"truncated", []byte("RIFF")},
	} {
		if _, _, err := decodeWAV(tc.in); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

// Resampling to DMR's rate is the step between a synthesiser and a vocoder, and
// getting the length wrong shows up as speech that is too fast or too slow.
func TestResampleLengthAndShape(t *testing.T) {
	// One second of a low tone at 22.05 kHz.
	const from, to = 22050, DMRSampleRate
	in := make([]int16, from)
	for i := range in {
		in[i] = int16(10000 * math.Sin(2*math.Pi*100*float64(i)/float64(from)))
	}
	out := resampleTo(in, from, to)

	if got, want := len(out), to; math.Abs(float64(got-want)) > 2 {
		t.Errorf("one second resampled to %d samples, want about %d", got, want)
	}
	// The tone should survive: peak amplitude within a sensible band of the
	// input's. A resampler that returned silence or clipped would fail here.
	var peak int16
	for _, s := range out {
		if s > peak {
			peak = s
		}
	}
	if peak < 8000 || peak > 11000 {
		t.Errorf("peak after resampling = %d, want near 10000", peak)
	}
	// Same rate in and out must not copy or alter anything.
	if same := resampleTo(in, from, from); len(same) != len(in) {
		t.Errorf("same-rate resample changed length")
	}
}

// The stock node's state. It must be an identifiable error, not a silent
// success, or a queue drains into nowhere and nobody knows.
func TestNullVocoderReportsHonestly(t *testing.T) {
	_, err := NullVocoder{}.Encode(context.Background(), []int16{1, 2, 3})
	if !errors.Is(err, ErrNoVocoder) {
		t.Fatalf("err = %v, want ErrNoVocoder", err)
	}
	if VocoderFor(Config{Vocoder: "none"}).Name() != "none" {
		t.Error("VocoderFor(none) is not the null vocoder")
	}
	// A backend that is named but unimplemented must also report none rather
	// than pretend, so the panel can show that the setting has not taken effect.
	if VocoderFor(Config{Vocoder: "dongle", DongleDevice: "/dev/ttyUSB0"}).Name() != "none" {
		t.Error("the dongle backend claims to work; it is not implemented")
	}
}

func TestExternalVocoderSplitsCodewords(t *testing.T) {
	// A stand-in vocoder: emit 18 bytes, i.e. two 9-byte codewords.
	v := ExternalVocoder{Command: "printf '%018d' 0", CodewordBytes: 9}
	got, err := v.Encode(context.Background(), []int16{1, 2, 3})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d codewords, want 2", len(got))
	}
	for i, cw := range got {
		if len(cw) != 9 {
			t.Errorf("codeword %d is %d bytes, want 9", i, len(cw))
		}
	}
}

// A partial trailing codeword means the contract was not met. Transmitting it
// would put a burst of noise on the air, so it is refused.
func TestExternalVocoderRefusesAPartialCodeword(t *testing.T) {
	v := ExternalVocoder{Command: "printf '%010d' 0", CodewordBytes: 9}
	_, err := v.Encode(context.Background(), []int16{1})
	if err == nil {
		t.Fatal("accepted 10 bytes as 9-byte codewords")
	}
	if !strings.Contains(err.Error(), "multiple") {
		t.Errorf("err = %v, want it to explain the size mismatch", err)
	}
}

func TestExternalVocoderSurfacesFailure(t *testing.T) {
	v := ExternalVocoder{Command: "echo problem >&2; exit 3"}
	if _, err := v.Encode(context.Background(), []int16{1}); err == nil {
		t.Fatal("a failing vocoder reported success")
	}
	if _, err := (ExternalVocoder{}).Encode(context.Background(), []int16{1}); !errors.Is(err, ErrNoVocoder) {
		t.Errorf("an empty command should report ErrNoVocoder, got %v", err)
	}
}

func TestSynthesizeNeedsAModel(t *testing.T) {
	if _, err := Synthesize(context.Background(), Config{PiperPath: "true"}, "hello"); err == nil {
		t.Fatal("synthesised without a voice model")
	}
}
