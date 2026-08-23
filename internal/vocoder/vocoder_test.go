package vocoder

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeServer is md380-emu's ambeServer with the vocoder replaced by a stub: the
// same dispatch-on-length protocol, the same silence for anything else. It is
// what lets this package's framing, correlation and timeout behaviour be tested
// without the licensed firmware blob, which needs an ARM target.
//
// It deliberately does NOT validate that the audio is real. That claim needs the
// actual vocoder and is made by TestRoundTripThroughRealVocoder in
// vocoder_hw_test.go, behind the `hw` tag.
type fakeServer struct {
	conn *net.UDPConn

	mu sync.Mutex
	// drop causes the next n requests to go unanswered, standing in for a lost
	// datagram or a wedged emulator.
	drop int
	// extra makes the server send an unsolicited datagram of the other valid
	// length before the real reply, which is what a straggler from a timed-out
	// request looks like on the wire.
	extra bool
	// seen counts requests that arrived, so a test can prove the client sent
	// exactly one at a time.
	seen int
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeServer{conn: conn}
	go f.serve()
	t.Cleanup(func() { conn.Close() })
	return f
}

func (f *fakeServer) addr() string { return f.conn.LocalAddr().String() }

func (f *fakeServer) serve() {
	buf := make([]byte, 1024)
	for {
		n, peer, err := f.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.seen++
		if f.drop > 0 {
			f.drop--
			f.mu.Unlock()
			continue
		}
		sendExtra := f.extra
		f.extra = false
		f.mu.Unlock()

		switch n {
		case PCMBytes:
			if sendExtra {
				f.conn.WriteToUDP(make([]byte, PCMBytes), peer)
			}
			// A recognisable codeword rather than zeros, so a test can tell a
			// real reply from a zero-filled buffer.
			f.conn.WriteToUDP([]byte{0xf8, 0xfe, 0x14, 0x39, 0x8c, 0x50, 0x80}, peer)
		case AMBEBytes:
			if sendExtra {
				f.conn.WriteToUDP(make([]byte, AMBEBytes), peer)
			}
			pcm := make([]byte, PCMBytes)
			for i := range pcm {
				pcm[i] = byte(i)
			}
			f.conn.WriteToUDP(pcm, peer)
		default:
			// Silence, exactly as ambeServer does.
		}
	}
}

func (f *fakeServer) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen
}

func dialFake(t *testing.T, f *fakeServer, timeout time.Duration) *Client {
	t.Helper()
	c, err := Dial(f.addr(), timeout)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestEncodeSendsOneFrameAndReturnsACodeword(t *testing.T) {
	f := newFakeServer(t)
	c := dialFake(t, f, time.Second)

	got, err := c.Encode(make([]int16, SamplesPerFrame))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(got) != AMBEBytes {
		t.Errorf("codeword length = %d, want %d", len(got), AMBEBytes)
	}
	if got[6] != 0x80 {
		t.Errorf("byte 6 = %#x, want 0x80 — the canonical form carries bit 48 as the MSB", got[6])
	}
}

func TestDecodeReturnsExactlyOneFrameOfSamples(t *testing.T) {
	f := newFakeServer(t)
	c := dialFake(t, f, time.Second)

	pcm, err := c.Decode([]byte{0xf8, 0xfe, 0x14, 0x39, 0x8c, 0x50, 0x80})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(pcm) != SamplesPerFrame {
		t.Fatalf("sample count = %d, want %d", len(pcm), SamplesPerFrame)
	}
	// The fake fills the reply with a byte ramp; little-endian pairs make
	// sample 0 = 0x0100 and sample 1 = 0x0302.
	if pcm[0] != 0x0100 || pcm[1] != 0x0302 {
		t.Errorf("samples decoded as %#x,%#x — byte order is wrong", pcm[0], pcm[1])
	}
}

// The server has no way to say "that was not a request", so a wrong-sized frame
// would otherwise surface as a timeout milliseconds later. Catching it as an
// argument error is the difference between a clear bug report and a hunt through
// the audio path.
func TestWrongSizedFramesAreRefusedWithoutSending(t *testing.T) {
	f := newFakeServer(t)
	c := dialFake(t, f, 50*time.Millisecond)

	if _, err := c.Encode(make([]int16, SamplesPerFrame-1)); err == nil {
		t.Error("short PCM frame was accepted")
	}
	if _, err := c.Decode(make([]byte, AMBEBytes+1)); err == nil {
		t.Error("oversized codeword was accepted")
	}
	if n := f.requests(); n != 0 {
		t.Errorf("server saw %d requests; a refused frame must never reach the wire", n)
	}
}

func TestSilenceBecomesATimeout(t *testing.T) {
	f := newFakeServer(t)
	f.mu.Lock()
	f.drop = 1
	f.mu.Unlock()
	c := dialFake(t, f, 80*time.Millisecond)

	_, err := c.Encode(make([]int16, SamplesPerFrame))
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

// This is the one that matters. Nothing in the protocol correlates a reply with
// its request, so a straggler from a request that already timed out is sitting in
// the socket when the next one goes out. Returning it would hand the caller the
// previous frame's audio — a fault that sounds like a stutter and logs nothing.
// Matching on length is the only correlation available.
func TestAStragglerOfTheWrongLengthIsDiscardedNotReturned(t *testing.T) {
	f := newFakeServer(t)
	f.mu.Lock()
	f.extra = true
	f.mu.Unlock()
	c := dialFake(t, f, time.Second)

	got, err := c.Encode(make([]int16, SamplesPerFrame))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(got) != AMBEBytes {
		t.Fatalf("returned a %d-byte reply; the %d-byte straggler was not discarded", len(got), PCMBytes)
	}
}

// The protocol cannot tell two outstanding requests apart, so the client must
// serialise them however many goroutines call it. If it did not, two callers
// would swap each other's audio.
func TestConcurrentCallersAreSerialised(t *testing.T) {
	f := newFakeServer(t)
	c := dialFake(t, f, 2*time.Second)

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := c.Encode(make([]int16, SamplesPerFrame))
			if err != nil {
				errs <- err
				return
			}
			if len(got) != AMBEBytes {
				errs <- errors.New("short codeword under concurrency")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent encode: %v", err)
	}
	if n := f.requests(); n != callers {
		t.Errorf("server saw %d requests, want %d", n, callers)
	}
}
