package wxvoice

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
)

type fakeInjector struct {
	sent [][]byte
	fail error
}

func (f *fakeInjector) InjectToHost(b []byte) error {
	if f.fail != nil {
		return f.fail
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	f.sent = append(f.sent, cp)
	return nil
}

func codewords(n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		cw := make([]byte, frames.AMBEBytes)
		cw[0] = byte(i + 1)
		out[i] = cw
	}
	return out
}

func noSleep(time.Duration) {}

// A transmission is a header, voice frames, and a terminator. Getting the
// bracket wrong leaves a radio's squelch open or drops the first syllable.
func TestTransmitBracketsTheStream(t *testing.T) {
	inj := &fakeInjector{}
	// 6 codewords = exactly 2 voice frames.
	n, err := Transmit(inj, codewords(6), TransmitOptions{SrcID: 3180202, DstID: 9, Slot: 2, StreamID: 42, Sleep: noSleep})
	if err != nil {
		t.Fatalf("Transmit: %v", err)
	}
	if want := 1 + 2 + 1; n != want {
		t.Errorf("sent %d frames, want %d (header + 2 voice + terminator)", n, want)
	}
	if len(inj.sent) != n {
		t.Fatalf("injector saw %d frames, Transmit reported %d", len(inj.sent), n)
	}
	for i, b := range inj.sent {
		if len(b) < 20 || string(b[0:4]) != "DMRD" {
			t.Fatalf("frame %d is not a DMRD frame: % x", i, b[:min(8, len(b))])
		}
	}
	// The sequence number must advance across the whole transmission, or a
	// receiver treats the frames as retransmissions.
	if inj.sent[0][4] == inj.sent[1][4] {
		t.Error("sequence number did not advance between frames")
	}
}

// A partial final group is padded, not dropped: the alternative clips the last
// word of the announcement.
func TestTransmitPadsTheFinalGroup(t *testing.T) {
	inj := &fakeInjector{}
	// 4 codewords = one full frame plus one leftover.
	n, err := Transmit(inj, codewords(4), TransmitOptions{DstID: 9, Slot: 2, Sleep: noSleep})
	if err != nil {
		t.Fatalf("Transmit: %v", err)
	}
	if want := 1 + 2 + 1; n != want {
		t.Errorf("sent %d frames, want %d; the leftover codeword was dropped", n, want)
	}
}

// A codeword of the wrong width means the encoder did not meet the contract.
// Transmitting it anyway puts noise on somebody's talkgroup.
func TestTransmitRefusesWrongSizedCodewords(t *testing.T) {
	inj := &fakeInjector{}
	bad := [][]byte{make([]byte, 9)} // the FEC'd form, the classic mistake
	_, err := Transmit(inj, bad, TransmitOptions{DstID: 9, Sleep: noSleep})
	if err == nil {
		t.Fatal("accepted a 9-byte codeword; that is FEC over FEC and sounds like noise")
	}
	if !strings.Contains(err.Error(), "want 7") && !strings.Contains(err.Error(), "7") {
		t.Errorf("error %q should name the expected width", err)
	}
	if len(inj.sent) != 0 {
		t.Error("frames were transmitted before the codewords were validated")
	}
}

func TestTransmitRefusesNothing(t *testing.T) {
	if _, err := Transmit(&fakeInjector{}, nil, TransmitOptions{Sleep: noSleep}); err == nil {
		t.Error("accepted an empty transmission")
	}
	if _, err := Transmit(nil, codewords(3), TransmitOptions{Sleep: noSleep}); err == nil {
		t.Error("accepted a nil relay")
	}
}

// A relay that fails mid-transmission must report how far it got, so the caller
// can say "cut off after N frames" rather than "failed".
func TestTransmitReportsProgressOnFailure(t *testing.T) {
	inj := &fakeInjector{fail: errors.New("relay closed")}
	n, err := Transmit(inj, codewords(3), TransmitOptions{DstID: 9, Sleep: noSleep})
	if err == nil {
		t.Fatal("a failing relay reported success")
	}
	if n != 0 {
		t.Errorf("reported %d frames sent through a relay that accepted none", n)
	}
}

// Pacing is what keeps MMDVM-Host's jitter buffer fed without overrunning it.
func TestTransmitPacesAtTheSlotCadence(t *testing.T) {
	inj := &fakeInjector{}
	var slept []time.Duration
	_, err := Transmit(inj, codewords(9), TransmitOptions{DstID: 9, Sleep: func(d time.Duration) { slept = append(slept, d) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(slept) != 3 {
		t.Fatalf("slept %d times for 3 voice frames", len(slept))
	}
	for _, d := range slept {
		if d != FrameInterval {
			t.Errorf("paced at %v, want %v (one DMR frame of one timeslot)", d, FrameInterval)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
