package main

import (
	"net/http/httptest"
	"testing"
)

// Property 4: the HTTP redirect handler 301s to the https:// form of the same
// host+path+query, applying a non-default HTTPS port and preserving the rest.
func TestHTTPSRedirect(t *testing.T) {
	cases := []struct {
		httpsPort, host, target, want string
	}{
		// standard port omitted
		{"443", "waypoint.local", "/settings.html?tab=dmr", "https://waypoint.local/settings.html?tab=dmr"},
		// non-default port applied
		{"8443", "192.168.1.50", "/", "https://192.168.1.50:8443/"},
		// host carrying a port is normalized to just the hostname + https port
		{"8443", "192.168.1.50:80", "/api/status", "https://192.168.1.50:8443/api/status"},
		// no configured https port → bare host
		{"", "hs.local", "/x", "https://hs.local/x"},
	}
	for _, c := range cases {
		h := httpsRedirect(c.httpsPort)
		req := httptest.NewRequest("GET", "http://"+c.host+c.target, nil)
		req.Host = c.host
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != 301 {
			t.Errorf("%s%s: status %d, want 301", c.host, c.target, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != c.want {
			t.Errorf("%s%s: Location = %q, want %q", c.host, c.target, got, c.want)
		}
	}
}

func TestPortOf(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1:8073": "8073",
		"0.0.0.0:443":    "443",
		":8443":          "8443",
		"noport":         "",
	} {
		if got := portOf(addr); got != want {
			t.Errorf("portOf(%q) = %q, want %q", addr, got, want)
		}
	}
}
