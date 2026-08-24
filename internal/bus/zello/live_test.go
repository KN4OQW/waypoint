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
	cfg := Config{
		Channel:   ch,
		Username:  os.Getenv("ZELLO_USER"),
		Password:  os.Getenv("ZELLO_PASS"),
		AuthToken: os.Getenv("ZELLO_TOKEN"),
	}
	// Key material wins over a pasted token, which is what the daemon does:
	// a token minted here is good for a minute and is created fresh, so the
	// tests exercise the arrangement an operator is meant to run.
	if issuer, keyFile := os.Getenv("ZELLO_ISSUER"), os.Getenv("ZELLO_KEY_FILE"); issuer != "" && keyFile != "" {
		key, err := os.ReadFile(keyFile)
		if err != nil {
			t.Fatalf("reading ZELLO_KEY_FILE: %v", err)
		}
		tok, err := TokenSigner{Issuer: issuer, PrivateKeyPEM: string(key)}.Token(DefaultTokenTTL)
		if err != nil {
			t.Fatalf("minting a token: %v", err)
		}
		cfg.AuthToken = tok
	}
	return cfg
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

// TestLiveMintedTokenLogon proves the operator never has to touch a token again.
//
// The Sample Development Token expires after 30 days, and that was read as an
// operational fact this feature would have to live with. It is not: a node
// holding the issuer and the private key mints its own, and Zello's own
// reference implementation mints one per connection with a sixty-second life.
// This signs a token here and logs on with it, which is the only way to know the
// signature and the claims are right.
//
//	ZELLO_ISSUER=... ZELLO_KEY_FILE=/path/to/key.pem ZELLO_CHANNEL=... \
//	  go test ./internal/bus/zello -run TestLiveMintedToken -v
func TestLiveMintedTokenLogon(t *testing.T) {
	cfg := liveConfig(t)
	issuer, keyFile := os.Getenv("ZELLO_ISSUER"), os.Getenv("ZELLO_KEY_FILE")
	if issuer == "" || keyFile == "" {
		t.Skip("set ZELLO_ISSUER and ZELLO_KEY_FILE to run this")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("reading the private key: %v", err)
	}

	tok, err := TokenSigner{Issuer: issuer, PrivateKeyPEM: string(key)}.Token(DefaultTokenTTL)
	if err != nil {
		t.Fatalf("minting a token: %v", err)
	}
	t.Logf("minted a %v token, %d bytes", DefaultTokenTTL, len(tok))

	cfg.AuthToken = tok
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatalf("the service refused a token we minted: %v", err)
	}
	defer c.Close()
	t.Log("LOGON ACCEPTED with a locally minted token")

	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				t.Fatalf("connection closed: %v", c.Err())
			}
			if ev.Command == EvtOnChannelStatus {
				t.Logf("channel %q %s, %d user(s)", ev.Channel, ev.Status, ev.UsersOnline)
				return
			}
			if ev.Command == EvtOnError {
				t.Fatalf("server error: %s", ev.Error)
			}
		case <-deadline:
			t.Fatal("no on_channel_status within 20 s")
		}
	}
}

// TestLiveAnonymousLogon checks the documented anonymous path, which API.md
// describes as: "(optional for Zello Friends and Family) Username to logon with.
// If not provided the client will connect anonymously."
//
// A receive-only bridge would be the obvious use for it — no account, no password,
// just listen — and the config validator was written to allow exactly that.
func TestLiveAnonymousLogon(t *testing.T) {
	cfg := liveConfig(t)
	issuer, keyFile := os.Getenv("ZELLO_ISSUER"), os.Getenv("ZELLO_KEY_FILE")
	if issuer == "" || keyFile == "" {
		t.Skip("set ZELLO_ISSUER and ZELLO_KEY_FILE to run this")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	signer := TokenSigner{Issuer: issuer, PrivateKeyPEM: string(key)}

	for _, tc := range []struct {
		name       string
		listenOnly bool
	}{
		{"anonymous", false},
		{"anonymous listen-only", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tok, err := signer.Token(DefaultTokenTTL)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			c, err := Dial(ctx, Config{
				Channel:    cfg.Channel,
				AuthToken:  tok,
				ListenOnly: tc.listenOnly,
			})
			if err != nil {
				t.Logf("REFUSED: %v", err)
				return
			}
			defer c.Close()
			t.Log("ACCEPTED — anonymous logon works as documented")
		})
	}
}
