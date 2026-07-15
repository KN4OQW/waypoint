package netconfig

import (
	"os/exec"
	"strconv"
	"strings"
)

// Status is the live host-network state the Network tab renders. It is read-only
// and derived from the running system (nmcli + timedatectl), NOT from the store —
// it is what the box is actually doing, which the store's rendered config is
// meant to converge to. Served at GET /api/network/status.
type Status struct {
	Hostname string      `json:"hostname"`
	Devices  []Device    `json:"devices"`
	WiFi     *WiFiStatus `json:"wifi,omitempty"` // present only when a Wi-Fi device is associated
	NTP      NTPStatus   `json:"ntp"`
}

// Device is one network interface's live state: its link, the active NM profile,
// and the resolved IPv4 details.
type Device struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`           // ethernet, wifi, …
	State      string   `json:"state"`          // nmcli device STATE (e.g. "connected")
	Connection string   `json:"connection"`     // active NM profile name, or "" if none
	MAC        string   `json:"mac,omitempty"`  // GENERAL.HWADDR
	IPv4       string   `json:"ipv4,omitempty"` // address/prefix, e.g. 192.168.1.50/24
	Gateway    string   `json:"gateway,omitempty"`
	DNS        []string `json:"dns,omitempty"`
	Method     string   `json:"method,omitempty"` // active profile's ipv4.method: auto | manual
	Managed    bool     `json:"managed"`          // whether the active profile is a waypoint-* one
}

// WiFiStatus is the associated Wi-Fi network summary (SSID + signal %).
type WiFiStatus struct {
	SSID   string `json:"ssid"`
	Signal int    `json:"signal"`
}

// NTPStatus is systemd-timesyncd's posture: whether the NTP client is enabled,
// whether the clock is synchronized, and the current upstream server if known.
type NTPStatus struct {
	Enabled      bool   `json:"enabled"`
	Synchronized bool   `json:"synchronized"`
	Server       string `json:"server,omitempty"`
}

// Runner runs a command and returns its combined stdout, for status collection.
// The seam lets Collect be tested against captured fixtures with a fake runner —
// the real one (ExecRunner) shells out to nmcli / timedatectl / hostnamectl.
type Runner func(name string, args ...string) (string, error)

// ExecRunner is the production Runner: it executes the command and returns stdout.
// A non-zero exit surfaces as an error, but Collect treats most command failures
// as "that datum is unavailable" rather than failing the whole status.
func ExecRunner(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

// Collect assembles a Status by running the status commands via run and parsing
// their terse output. It is resilient: a failing sub-command yields a partial
// Status (e.g. no Wi-Fi block on a wired-only node) rather than an error, so the
// tab always renders something. Only a total nmcli failure returns an error.
func Collect(run Runner) (*Status, error) {
	s := &Status{}
	s.Hostname = collectHostname(run)

	devOut, err := run("nmcli", "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device", "status")
	if err != nil {
		return nil, err
	}
	s.Devices = parseDeviceStatus(devOut)
	for i := range s.Devices {
		d := &s.Devices[i]
		if d.Type != "ethernet" && d.Type != "wifi" {
			continue // skip loopback, bridges, p2p, etc.
		}
		if detail, err := run("nmcli", "-t", "-f", "GENERAL.HWADDR,IP4.ADDRESS,IP4.GATEWAY,IP4.DNS", "device", "show", d.Name); err == nil {
			applyDeviceDetail(d, detail)
		}
		if d.Connection != "" {
			d.Managed = managedName(d.Connection)
			if mOut, err := run("nmcli", "-t", "-f", "ipv4.method", "connection", "show", d.Connection); err == nil {
				_, d.Method = terseKV(strings.TrimSpace(firstLine(mOut)))
			}
		}
	}

	if wifiOut, err := run("nmcli", "-t", "-f", "ACTIVE,SSID,SIGNAL", "device", "wifi"); err == nil {
		if w := parseWiFi(wifiOut); w != nil {
			s.WiFi = w
		}
	}

	if tdOut, err := run("timedatectl", "show", "-p", "NTP", "-p", "NTPSynchronized"); err == nil {
		s.NTP = parseNTP(tdOut)
	}
	if tsOut, err := run("timedatectl", "show-timesync", "-p", "ServerName"); err == nil {
		if _, v := terseEq(strings.TrimSpace(firstLine(tsOut))); v != "" {
			s.NTP.Server = v
		}
	}
	return s, nil
}

// collectHostname reads the static hostname. hostnamectl --static is the
// canonical source; a blank/failed read leaves it empty (the tab shows "—").
func collectHostname(run Runner) string {
	if out, err := run("hostnamectl", "--static"); err == nil {
		return strings.TrimSpace(out)
	}
	return ""
}

// parseDeviceStatus parses `nmcli -t -f DEVICE,TYPE,STATE,CONNECTION device status`.
// Each line is four terse fields; a CONNECTION of "--" (nmcli's "no connection"
// sentinel) becomes "".
func parseDeviceStatus(out string) []Device {
	var devs []Device
	for _, line := range splitLines(out) {
		f := terseFields(line, 4)
		if f[0] == "" {
			continue
		}
		conn := f[3]
		if conn == "--" {
			conn = ""
		}
		devs = append(devs, Device{Name: f[0], Type: f[1], State: f[2], Connection: conn})
	}
	return devs
}

// applyDeviceDetail folds `nmcli -t -f GENERAL.HWADDR,IP4.ADDRESS,IP4.GATEWAY,IP4.DNS
// device show <dev>` into a Device. IP4.ADDRESS/DNS are multi-valued (IP4.DNS[1],
// IP4.DNS[2], …); the first address wins and every DNS entry is collected.
func applyDeviceDetail(d *Device, out string) {
	for _, line := range splitLines(out) {
		key, val := terseKV(line)
		if val == "" {
			continue
		}
		base := key
		if br := strings.IndexByte(key, '['); br >= 0 {
			base = key[:br] // strip the [n] index suffix
		}
		switch base {
		case "GENERAL.HWADDR":
			d.MAC = val
		case "IP4.ADDRESS":
			if d.IPv4 == "" {
				d.IPv4 = val
			}
		case "IP4.GATEWAY":
			d.Gateway = val
		case "IP4.DNS":
			d.DNS = append(d.DNS, val)
		}
	}
}

// parseWiFi finds the associated network in `nmcli -t -f ACTIVE,SSID,SIGNAL device
// wifi` (the row whose ACTIVE field is "yes"). Returns nil when nothing is
// associated, so the status omits the Wi-Fi block.
func parseWiFi(out string) *WiFiStatus {
	for _, line := range splitLines(out) {
		f := terseFields(line, 3)
		if f[0] != "yes" {
			continue
		}
		sig, _ := strconv.Atoi(f[2])
		return &WiFiStatus{SSID: f[1], Signal: sig}
	}
	return nil
}

// parseNTP folds `timedatectl show -p NTP -p NTPSynchronized` (KEY=VALUE lines)
// into an NTPStatus.
func parseNTP(out string) NTPStatus {
	var st NTPStatus
	for _, line := range splitLines(out) {
		k, v := terseEq(line)
		switch k {
		case "NTP":
			st.Enabled = v == "yes"
		case "NTPSynchronized":
			st.Synchronized = v == "yes"
		}
	}
	return st
}

// --- terse-output helpers -------------------------------------------------

// splitLines splits command output into non-empty, trimmed lines.
func splitLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimRight(l, "\r"); strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func firstLine(out string) string {
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		return out[:i]
	}
	return out
}

// terseKV splits one `nmcli -t` field line on its first unescaped ':' into a
// (field-name, value) pair and unescapes the value. Field names never contain a
// colon, so the first ':' is always the separator; values may contain '\:'
// (e.g. a MAC address DC\:A6\:32\:…).
func terseKV(line string) (key, val string) {
	i := indexUnescaped(line, ':')
	if i < 0 {
		return line, ""
	}
	return line[:i], unescapeTerse(line[i+1:])
}

// terseEq splits a KEY=VALUE line (timedatectl show) on the first '='.
func terseEq(line string) (key, val string) {
	i := strings.IndexByte(line, '=')
	if i < 0 {
		return line, ""
	}
	return line[:i], strings.TrimSpace(line[i+1:])
}

// terseFields splits a multi-field `nmcli -t` line into exactly n unescaped-colon
// -separated fields (padding with "" if short), unescaping each. `nmcli -t`
// escapes literal ':' and '\' within a field, so a plain Split on ':' would
// mangle a value; this splits only on unescaped colons.
func terseFields(line string, n int) []string {
	out := make([]string, n)
	idx := 0
	start := 0
	for i := 0; i < len(line) && idx < n-1; i++ {
		if line[i] == '\\' {
			i++ // skip the escaped char
			continue
		}
		if line[i] == ':' {
			out[idx] = unescapeTerse(line[start:i])
			idx++
			start = i + 1
		}
	}
	out[idx] = unescapeTerse(line[start:])
	return out
}

// indexUnescaped returns the index of the first unescaped byte c, or -1.
func indexUnescaped(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == c {
			return i
		}
	}
	return -1
}

// unescapeTerse reverses nmcli -t's field escaping: "\:" -> ":", "\\" -> "\".
func unescapeTerse(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
