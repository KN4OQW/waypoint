package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/store"
)

// These drive the real HTTP handlers rather than the config package directly,
// because the round trip the panel depends on spans both: what a PUT stores, and
// what the next GET hands back to a browser that must never see a secret.

func zelloTestServer(t *testing.T) *server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &server{store: st}
}

func put(t *testing.T, s *server, section, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, "/api/config/"+section, strings.NewReader(body))
	r.SetPathValue("section", section)
	w := httptest.NewRecorder()
	s.configPut(w, r)
	return w
}

func view(t *testing.T, s *server) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	w := httptest.NewRecorder()
	s.configView(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/config = %d: %s", w.Code, w.Body.String())
	}
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("view is not JSON: %v", err)
	}
	return v
}

func seedBus(t *testing.T, s *server) {
	t.Helper()
	if w := put(t, s, "buses", `[{"id":"b1","name":"Bus 1","enabled":true}]`); w.Code != http.StatusNoContent {
		t.Fatalf("seeding a bus: %d %s", w.Code, w.Body.String())
	}
}

// The whole point of the panel: what goes in comes back, minus the secrets.
func TestZelloRoundTripsThroughTheAPI(t *testing.T) {
	s := zelloTestServer(t)
	seedBus(t, s)

	acct := `[{"name":"bridge","username":"kn4oqw-bridge","password":"pw",
	           "issuer":"ISS.aaa","private_key":"-----BEGIN PRIVATE KEY-----k-----END PRIVATE KEY-----",
	           "auth_token":"","enabled":true}]`
	if w := put(t, s, "zello_accounts", acct); w.Code != http.StatusNoContent {
		t.Fatalf("PUT accounts = %d: %s", w.Code, w.Body.String())
	}
	chans := `[{"id":"z1","bus_id":"b1","channel":"Ham Radio","account_ref":"bridge",
	            "listen_only":false,"packet_ms":20,"enabled":true}]`
	if w := put(t, s, "zello_channels", chans); w.Code != http.StatusNoContent {
		t.Fatalf("PUT channels = %d: %s", w.Code, w.Body.String())
	}

	v := view(t, s)
	raw, _ := json.Marshal(v)
	// No secret may reach the browser, in any field, under any name.
	for _, secret := range []string{`"pw"`, "BEGIN PRIVATE KEY", `"private_key"`, `"password"`, `"auth_token"`} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the config view carried %s", secret)
		}
	}
	// What the panel needs instead: presence, and which credential arrangement.
	for _, want := range []string{`"has_password":true`, `"has_private_key":true`, `"can_mint_tokens":true`, `"issuer":"ISS.aaa"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the config view is missing %s", want)
		}
	}

	// The channel is not a secret and comes back whole.
	got, _ := json.Marshal(v["zello_channels"])
	for _, want := range []string{`"channel":"Ham Radio"`, `"account_ref":"bridge"`, `"packet_ms":20`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("channel round trip lost %s: %s", want, got)
		}
	}
}

// The panel never has the secrets, so it sends them blank. Blank must mean keep,
// or saving any other field would erase the token and the bridge would stop
// connecting with nothing on screen to explain it.
func TestASaveWithBlankSecretsKeepsThem(t *testing.T) {
	s := zelloTestServer(t)
	seedBus(t, s)

	first := `[{"name":"bridge","username":"a","password":"pw","issuer":"ISS.aaa",
	            "private_key":"KEYMATERIAL","auth_token":"","enabled":true}]`
	if w := put(t, s, "zello_accounts", first); w.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
	}

	// Exactly what the panel sends back after editing only the username.
	second := `[{"name":"bridge","username":"b","password":"","issuer":"ISS.aaa",
	             "private_key":"","auth_token":"","enabled":true}]`
	if w := put(t, s, "zello_accounts", second); w.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
	}

	var stored []config.ZelloAccount
	if _, err := s.store.GetInto("zello_accounts", &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("got %d accounts", len(stored))
	}
	if stored[0].Username != "b" {
		t.Errorf("the edit did not apply: username = %q", stored[0].Username)
	}
	if stored[0].Password != "pw" || stored[0].PrivateKey != "KEYMATERIAL" {
		t.Errorf("blank fields erased the secrets: %+v", stored[0])
	}
}

// A configuration the daemon could not start must be refused at the save, with a
// reason the panel can show verbatim.
func TestTheAPIRefusesAZelloConfigThatCannotRun(t *testing.T) {
	s := zelloTestServer(t)
	seedBus(t, s)
	if w := put(t, s, "zello_accounts",
		`[{"name":"bridge","username":"a","password":"pw","issuer":"ISS.aaa","private_key":"K","auth_token":"","enabled":true}]`); w.Code != http.StatusNoContent {
		t.Fatalf("seeding the account: %d %s", w.Code, w.Body.String())
	}

	for _, tc := range []struct{ name, body, wants string }{
		{"a channel on a bus that does not exist",
			`[{"id":"z1","bus_id":"nope","channel":"X","account_ref":"bridge","enabled":true}]`, "does not exist"},
		{"a channel naming an account that does not exist",
			`[{"id":"z1","bus_id":"b1","channel":"X","account_ref":"ghost","enabled":true}]`, "does not exist"},
		{"a packet size Opus does not have",
			`[{"id":"z1","bus_id":"b1","channel":"X","account_ref":"bridge","packet_ms":30,"enabled":true}]`, "5, 10, 20, 40 or 60"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := put(t, s, "zello_channels", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wants) {
				t.Errorf("body %q does not contain %q; the panel shows this verbatim", w.Body.String(), tc.wants)
			}
		})
	}
}

// An account with no username cannot log on at all — measured against the live
// service, not inferred — so it is refused here rather than at the first connect.
func TestTheAPIRefusesAnAccountWithNoUsername(t *testing.T) {
	s := zelloTestServer(t)
	seedBus(t, s)
	if w := put(t, s, "zello_accounts",
		`[{"name":"bridge","username":"","password":"pw","issuer":"I","private_key":"K","auth_token":"","enabled":true}]`); w.Code != http.StatusNoContent {
		t.Fatalf("an account with no channel attached should still save: %d", w.Code)
	}
	w := put(t, s, "zello_channels", `[{"id":"z1","bus_id":"b1","channel":"X","account_ref":"bridge","enabled":true}]`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "dedicated Zello account") {
		t.Errorf("body %q should tell the operator the node needs its own account", w.Body.String())
	}
}
