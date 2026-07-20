// Package peer is the LAN bus-peering transport (RFC-0016 decisions 1, 4, 5):
// the wire protocol, the owner-held token state machine, the cross-peer loop
// prevention, and the play-out jitter buffer. The protocol core is SOCKET-FREE
// and unit-tested; the mTLS/TCP layer (conn.go) is a thin shell that reads and
// writes messages over an io.ReadWriteCloser (net.Pipe in tests, a *tls.Conn in
// the field).
package peer

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
)

// ProtocolVersion is the wire version byte. It is 1 and only ever increases; the
// frame envelope and message set are N-node from the first byte (RFC-0016 §2), so
// adding a node needs no bump — a bump is reserved for a genuine format change.
const ProtocolVersion = 1

// MsgType tags a wire message (RFC-0016 decision 1 names voice, token
// request/grant/deny/release, and keepalive; Hello identifies a freshly-dialed
// session before any media flows).
type MsgType byte

const (
	MsgHello        MsgType = iota + 1 // node id + bus + role, sent once at connect
	MsgVoice                           // a voice frame + its cross-peer envelope
	MsgTokenRequest                    // member -> owner: I want to key up on this bus
	MsgTokenGrant                      // owner -> member: you hold the token
	MsgTokenDeny                       // owner -> member: busy, dropped
	MsgTokenRelease                    // member -> owner: I'm done (hang expired)
	MsgKeepalive                       // liveness heartbeat, either direction
	msgMax
)

func (t MsgType) valid() bool { return t >= MsgHello && t < msgMax }

// Role identifies a session end.
type Role byte

const (
	RoleMember Role = 0 // contributes a mode to a remote bus
	RoleOwner  Role = 1 // owns the bus + token
)

// Envelope is the cross-peer header every voice frame carries (RFC-0016 §5): the
// node + attachment the frame first entered the cluster on, the bus it belongs
// to, and how many peer links it has crossed. Loop prevention (loop.go) reads it.
type Envelope struct {
	OriginNode       string
	OriginAttachment string // the attachment/mode id the frame entered on
	BusID            string
	HopCount         uint8
}

// Hello identifies a session at connect.
type Hello struct {
	NodeID string
	BusID  string
	Role   Role
}

// Token carries a token request/grant/deny/release for a bus. StreamID ties a
// grant to the transmission it authorizes (so a stale grant for an old stream is
// ignored).
type Token struct {
	BusID    string
	StreamID uint32
}

// Voice is a voice frame with its envelope. Frame is the normalized frames.Frame
// (mode, kind, addressing, stream, AMBE codewords) reframed verbatim.
type Voice struct {
	Env   Envelope
	Frame frames.Frame
}

// Message is one decoded wire message; exactly one of the pointers is set for the
// composite types, and Type selects it. Keepalive carries only Type.
type Message struct {
	Type  MsgType
	Hello *Hello
	Token *Token
	Voice *Voice
}

// Errors from Decode. They are sentinel-wrapped so a caller can tell a malformed
// frame (drop the connection or the message) from a real fault. Decode NEVER
// panics on arbitrary input — that is fuzzed.
var (
	ErrShort   = errors.New("peer: buffer too short")
	ErrVersion = errors.New("peer: unsupported protocol version")
	ErrType    = errors.New("peer: unknown message type")
	ErrBadMsg  = errors.New("peer: malformed message")
)

// maxMessage bounds a single wire message so a hostile length prefix cannot force
// a huge allocation. A voice frame is ~55-155 bytes + envelope; 4 KiB is ample.
const maxMessage = 4096

// Encode serializes a message to the on-wire form: a 2-byte big-endian length
// prefix followed by [version][type][payload], where the length counts
// version+type+payload.
func (m Message) Encode() []byte {
	body := []byte{ProtocolVersion, byte(m.Type)}
	switch m.Type {
	case MsgHello:
		h := orEmptyHello(m.Hello)
		body = putStr(body, h.NodeID)
		body = putStr(body, h.BusID)
		body = append(body, byte(h.Role))
	case MsgVoice:
		body = encodeVoice(body, orEmptyVoice(m.Voice))
	case MsgTokenRequest, MsgTokenGrant, MsgTokenDeny, MsgTokenRelease:
		tk := orEmptyToken(m.Token)
		body = putStr(body, tk.BusID)
		body = binary.BigEndian.AppendUint32(body, tk.StreamID)
	case MsgKeepalive:
		// no payload
	}
	out := make([]byte, 2+len(body))
	binary.BigEndian.PutUint16(out, uint16(len(body)))
	copy(out[2:], body)
	return out
}

// Decode parses one message body (the [version][type][payload] bytes AFTER the
// length prefix has been stripped). It validates the version, the type, and every
// length-prefixed field, returning an error rather than panicking on any input.
func Decode(body []byte) (Message, error) {
	if len(body) < 2 {
		return Message{}, ErrShort
	}
	if body[0] != ProtocolVersion {
		return Message{}, ErrVersion
	}
	t := MsgType(body[1])
	if !t.valid() {
		return Message{}, ErrType
	}
	p := body[2:]
	m := Message{Type: t}
	switch t {
	case MsgHello:
		h := &Hello{}
		var err error
		if h.NodeID, p, err = getStr(p); err != nil {
			return Message{}, err
		}
		if h.BusID, p, err = getStr(p); err != nil {
			return Message{}, err
		}
		if len(p) < 1 {
			return Message{}, ErrShort
		}
		h.Role = Role(p[0])
		m.Hello = h
	case MsgVoice:
		v, err := decodeVoice(p)
		if err != nil {
			return Message{}, err
		}
		m.Voice = v
	case MsgTokenRequest, MsgTokenGrant, MsgTokenDeny, MsgTokenRelease:
		tk := &Token{}
		var err error
		if tk.BusID, p, err = getStr(p); err != nil {
			return Message{}, err
		}
		if len(p) < 4 {
			return Message{}, ErrShort
		}
		tk.StreamID = binary.BigEndian.Uint32(p)
		m.Token = tk
	case MsgKeepalive:
		// nothing
	}
	return m, nil
}

func encodeVoice(body []byte, v *Voice) []byte {
	body = putStr(body, v.Env.OriginNode)
	body = putStr(body, v.Env.OriginAttachment)
	body = putStr(body, v.Env.BusID)
	body = append(body, v.Env.HopCount)

	f := v.Frame
	body = append(body, byte(f.Mode), byte(f.Kind))
	body = binary.BigEndian.AppendUint32(body, f.SrcID)
	body = binary.BigEndian.AppendUint32(body, f.DstID)
	body = putStr(body, f.SrcCallsign)
	body = putStr(body, f.DstCallsign)
	body = binary.BigEndian.AppendUint32(body, f.Stream.ID)
	body = append(body, f.Stream.Seq)
	body = append(body, byte(len(f.AMBE)))
	for _, cw := range f.AMBE {
		c := make([]byte, frames.AMBEBytes)
		copy(c, cw) // pad/truncate to the fixed codeword width, so decode is exact
		body = append(body, c...)
	}
	return body
}

func decodeVoice(p []byte) (*Voice, error) {
	v := &Voice{}
	var err error
	if v.Env.OriginNode, p, err = getStr(p); err != nil {
		return nil, err
	}
	if v.Env.OriginAttachment, p, err = getStr(p); err != nil {
		return nil, err
	}
	if v.Env.BusID, p, err = getStr(p); err != nil {
		return nil, err
	}
	if len(p) < 1 {
		return nil, ErrShort
	}
	v.Env.HopCount = p[0]
	p = p[1:]

	if len(p) < 2 {
		return nil, ErrShort
	}
	f := frames.Frame{Mode: frames.Mode(p[0]), Kind: frames.Kind(p[1])}
	p = p[2:]
	if len(p) < 8 {
		return nil, ErrShort
	}
	f.SrcID = binary.BigEndian.Uint32(p)
	f.DstID = binary.BigEndian.Uint32(p[4:])
	p = p[8:]
	if f.SrcCallsign, p, err = getStr(p); err != nil {
		return nil, err
	}
	if f.DstCallsign, p, err = getStr(p); err != nil {
		return nil, err
	}
	if len(p) < 5 {
		return nil, ErrShort
	}
	f.Stream.ID = binary.BigEndian.Uint32(p)
	f.Stream.Seq = p[4]
	p = p[5:]
	if len(p) < 1 {
		return nil, ErrShort
	}
	n := int(p[0])
	p = p[1:]
	if n > 8 { // a voice frame carries at most 5 (YSF); 8 is a hard cap against a hostile count
		return nil, ErrBadMsg
	}
	if len(p) < n*frames.AMBEBytes {
		return nil, ErrShort
	}
	f.AMBE = make([][]byte, n)
	for i := 0; i < n; i++ {
		cw := make([]byte, frames.AMBEBytes)
		copy(cw, p[i*frames.AMBEBytes:])
		f.AMBE[i] = cw
	}
	v.Frame = f
	return v, nil
}

// putStr appends a u8-length-prefixed string (node ids, callsigns, bus ids are all
// short). A string longer than 255 bytes is truncated — none of the real fields
// approach that.
func putStr(b []byte, s string) []byte {
	if len(s) > 255 {
		s = s[:255]
	}
	return append(append(b, byte(len(s))), s...)
}

// getStr reads a u8-length-prefixed string, returning the value and the remaining
// bytes, or ErrShort if the buffer is truncated.
func getStr(p []byte) (string, []byte, error) {
	if len(p) < 1 {
		return "", nil, ErrShort
	}
	n := int(p[0])
	if len(p) < 1+n {
		return "", nil, ErrShort
	}
	return string(p[1 : 1+n]), p[1+n:], nil
}

func orEmptyHello(h *Hello) *Hello {
	if h == nil {
		return &Hello{}
	}
	return h
}
func orEmptyToken(t *Token) *Token {
	if t == nil {
		return &Token{}
	}
	return t
}
func orEmptyVoice(v *Voice) *Voice {
	if v == nil {
		return &Voice{}
	}
	return v
}

// String helps test output.
func (t MsgType) String() string {
	switch t {
	case MsgHello:
		return "hello"
	case MsgVoice:
		return "voice"
	case MsgTokenRequest:
		return "token_request"
	case MsgTokenGrant:
		return "token_grant"
	case MsgTokenDeny:
		return "token_deny"
	case MsgTokenRelease:
		return "token_release"
	case MsgKeepalive:
		return "keepalive"
	default:
		return fmt.Sprintf("msg(%d)", byte(t))
	}
}
