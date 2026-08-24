package zello

import (
	"bytes"
	"encoding/json"
	"testing"
)

// API.md gives one worked example of codec_header and it is the whole test:
// "value of gD4BPA== in base64 decodes to {0x80, 0x3e, 0x01, 0x3c} which
// represents 16000 Hz sample rate, 1 frame per packet, 60 ms frame size."
//
// It pins the byte order too. 16000 is 0x3e80, and the bytes are 0x80 then 0x3e,
// so the sample rate is little-endian — the opposite convention to the binary
// stream packets, which are network byte order. Getting this backwards yields
// 32830 Hz, which the server accepts and then renders as noise.
func TestCodecHeaderMatchesTheDocumentedExample(t *testing.T) {
	h := CodecHeader{SampleRateHz: 16000, FramesPerPacket: 1, FrameSizeMS: 60}
	const want = "gD4BPA=="
	if got := h.Encode(); got != want {
		t.Errorf("Encode() = %q, want %q", got, want)
	}

	back, err := ParseCodecHeader(want)
	if err != nil {
		t.Fatalf("ParseCodecHeader: %v", err)
	}
	if back != h {
		t.Errorf("round trip gave %+v, want %+v", back, h)
	}
}

func TestCodecHeaderRejectsMalformedInput(t *testing.T) {
	for _, in := range []string{"", "not base64!!", "AAA=", "AAAAAAAA"} {
		if _, err := ParseCodecHeader(in); err == nil {
			t.Errorf("ParseCodecHeader(%q) succeeded; it is not a 4-byte header", in)
		}
	}
}

// frames_per_packet is documented as 1 or 2, and the frame size as 2.5-60 ms.
// A header outside that is refused here rather than at the server, where the
// failure arrives as a stream that starts and carries nothing.
func TestCodecHeaderValidation(t *testing.T) {
	ok := CodecHeader{SampleRateHz: 16000, FramesPerPacket: 1, FrameSizeMS: 60}
	if err := ok.Valid(); err != nil {
		t.Errorf("the documented example was refused: %v", err)
	}
	bad := []CodecHeader{
		{SampleRateHz: 0, FramesPerPacket: 1, FrameSizeMS: 20},
		{SampleRateHz: 16000, FramesPerPacket: 3, FrameSizeMS: 20},
		{SampleRateHz: 16000, FramesPerPacket: 1, FrameSizeMS: 61},
	}
	for _, h := range bad {
		if err := h.Valid(); err == nil {
			t.Errorf("Valid() accepted %+v", h)
		}
	}
}

// {type(8) = 0x01, stream_id(32), packet_id(32), data[]}, network byte order.
func TestStreamPacketWireLayout(t *testing.T) {
	p := StreamPacket{
		Type:     StreamPacketTypeAudio,
		StreamID: 0x11223344,
		PacketID: 0x55667788,
		Data:     []byte{0xde, 0xad},
	}
	want := []byte{0x01, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0xde, 0xad}
	if got := p.Marshal(); !bytes.Equal(got, want) {
		t.Fatalf("Marshal() = % x, want % x", got, want)
	}

	back, err := ParseStreamPacket(want)
	if err != nil {
		t.Fatalf("ParseStreamPacket: %v", err)
	}
	if back.Type != p.Type || back.StreamID != p.StreamID || back.PacketID != p.PacketID {
		t.Errorf("header round trip gave %+v, want %+v", back, p)
	}
	if !bytes.Equal(back.Data, p.Data) {
		t.Errorf("payload = % x, want % x", back.Data, p.Data)
	}
}

func TestStreamPacketRejectsAShortHeader(t *testing.T) {
	for n := 0; n < StreamPacketHeaderBytes; n++ {
		if _, err := ParseStreamPacket(make([]byte, n)); err == nil {
			t.Errorf("a %d-byte packet was accepted; the header is %d bytes", n, StreamPacketHeaderBytes)
		}
	}
}

// A header with no payload is well formed. Refusing it would turn the server's
// problem into a framing error and hide what actually happened.
func TestStreamPacketAcceptsAnEmptyPayload(t *testing.T) {
	p, err := ParseStreamPacket(make([]byte, StreamPacketHeaderBytes))
	if err != nil {
		t.Fatalf("ParseStreamPacket: %v", err)
	}
	if len(p.Data) != 0 {
		t.Errorf("Data = % x, want empty", p.Data)
	}
}

// Logon must not emit a password field when there is none: an empty password
// alongside a username is a different request from no username at all, and the
// second is the anonymous listen-only path.
func TestLogonOmitsUnsetOptionalFields(t *testing.T) {
	b, err := json.Marshal(Logon{
		Command:   CmdLogon,
		Seq:       1,
		AuthToken: "tok",
		Channels:  []string{"A Channel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"password", "username", "refresh_token", "listen_only", "platform_name"} {
		if bytes.Contains(b, []byte(`"`+absent+`"`)) {
			t.Errorf("logon carried %q when it was not set: %s", absent, b)
		}
	}
	if !bytes.Contains(b, []byte(`"channels":["A Channel"]`)) {
		t.Errorf("channels missing or wrong: %s", b)
	}
}

// Responses and events share the text channel. A non-zero seq is the only thing
// that distinguishes a reply to our command from an unsolicited event, so the
// classification is worth pinning.
func TestTextFramesAreClassifiedBySeq(t *testing.T) {
	cases := []struct {
		in         string
		wantIsResp bool
		wantCmd    string
	}{
		{`{"seq":2,"success":true,"stream_id":22695}`, true, ""},
		{`{"seq":1,"error":"not authorized"}`, true, ""},
		{`{"command":"on_stream_start","stream_id":22695,"from":"alice"}`, false, EvtOnStreamStart},
		{`{"command":"on_channel_status","channel":"A","status":"online","users_online":3}`, false, EvtOnChannelStatus},
		{`{"command":"on_error","error":"server closed connection"}`, false, EvtOnError},
	}
	for _, c := range cases {
		e, isResp, err := decodeText([]byte(c.in))
		if err != nil {
			t.Errorf("decodeText(%s): %v", c.in, err)
			continue
		}
		if isResp != c.wantIsResp {
			t.Errorf("decodeText(%s) isResponse = %v, want %v", c.in, isResp, c.wantIsResp)
		}
		if e.Command != c.wantCmd {
			t.Errorf("decodeText(%s) command = %q, want %q", c.in, e.Command, c.wantCmd)
		}
	}
}

// The API is beta and documented as subject to change, so an unknown field must
// not break a working bridge.
func TestUnknownFieldsAreIgnored(t *testing.T) {
	e, _, err := decodeText([]byte(`{"command":"on_stream_start","stream_id":7,"something_new":{"a":1}}`))
	if err != nil {
		t.Fatalf("an unrecognised field broke decoding: %v", err)
	}
	if e.StreamID != 7 {
		t.Errorf("stream_id = %d, want 7", e.StreamID)
	}
}

// Recorded as a test because the design that preceded this package got both
// wrong: the listen-only code has no hyphen, and `no phone` is not a documented
// code at all. A client keying off either mistake fails silently.
func TestErrorCodeSpellings(t *testing.T) {
	if ErrCodeListenOnly != "listen only connection" {
		t.Errorf("ErrCodeListenOnly = %q; the hyphenated form is the description, not the code", ErrCodeListenOnly)
	}
	if ErrCodeNotAuthorized != "not authorized" {
		t.Errorf("ErrCodeNotAuthorized = %q", ErrCodeNotAuthorized)
	}
}
