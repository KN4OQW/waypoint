// Package zello speaks the Zello Channels API: JSON control messages and binary
// audio over one WebSocket, so a bus can bridge to a Zello channel.
//
// # Ground truth
//
// Everything in this file was read from zelloptt/zello-channel-api's API.md
// (commit e11b2d5) rather than inferred, and the findings are recorded in
// docs/zello/ground-truth.md. Three points shape the design:
//
//   - One channel per connection. API.md states verbatim: "Connecting to
//     multiple channels (up to 100) is currently supported for Zello Work only."
//     A consumer gateway therefore opens one WebSocket per channel.
//   - The server pings every 30 seconds and drops a client that takes longer
//     than 30 seconds to pong.
//   - The API is beta and documented as subject to change.
//
// # Error codes
//
// The codes below are the documented set, copied exactly. Two are worth calling
// out because the design that preceded this file got them wrong. The listen-only
// code is "listen only connection", unhyphenated — the hyphenated form is the
// human-readable description, and matching on it would never fire. And "no
// phone", which community bridges treat as a common transmit failure, is not a
// documented code at all; it appears nowhere in the API repository. Nothing here
// keys off it.
package zello

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// Documented error codes, from API.md's table. Compared against the `error`
// field of a command response or an on_error event.
const (
	ErrCodeUnknownCommand   = "unknown command"
	ErrCodeInternalServer   = "internal server error"
	ErrCodeInvalidJSON      = "invalid json"
	ErrCodeInvalidRequest   = "invalid request"
	ErrCodeNotAuthorized    = "not authorized"
	ErrCodeNotLoggedIn      = "not logged in"
	ErrCodeNotEnoughParams  = "not enough params"
	ErrCodeServerClosed     = "server closed connection"
	ErrCodeChannelNotReady  = "channel is not ready"
	ErrCodeListenOnly       = "listen only connection"
	ErrCodeFailedStartStrm  = "failed to start stream"
	ErrCodeFailedStopStream = "failed to stop stream"
	ErrCodeFailedSendData   = "failed to send data"
	ErrCodeInvalidAudio     = "invalid audio packet"
	ErrCodeChannelsLimit    = "channels limit exceeded"

	// ErrCodeInvalidUsername is NOT in Zello's documented error table. It was
	// observed against the live service, and it is what an anonymous logon
	// actually gets — see the note below.
	ErrCodeInvalidUsername = "invalid username"
)

// Anonymous logon does not work, whatever API.md says.
//
// The documentation describes `username` as "(optional for Zello Friends and
// Family) Username to logon with. If not provided the client will connect
// anonymously," and a receive-only bridge is the obvious use for that: no
// account, no password, just listen.
//
// Measured against the live service with a valid, freshly minted token, both a
// plain anonymous logon and an anonymous logon with listen_only set are refused
// with `invalid username` — a code the error table does not contain. So every
// connection needs a real account, including a listen-only one, and the config
// validator enforces that rather than discovering it at the first connect.

// CodecHeader is start_stream's codec_header: four bytes describing how the
// Opus payload is framed.
//
// API.md: "codec_header is base64-encoded 4 byte array" laid out as
// {sample_rate_hz(16LE), frames_per_packet(8), frame_size_ms(8)}. Note the
// sample rate is LITTLE-endian here while the binary stream packets are
// big-endian — the two are not the same convention, and this is the only place
// in the protocol where a multi-byte integer is little-endian.
type CodecHeader struct {
	SampleRateHz    uint16
	FramesPerPacket uint8 // API.md constrains this to 1 or 2
	FrameSizeMS     uint8
}

// Encode renders the header as the base64 string start_stream carries.
func (h CodecHeader) Encode() string {
	var b [4]byte
	binary.LittleEndian.PutUint16(b[0:2], h.SampleRateHz)
	b[2] = h.FramesPerPacket
	b[3] = h.FrameSizeMS
	return base64.StdEncoding.EncodeToString(b[:])
}

// ParseCodecHeader reads the header an on_stream_start carries.
func ParseCodecHeader(s string) (CodecHeader, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return CodecHeader{}, fmt.Errorf("zello: codec_header is not base64: %w", err)
	}
	if len(b) != 4 {
		return CodecHeader{}, fmt.Errorf("zello: codec_header is %d bytes, want 4", len(b))
	}
	return CodecHeader{
		SampleRateHz:    binary.LittleEndian.Uint16(b[0:2]),
		FramesPerPacket: b[2],
		FrameSizeMS:     b[3],
	}, nil
}

// Valid reports whether the header describes something the API accepts.
// packet_duration is documented as 2.5 ms to 60 ms, and frames_per_packet as
// 1 or 2.
func (h CodecHeader) Valid() error {
	if h.SampleRateHz == 0 {
		return fmt.Errorf("zello: codec_header sample rate is zero")
	}
	if h.FramesPerPacket != 1 && h.FramesPerPacket != 2 {
		return fmt.Errorf("zello: frames_per_packet is %d, but the API documents 1 or 2", h.FramesPerPacket)
	}
	if h.FrameSizeMS < 1 || h.FrameSizeMS > 60 {
		return fmt.Errorf("zello: frame size %d ms is outside the documented 2.5-60 ms range", h.FrameSizeMS)
	}
	return nil
}

// --- Binary stream packets ----------------------------------------------------

// StreamPacketHeaderBytes is the fixed header on every binary audio packet:
// {type(8), stream_id(32), packet_id(32)}, network byte order.
const StreamPacketHeaderBytes = 9

// StreamPacketTypeAudio is the only documented packet type.
const StreamPacketTypeAudio byte = 0x01

// StreamPacket is one binary WebSocket message carrying audio.
//
// API.md: "The same binary packet structure is used for any streamed data
// travelling both ways. The packet_id field is populated with the packet number
// for the audio packets sent from the server to a client. When streaming data to
// the server the packet_id value is ignored and should be filled with zeroes.
// Fields are stored in network byte order."
type StreamPacket struct {
	Type     byte
	StreamID uint32
	PacketID uint32
	Data     []byte
}

// Marshal renders the packet for the wire. PacketID is sent as given; callers
// streaming to the server leave it zero, as the API says it is ignored.
func (p StreamPacket) Marshal() []byte {
	out := make([]byte, StreamPacketHeaderBytes+len(p.Data))
	out[0] = p.Type
	binary.BigEndian.PutUint32(out[1:5], p.StreamID)
	binary.BigEndian.PutUint32(out[5:9], p.PacketID)
	copy(out[StreamPacketHeaderBytes:], p.Data)
	return out
}

// ParseStreamPacket reads a binary WebSocket message.
//
// A packet with no payload is accepted rather than refused: the header is
// complete and well formed, and a zero-length Opus payload is the server's
// business to explain, not a framing error.
func ParseStreamPacket(b []byte) (StreamPacket, error) {
	if len(b) < StreamPacketHeaderBytes {
		return StreamPacket{}, fmt.Errorf("zello: binary packet is %d bytes, need at least %d",
			len(b), StreamPacketHeaderBytes)
	}
	p := StreamPacket{
		Type:     b[0],
		StreamID: binary.BigEndian.Uint32(b[1:5]),
		PacketID: binary.BigEndian.Uint32(b[5:9]),
	}
	if n := len(b) - StreamPacketHeaderBytes; n > 0 {
		p.Data = make([]byte, n)
		copy(p.Data, b[StreamPacketHeaderBytes:])
	}
	return p, nil
}

// --- Control messages ---------------------------------------------------------

// Logon is the first message after the WebSocket opens.
//
// Channels is a slice because the wire field is an array, but on consumer Zello
// it carries exactly one entry — see the package comment. AuthToken is required
// unless RefreshToken is set. Omitting Username connects anonymously, which is
// listen-only, so a gateway that transmits must supply both Username and
// Password.
type Logon struct {
	Command      string   `json:"command"`
	Seq          int      `json:"seq"`
	AuthToken    string   `json:"auth_token,omitempty"`
	RefreshToken string   `json:"refresh_token,omitempty"`
	Username     string   `json:"username,omitempty"`
	Password     string   `json:"password,omitempty"`
	Channels     []string `json:"channels"`
	ListenOnly   bool     `json:"listen_only,omitempty"`
	// PlatformName is worth setting: API.md notes that a name containing
	// "Gateway" or "Kiosk" (case-insensitive) makes the Zello Alarms service
	// track this client's online status, which is what a bridge is.
	PlatformName string `json:"platform_name,omitempty"`
	PlatformType string `json:"platform_type,omitempty"`
}

// Response is the reply to any command carrying a seq.
type Response struct {
	Seq          int    `json:"seq"`
	Success      bool   `json:"success"`
	Error        string `json:"error,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	StreamID     uint32 `json:"stream_id,omitempty"`
}

// StartStream opens a voice message. The response carries the stream_id that
// every binary packet and the matching stop_stream must use.
type StartStream struct {
	Command        string `json:"command"`
	Seq            int    `json:"seq"`
	Channel        string `json:"channel,omitempty"`
	Type           string `json:"type"`
	Codec          string `json:"codec"`
	CodecHeader    string `json:"codec_header"`
	PacketDuration int    `json:"packet_duration"`
	For            string `json:"for,omitempty"`
}

// StopStream ends a voice message.
type StopStream struct {
	Command  string `json:"command"`
	StreamID uint32 `json:"stream_id"`
}

// Event is an unsolicited message from the server. Only the fields Waypoint
// acts on are decoded; the rest are ignored rather than rejected, because the
// API is beta and documented as subject to change — a new field must not break
// an established bridge.
type Event struct {
	Command        string `json:"command"`
	Channel        string `json:"channel,omitempty"`
	Status         string `json:"status,omitempty"`
	UsersOnline    int    `json:"users_online,omitempty"`
	StreamID       uint32 `json:"stream_id,omitempty"`
	Type           string `json:"type,omitempty"`
	Codec          string `json:"codec,omitempty"`
	CodecHeader    string `json:"codec_header,omitempty"`
	PacketDuration int    `json:"packet_duration,omitempty"`
	From           string `json:"from,omitempty"`
	For            string `json:"for,omitempty"`
	Error          string `json:"error,omitempty"`
	// Seq is set on command responses, which arrive on the same text channel as
	// events; a non-zero Seq is how the two are told apart.
	Seq int `json:"seq,omitempty"`
}

// Documented event and command names.
const (
	CmdLogon           = "logon"
	CmdStartStream     = "start_stream"
	CmdStopStream      = "stop_stream"
	EvtOnChannelStatus = "on_channel_status"
	EvtOnStreamStart   = "on_stream_start"
	EvtOnStreamStop    = "on_stream_stop"
	EvtOnError         = "on_error"
)

// decodeText classifies an inbound text frame. A frame carrying a non-zero seq
// is a response to a command this client sent; anything else is an event.
func decodeText(b []byte) (Event, bool, error) {
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return Event{}, false, fmt.Errorf("zello: decoding a text frame: %w", err)
	}
	return e, e.Seq != 0, nil
}
