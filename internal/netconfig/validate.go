package netconfig

import (
	"fmt"
	"net/netip"
	"strconv"
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

// validateVLANs enforces the VLAN rules: the 802.1Q id range (1–4094), a parent
// interface, per-VLAN IPv4 sanity, and uniqueness. The prompt's (parent,id)
// uniqueness is enforced, and — because each VLAN's profile is the flat
// waypoint-vlan<id> — the id must also be unique on its own so two VLANs never
// collide on the same keyfile. Both are checked with precise errors.
func validateVLANs(vlans []VLAN) error {
	byID := map[int]string{}    // id -> parent (for filename-collision detection)
	byPair := map[string]bool{} // "parent/id" (the prompt's uniqueness)
	for _, v := range vlans {
		if v.ID < 1 || v.ID > 4094 {
			return fmt.Errorf("netconfig: VLAN id %d out of range (must be 1–4094)", v.ID)
		}
		if strings.TrimSpace(v.Parent) == "" {
			return fmt.Errorf("netconfig: VLAN %d has no parent interface", v.ID)
		}
		pair := v.Parent + "/" + strconv.Itoa(v.ID)
		if byPair[pair] {
			return fmt.Errorf("netconfig: duplicate VLAN (parent %s, id %d)", v.Parent, v.ID)
		}
		byPair[pair] = true
		if other, ok := byID[v.ID]; ok {
			return fmt.Errorf("netconfig: VLAN id %d is used on both %s and %s — the profile is waypoint-vlan%d, so an id can be used once", v.ID, other, v.Parent, v.ID)
		}
		byID[v.ID] = v.Parent
		if err := validateIPv4(v.IPv4); err != nil {
			return fmt.Errorf("netconfig: VLAN %d: %w", v.ID, err)
		}
	}
	return nil
}

// validateHost checks the hostname is a valid DNS label set (letters, digits,
// hyphens, dots for FQDN; each label ≤63, not starting/ending with a hyphen). A
// blank hostname means "leave it", so it is allowed. The timezone is left to
// timedatectl to validate on apply (checking against the tz database here would
// duplicate it); a blank timezone means "leave it".
func validateHost(h Host) error {
	name := strings.TrimSpace(h.Hostname)
	if name == "" {
		return nil
	}
	if len(name) > 253 {
		return fmt.Errorf("netconfig: hostname %q is too long", h.Hostname)
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("netconfig: hostname %q has an empty or over-long label", h.Hostname)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("netconfig: hostname label %q must not start or end with a hyphen", label)
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return fmt.Errorf("netconfig: hostname %q has an invalid character %q", h.Hostname, string(c))
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
