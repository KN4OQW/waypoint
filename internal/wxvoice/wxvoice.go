// Package wxvoice turns an alert into spoken audio and, if a vocoder is
// available, into the AMBE codewords a DMR radio can play.
//
// # The pipeline, and where it stops today
//
//	text ──▶ Piper ──▶ 22.05 kHz PCM ──▶ resample ──▶ 8 kHz PCM ──▶ vocoder ──▶ AMBE
//	                                                                            │
//	                          internal/bus/frames.ConstructDMR ◀────────────────┘
//
// Everything downstream of the vocoder already exists and is tested: frames/dmr.go
// builds DMR voice bursts from AMBE codewords, including the placement of the
// three codewords per burst and the second one straddling the sync. The shim
// puts them on the air. So the vocoder is the only missing link, and it is
// missing for a reason rather than an oversight — turning PCM into AMBE needs a
// vocoder an operator is entitled to run, and this project does not ship one.
//
// # Why every path here is configuration
//
// The synthesiser, its model, the speaking rate and the vocoder backend are all
// things an operator sites differently, and several of them are things Waypoint
// cannot ship. A build-time constant would mean a node that cannot speak has no
// way to be told where its voice lives. So this package reads a config struct
// and holds no defaults of its own beyond what that struct carries.
package wxvoice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DMRSampleRate is what a DMR vocoder consumes: 8 kHz, signed 16-bit, mono.
// This is a property of the protocol, not a preference, so it is a constant.
const DMRSampleRate = 8000

// ErrNoVocoder is returned when speech was synthesised but there is no way to
// encode it for the air. It is a distinct error because it is the expected
// state of a stock node, and a caller should log it once rather than treat it
// as a fault.
var ErrNoVocoder = errors.New("wxvoice: no vocoder configured; nothing can be transmitted")

// Config is the subset of the store this package needs. It mirrors
// config.WXVoice rather than importing it, so the audio path can be tested
// without a store and so a dependency does not run the wrong way.
type Config struct {
	PiperPath   string
	ModelPath   string
	Speaker     int
	LengthScale float64

	Vocoder         string
	DongleDevice    string
	ExternalCommand string
}

// Vocoder turns 8 kHz mono PCM into AMBE codewords, one 9-byte codeword per
// 20 ms of audio.
//
// The interface is deliberately this narrow. It is the whole contract an
// operator's chosen vocoder has to satisfy, whether that is a USB dongle, a
// command they supply, or something not invented yet.
type Vocoder interface {
	Encode(ctx context.Context, pcm []int16) ([][]byte, error)
	// Name is what the panel shows.
	Name() string
}

// NullVocoder is what a stock node has. It reports honestly rather than
// pretending: a caller gets ErrNoVocoder and can log a skip, which is far better
// than a queue that silently never drains.
type NullVocoder struct{}

func (NullVocoder) Name() string { return "none" }
func (NullVocoder) Encode(context.Context, []int16) ([][]byte, error) {
	return nil, ErrNoVocoder
}

// ExternalVocoder runs an operator-supplied command. The contract is 8 kHz
// signed 16-bit little-endian mono PCM on stdin, raw AMBE codewords on stdout.
//
// Narrow on purpose: it lets an operator use whatever vocoder they are entitled
// to run without this project shipping, bundling or endorsing a particular one.
type ExternalVocoder struct {
	Command string
	// CodewordBytes is how many bytes one codeword occupies on stdout. DMR's
	// AMBE+2 at 2450x1150 is 49 bits of voice plus FEC, carried as 9 bytes by
	// every implementation this has been checked against — but it is a field
	// rather than a constant because "every implementation checked" is two.
	CodewordBytes int
}

func (e ExternalVocoder) Name() string { return "external: " + e.Command }

func (e ExternalVocoder) Encode(ctx context.Context, pcm []int16) ([][]byte, error) {
	if strings.TrimSpace(e.Command) == "" {
		return nil, ErrNoVocoder
	}
	size := e.CodewordBytes
	if size <= 0 {
		size = 9
	}

	var in bytes.Buffer
	for _, s := range pcm {
		if err := binary.Write(&in, binary.LittleEndian, s); err != nil {
			return nil, err
		}
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", e.Command)
	cmd.Stdin = &in
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("wxvoice: vocoder command failed: %w (%s)", err, strings.TrimSpace(errBuf.String()))
	}

	raw := out.Bytes()
	if len(raw) == 0 {
		return nil, fmt.Errorf("wxvoice: vocoder produced no output")
	}
	// A partial trailing codeword means the contract was not met. Refusing is
	// better than transmitting a truncated frame, which a radio renders as a
	// burst of noise.
	if len(raw)%size != 0 {
		return nil, fmt.Errorf("wxvoice: vocoder produced %d bytes, not a multiple of the %d-byte codeword", len(raw), size)
	}
	out2 := make([][]byte, 0, len(raw)/size)
	for i := 0; i+size <= len(raw); i += size {
		cw := make([]byte, size)
		copy(cw, raw[i:i+size])
		out2 = append(out2, cw)
	}
	return out2, nil
}

// VocoderFor builds the backend the configuration asks for.
func VocoderFor(c Config) Vocoder {
	switch strings.ToLower(strings.TrimSpace(c.Vocoder)) {
	case "external":
		return ExternalVocoder{Command: c.ExternalCommand}
	case "dongle":
		// An AMBE-3000 style device speaks a framed serial protocol; it is a
		// backend worth having and is not implemented here. Reporting none is
		// honest, and the panel shows the name so an operator can see that the
		// setting has not taken effect.
		return NullVocoder{}
	default:
		return NullVocoder{}
	}
}

// Synthesize runs Piper over text and returns 8 kHz mono PCM ready for a
// vocoder.
//
// Piper writes a WAV to stdout when asked for "-", at whatever rate its model
// was trained at — commonly 22.05 kHz. DMR wants 8 kHz, so the result is
// resampled here rather than by asking Piper for a rate it may not support.
func Synthesize(ctx context.Context, c Config, text string) ([]int16, error) {
	bin := strings.TrimSpace(c.PiperPath)
	if bin == "" {
		bin = "piper"
	}
	if strings.TrimSpace(c.ModelPath) == "" {
		return nil, fmt.Errorf("wxvoice: no voice model configured")
	}

	args := []string{"--model", c.ModelPath, "--output_file", "-"}
	if c.Speaker >= 0 {
		args = append(args, "--speaker", strconv.Itoa(c.Speaker))
	}
	if c.LengthScale > 0 && c.LengthScale != 1 {
		args = append(args, "--length_scale", strconv.FormatFloat(c.LengthScale, 'f', -1, 64))
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = strings.NewReader(text)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("wxvoice: piper failed: %w (%s)", err, strings.TrimSpace(errBuf.String()))
	}

	pcm, rate, err := decodeWAV(out.Bytes())
	if err != nil {
		return nil, err
	}
	return resampleTo(pcm, rate, DMRSampleRate), nil
}

// decodeWAV reads the minimal RIFF/WAVE a synthesiser emits: PCM, mono, 16-bit.
// A full WAV parser is not wanted here — anything this does not understand is an
// input this package should refuse rather than guess at.
func decodeWAV(b []byte) ([]int16, int, error) {
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("wxvoice: synthesiser did not produce a WAV")
	}
	var rate, channels, bits int
	var data []byte
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := off + 8
		if body+size > len(b) {
			size = len(b) - body
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, 0, fmt.Errorf("wxvoice: short fmt chunk")
			}
			channels = int(binary.LittleEndian.Uint16(b[body+2 : body+4]))
			rate = int(binary.LittleEndian.Uint32(b[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(b[body+14 : body+16]))
		case "data":
			data = b[body : body+size]
		}
		off = body + size
		if size%2 == 1 {
			off++ // RIFF chunks are word-aligned
		}
	}
	if rate == 0 || data == nil {
		return nil, 0, fmt.Errorf("wxvoice: WAV has no format or no samples")
	}
	if bits != 16 {
		return nil, 0, fmt.Errorf("wxvoice: WAV is %d-bit; 16 is required", bits)
	}
	if channels < 1 {
		channels = 1
	}

	n := len(data) / 2 / channels
	pcm := make([]int16, n)
	for i := 0; i < n; i++ {
		// Take the first channel; a synthesiser emitting stereo is unusual but
		// mixing would be a silent quality change rather than a decision.
		j := i * channels * 2
		pcm[i] = int16(binary.LittleEndian.Uint16(data[j : j+2]))
	}
	return pcm, rate, nil
}

// resampleTo converts between sample rates by linear interpolation.
//
// Linear is not the best resampler, and for speech going into a 2450 bit/s
// vocoder it does not need to be: the vocoder's own model is a far larger
// bandwidth limit than the interpolation error. Something better belongs here
// only if a bench recording says it is audible.
func resampleTo(in []int16, from, to int) []int16 {
	if from == to || from <= 0 || len(in) == 0 {
		return in
	}
	ratio := float64(from) / float64(to)
	n := int(float64(len(in)) / ratio)
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		pos := float64(i) * ratio
		j := int(pos)
		if j+1 >= len(in) {
			out[i] = in[len(in)-1]
			continue
		}
		frac := pos - float64(j)
		out[i] = int16(float64(in[j])*(1-frac) + float64(in[j+1])*frac)
	}
	return out
}

// Job is one announcement waiting to be spoken.
type Job struct {
	Text       string
	Talkgroups []uint32
	Queued     time.Time
}
