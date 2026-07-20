package peer

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"reflect"
	"testing"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
)

func sampleVoice(seed int64) *Voice {
	rng := rand.New(rand.NewSource(seed))
	n := 1 + rng.Intn(5)
	ambe := make([][]byte, n)
	for i := range ambe {
		cw := make([]byte, frames.AMBEBytes)
		rng.Read(cw)
		ambe[i] = cw
	}
	return &Voice{
		Env: Envelope{OriginNode: "shack", OriginAttachment: "ysf", BusID: "A", HopCount: byte(rng.Intn(4))},
		Frame: frames.Frame{
			Mode: frames.ModeYSF, Kind: frames.KindVoice,
			SrcID: 3180202, DstID: 91, SrcCallsign: "KN4OQW", DstCallsign: "ALL",
			Stream: frames.Stream{ID: 0xDEADBEEF, Seq: byte(rng.Intn(256))}, AMBE: ambe,
		},
	}
}

func allSamples() []Message {
	return []Message{
		{Type: MsgHello, Hello: &Hello{NodeID: "garage", BusID: "A", Role: RoleMember}},
		{Type: MsgHello, Hello: &Hello{NodeID: "shack", BusID: "A", Role: RoleOwner}},
		{Type: MsgVoice, Voice: sampleVoice(1)},
		{Type: MsgVoice, Voice: sampleVoice(2)},
		{Type: MsgTokenRequest, Token: &Token{BusID: "A", StreamID: 0x1234}},
		{Type: MsgTokenGrant, Token: &Token{BusID: "A", StreamID: 0x1234}},
		{Type: MsgTokenDeny, Token: &Token{BusID: "A", StreamID: 0x1234}},
		{Type: MsgTokenRelease, Token: &Token{BusID: "A", StreamID: 0x1234}},
		{Type: MsgKeepalive},
	}
}

// TestWireRoundTrip: Decode(Encode(m)-without-prefix) == m for every message type.
func TestWireRoundTrip(t *testing.T) {
	for _, m := range allSamples() {
		wire := m.Encode()
		// strip the 2-byte length prefix, and check it matches the body length
		if len(wire) < 2 {
			t.Fatalf("%s: encoded too short", m.Type)
		}
		if int(binary.BigEndian.Uint16(wire)) != len(wire)-2 {
			t.Fatalf("%s: length prefix wrong", m.Type)
		}
		got, err := Decode(wire[2:])
		if err != nil {
			t.Fatalf("%s: decode: %v", m.Type, err)
		}
		if !reflect.DeepEqual(got, m) {
			t.Fatalf("%s round-trip mismatch:\n want %+v\n  got %+v", m.Type, m, got)
		}
	}
}

// TestWireRejectsMalformed: bad version, unknown type, and truncations error
// (never panic).
func TestWireRejectsMalformed(t *testing.T) {
	cases := [][]byte{
		{},                                // empty
		{ProtocolVersion},                 // no type
		{9, byte(MsgVoice)},               // bad version
		{ProtocolVersion, 0},              // type 0 invalid
		{ProtocolVersion, 99},             // unknown type
		{ProtocolVersion, byte(MsgVoice)}, // voice, no payload
		{ProtocolVersion, byte(MsgHello), 5, 'a'},        // hello nodeID len 5 but 1 byte
		{ProtocolVersion, byte(MsgTokenRequest), 1, 'A'}, // token busID ok, no streamID
		{ProtocolVersion, byte(MsgVoice), 200, 'x'},      // voice originNode len 200, truncated
	}
	for i, c := range cases {
		if _, err := Decode(c); err == nil {
			t.Fatalf("case %d %x should have errored", i, c)
		}
	}
}

// TestVoiceAMBEPreserved: the AMBE codewords survive encode/decode byte-exactly.
func TestVoiceAMBEPreserved(t *testing.T) {
	v := sampleVoice(42)
	m := Message{Type: MsgVoice, Voice: v}
	got, err := Decode(m.Encode()[2:])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Voice.Frame.AMBE, v.Frame.AMBE) {
		t.Fatalf("AMBE not preserved:\n want %x\n  got %x", v.Frame.AMBE, got.Voice.Frame.AMBE)
	}
	if got.Voice.Env != v.Env {
		t.Fatalf("envelope not preserved: %+v vs %+v", got.Voice.Env, v.Env)
	}
}

// TestReadWriteMessage exercises the streaming framing over an in-memory buffer:
// several messages back to back are read one at a time.
func TestReadWriteMessage(t *testing.T) {
	var buf bytes.Buffer
	msgs := allSamples()
	for _, m := range msgs {
		if err := WriteMessage(&buf, m); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range msgs {
		got, err := ReadMessage(&buf)
		if err != nil {
			t.Fatalf("msg %d read: %v", i, err)
		}
		if got.Type != want.Type {
			t.Fatalf("msg %d type %s, want %s", i, got.Type, want.Type)
		}
	}
}
