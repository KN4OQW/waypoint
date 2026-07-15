package netconfig

import (
	"sort"
	"strconv"
)

// WiFiScanResult is one visible Wi-Fi network from a scan, for the join picker.
type WiFiScanResult struct {
	SSID     string `json:"ssid"`
	Signal   int    `json:"signal"`   // 0–100 %
	Security string `json:"security"` // e.g. "WPA2", "" for open
	InUse    bool   `json:"in_use"`   // the currently-associated network
}

// ScanWiFi runs a Wi-Fi scan and returns visible networks, strongest first,
// deduped by SSID (the strongest BSSID per SSID wins). Hidden networks (blank
// SSID) are dropped — they cannot be joined from the list, only via manual entry.
// The server caches the result briefly so the picker does not re-scan on every
// poll.
func ScanWiFi(run Runner) ([]WiFiScanResult, error) {
	out, err := run("nmcli", "-t", "-f", "IN-USE,SSID,SIGNAL,SECURITY", "device", "wifi", "list")
	if err != nil {
		return nil, err
	}
	return parseWiFiScan(out), nil
}

// parseWiFiScan parses `nmcli -t -f IN-USE,SSID,SIGNAL,SECURITY device wifi list`.
// IN-USE is "*" for the associated network. SSID may contain an escaped ':' and is
// blank for a hidden network.
func parseWiFiScan(out string) []WiFiScanResult {
	best := map[string]WiFiScanResult{}
	order := []string{}
	for _, line := range splitLines(out) {
		f := terseFields(line, 4)
		ssid := f[1]
		if ssid == "" {
			continue // hidden network — join via manual entry, not the list
		}
		sig, _ := strconv.Atoi(f[2])
		r := WiFiScanResult{SSID: ssid, Signal: sig, Security: f[3], InUse: f[0] == "*"}
		prev, seen := best[ssid]
		if !seen {
			order = append(order, ssid)
		}
		// Keep the strongest BSSID for this SSID, but never lose the in-use flag.
		if !seen || r.Signal > prev.Signal {
			if seen && prev.InUse {
				r.InUse = true
			}
			best[ssid] = r
		} else if r.InUse {
			prev.InUse = true
			best[ssid] = prev
		}
	}
	results := make([]WiFiScanResult, 0, len(order))
	for _, ssid := range order {
		results = append(results, best[ssid])
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Signal > results[j].Signal })
	return results
}
