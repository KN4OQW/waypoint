// Package vocoder is the client for md380-emu's UDP server, the AMBE+2 encoder
// and decoder a bridge needs when audio has to leave the digital modes for a
// codec nothing on the air speaks.
//
// The bus itself never needs this. It carries AMBE+2 codewords between modes and
// copies them verbatim, which is why internal/bus/router can promise it touches
// no codec. A Zello attachment is the first thing that has to turn those
// codewords into audio, so the vocoder lives at the endpoint edge and that
// promise survives.
//
// # Ground truth
//
// The protocol below was read in full from md380-emu's own ambeServer(), not
// from documentation, and it is not what the DVSwitch ecosystem's other AMBE
// servers speak. There is no DV3000/AMBEServer packet framing here: no 0x61
// start byte, no length field, no packet type, no sequence number. The server
// dispatches on datagram length alone.
//
//	send exactly 320 bytes (160 int16 samples, 20 ms at 8 kHz) -> receive 7 bytes
//	send exactly 7 bytes                                       -> receive 320 bytes
//	send anything else                                         -> silently dropped
//
// Two consequences shape this whole package. Nothing correlates a reply with its
// request, so a client may keep exactly one request in flight (see Client.mu) and
// must treat silence as a timeout rather than waiting for a match. And because a
// late reply to a timed-out request is indistinguishable from the reply to the
// next one, replies are matched by the only thing that distinguishes them —
// their length — and anything else is discarded rather than returned. Without
// that, one dropped datagram desynchronises the stream permanently and every
// frame after it is the previous frame's audio.
//
// # The codeword is already ours
//
// md380-emu's 7-byte form is byte-for-byte internal/bus/frames' canonical
// AMBE+2 codeword. Its encode_amb_buffer packs bits 0-47 MSB-first into bytes
// 0-5 and bit 48 into byte 6 as 0x80, which is exactly what that package's
// MSB-first bit addressing (bitMask{0x80..0x01}, byte i>>3, mask i&7) produces
// for bit 48. So nothing here repacks anything, and the bytes this returns go
// straight to dmrAMBEFromCanonical for the on-air FEC.
//
// Do not confuse this with the 8-byte frame in a .amb file, which is what
// md380-emu's -d and -e file modes read and write. That form carries a leading
// status byte and puts bit 48 in byte 7 as the LSB. It is a different shape in
// two ways at once, and code that mixes them up produces noise rather than an
// error.
package vocoder

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	// SamplesPerFrame is one 20 ms frame of 8 kHz audio, the unit md380-emu
	// works in. Both directions are fixed at this size; the server has no
	// concept of a partial frame.
	SamplesPerFrame = 160

	// PCMBytes and AMBEBytes are the two datagram lengths the server
	// recognises. They are the entire protocol: the server switches on these
	// two numbers and drops everything else without a reply.
	PCMBytes  = SamplesPerFrame * 2
	AMBEBytes = 7

	// FrameDuration is what one frame represents. A node that cannot service a
	// frame in less than this cannot bridge in real time, which is the headline
	// number the bench measurement reports.
	FrameDuration = 20 * time.Millisecond

	// DefaultPort is the port DVSwitch's tooling has always used for the
	// software emulator. 2460 is the DV3000 hardware AMBEServer; keeping them
	// distinct means a misconfigured node fails to connect rather than talking
	// to the wrong vocoder.
	DefaultPort = 2470
)

// ErrTimeout is returned when the server does not answer within the deadline.
// It is worth distinguishing because it is the expected failure when md380-emu
// has died (see Supervisor) rather than a sign the request was malformed.
var ErrTimeout = errors.New("vocoder: no reply from md380-emu")

// Client is one connection to a running md380-emu server.
//
// It is safe for concurrent use, but callers gain nothing by it: mu serialises
// every exchange because the protocol cannot tell two outstanding requests
// apart. A caller that wants parallelism needs a second md380-emu process, not
// a second goroutine.
type Client struct {
	mu      sync.Mutex
	conn    *net.UDPConn
	timeout time.Duration
}

// Dial connects to an md380-emu server. The address should be a loopback one:
// as shipped the server binds INADDR_ANY, so a node that points this at a
// routable interface has an unauthenticated vocoder answering the LAN.
//
// The socket is connected, not merely bound, so the kernel drops datagrams from
// anywhere other than the server and readReply never sees a stranger's packet.
func Dial(addr string, timeout time.Duration) (*Client, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("vocoder: resolve %q: %w", addr, err)
	}
	conn, err := net.DialUDP("udp", nil, ua)
	if err != nil {
		return nil, fmt.Errorf("vocoder: dial %q: %w", addr, err)
	}
	if timeout <= 0 {
		timeout = FrameDuration * 5
	}
	return &Client{conn: conn, timeout: timeout}, nil
}

// Close releases the socket.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Close()
}

// Encode turns one 20 ms frame of 8 kHz signed 16-bit audio into a canonical
// AMBE+2 codeword. pcm must be exactly SamplesPerFrame long: the server drops a
// datagram of any other size without replying, so a short frame would surface as
// a timeout several milliseconds later rather than as the argument error it is.
func (c *Client) Encode(pcm []int16) ([]byte, error) {
	if len(pcm) != SamplesPerFrame {
		return nil, fmt.Errorf("vocoder: encode needs exactly %d samples, got %d", SamplesPerFrame, len(pcm))
	}
	req := make([]byte, PCMBytes)
	for i, s := range pcm {
		// Host byte order, matching the server's cast of the datagram straight
		// to short*. Both ends are little-endian on every platform Waypoint
		// runs on; this is not a wire format anyone else parses.
		req[i*2] = byte(uint16(s))
		req[i*2+1] = byte(uint16(s) >> 8)
	}
	reply, err := c.exchange(req, AMBEBytes)
	if err != nil {
		return nil, err
	}
	return reply, nil
}

// Decode turns a canonical AMBE+2 codeword back into one 20 ms frame of audio.
func (c *Client) Decode(ambe []byte) ([]int16, error) {
	if len(ambe) != AMBEBytes {
		return nil, fmt.Errorf("vocoder: decode needs exactly %d bytes, got %d", AMBEBytes, len(ambe))
	}
	reply, err := c.exchange(ambe, PCMBytes)
	if err != nil {
		return nil, err
	}
	pcm := make([]int16, SamplesPerFrame)
	for i := range pcm {
		pcm[i] = int16(uint16(reply[i*2]) | uint16(reply[i*2+1])<<8)
	}
	return pcm, nil
}

// exchange sends one request and waits for a reply of exactly wantLen bytes.
//
// The length check is the correlation the protocol does not provide. A reply of
// the other valid length is a straggler from a request that already timed out,
// and returning it would hand the caller the previous frame's audio — a fault
// that sounds like a stutter and never logs. Discarding it and continuing to
// wait re-synchronises within the same deadline in the common case.
func (c *Client) exchange(req []byte, wantLen int) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return nil, fmt.Errorf("vocoder: set write deadline: %w", err)
	}
	if _, err := c.conn.Write(req); err != nil {
		return nil, fmt.Errorf("vocoder: send: %w", err)
	}

	deadline := time.Now().Add(c.timeout)
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("vocoder: set read deadline: %w", err)
	}
	// A buffer larger than either valid reply, so an oversized datagram is
	// truncated to a length that fails the check below rather than being
	// silently accepted as a short one.
	buf := make([]byte, PCMBytes+1)
	for {
		n, err := c.conn.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return nil, ErrTimeout
			}
			return nil, fmt.Errorf("vocoder: receive: %w", err)
		}
		if n == wantLen {
			out := make([]byte, n)
			copy(out, buf[:n])
			return out, nil
		}
		// Wrong length: a straggler, or a server that answered something we did
		// not ask. Keep waiting until the deadline rather than failing, so a
		// single lost frame does not end the stream.
		if time.Now().After(deadline) {
			return nil, ErrTimeout
		}
	}
}
