package zello

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeZello is enough of the Channels API to drive the client: it answers the
// commands that carry a seq and lets a test script whatever else it needs. The
// real service is beta and cannot be depended on in CI, and a gateway account
// and token must never live in this repository.
type fakeZello struct {
	srv *httptest.Server
	url string

	// handle is called for every text frame the client sends. It returns the
	// frames to write back.
	handle func(t *testing.T, conn *websocket.Conn, raw []byte)

	// onConn runs once the socket is open, for tests that need the server to
	// speak first.
	onConn func(t *testing.T, conn *websocket.Conn)
}

func newFakeZello(t *testing.T, f *fakeZello) *fakeZello {
	t.Helper()
	up := websocket.Upgrader{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if f.onConn != nil {
			f.onConn(t, conn)
		}
		for {
			typ, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if typ == websocket.TextMessage && f.handle != nil {
				f.handle(t, conn, raw)
			}
		}
	}))
	f.url = "ws" + strings.TrimPrefix(f.srv.URL, "http")
	t.Cleanup(f.srv.Close)
	return f
}

// logonOK is the common case: accept the logon, then defer to extra.
func logonOK(extra func(t *testing.T, conn *websocket.Conn, m map[string]any)) func(*testing.T, *websocket.Conn, []byte) {
	return func(t *testing.T, conn *websocket.Conn, raw []byte) {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Errorf("server got malformed JSON: %s", raw)
			return
		}
		switch m["command"] {
		case CmdLogon:
			conn.WriteJSON(map[string]any{
				"seq": m["seq"], "success": true, "refresh_token": "refresh-abc",
			})
		default:
			if extra != nil {
				extra(t, conn, m)
			}
		}
	}
}

func dialFake(t *testing.T, f *fakeZello, cfg Config) *Client {
	t.Helper()
	cfg.URL = f.url
	if cfg.Channel == "" {
		cfg.Channel = "Test Channel"
	}
	if cfg.AuthToken == "" {
		cfg.AuthToken = "token"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// Consumer Zello takes exactly one channel per connection, so logon must carry a
// single-entry array — and the refresh token it returns has to be kept, because
// it is what makes a reconnect after a brief outage cheap.
func TestLogonSendsOneChannelAndKeepsTheRefreshToken(t *testing.T) {
	got := make(chan map[string]any, 1)
	f := newFakeZello(t, &fakeZello{
		handle: func(t *testing.T, conn *websocket.Conn, raw []byte) {
			var m map[string]any
			json.Unmarshal(raw, &m)
			if m["command"] == CmdLogon {
				got <- m
				conn.WriteJSON(map[string]any{"seq": m["seq"], "success": true, "refresh_token": "refresh-abc"})
			}
		},
	})
	c := dialFake(t, f, Config{Channel: "Baker Street 221B", Username: "gw", Password: "pw"})

	select {
	case m := <-got:
		ch, ok := m["channels"].([]any)
		if !ok || len(ch) != 1 || ch[0] != "Baker Street 221B" {
			t.Errorf("channels = %v, want exactly one entry", m["channels"])
		}
		if m["auth_token"] != "token" || m["username"] != "gw" || m["password"] != "pw" {
			t.Errorf("credentials not sent as configured: %v", m)
		}
		if pn, _ := m["platform_name"].(string); !strings.Contains(strings.ToLower(pn), "gateway") {
			t.Errorf("platform_name = %q; it should contain Gateway so Zello tracks the bridge's status", pn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never saw a logon")
	}

	if c.RefreshToken != "refresh-abc" {
		t.Errorf("RefreshToken = %q, want refresh-abc", c.RefreshToken)
	}
}

// A refused logon is the failure an operator will actually hit, so the error has
// to say what to do about it rather than repeating the code.
func TestARefusedLogonExplainsItself(t *testing.T) {
	f := newFakeZello(t, &fakeZello{
		handle: func(t *testing.T, conn *websocket.Conn, raw []byte) {
			var m map[string]any
			json.Unmarshal(raw, &m)
			conn.WriteJSON(map[string]any{"seq": m["seq"], "success": false, "error": ErrCodeNotAuthorized})
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, Config{URL: f.url, Channel: "C", AuthToken: "stale"})
	if err == nil {
		t.Fatal("a refused logon was reported as success")
	}
	if !strings.Contains(err.Error(), ErrCodeNotAuthorized) || !strings.Contains(err.Error(), "30 days") {
		t.Errorf("error = %q; it should name the code and mention the 30-day token expiry", err)
	}
}

func TestStartStreamCarriesTheCodecHeaderAndReturnsTheStreamID(t *testing.T) {
	seen := make(chan map[string]any, 1)
	f := newFakeZello(t, &fakeZello{
		handle: logonOK(func(t *testing.T, conn *websocket.Conn, m map[string]any) {
			if m["command"] == CmdStartStream {
				seen <- m
				conn.WriteJSON(map[string]any{"seq": m["seq"], "success": true, "stream_id": 22695})
			}
		}),
	})
	c := dialFake(t, f, Config{})

	id, err := c.StartStream(context.Background(), CodecHeader{SampleRateHz: 16000, FramesPerPacket: 1, FrameSizeMS: 60}, 60)
	if err != nil {
		t.Fatalf("StartStream: %v", err)
	}
	if id != 22695 {
		t.Errorf("stream id = %d, want 22695", id)
	}
	m := <-seen
	if m["codec"] != "opus" || m["type"] != "audio" {
		t.Errorf("start_stream codec/type wrong: %v", m)
	}
	if m["codec_header"] != "gD4BPA==" {
		t.Errorf("codec_header = %v, want the documented gD4BPA==", m["codec_header"])
	}
	if m["packet_duration"] != float64(60) {
		t.Errorf("packet_duration = %v, want 60", m["packet_duration"])
	}
}

// packet_duration outside 2.5-60 ms and a malformed codec header are refused
// here, because the server's answer to both is a stream that opens and then
// carries nothing.
func TestStartStreamRefusesOutOfRangeParameters(t *testing.T) {
	f := newFakeZello(t, &fakeZello{handle: logonOK(nil)})
	c := dialFake(t, f, Config{})

	good := CodecHeader{SampleRateHz: 16000, FramesPerPacket: 1, FrameSizeMS: 60}
	if _, err := c.StartStream(context.Background(), good, 61); err == nil {
		t.Error("packet_duration of 61 ms was accepted")
	}
	bad := CodecHeader{SampleRateHz: 16000, FramesPerPacket: 3, FrameSizeMS: 60}
	if _, err := c.StartStream(context.Background(), bad, 60); err == nil {
		t.Error("frames_per_packet of 3 was accepted")
	}
}

// Outbound audio must be a binary frame with the 9-byte header and a zeroed
// packet_id, which is what API.md says the server expects on this path.
func TestOutboundAudioIsFramedForTheServer(t *testing.T) {
	raw := make(chan []byte, 1)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			typ, b, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if typ == websocket.BinaryMessage {
				raw <- b
				continue
			}
			var m map[string]any
			json.Unmarshal(b, &m)
			conn.WriteJSON(map[string]any{"seq": m["seq"], "success": true, "stream_id": 5})
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Channel: "C", AuthToken: "t"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	if err := c.SendAudio(5, []byte{0xaa, 0xbb}); err != nil {
		t.Fatalf("SendAudio: %v", err)
	}
	select {
	case b := <-raw:
		want := []byte{0x01, 0, 0, 0, 5, 0, 0, 0, 0, 0xaa, 0xbb}
		if len(b) != len(want) {
			t.Fatalf("packet = % x, want % x", b, want)
		}
		for i := range want {
			if b[i] != want[i] {
				t.Fatalf("packet = % x, want % x", b, want)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no binary packet reached the server")
	}
}

func TestInboundStreamAndAudioAreDelivered(t *testing.T) {
	f := newFakeZello(t, &fakeZello{
		handle: logonOK(nil),
		onConn: nil,
	})
	// Push an inbound stream once the client has logged on.
	f.handle = func(t *testing.T, conn *websocket.Conn, raw []byte) {
		var m map[string]any
		json.Unmarshal(raw, &m)
		if m["command"] != CmdLogon {
			return
		}
		conn.WriteJSON(map[string]any{"seq": m["seq"], "success": true})
		conn.WriteJSON(map[string]any{
			"command": EvtOnStreamStart, "stream_id": 99, "from": "alice",
			"codec": "opus", "codec_header": "gD4BPA==", "packet_duration": 60,
		})
		conn.WriteMessage(websocket.BinaryMessage,
			StreamPacket{Type: StreamPacketTypeAudio, StreamID: 99, PacketID: 3, Data: []byte{1, 2, 3}}.Marshal())
		conn.WriteJSON(map[string]any{"command": EvtOnStreamStop, "stream_id": 99})
	}
	c := dialFake(t, f, Config{})

	var start, stop bool
	deadline := time.After(3 * time.Second)
	for !(start && stop) {
		select {
		case ev := <-c.Events():
			switch ev.Command {
			case EvtOnStreamStart:
				start = true
				if ev.From != "alice" || ev.StreamID != 99 {
					t.Errorf("on_stream_start = %+v", ev)
				}
				if h, err := ParseCodecHeader(ev.CodecHeader); err != nil || h.SampleRateHz != 16000 {
					t.Errorf("inbound codec_header did not parse: %v %+v", err, h)
				}
			case EvtOnStreamStop:
				stop = true
			}
		case <-deadline:
			t.Fatalf("timed out; start=%v stop=%v", start, stop)
		}
	}

	select {
	case p := <-c.Audio():
		if p.StreamID != 99 || p.PacketID != 3 || len(p.Data) != 3 {
			t.Errorf("audio packet = %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no audio packet delivered")
	}
}

// The API pings every 30 seconds and drops a client that takes longer than 30
// seconds to pong. gorilla answers automatically, but only from inside its read
// loop — so this is really a test that the reader is running, which is the thing
// that would break if the loop ever gained an early return.
func TestTheClientAnswersServerPings(t *testing.T) {
	ponged := make(chan struct{}, 1)
	f := newFakeZello(t, &fakeZello{
		handle: logonOK(nil),
		onConn: func(t *testing.T, conn *websocket.Conn) {
			conn.SetPongHandler(func(string) error {
				select {
				case ponged <- struct{}{}:
				default:
				}
				return nil
			})
			go func() {
				time.Sleep(100 * time.Millisecond)
				conn.WriteControl(websocket.PingMessage, []byte("keepalive"), time.Now().Add(time.Second))
			}()
		},
	})
	dialFake(t, f, Config{})

	select {
	case <-ponged:
	case <-time.After(3 * time.Second):
		t.Fatal("the client never answered a ping; the server would drop it after 30 s")
	}
}

// When the connection ends the caller has to find out, because the bus state
// behind it needs unwinding. Events closing is that signal.
func TestEventsCloseWhenTheConnectionDrops(t *testing.T) {
	f := newFakeZello(t, &fakeZello{
		handle: func(t *testing.T, conn *websocket.Conn, raw []byte) {
			var m map[string]any
			json.Unmarshal(raw, &m)
			conn.WriteJSON(map[string]any{"seq": m["seq"], "success": true})
			conn.Close()
		},
	})
	c := dialFake(t, f, Config{})

	select {
	case _, ok := <-c.Events():
		if ok {
			// drain until closed
			for range c.Events() {
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Events stayed open after the server hung up")
	}
	if c.Err() == nil {
		t.Error("Err() is nil after an unexpected disconnect")
	}
}

func TestDialRefusesAConfigThatCannotWork(t *testing.T) {
	ctx := context.Background()
	if _, err := Dial(ctx, Config{AuthToken: "t"}); err == nil {
		t.Error("a config with no channel was accepted")
	}
	if _, err := Dial(ctx, Config{Channel: "C"}); err == nil {
		t.Error("a config with neither auth token nor refresh token was accepted")
	}
}
