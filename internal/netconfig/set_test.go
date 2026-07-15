package netconfig

import (
	"encoding/json"
	"testing"

	"github.com/KN4OQW/waypoint/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The View never carries a PSK — only HasPSK — so the credential never crosses the
// API boundary.
func TestViewRedactsPSK(t *testing.T) {
	m := sampleModel()
	v := m.View()
	b, _ := json.Marshal(v)
	if got := string(b); containsSecret(got) {
		t.Fatalf("view leaked the PSK: %s", got)
	}
	// The wifi connection reports has_psk=true, the ethernet one false.
	if !v.Connections[1].HasPSK {
		t.Error("wifi connection should report has_psk=true")
	}
	if v.Connections[0].HasPSK {
		t.Error("ethernet connection should report has_psk=false")
	}
}

func containsSecret(s string) bool {
	for _, secret := range []string{"s3cr3tpass", "\"psk\""} {
		if contains(s, secret) {
			return true
		}
	}
	return false
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

// A blank incoming PSK preserves the stored one (matched by connection name); a
// non-blank one replaces it. This is the write-only-secret rule, wired now for the
// Wi-Fi surface that lands next.
func TestSetPreservesPSKOnBlank(t *testing.T) {
	s := testStore(t)
	// Seed a wifi connection with a stored PSK.
	seed := Model{Connections: []Connection{
		{Name: "wifi-home", Type: TypeWiFi, WiFi: WiFi{SSID: "Home", PSK: "original-pass"}},
	}}
	if err := s.Set(storeKey, &seed, "test"); err != nil {
		t.Fatal(err)
	}

	// The UI PUTs the connection back with a blank PSK (it never held the secret).
	body := `{"connections":[{"name":"wifi-home","type":"wifi","wifi":{"ssid":"Home-Renamed","psk":""}}]}`
	if err := Set(s, []byte(body), "test"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	m, _ := Load(s)
	if m.Connections[0].WiFi.PSK != "original-pass" {
		t.Errorf("blank PSK should preserve stored, got %q", m.Connections[0].WiFi.PSK)
	}
	if m.Connections[0].WiFi.SSID != "Home-Renamed" {
		t.Errorf("non-secret field should update, got %q", m.Connections[0].WiFi.SSID)
	}

	// A non-blank PSK replaces it.
	body2 := `{"connections":[{"name":"wifi-home","type":"wifi","wifi":{"ssid":"Home-Renamed","psk":"new-pass"}}]}`
	if err := Set(s, []byte(body2), "test"); err != nil {
		t.Fatal(err)
	}
	m, _ = Load(s)
	if m.Connections[0].WiFi.PSK != "new-pass" {
		t.Errorf("non-blank PSK should replace, got %q", m.Connections[0].WiFi.PSK)
	}
}

// A body that omits "connections" merges top-level fields without dropping the
// stored connection list (and its secrets).
func TestSetMergePreservesConnections(t *testing.T) {
	s := testStore(t)
	seed := Model{
		Hostname:    "old-name",
		Connections: []Connection{{Name: "wifi", Type: TypeWiFi, WiFi: WiFi{SSID: "S", PSK: "keep-me"}}},
	}
	if err := s.Set(storeKey, &seed, "test"); err != nil {
		t.Fatal(err)
	}
	if err := Set(s, []byte(`{"hostname":"new-name"}`), "test"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	m, _ := Load(s)
	if m.Hostname != "new-name" {
		t.Errorf("hostname = %q", m.Hostname)
	}
	if len(m.Connections) != 1 || m.Connections[0].WiFi.PSK != "keep-me" {
		t.Errorf("connections dropped by a hostname-only write: %+v", m.Connections)
	}
}

// Set rejects a body that produces an invalid model (validation at save time).
func TestSetRejectsInvalid(t *testing.T) {
	s := testStore(t)
	if err := Set(s, []byte(`{"connections":[{"name":"x","type":"bridge"}]}`), "test"); err == nil {
		t.Fatal("Set should reject an unknown connection type")
	}
}

// Set rejects unknown JSON fields (parity with the radio family's SetSection).
func TestSetRejectsUnknownField(t *testing.T) {
	s := testStore(t)
	if err := Set(s, []byte(`{"bogus":true}`), "test"); err == nil {
		t.Fatal("Set should reject an unknown field")
	}
}
