package zello

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The live tests reach the real Zello service and are skipped unless credentials
// are in the environment. Nothing here embeds one: Waypoint must never ship an
// account or a token, and a repository is exactly the wrong place for either.
//
//	ZELLO_CHANNEL="A Channel" ZELLO_USER=... ZELLO_PASS=... ZELLO_TOKEN=... \
//	  go test ./internal/bus/zello -run Live -v
//
// They exist because everything else in this package is checked against a fake.
// The auth model in particular is documented rather than observed, and the one
// question a fake cannot answer is what the real server does with a given set of
// credentials.
func liveConfig(t *testing.T) Config {
	t.Helper()
	ch := os.Getenv("ZELLO_CHANNEL")
	if ch == "" {
		t.Skip("set ZELLO_CHANNEL (and ZELLO_USER/ZELLO_PASS/ZELLO_TOKEN) to run the live tests")
	}
	return Config{
		Channel:   ch,
		Username:  os.Getenv("ZELLO_USER"),
		Password:  os.Getenv("ZELLO_PASS"),
		AuthToken: os.Getenv("ZELLO_TOKEN"),
	}
}

// TestLiveLogonWithoutAToken asks the real server what a username and password
// alone are worth on consumer Zello.
//
// The documentation says auth_token is required unless refresh_token is given,
// but a client that refuses locally never finds out what the server thinks — and
// "the documentation says so" is not evidence about a live service. This sends
// the logon by hand, bypassing Dial's own validation, and records the answer.
func TestLiveLogonWithoutAToken(t *testing.T) {
	cfg := liveConfig(t)
	if cfg.Username == "" || cfg.Password == "" {
		t.Skip("set ZELLO_USER and ZELLO_PASS to run this")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, DefaultURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	logon := map[string]any{
		"command":  CmdLogon,
		"seq":      1,
		"username": cfg.Username,
		"password": cfg.Password,
		"channels": []string{cfg.Channel},
	}
	if err := conn.WriteJSON(logon); err != nil {
		t.Fatalf("write logon: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	t.Logf("server answered a token-less logon with: %s", raw)

	var r Response
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Success {
		t.Errorf("a username and password alone LOGGED ON — the documented requirement for "+
			"auth_token on Friends and Family does not hold, and Dial refusing such a config is wrong: %s", raw)
		return
	}
	if !strings.Contains(r.Error, ErrCodeNotAuthorized) {
		t.Logf("refused with %q rather than %q; worth recording", r.Error, ErrCodeNotAuthorized)
	}
}

// TestLiveLogon is the one that needs a real token: it is the first time anything
// in this package speaks to Zello and is accepted.
func TestLiveLogon(t *testing.T) {
	cfg := liveConfig(t)
	if cfg.AuthToken == "" {
		t.Skip("set ZELLO_TOKEN (a JWT from developers.zello.com) to run this")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatalf("logon: %v", err)
	}
	defer c.Close()

	if c.RefreshToken == "" {
		t.Error("logon succeeded but returned no refresh token; reconnects will be full logons")
	}

	// The channel has to report online before a transmission is possible —
	// "channel is not ready" is a documented refusal for talking too early.
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				t.Fatalf("connection closed during logon: %v", c.Err())
			}
			t.Logf("event %s channel=%q status=%q users_online=%d", ev.Command, ev.Channel, ev.Status, ev.UsersOnline)
			if ev.Command == EvtOnChannelStatus {
				return
			}
		case <-deadline:
			t.Fatal("no on_channel_status within 20 s; the channel never came online")
		}
	}
}
