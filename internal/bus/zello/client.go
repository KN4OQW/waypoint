package zello

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// DefaultURL is the consumer ("Friends and Family") endpoint. Zello Work is
// wss://zellowork.io/ws/<network> and supports multi-channel logon, which this
// client deliberately does not use — see the package comment.
const DefaultURL = "wss://zello.io/ws"

// DefaultPlatformName is sent as platform_name. API.md notes that a name
// containing "Gateway" (case-insensitive) makes the Zello Alarms service track
// the client's online status, which is exactly what a bridge wants: an operator
// looking at Zello can see whether the node is up.
const DefaultPlatformName = "Waypoint Gateway"

// commandTimeout bounds how long a command waits for its response. It is well
// inside the 30-second ping interval, so a wedged server surfaces as a failed
// command before it surfaces as a dead connection.
const commandTimeout = 10 * time.Second

// Config describes one connection, which is one channel. Consumer Zello does not
// support connecting to several channels over one socket, so a gateway bridging
// N channels opens N of these.
type Config struct {
	// URL defaults to DefaultURL.
	URL string

	// Channel is the single channel this connection joins.
	Channel string

	// AuthToken is the JWT from developers.zello.com. A sample development token
	// expires after 30 days; production tokens are signed on the operator's own
	// server. Required unless RefreshToken is set.
	AuthToken string

	// RefreshToken reconnects an interrupted session quickly. A successful logon
	// returns one.
	RefreshToken string

	// Username and Password name the account the gateway talks as. Without a
	// username the connection is anonymous, which is listen-only: every
	// transmission then fails with "listen only connection".
	Username string
	Password string

	// ListenOnly joins without the ability to transmit. Set it for a
	// receive-only bridge so the intent is explicit rather than an accident of
	// leaving Username blank.
	ListenOnly bool

	// PlatformName defaults to DefaultPlatformName.
	PlatformName string

	// Dialer, when set, replaces the default WebSocket dialer. Tests use it to
	// reach an httptest server; production leaves it nil.
	Dialer *websocket.Dialer
}

// Client is one logged-on WebSocket carrying one Zello channel.
//
// It is not self-healing: when the connection drops, Events is closed and the
// caller reconnects with a fresh Client. That keeps the reconnect schedule and
// the decision to give up in the endpoint, next to the bus state that has to be
// unwound anyway, rather than hidden in here.
type Client struct {
	conn *websocket.Conn
	cfg  Config

	// RefreshToken is what logon returned, for a fast reconnect.
	RefreshToken string

	writeMu sync.Mutex // one writer at a time; gorilla requires it

	mu      sync.Mutex
	seq     int
	pending map[int]chan Response
	closed  bool

	events chan Event
	audio  chan StreamPacket
	done   chan struct{}
	err    error
}

// Dial opens the connection and performs the logon handshake, returning only
// once the server has accepted it.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Channel == "" {
		return nil, errors.New("zello: a channel is required")
	}
	if cfg.AuthToken == "" && cfg.RefreshToken == "" {
		return nil, errors.New("zello: an auth token or a refresh token is required")
	}
	if cfg.URL == "" {
		cfg.URL = DefaultURL
	}
	if cfg.PlatformName == "" {
		cfg.PlatformName = DefaultPlatformName
	}
	dialer := cfg.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}

	conn, resp, err := dialer.DialContext(ctx, cfg.URL, http.Header{})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("zello: dialling %s: %w (HTTP %s)", cfg.URL, err, resp.Status)
		}
		return nil, fmt.Errorf("zello: dialling %s: %w", cfg.URL, err)
	}

	c := &Client{
		conn:    conn,
		cfg:     cfg,
		pending: make(map[int]chan Response),
		events:  make(chan Event, 16),
		audio:   make(chan StreamPacket, 64),
		done:    make(chan struct{}),
	}

	// gorilla answers the server's ping frames with a pong automatically, but
	// only from inside its read loop — so the pong contract is met by having a
	// reader running, not by anything explicit. If this goroutine ever stops
	// while the connection stays open, the server drops us after 30 seconds.
	go c.readLoop()

	if err := c.logon(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) logon(ctx context.Context) error {
	seq, ch := c.nextSeq()
	msg := Logon{
		Command:      CmdLogon,
		Seq:          seq,
		AuthToken:    c.cfg.AuthToken,
		RefreshToken: c.cfg.RefreshToken,
		Username:     c.cfg.Username,
		Password:     c.cfg.Password,
		Channels:     []string{c.cfg.Channel},
		ListenOnly:   c.cfg.ListenOnly,
		PlatformName: c.cfg.PlatformName,
	}
	if err := c.writeJSON(msg); err != nil {
		c.clearPending(seq)
		return fmt.Errorf("zello: sending logon: %w", err)
	}
	resp, err := c.await(ctx, seq, ch)
	if err != nil {
		return err
	}
	if !resp.Success {
		// The documented codes are worth naming in the message, because the two
		// an operator actually hits mean different things to fix: a bad or
		// expired token, or an account without permission on the channel.
		return fmt.Errorf("zello: logon refused: %s", describeError(resp.Error))
	}
	c.RefreshToken = resp.RefreshToken
	return nil
}

// describeError turns a documented code into something that says what to do.
func describeError(code string) string {
	switch code {
	case ErrCodeNotAuthorized:
		return code + " — the username, password or token is not valid; a sample development token expires after 30 days"
	case ErrCodeListenOnly:
		return code + " — this connection cannot transmit; set a username and password, or set ListenOnly and stop sending"
	case ErrCodeChannelsLimit:
		return code + " — too many channels for one connection; consumer Zello allows exactly one"
	case ErrCodeChannelNotReady:
		return code + " — wait for the channel to report online before transmitting"
	case ErrCodeNotEnoughParams:
		// Observed against the live service: a logon carrying a username and
		// password but no auth_token is refused with this, not with "not
		// authorized". The credentials are never evaluated, so an operator who
		// has entered a working Zello password sees a failure that says nothing
		// about the field that is actually missing.
		return code + " — a Zello logon needs an auth token as well as an account; " +
			"a username and password alone are refused before the credentials are checked"
	case "":
		return "no error code given"
	default:
		return code
	}
}

// StartStream opens a voice message and returns the stream id every packet and
// the matching StopStream must carry.
func (c *Client) StartStream(ctx context.Context, header CodecHeader, packetMS int) (uint32, error) {
	if err := header.Valid(); err != nil {
		return 0, err
	}
	if packetMS < 1 || packetMS > 60 {
		return 0, fmt.Errorf("zello: packet_duration %d ms is outside the documented 2.5-60 ms range", packetMS)
	}
	seq, ch := c.nextSeq()
	msg := StartStream{
		Command:        CmdStartStream,
		Seq:            seq,
		Channel:        c.cfg.Channel,
		Type:           "audio",
		Codec:          "opus",
		CodecHeader:    header.Encode(),
		PacketDuration: packetMS,
	}
	if err := c.writeJSON(msg); err != nil {
		c.clearPending(seq)
		return 0, fmt.Errorf("zello: sending start_stream: %w", err)
	}
	resp, err := c.await(ctx, seq, ch)
	if err != nil {
		return 0, err
	}
	if !resp.Success {
		return 0, fmt.Errorf("zello: start_stream refused: %s", describeError(resp.Error))
	}
	return resp.StreamID, nil
}

// SendAudio writes one Opus packet into an open stream.
//
// packet_id is sent as zero: API.md says the server ignores it on the inbound
// path and it should be filled with zeroes.
func (c *Client) SendAudio(streamID uint32, opus []byte) error {
	p := StreamPacket{Type: StreamPacketTypeAudio, StreamID: streamID, Data: opus}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteMessage(websocket.BinaryMessage, p.Marshal())
}

// StopStream ends a voice message. It carries no seq, so there is no response to
// wait for — which also means a failure here is only visible as the next
// command failing.
func (c *Client) StopStream(streamID uint32) error {
	return c.writeJSON(StopStream{Command: CmdStopStream, StreamID: streamID})
}

// Events yields on_stream_start, on_stream_stop, on_channel_status and on_error.
// It closes when the connection ends; Err then says why.
func (c *Client) Events() <-chan Event { return c.events }

// Audio yields inbound binary packets. The payload is Opus framed as the
// matching on_stream_start's codec_header described.
func (c *Client) Audio() <-chan StreamPacket { return c.audio }

// Err reports why the connection ended, once Events has closed.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the connection.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return c.conn.Close()
}

func (c *Client) nextSeq() (int, chan Response) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	ch := make(chan Response, 1)
	c.pending[c.seq] = ch
	return c.seq, ch
}

func (c *Client) clearPending(seq int) {
	c.mu.Lock()
	delete(c.pending, seq)
	c.mu.Unlock()
}

// await waits for the response to one command, or for the connection to end.
func (c *Client) await(ctx context.Context, seq int, ch chan Response) (Response, error) {
	defer c.clearPending(seq)
	select {
	case r := <-ch:
		return r, nil
	case <-c.done:
		if err := c.Err(); err != nil {
			return Response{}, fmt.Errorf("zello: connection ended waiting for a response: %w", err)
		}
		return Response{}, errors.New("zello: connection ended waiting for a response")
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case <-time.After(commandTimeout):
		return Response{}, fmt.Errorf("zello: no response within %v", commandTimeout)
	}
}

func (c *Client) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, b)
}

// readLoop is the only reader. Besides dispatching messages it is what keeps the
// connection alive, because gorilla replies to the server's ping frames from
// inside ReadMessage.
func (c *Client) readLoop() {
	defer func() {
		close(c.done)
		close(c.events)
		close(c.audio)
	}()

	for {
		typ, b, err := c.conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			if !c.closed {
				c.err = err
			}
			// Wake anything waiting on a response rather than leaving it to time
			// out: the connection is gone and no reply is coming.
			for seq, ch := range c.pending {
				close(ch)
				delete(c.pending, seq)
			}
			c.mu.Unlock()
			return
		}

		switch typ {
		case websocket.BinaryMessage:
			p, err := ParseStreamPacket(b)
			if err != nil {
				continue // a malformed packet is one lost frame, not a dead link
			}
			select {
			case c.audio <- p:
			default:
				// A full buffer means the consumer is not keeping up with real
				// time. Dropping the packet is right: audio is only useful now,
				// and blocking here would stall the pongs that keep the socket
				// open and turn a slow consumer into a disconnection.
			}

		case websocket.TextMessage:
			ev, isResponse, err := decodeText(b)
			if err != nil {
				continue
			}
			if isResponse {
				// Decoded as a Response rather than reconstructed from the
				// Event: success is its own field on the wire, and inferring it
				// from an empty error would read a malformed reply as a
				// successful one.
				var r Response
				if err := json.Unmarshal(b, &r); err != nil {
					continue
				}
				c.mu.Lock()
				ch, ok := c.pending[r.Seq]
				delete(c.pending, r.Seq)
				c.mu.Unlock()
				if ok {
					ch <- r
				}
				continue
			}
			select {
			case c.events <- ev:
			default:
			}
		}
	}
}
