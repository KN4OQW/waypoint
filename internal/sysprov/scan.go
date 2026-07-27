package sysprov

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/privhelper"
)

// Scanning exists so the operator picks their network from a list rather than
// typing its name into a phone keyboard from memory.
//
// That is not a convenience. A mistyped SSID and a wrong passphrase fail the
// same way — the association simply never comes up — so an operator who is
// certain they typed both correctly has no way to tell which one was wrong, or
// that anything was wrong at all. Showing what the radio can actually see
// removes one of the two guesses.

// scanFields are the nmcli terse columns, in order. Terse output is stable
// across nmcli versions in a way the human table is not.
const scanFields = "IN-USE,SSID,SIGNAL,SECURITY"

// NetScan lists the wireless networks the node can see.
func (s *System) NetScan(ctx context.Context, req privhelper.NetScanRequest) (privhelper.NetScanResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.NetScanResponse{}, err
	}
	defer s.mu.Unlock()

	iface := req.Interface
	if iface == "" {
		iface = s.wirelessDevice(ctx)
	}
	if iface == "" {
		return privhelper.NetScanResponse{}, privhelper.Errorf(privhelper.CodeUnsupported,
			"this node has no wireless interface")
	}

	// --rescan no unless explicitly asked. The setup access point is running on
	// this same radio: a fresh sweep makes brcmfmac leave the channel and walk the
	// band, which drops the very network the operator is reading this list over.
	// NetworkManager's cache was populated before the access point went up, which
	// is exactly the list of networks this node can reach.
	rescan := "no"
	if req.Rescan {
		rescan = "yes"
	}
	out, err := s.run(ctx, nil, "nmcli", "-t", "-f", scanFields,
		"device", "wifi", "list", "ifname", iface, "--rescan", rescan)
	if err != nil {
		return privhelper.NetScanResponse{}, privhelper.Errorf(privhelper.CodeInternal,
			"could not list wireless networks: %v", err)
	}

	return privhelper.NetScanResponse{
		Networks: parseScan(out),
		Cached:   !req.Rescan,
	}, nil
}

// parseScan turns nmcli's terse output into networks, strongest first.
//
// Terse output escapes colons inside values with a backslash, so fields are split
// on unescaped colons only — an SSID containing a colon would otherwise shift
// every column after it and report a nonsense signal strength.
func parseScan(out string) []privhelper.ScanNetwork {
	seen := map[string]privhelper.ScanNetwork{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := splitTerse(line)
		if len(f) < 4 {
			continue
		}
		ssid := f[1]
		if ssid == "" {
			continue // a hidden network beacons no name; there is nothing to offer
		}
		signal, _ := strconv.Atoi(f[2])
		n := privhelper.ScanNetwork{
			SSID:     ssid,
			Signal:   signal,
			Security: normalizeSecurity(f[3]),
			InUse:    strings.TrimSpace(f[0]) == "*",
		}
		// One entry per SSID, keeping the strongest. A mesh or an access point on
		// both bands appears once per BSS, and a list with the same name four times
		// is a worse answer than one.
		if prev, ok := seen[ssid]; !ok || n.Signal > prev.Signal {
			if ok && prev.InUse {
				n.InUse = true
			}
			seen[ssid] = n
		}
	}

	list := make([]privhelper.ScanNetwork, 0, len(seen))
	for _, n := range seen {
		list = append(list, n)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Signal != list[j].Signal {
			return list[i].Signal > list[j].Signal
		}
		return list[i].SSID < list[j].SSID
	})
	return list
}

// splitTerse splits an nmcli terse line on unescaped colons and unescapes the
// values.
func splitTerse(line string) []string {
	var fields []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			if i+1 < len(line) {
				i++
				cur.WriteByte(line[i])
			}
		case ':':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(line[i])
		}
	}
	fields = append(fields, cur.String())
	return fields
}

// normalizeSecurity turns nmcli's security column into something short enough for
// a phone screen. An empty column means an open network, which the wizard warns
// about — so it is reported as the explicit "open" rather than as nothing.
func normalizeSecurity(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "--" {
		return "open"
	}
	return s
}
