// Package setupap decides what the setup access point is called and whether it
// is protected.
//
// Both answers have to be reachable by unprivileged waypointd, before any
// privileged call is made, because they are arguments to APUp rather than
// decisions the helper makes. Neither input is secret: /proc/cpuinfo is
// world-readable, and the optional key file on the boot partition is written by
// an operator with the card in their hand.
package setupap

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const (
	// CPUInfoPath carries the Raspberry Pi board serial.
	CPUInfoPath = "/proc/cpuinfo"

	// PSKFilePath is the optional operator-supplied key file. It is on the boot
	// partition because that is the one filesystem a Windows or macOS machine can
	// write to with the card in a reader — the same place Raspberry Pi Imager used
	// to drop its own seed files, and the only way to influence a node before it
	// has ever booted.
	PSKFilePath = "/boot/firmware/waypoint-setup.txt"

	// SSIDPrefix is the fixed part of the setup network's name.
	SSIDPrefix = "Waypoint-Setup-"

	// FallbackSuffix names an AP on hardware whose serial cannot be read. It is
	// deliberately not a random value: an operator who reflashes a card should see
	// the same network name, and a node that cannot identify itself is better
	// described as unknown than given a fake identity.
	FallbackSuffix = "0000"

	// WPA2 passphrase bounds, from the standard.
	minPSK = 8
	maxPSK = 63
)

// Serial returns the board serial from /proc/cpuinfo, or "" if there is none.
// x86 development machines have no Serial line at all, which is not an error.
func Serial(cpuinfoPath string) string {
	f, err := os.Open(cpuinfoPath)
	if err != nil {
		return ""
	}
	defer f.Close() //nolint:errcheck // read-only

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), ":")
		if !ok || strings.TrimSpace(key) != "Serial" {
			continue
		}
		return strings.TrimSpace(value)
	}
	return ""
}

// SSID returns the setup network's name: the prefix plus the last four
// characters of the board serial.
//
// The suffix exists for a specific failure an operator hits at a hamfest or in a
// club shack — several unconfigured Waypoints powered up in the same room, all
// broadcasting an identically named network, and no way to tell which one their
// phone just joined. Four hex characters from the serial is enough to
// disambiguate a room's worth of nodes and short enough to read off a screen.
func SSID(cpuinfoPath string) string {
	return SSIDForSerial(Serial(cpuinfoPath))
}

// SSIDForSerial builds the name from a serial that has already been read.
func SSIDForSerial(serial string) string {
	suffix := FallbackSuffix
	// Serials are hex; trailing characters vary between boards where leading ones
	// do not, so the last four are what actually distinguish two units.
	if s := strings.TrimSpace(serial); len(s) >= 4 {
		suffix = strings.ToUpper(s[len(s)-4:])
	}
	return SSIDPrefix + suffix
}

// PSKResult is the outcome of reading the optional key file.
type PSKResult struct {
	// PSK is the passphrase to use, or "" for an open network.
	PSK string
	// Found reports whether the file existed at all.
	Found bool
	// Warning explains why a file that existed was ignored. It is a warning
	// rather than an error on purpose: see ReadPSKFile.
	Warning string
}

// ReadPSKFile reads the operator's optional key file.
//
// The default setup AP is open. That is a deliberate trade and worth stating
// plainly: an open network means anyone in radio range can reach the setup
// wizard, and the mitigations are that the AP exists for minutes rather than
// permanently, that the session lock admits one device, and that the wizard warns
// about it inline. The alternative — a printed per-device key — is the failure
// mode where an operator with no key cannot set up the node they own.
//
// An operator who wants the AP protected drops a file on the boot partition:
//
//	psk=my-setup-passphrase
//
// A malformed file is ignored with a warning rather than refused. This file is
// edited on a card in a reader, by hand, often on Windows — the realistic
// failure is a typo or a stray BOM, and a node that refuses to raise its setup AP
// over one is a node the operator cannot reach at all. Coming up open and saying
// so loudly is recoverable; coming up not at all is not.
func ReadPSKFile(path string) PSKResult {
	b, err := os.ReadFile(path)
	if err != nil {
		return PSKResult{} // absent is the normal case, not a warning
	}

	body := strings.TrimPrefix(string(b), "\ufeff") // a BOM from a Windows editor
	var (
		psk   string
		found bool
	)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(key), "psk") {
			continue
		}
		// Only the surrounding whitespace is trimmed — a passphrase may legitimately
		// contain spaces, and silently altering one produces an AP the operator
		// cannot join with the key they wrote down.
		psk, found = strings.TrimSpace(value), true
	}

	switch {
	case !found:
		return PSKResult{Found: true,
			Warning: fmt.Sprintf("%s has no psk= line; the setup access point will be open", path)}
	case len(psk) < minPSK:
		return PSKResult{Found: true,
			Warning: fmt.Sprintf("%s: the psk is %d characters; WPA2 needs at least %d, so the setup access point will be open",
				path, len(psk), minPSK)}
	case len(psk) > maxPSK:
		return PSKResult{Found: true,
			Warning: fmt.Sprintf("%s: the psk is %d characters; WPA2 allows at most %d, so the setup access point will be open",
				path, len(psk), maxPSK)}
	case strings.ContainsFunc(psk, isControl):
		return PSKResult{Found: true,
			Warning: fmt.Sprintf("%s: the psk contains control characters, so the setup access point will be open", path)}
	}
	return PSKResult{PSK: psk, Found: true}
}

func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f
}
