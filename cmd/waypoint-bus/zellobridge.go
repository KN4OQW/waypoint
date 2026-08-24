package main

import (
	"fmt"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/bus/zello"
	"github.com/KN4OQW/waypoint/internal/config"
)

// zellobridge.go is the transcode between the bus's AMBE+2 codewords and Zello's
// Opus. It is the only place on the bus where audio is decoded, and it lives here
// at the endpoint edge rather than in internal/bus/router precisely so the
// router's promise — that it touches no codec — survives this feature.
//
// The buffering is the whole problem. Neither side's frame size divides the
// other's cleanly at every setting: the bus hands over codewords in groups of
// three (60 ms) while a Zello packet may be 5, 10, 20, 40 or 60 ms, and inbound
// Opus packets carry whatever the far end chose. So each direction buffers PCM at
// 8 kHz — the vocoder's rate, and the smaller of the two — and emits whole units
// only.
//
// The interfaces exist so this logic is testable without the licensed firmware
// blob or a network. Both real implementations are ARM-and-tag-only; the
// arithmetic here is neither, and it is where the mistakes would be.

// vocoderCodec is the AMBE+2 half. internal/vocoder.Vocoder satisfies it.
type vocoderCodec interface {
	Encode(pcm []int16) ([]byte, error)
	Decode(codeword []byte) ([]int16, error)
}

// opusCodec is the Zello half. internal/bus/zello's Encoder and Decoder satisfy
// the two halves of it.
type opusEncoderIface interface {
	Encode(pcm []int16) ([]byte, error)
	FrameSamples() int
}
type opusDecoderIface interface {
	Decode(packet []byte) ([]int16, error)
}

// vocoderRate is the vocoder's sample rate and the bridge's internal one. Zello
// runs at 16 kHz, so every crossing resamples; see internal/bus/zello.
const vocoderRate = 8000

// vocoderFrameSamples is one AMBE codeword's worth of audio: 20 ms at 8 kHz.
const vocoderFrameSamples = 160

// codewordsPerBusFrame is how many codewords the bus hands over at a time for a
// Zello endpoint. It is DMR's cadence, and it is chosen rather than inherited:
// three codewords is 60 ms, which is exactly Zello's default packet duration, so
// the common configuration buffers nothing at all in the outbound direction.
const codewordsPerBusFrame = 3

// zelloBridge holds the buffered state for one Zello endpoint. One per endpoint,
// never shared: the vocoder carries model state across frames in both directions,
// so two endpoints sharing one would corrupt each other's audio.
type zelloBridge struct {
	voc      vocoderCodec
	enc      opusEncoderIface
	dec      opusDecoderIface
	packetMS int

	// toZello is PCM at 8 kHz awaiting enough samples for one Opus packet.
	toZello []int16

	// toBus is PCM at 8 kHz awaiting enough samples for one codeword, and the
	// codewords awaiting a full bus frame.
	toBus     []int16
	pendingCW [][]byte
}

func newZelloBridge(voc vocoderCodec, enc opusEncoderIface, dec opusDecoderIface, packetMS int) *zelloBridge {
	return &zelloBridge{voc: voc, enc: enc, dec: dec, packetMS: packetMS}
}

// packetSamples8k is how many 8 kHz samples make one Opus packet at this
// endpoint's duration.
func (b *zelloBridge) packetSamples8k() int { return vocoderRate * b.packetMS / 1000 }

// FromBus turns codewords the bus emitted into Opus packets to send.
//
// It returns whole packets only. A 20 ms endpoint gets three packets from one bus
// frame; a 60 ms endpoint gets one; a 40 ms endpoint gets one now and the
// remainder next time. Nothing is padded to make a packet: silence invented here
// would be audible as a stutter at the far end, and the next frame is 60 ms away.
func (b *zelloBridge) FromBus(codewords [][]byte) ([][]byte, error) {
	for _, cw := range codewords {
		pcm, err := b.voc.Decode(cw)
		if err != nil {
			return nil, fmt.Errorf("bridge: decoding a codeword for Zello: %w", err)
		}
		b.toZello = append(b.toZello, pcm...)
	}

	want := b.packetSamples8k()
	var out [][]byte
	for len(b.toZello) >= want {
		wide := zello.Upsample8to16(b.toZello[:want])
		if got := b.enc.FrameSamples(); len(wide) != got {
			return nil, fmt.Errorf("bridge: %d ms is %d samples at 16 kHz but the encoder wants %d",
				b.packetMS, len(wide), got)
		}
		pkt, err := b.enc.Encode(wide)
		if err != nil {
			return nil, fmt.Errorf("bridge: Opus encode: %w", err)
		}
		// Copied: the encoder reuses its output buffer, so keeping the slice
		// would hand every packet the last one's bytes.
		cp := make([]byte, len(pkt))
		copy(cp, pkt)
		out = append(out, cp)
		b.toZello = b.toZello[want:]
	}
	return out, nil
}

// ToBus turns one inbound Opus packet into whole bus frames' worth of codewords.
//
// The grouping matters as much as the transcode. The router reframes a voice
// frame to each destination's cadence, but it can only do that with the codewords
// a frame actually carries, so handing it a partial group would put a short frame
// on the air. Whole groups only; the remainder waits for the next packet.
func (b *zelloBridge) ToBus(packet []byte) ([][][]byte, error) {
	wide, err := b.dec.Decode(packet)
	if err != nil {
		return nil, fmt.Errorf("bridge: Opus decode: %w", err)
	}
	b.toBus = append(b.toBus, zello.Downsample16to8(wide)...)

	for len(b.toBus) >= vocoderFrameSamples {
		cw, err := b.voc.Encode(b.toBus[:vocoderFrameSamples])
		if err != nil {
			return nil, fmt.Errorf("bridge: encoding a codeword from Zello: %w", err)
		}
		b.pendingCW = append(b.pendingCW, cw)
		b.toBus = b.toBus[vocoderFrameSamples:]
	}

	var out [][][]byte
	for len(b.pendingCW) >= codewordsPerBusFrame {
		grp := make([][]byte, codewordsPerBusFrame)
		copy(grp, b.pendingCW[:codewordsPerBusFrame])
		out = append(out, grp)
		b.pendingCW = b.pendingCW[codewordsPerBusFrame:]
	}
	return out, nil
}

// Reset drops buffered audio at the end of a transmission.
//
// The partial frame is discarded rather than flushed. It is under 60 ms of the
// tail of an over, and padding it to a whole frame would put invented audio on
// the air after the operator stopped talking. The vocoder's own model state is
// not reset — it has no entry point for that, which is why the decoder's first
// frame after a gap is near-silent.
func (b *zelloBridge) Reset() {
	b.toZello = b.toZello[:0]
	b.toBus = b.toBus[:0]
	b.pendingCW = b.pendingCW[:0]
}

// zelloMode is the synthetic bus mode one Zello endpoint occupies.
//
// The router keys attachments, arbitration and its loop rules by config.Mode, and
// treats it as an opaque token — so a Zello endpoint joins the bus as an ordinary
// attachment under a name no RF mode can collide with, and inherits RFC-0003 §5
// whole: never emitting to its own source, losing to a holder with a bus_busy
// event, releasing on the hang time. None of that is reimplemented here, which
// was the point.
//
// The prefix is what keeps it distinct. config.Mode's real values are bare tokens
// ("dmr", "ysf"), so a colon cannot collide with one, and the row id after it
// keeps two channels on the same bus apart.
func zelloMode(id string) config.Mode { return config.Mode("zello:" + id) }

// zelloAttachment is the router edge for one Zello endpoint.
//
// FMode is DMR's, and that is a statement about cadence rather than about mode.
// The reframer's only use of it is CodewordsPerFrame, and DMR's three codewords
// per frame is 60 ms — Zello's own default packet duration. Nothing constructs a
// DMR burst from this attachment: the daemon dispatches on Dst, and a Zello
// destination goes to the bridge above instead of to a loopback.
func zelloAttachment(id string) (mode config.Mode, fmode frames.Mode) {
	return zelloMode(id), frames.ModeDMR
}
