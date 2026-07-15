package netconfig

import (
	"fmt"
	"net/netip"
	"strings"
)

// validateWiFi checks a Wi-Fi profile's operator-facing fields. A rendered Wi-Fi
// keyfile with an empty SSID is a dead profile NM will not associate, and the
// regulatory country (when set) must be a 2-letter code.
func validateWiFi(w WiFi) error {
	if strings.TrimSpace(w.SSID) == "" {
		return fmt.Errorf("Wi-Fi profile has no SSID")
	}
	if c := strings.TrimSpace(w.Country); c != "" {
		if len(c) != 2 || !isAlpha(c) {
			return fmt.Errorf("regulatory country %q must be a 2-letter code (e.g. US)", w.Country)
		}
	}
	return nil
}

// validateIPv4 enforces address/prefix/gateway sanity so a bad static config is
// refused before it can be applied and strand the node:
//
//   - manual with no address is an empty static config (rejected);
//   - the address+prefix must parse as a CIDR;
//   - the gateway, if set, must be a valid IP that falls INSIDE that subnet (a
//     gateway outside the subnet is unreachable — the classic way to lock oneself
//     out with a static config);
//   - every DNS server must be a valid IP.
//
// auto (DHCP) needs no address; its optional DNS override / search domains are
// still IP/format-checked.
func validateIPv4(ip IPv4) error {
	method := ip.Method
	if method == "" {
		method = "auto"
	}
	switch method {
	case "auto", "disabled":
		// No address required. Validate any DNS override entries below.
	case "manual":
		if strings.TrimSpace(ip.Address) == "" {
			return fmt.Errorf("static IPv4 requires an address (empty static config refused)")
		}
		prefix := strings.TrimSpace(ip.Prefix)
		if prefix == "" {
			prefix = "24"
		}
		pfx, err := netip.ParsePrefix(ip.Address + "/" + prefix)
		if err != nil {
			return fmt.Errorf("static IPv4 address/prefix %q/%q is not valid: %v", ip.Address, ip.Prefix, err)
		}
		if !pfx.Addr().Is4() {
			return fmt.Errorf("static IPv4 address %q is not an IPv4 address", ip.Address)
		}
		if gw := strings.TrimSpace(ip.Gateway); gw != "" {
			gwAddr, err := netip.ParseAddr(gw)
			if err != nil || !gwAddr.Is4() {
				return fmt.Errorf("gateway %q is not a valid IPv4 address", ip.Gateway)
			}
			// Masked() so the check is against the subnet range, not the host bits.
			if !pfx.Masked().Contains(gwAddr) {
				return fmt.Errorf("gateway %s is outside the subnet %s (would be unreachable)", gw, pfx.Masked())
			}
		}
	default:
		return fmt.Errorf("unknown IPv4 method %q (want auto, manual, or disabled)", ip.Method)
	}
	for _, d := range ip.DNS {
		if d = strings.TrimSpace(d); d != "" {
			if _, err := netip.ParseAddr(d); err != nil {
				return fmt.Errorf("DNS server %q is not a valid IP", d)
			}
		}
	}
	return nil
}

// isAlpha reports whether s is all ASCII letters (used for the country code).
func isAlpha(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}
