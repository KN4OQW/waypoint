package netconfig

import "testing"

func TestValidateIPv4(t *testing.T) {
	cases := []struct {
		name string
		ip   IPv4
		ok   bool
	}{
		{"dhcp ok", IPv4{Method: "auto"}, true},
		{"blank method = dhcp ok", IPv4{}, true},
		{"static ok", IPv4{Method: "manual", Address: "192.168.1.50", Prefix: "24", Gateway: "192.168.1.1"}, true},
		{"static default prefix", IPv4{Method: "manual", Address: "192.168.1.50", Gateway: "192.168.1.1"}, true},
		{"empty static refused", IPv4{Method: "manual"}, false},
		{"gateway outside subnet", IPv4{Method: "manual", Address: "192.168.1.50", Prefix: "24", Gateway: "10.0.0.1"}, false},
		{"gateway inside /30", IPv4{Method: "manual", Address: "192.168.1.1", Prefix: "30", Gateway: "192.168.1.2"}, true},
		{"bad address", IPv4{Method: "manual", Address: "not-an-ip", Prefix: "24"}, false},
		{"bad gateway", IPv4{Method: "manual", Address: "192.168.1.5", Prefix: "24", Gateway: "nope"}, false},
		{"bad dns", IPv4{Method: "auto", DNS: []string{"1.1.1.1", "garbage"}}, false},
		{"dhcp dns override ok", IPv4{Method: "auto", DNS: []string{"1.1.1.1"}}, true},
		{"unknown method", IPv4{Method: "bootp"}, false},
	}
	for _, tc := range cases {
		err := validateIPv4(tc.ip)
		if tc.ok && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}
}

func TestValidateWiFi(t *testing.T) {
	cases := []struct {
		name string
		w    WiFi
		ok   bool
	}{
		{"ssid ok", WiFi{SSID: "Net"}, true},
		{"ssid + country ok", WiFi{SSID: "Net", Country: "US"}, true},
		{"lowercase country ok", WiFi{SSID: "Net", Country: "gb"}, true},
		{"no ssid", WiFi{}, false},
		{"blank ssid", WiFi{SSID: "  "}, false},
		{"bad country len", WiFi{SSID: "Net", Country: "USA"}, false},
		{"bad country char", WiFi{SSID: "Net", Country: "1!"}, false},
	}
	for _, tc := range cases {
		err := validateWiFi(tc.w)
		if tc.ok && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}
}

// Model.Validate wires the per-connection checks and prefixes the connection name.
func TestModelValidateSurfacesConnErrors(t *testing.T) {
	m := Model{Connections: []Connection{
		{Name: "wifi", Type: TypeWiFi, WiFi: WiFi{SSID: "Net"}, IPv4: IPv4{Method: "manual", Address: "192.168.1.5", Prefix: "24", Gateway: "10.0.0.1"}},
	}}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected a validation error for gateway outside subnet")
	}
	if !contains(err.Error(), "wifi") || !contains(err.Error(), "outside the subnet") {
		t.Fatalf("error should name the connection and reason: %v", err)
	}
}
