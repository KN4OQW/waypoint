package netconfig

import (
	"bufio"
	"strings"
)

// keyfile is a minimally-parsed NM keyfile: group -> key -> value. It exists for
// the render round-trip test (render → parse → reconstruct → compare) and for any
// future import path; it is deliberately not used on the write side, where the
// store is authoritative and the file is a compiled output.
type keyfile struct {
	groups map[string]map[string]string
}

// parseKeyfile parses NM keyfile content: [group] headers, key=value lines, and
// '#'/';' comments. The value is everything after the first '=' (NM's rule), with
// surrounding whitespace trimmed.
func parseKeyfile(content string) *keyfile {
	kf := &keyfile{groups: map[string]map[string]string{}}
	cur := ""
	kf.groups[cur] = map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			cur = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := kf.groups[cur]; !ok {
				kf.groups[cur] = map[string]string{}
			}
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		kf.groups[cur][strings.TrimSpace(line[:eq])] = strings.TrimSpace(line[eq+1:])
	}
	return kf
}

// get returns a key's value within a group, or "" if absent.
func (kf *keyfile) get(group, key string) string {
	if g, ok := kf.groups[group]; ok {
		return g[key]
	}
	return ""
}

// has reports whether a group is present.
func (kf *keyfile) has(group string) bool {
	_, ok := kf.groups[group]
	return ok
}

// toConnection reconstructs a Connection from a parsed keyfile. It is the inverse
// of Connection.render for the managed keys, used by the round-trip test to prove
// no operator-facing field is dropped in rendering. The derived uuid and the fixed
// ipv6 group are not reconstructed — they are outputs, not model state. The name
// is recovered by stripping the waypoint- prefix from the [connection] id.
func (kf *keyfile) toConnection() Connection {
	c := Connection{
		Name:        strings.TrimPrefix(kf.get("connection", "id"), profilePrefix),
		Type:        ConnType(kf.get("connection", "type")),
		Interface:   kf.get("connection", "interface-name"),
		Autoconnect: kf.get("connection", "autoconnect") == "true",
	}
	if c.Type == TypeWiFi {
		c.WiFi.SSID = kf.get("wifi", "ssid")
		if kf.has("wifi-security") {
			c.WiFi.PSK = kf.get("wifi-security", "psk")
		}
	}
	c.IPv4.Method = kf.get("ipv4", "method")
	if addr := kf.get("ipv4", "address1"); addr != "" {
		// address1 = <ip>/<prefix>[,<gateway>]
		ipPart := addr
		if comma := strings.IndexByte(addr, ','); comma >= 0 {
			ipPart = addr[:comma]
			c.IPv4.Gateway = addr[comma+1:]
		}
		if slash := strings.IndexByte(ipPart, '/'); slash >= 0 {
			c.IPv4.Address = ipPart[:slash]
			c.IPv4.Prefix = ipPart[slash+1:]
		} else {
			c.IPv4.Address = ipPart
		}
	}
	if dns := kf.get("ipv4", "dns"); dns != "" {
		for _, d := range strings.Split(strings.TrimSuffix(dns, ";"), ";") {
			if d != "" {
				c.IPv4.DNS = append(c.IPv4.DNS, d)
			}
		}
	}
	return c
}
