package wxvoice

import (
	"fmt"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
)

// Turning AMBE codewords into a DMR voice transmission.
//
// This is the last hop, and it is short because the hard part is already done:
// frames.ConstructDMR builds the wire frame from a Frame carrying codewords,
// including the three-per-burst placement and the second codeword straddling
// the sync. All that is left is to bracket them correctly and pace them.
//
// # The shape of a transmission
//
// A DMR voice transmission is a voice header, then voice frames each carrying
// three codewords, then a terminator. The header and terminator carry link
// control rather than audio, which is why they hold no AMBE.
//
// # Pacing
//
// A DMR frame is 60 ms of one timeslot's air time, carrying 3 codewords of 20 ms
// each. Frames must be handed over at roughly that rate: sending the whole
// transmission at once overruns MMDVM-Host's jitter buffer and the tail is
// dropped, which sounds like the announcement being cut off mid-word. Sending
// slower starves it and produces gaps.

// FramesPerSecond and the derived interval are the cadence of one DMR timeslot.
const (
	CodewordsPerFrame = 3
	FrameInterval     = 60 * time.Millisecond
)

// Injector is the transmit side of the relay. It matches the message service's
// own Relay so both can be handed the same object.
type Injector interface {
	InjectToHost(datagram []byte) error
}

// TransmitOptions is everything the frame layer needs that is not audio.
type TransmitOptions struct {
	SrcID    uint32
	DstID    uint32
	Slot     uint8
	StreamID uint32
	// Sleep is the pacing function. Tests pass a no-op; production passes
	// time.Sleep. It is a field rather than a call so a test does not spend
	// real seconds proving the frame sequence.
	Sleep func(time.Duration)
}

// Transmit sends codewords as a complete DMR voice transmission.
//
// It returns the number of wire frames written, which is what a caller logs:
// "17 frames" is a claim an operator can check against roughly a second of air
// time, where "sent" is not.
func Transmit(inj Injector, codewords [][]byte, o TransmitOptions) (int, error) {
	if inj == nil {
		return 0, fmt.Errorf("wxvoice: no relay to transmit through")
	}
	if len(codewords) == 0 {
		return 0, fmt.Errorf("wxvoice: nothing to transmit")
	}
	for i, cw := range codewords {
		// Refuse rather than pad. A codeword of the wrong width means the
		// encoder did not meet the contract, and the audible result of guessing
		// is noise on somebody's talkgroup.
		if len(cw) != frames.AMBEBytes {
			return 0, fmt.Errorf("wxvoice: codeword %d is %d bytes, want %d", i, len(cw), frames.AMBEBytes)
		}
	}
	sleep := o.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	slot := o.Slot
	if slot != 1 && slot != 2 {
		slot = 2
	}
	p := frames.Params{Slot: slot, DefaultTG: o.DstID}

	seq := uint8(0)
	// voiceN is the position in the DMR voice superframe, counted over voice
	// frames only. It is NOT the DMRD sequence number: that one counts the
	// header and terminator too, so deriving one from the other would put the
	// sync on the wrong frame and leave a radio unable to assemble link control.
	voiceN := uint8(0)
	sent := 0
	emit := func(kind frames.Kind, ambe [][]byte) error {
		f := frames.Frame{
			Mode: frames.ModeDMR, Kind: kind,
			SrcID: o.SrcID, DstID: o.DstID,
			Stream: frames.Stream{ID: o.StreamID, Seq: seq},
			AMBE:   ambe,
		}
		if kind == frames.KindVoice {
			f.VoiceSeq = voiceN
			voiceN = (voiceN + 1) % frames.DMRVoiceSuperframe
		}
		b, err := frames.ConstructDMR(f, p, nil)
		if err != nil {
			return err
		}
		if err := inj.InjectToHost(b); err != nil {
			return err
		}
		seq++
		sent++
		return nil
	}

	if err := emit(frames.KindHeader, nil); err != nil {
		return sent, fmt.Errorf("wxvoice: voice header: %w", err)
	}
	for i := 0; i < len(codewords); i += CodewordsPerFrame {
		end := i + CodewordsPerFrame
		if end > len(codewords) {
			end = len(codewords)
		}
		group := codewords[i:end]
		// A final partial group is padded with silence-shaped codewords rather
		// than dropped, so the last word of an announcement is not clipped. A
		// zeroed codeword is not true silence to a vocoder, but it is a fraction
		// of one frame at the very end of a transmission.
		for len(group) < CodewordsPerFrame {
			group = append(group, make([]byte, frames.AMBEBytes))
		}
		if err := emit(frames.KindVoice, group); err != nil {
			return sent, fmt.Errorf("wxvoice: voice frame: %w", err)
		}
		sleep(FrameInterval)
	}
	if err := emit(frames.KindTerminator, nil); err != nil {
		return sent, fmt.Errorf("wxvoice: terminator: %w", err)
	}
	return sent, nil
}
