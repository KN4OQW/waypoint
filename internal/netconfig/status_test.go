package netconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return string(b)
}

// fixtureRunner routes the status commands Collect issues to captured fixtures,
// so the parsers are exercised against real nmcli/timedatectl terse output.
func fixtureRunner(t *testing.T) Runner {
	return func(name string, args ...string) (string, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case name == "hostnamectl":
			return "waypoint-node\n", nil
		case strings.Contains(joined, "device status"):
			return readFixture(t, "device-status.txt"), nil
		case strings.Contains(joined, "device show eth0"):
			return readFixture(t, "device-show-eth0.txt"), nil
		case strings.Contains(joined, "device show wlan0"):
			return readFixture(t, "device-show-wlan0.txt"), nil
		case strings.Contains(joined, "ipv4.method connection show waypoint-lan"):
			return "ipv4.method:manual\n", nil
		case strings.Contains(joined, "ipv4.method connection show MyNetwork"):
			return "ipv4.method:auto\n", nil
		case strings.Contains(joined, "device wifi"):
			return readFixture(t, "wifi-list.txt"), nil
		case strings.Contains(joined, "show-timesync"):
			return "ServerName=time.cloudflare.com\n", nil
		case strings.Contains(joined, "Timezone"):
			return "America/New_York\n", nil
		case strings.Contains(joined, "timedatectl show"):
			return readFixture(t, "timedatectl-show.txt"), nil
		}
		return "", fmt.Errorf("unexpected command: %s", joined)
	}
}

func TestCollectStatus(t *testing.T) {
	s, err := Collect(fixtureRunner(t))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if s.Hostname != "waypoint-node" {
		t.Errorf("hostname = %q", s.Hostname)
	}
	if len(s.Devices) != 4 {
		t.Fatalf("want 4 devices (incl. lo, p2p), got %d", len(s.Devices))
	}

	eth := s.Devices[0]
	want := Device{
		Name: "eth0", Type: "ethernet", State: "connected", Connection: "waypoint-lan",
		MAC: "DC:A6:32:AB:CD:EF", IPv4: "192.168.1.50/24", Gateway: "192.168.1.1",
		DNS: []string{"1.1.1.1", "8.8.8.8"}, Method: "manual", Managed: true,
	}
	if !reflect.DeepEqual(eth, want) {
		t.Errorf("eth0:\n want %+v\n  got %+v", want, eth)
	}

	wlan := s.Devices[1]
	if wlan.Connection != "MyNetwork" || wlan.Managed {
		t.Errorf("wlan0 connection/managed = %q/%v (a hand-made profile is not managed)", wlan.Connection, wlan.Managed)
	}
	if wlan.Method != "auto" || wlan.IPv4 != "192.168.1.60/24" {
		t.Errorf("wlan0 method/ipv4 = %q/%q", wlan.Method, wlan.IPv4)
	}

	// The unmanaged loopback gets no detail lookup and no active connection.
	if s.Devices[2].Name != "lo" || s.Devices[2].Connection != "" {
		t.Errorf("lo device parsed wrong: %+v", s.Devices[2])
	}

	if s.WiFi == nil || s.WiFi.SSID != "MyNetwork" || s.WiFi.Signal != 82 {
		t.Errorf("wifi status = %+v, want SSID MyNetwork signal 82", s.WiFi)
	}
	if !s.NTP.Enabled || !s.NTP.Synchronized || s.NTP.Server != "time.cloudflare.com" {
		t.Errorf("ntp = %+v", s.NTP)
	}
}

// Collect degrades gracefully: a wired-only node with no Wi-Fi device and no
// timesync still yields a Status (the tab renders something), not an error.
func TestCollectPartial(t *testing.T) {
	run := func(name string, args ...string) (string, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case name == "hostnamectl":
			return "node\n", nil
		case strings.Contains(joined, "device status"):
			return "eth0:ethernet:connected:waypoint-lan\n", nil
		case strings.Contains(joined, "device show eth0"):
			return "IP4.ADDRESS[1]:10.0.0.5/24\n", nil
		case strings.Contains(joined, "ipv4.method"):
			return "ipv4.method:manual\n", nil
		}
		return "", fmt.Errorf("unavailable")
	}
	s, err := Collect(run)
	if err != nil {
		t.Fatalf("Collect should not fail on missing wifi/ntp: %v", err)
	}
	if s.WiFi != nil {
		t.Error("no wifi device ⇒ no wifi block")
	}
	if len(s.Devices) != 1 || s.Devices[0].IPv4 != "10.0.0.5/24" {
		t.Errorf("devices = %+v", s.Devices)
	}
}

// A total nmcli failure (device status itself unavailable) is a real error.
func TestCollectHardFailure(t *testing.T) {
	run := func(name string, args ...string) (string, error) {
		if name == "hostnamectl" {
			return "node\n", nil
		}
		return "", fmt.Errorf("nmcli not found")
	}
	if _, err := Collect(run); err == nil {
		t.Fatal("Collect should error when device status is unavailable")
	}
}

// terseFields splits on unescaped colons and unescapes each field — an SSID
// containing a literal ':' must survive.
func TestTerseFieldsEscaping(t *testing.T) {
	got := terseFields(`no:CoffeeShop\:Guest:31`, 3)
	want := []string{"no", "CoffeeShop:Guest", "31"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("terseFields = %q, want %q", got, want)
	}
}
