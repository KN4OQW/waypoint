//go:build zello

package zello

// Opus, via cgo.
//
// pion/opus is pure Go but decode-only and SILK-only — its own documentation
// says "It currently only supports the SILK codec, not the CELT codec" and it
// exposes no encoder. The RF-to-Zello direction needs an encoder, so this uses
// hraban/opus over libopus and accepts the cgo cost, which is why the whole
// feature sits behind the `zello` build tag: a node with no Zello channel builds
// pure Go and cross-compiles as before.
//
// libopusfile is not used and is not present on every build host, so builds add
// hraban's tag for it:
//
//	go build -tags "zello nolibopusfile" ./cmd/waypoint-bus

import (
	"fmt"

	hopus "gopkg.in/hraban/opus.v2"
)

// Zello's own default, from the documented codec_header example gD4BPA==.
// 60 ms costs latency and saves bandwidth; 20 ms is the other way round. The
// endpoint exposes it as a config key rather than choosing for the operator.
const (
	DefaultSampleRate = 16000
	DefaultFrameMS    = 60
)

// validFrameMS are the frame sizes Opus itself accepts. Zello's documented range
// is 2.5-60 ms, and every legal Opus size in that range is here. A duration that
// is inside Zello's range but not an Opus frame size — 30 ms, say — is refused
// rather than rounded, because libopus rejects it at encode time with an error
// that says nothing about which value was wrong.
var validFrameMS = map[int]bool{5: true, 10: true, 20: true, 40: true, 60: true}

// Encoder turns PCM into Opus packets for one outbound stream.
//
// One per stream, not one per client: Opus carries prediction state across
// packets, so feeding two streams through one encoder makes each one's audio
// depend on the other's.
type Encoder struct {
	enc        *hopus.Encoder
	sampleRate int
	frameMS    int
	buf        []byte
}

// NewEncoder builds an encoder for a given rate and frame size.
func NewEncoder(sampleRate, frameMS int) (*Encoder, error) {
	if !validFrameMS[frameMS] {
		return nil, fmt.Errorf("zello: %d ms is not an Opus frame size (5, 10, 20, 40 or 60)", frameMS)
	}
	// VoIP rather than Audio: this is speech that has already been through
	// AMBE+2 at 8 kHz, and the VoIP mode's bias towards intelligibility is the
	// right trade for it.
	enc, err := hopus.NewEncoder(sampleRate, 1, hopus.AppVoIP)
	if err != nil {
		return nil, fmt.Errorf("zello: creating an Opus encoder at %d Hz: %w", sampleRate, err)
	}
	return &Encoder{
		enc:        enc,
		sampleRate: sampleRate,
		frameMS:    frameMS,
		// Generous: a 60 ms mono frame at 16 kHz never approaches this, and
		// libopus fails rather than truncating if the buffer is short.
		buf: make([]byte, 4000),
	}, nil
}

// FrameSamples is how many samples one packet must carry.
func (e *Encoder) FrameSamples() int { return e.sampleRate * e.frameMS / 1000 }

// Encode compresses exactly one frame. The returned slice is only valid until
// the next call.
func (e *Encoder) Encode(pcm []int16) ([]byte, error) {
	if want := e.FrameSamples(); len(pcm) != want {
		return nil, fmt.Errorf("zello: Opus encode needs exactly %d samples for a %d ms frame at %d Hz, got %d",
			want, e.frameMS, e.sampleRate, len(pcm))
	}
	n, err := e.enc.Encode(pcm, e.buf)
	if err != nil {
		return nil, fmt.Errorf("zello: Opus encode: %w", err)
	}
	return e.buf[:n], nil
}

// CodecHeader describes this encoder's output for start_stream.
func (e *Encoder) CodecHeader() CodecHeader {
	return CodecHeader{
		SampleRateHz:    uint16(e.sampleRate),
		FramesPerPacket: 1,
		FrameSizeMS:     uint8(e.frameMS),
	}
}

// Decoder turns inbound Opus packets back into PCM, one per inbound stream for
// the same reason the encoder is per-stream.
type Decoder struct {
	dec        *hopus.Decoder
	sampleRate int
}

// NewDecoder builds a decoder for the rate an on_stream_start advertised.
func NewDecoder(sampleRate int) (*Decoder, error) {
	dec, err := hopus.NewDecoder(sampleRate, 1)
	if err != nil {
		return nil, fmt.Errorf("zello: creating an Opus decoder at %d Hz: %w", sampleRate, err)
	}
	return &Decoder{dec: dec, sampleRate: sampleRate}, nil
}

// Decode expands one packet. maxFrameMS bounds the output buffer; 60 covers
// every size Zello permits.
func (d *Decoder) Decode(pkt []byte) ([]int16, error) {
	if len(pkt) == 0 {
		return nil, fmt.Errorf("zello: Opus decode: empty packet")
	}
	pcm := make([]int16, d.sampleRate*60/1000)
	n, err := d.dec.Decode(pkt, pcm)
	if err != nil {
		return nil, fmt.Errorf("zello: Opus decode: %w", err)
	}
	return pcm[:n], nil
}
