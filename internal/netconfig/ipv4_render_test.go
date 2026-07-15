package netconfig

import (
	"strings"
	"testing"
)

// ethWith renders a single ethernet connection with the given IPv4 config.
func ethWith(ip IPv4) string {
	return Connection{Name: "lan", Type: TypeEthernet, Interface: "eth0", Autoconnect: true, IPv4: ip}.render()
}

// The three IPv4 shapes the surface must render: DHCP, static, and static/DHCP
// with a DNS override.
func TestIPv4RenderShapes(t *testing.T) {
	cases := []struct {
		name    string
		ip      IPv4
		want    []string // substrings that MUST appear
		notWant []string // substrings that must NOT appear
	}{
		{
			name:    "dhcp",
			ip:      IPv4{Method: "auto"},
			want:    []string{"method=auto"},
			notWant: []string{"address1=", "dns=", "ignore-auto-dns"},
		},
		{
			name:    "static",
			ip:      IPv4{Method: "manual", Address: "192.168.1.50", Prefix: "24", Gateway: "192.168.1.1"},
			want:    []string{"method=manual", "address1=192.168.1.50/24,192.168.1.1"},
			notWant: []string{"ignore-auto-dns"}, // no DNS given → no override marker
		},
		{
			name:    "static+dns",
			ip:      IPv4{Method: "manual", Address: "10.0.0.5", Prefix: "8", DNS: []string{"1.1.1.1", "9.9.9.9"}},
			want:    []string{"method=manual", "address1=10.0.0.5/8", "dns=1.1.1.1;9.9.9.9;"},
			notWant: []string{"ignore-auto-dns"}, // static DNS stands alone, not an override
		},
		{
			name:    "dhcp+dns-override",
			ip:      IPv4{Method: "auto", DNS: []string{"1.1.1.1"}},
			want:    []string{"method=auto", "dns=1.1.1.1;", "ignore-auto-dns=true"},
			notWant: []string{"address1="},
		},
		{
			name:    "static+search-domains",
			ip:      IPv4{Method: "manual", Address: "10.0.0.5", Prefix: "24", SearchDomains: []string{"lan", "example.org"}},
			want:    []string{"dns-search=lan;example.org;"},
			notWant: nil,
		},
	}
	for _, tc := range cases {
		out := ethWith(tc.ip)
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q\n---\n%s", tc.name, w, out)
			}
		}
		for _, nw := range tc.notWant {
			if strings.Contains(out, nw) {
				t.Errorf("%s: should not contain %q\n---\n%s", tc.name, nw, out)
			}
		}
	}
}

// autoconnect-priority and hidden render only when set, and round-trip.
func TestPriorityAndHiddenRender(t *testing.T) {
	c := Connection{
		Name: "wifi", Type: TypeWiFi, Autoconnect: true, Priority: "10",
		WiFi: WiFi{SSID: "Net", PSK: "pw", Hidden: true},
		IPv4: IPv4{Method: "auto"},
	}
	out := c.render()
	for _, w := range []string{"autoconnect-priority=10", "hidden=true"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q\n%s", w, out)
		}
	}
	// A zero/blank priority renders no key.
	c.Priority = "0"
	if strings.Contains(c.render(), "autoconnect-priority") {
		t.Error("priority 0 should render no autoconnect-priority key")
	}

	// Priority + hidden survive the render→parse round-trip (country does not — it
	// is applied out of band, not written to the keyfile).
	c.Priority = "10"
	got := parseKeyfile(c.render()).toConnection()
	if got.Priority != "10" || !got.WiFi.Hidden {
		t.Errorf("priority/hidden did not round-trip: %+v", got)
	}
}

// A DHCP+override profile round-trips its DNS + search domains.
func TestIPv4OverrideRoundTrip(t *testing.T) {
	c := Connection{Name: "lan", Type: TypeEthernet, IPv4: IPv4{
		Method: "auto", DNS: []string{"1.1.1.1", "8.8.8.8"}, SearchDomains: []string{"lan"},
	}}
	got := parseKeyfile(c.render()).toConnection()
	if len(got.IPv4.DNS) != 2 || got.IPv4.DNS[0] != "1.1.1.1" || len(got.IPv4.SearchDomains) != 1 {
		t.Fatalf("override did not round-trip: %+v", got.IPv4)
	}
}
