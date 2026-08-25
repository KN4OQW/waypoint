package main

import (
	"fmt"
	"strings"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/bus/peer"
	"github.com/KN4OQW/waypoint/internal/bus/zello"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/mqtt"
	"github.com/KN4OQW/waypoint/internal/talkeralias"
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
	SampleRate() int
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
	// Resampled from whatever the far end announced, not from an assumed 16 kHz.
	// A stream at another rate decoded fine and then played at the wrong speed —
	// audible as everyone sounding wrong, with nothing logged.
	b.toBus = append(b.toBus, zello.ResampleTo8k(wide, b.dec.SampleRate())...)

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

// --- the seam between the run loop and the codecs -----------------------------

// zelloSink is one live Zello endpoint as the run loop sees it. The run loop is
// built untagged and knows nothing about libopus or the vocoder; everything
// tag-gated sits behind this.
type zelloSink interface {
	// Emit hands the run loop's emission for this endpoint to the channel. It is
	// called with header, voice and terminator frames alike, because the stream
	// bracketing Zello needs is derived from them.
	Emit(f frames.Frame) error
	// Close ends the connection.
	Close() error
}

// newZelloSink builds an endpoint. It is a variable so the tagged build can
// replace it: an untagged binary refuses rather than silently running a bus with
// its Zello channels missing, because a bridge that is quietly not bridging looks
// exactly like one whose far end is idle.
var newZelloSink = func(config.BusZello, config.BusVocoder, uint32, func(frames.Frame)) (zelloSink, error) {
	return nil, fmt.Errorf("bus: this build has no Zello support; rebuild with -tags zello")
}

// envFor builds the cross-peer envelope for a frame that entered the cluster at
// this node on a Zello endpoint. It is the same envelope a loopback datagram
// gets, so RFC-0016's cross-peer loop prevention treats Zello-origin audio
// exactly like RF-origin audio and a peered bus cannot send it back.
func envFor(node string, mode config.Mode, busID string) *peer.Envelope {
	e := peer.NewEnvelope(node, string(mode), busID)
	return &e
}

// --- Talker Alias for inbound Zello audio -------------------------------------

// zelloAliaser says who is talking on an inbound Zello transmission, so a radio
// shows a name rather than only the node's own ID.
//
// Every inbound Zello transmission is sourced from one DMR ID — the node's own —
// because a Zello user without a DMR registration has no ID to borrow and one who
// has an ID has not authorised this node to transmit as them. The alias is
// therefore the only thing that says who is actually speaking, which is what that
// field exists for.
//
// The name comes off the wire, from the `from` on Zello's own on_stream_start,
// carried here on the frame's SrcCallsign. It is NOT a phonebook lookup: the bus
// daemon reads only rendered config, and internal/config's phonebook isolation
// test makes it a rule that no phonebook row is ever rendered into one. So an
// operator's callsign cannot reach this daemon, and the Zello handle is both what
// is available and what is true.
//
// # Why this ANNOUNCES rather than transmits
//
// It used to build the DMRA frames here and write them to the DMR attachment's
// endpoint. That could never have worked, and the correction is worth recording
// because reading the code does not reveal it — issue #279.
//
// This daemon's DMR attachment is a Homebrew MASTER that the local DMRGateway logs
// into, so anything written to it goes to DMRGateway, not to MMDVM-Host. And
// DMRGateway's Talker Alias path runs one way only: CDMRGateway::processTalkerAlias
// reads from the repeater (CMMDVMNetwork::readTalkerAlias) and writes to the
// upstream networks (CDMRNetwork::writeTalkerAlias). At the pinned 79edbc4 the
// network side has no DMRA case at all — CDMRNetwork::clock knows DMRD, MSTNAK,
// RPTACK, MSTCL, MSTPONG and RPTSBKN — so an alias arriving from a bus falls into
// the closing else and becomes CUtils::dump("Unknown packet from the master").
//
// The ordering was wrong as well, independently: the frames went out BEFORE the
// voice header, because handleFrame emitted them ahead of the router, and the
// fork's CDMRSlot::setTalkerAlias drops any block whose slot is not already in
// RPT_NET_STATE::AUDIO for that source. Even a DMRGateway that forwarded them
// would have shown nothing.
//
// So the name is published instead, and waypointd injects it at the DMR relay —
// the only address MMDVM-Host accepts DMR-network datagrams from, and a seam whose
// taps run after a datagram has been forwarded, which makes the ordering right by
// construction rather than by timing.
type zelloAliaser struct {
	template talkeralias.Template
	srcID    uint32
	// sent remembers which streams have already had their alias, so it goes out
	// once per transmission rather than once per frame.
	sent map[uint32]bool
}

func newZelloAliaser(template string, srcID uint32) *zelloAliaser {
	t := talkeralias.Template(template)
	if t == talkeralias.TemplateOff || !t.Valid() || srcID == 0 {
		return nil // off, unrecognised, or no ID to attribute it to: emit nothing
	}
	return &zelloAliaser{template: t, srcID: srcID, sent: map[uint32]bool{}}
}

// noteFor returns the announcement for f, or ok=false.
//
// ok=false is the common answer — every frame after the first of a transmission,
// and every transmission whose talker Zello did not name — and the caller treats
// it as nothing to do rather than as a failure.
//
// The name is carried untouched. It is a display name whose case is part of it,
// and the receiving end knows not to run it through a callsign template; see
// talkeralias.Emitter.Announce, which is where that decision now lives.
func (a *zelloAliaser) noteFor(f frames.Frame) (mqtt.TalkerAliasNote, bool) {
	if a == nil || f.Kind == frames.KindTerminator {
		if a != nil && f.Kind == frames.KindTerminator {
			delete(a.sent, f.Stream.ID)
		}
		return mqtt.TalkerAliasNote{}, false
	}
	if f.SrcCallsign == "" || f.Stream.ID == 0 || a.sent[f.Stream.ID] {
		return mqtt.TalkerAliasNote{}, false
	}
	text := strings.TrimSpace(f.SrcCallsign)
	if text == "" {
		return mqtt.TalkerAliasNote{}, false
	}
	a.sent[f.Stream.ID] = true
	return mqtt.TalkerAliasNote{
		Type:     mqtt.TalkerAliasNoteType,
		StreamID: f.Stream.ID,
		SrcID:    a.srcID,
		Name:     text,
	}, true
}

// SetDecoder replaces the inbound Opus decoder, for when a stream announces a
// rate other than the one this bridge was built with. Called between
// transmissions, never during one.
func (b *zelloBridge) SetDecoder(d opusDecoderIface) {
	b.dec = d
	b.toBus = b.toBus[:0]
	b.pendingCW = b.pendingCW[:0]
}
