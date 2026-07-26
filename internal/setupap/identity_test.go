package setupap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Property 1: the SSID carries the last four characters of the serial, so two
// unconfigured nodes in the same room are distinguishable — which is the whole
// reason the suffix exists.
func TestSSIDForSerial(t *testing.T) {
	cases := map[string]string{
		"100000003f9c1a2b": "Waypoint-Setup-1A2B",
		"00000000abcdef01": "Waypoint-Setup-EF01",
		"  10000000dead  ": "Waypoint-Setup-DEAD",
		"beef":             "Waypoint-Setup-BEEF",
		"a1b2c3":           "Waypoint-Setup-B2C3",
		// Too short or absent: named unknown rather than given a fake identity, and
		// stable across reflashes so the operator sees the same network name.
		"":    "Waypoint-Setup-0000",
		"abc": "Waypoint-Setup-0000",
		"   ": "Waypoint-Setup-0000",
	}
	for serial, want := range cases {
		if got := SSIDForSerial(serial); got != want {
			t.Errorf("SSIDForSerial(%q) = %q, want %q", serial, got, want)
		}
	}

	// The name has to fit in an SSID, whatever the serial looks like.
	if n := len(SSIDForSerial("100000003f9c1a2b")); n > 32 {
		t.Errorf("the SSID is %d bytes; 802.11 allows 32", n)
	}
}

// Property 2: the serial is read from the real /proc/cpuinfo shape, and its
// absence on a machine that has none is not an error.
func TestSerial(t *testing.T) {
	dir := t.TempDir()
	pi := filepath.Join(dir, "cpuinfo-pi")
	if err := os.WriteFile(pi, []byte(`processor	: 0
model name	: ARMv7 Processor rev 4 (v7l)
Hardware	: BCM2835
Revision	: a020d3
Serial		: 00000000f3a91b2c
Model		: Raspberry Pi 3 Model B Plus Rev 1.3
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Serial(pi); got != "00000000f3a91b2c" {
		t.Errorf("Serial = %q", got)
	}
	if got := SSID(pi); got != "Waypoint-Setup-1B2C" {
		t.Errorf("SSID = %q", got)
	}

	// An x86 development machine has no Serial line at all.
	x86 := filepath.Join(dir, "cpuinfo-x86")
	if err := os.WriteFile(x86, []byte("processor\t: 0\nvendor_id\t: GenuineIntel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Serial(x86); got != "" {
		t.Errorf("Serial on a machine with none = %q", got)
	}
	if got := SSID(x86); got != SSIDPrefix+FallbackSuffix {
		t.Errorf("SSID = %q, want the fallback", got)
	}

	// A missing file is the same case, not a crash.
	if got := Serial(filepath.Join(dir, "nope")); got != "" {
		t.Errorf("Serial of a missing file = %q", got)
	}
}

func writePSK(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "waypoint-setup.txt")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Property 3: a well-formed key file protects the AP.
func TestReadPSKFileAccepts(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"plain":               {"psk=correct-horse\n", "correct-horse"},
		"no trailing newline": {"psk=correct-horse", "correct-horse"},
		"windows line ending": {"psk=correct-horse\r\n", "correct-horse"},
		"utf-8 BOM":           {"\ufeffpsk=correct-horse\n", "correct-horse"},
		"spaces around":       {"  psk =  correct-horse  \n", "correct-horse"},
		"uppercase key":       {"PSK=correct-horse\n", "correct-horse"},
		"comments and blanks": {"# the setup AP key\n\npsk=correct-horse\n\n", "correct-horse"},
		// A passphrase may contain spaces; altering one would produce an AP the
		// operator cannot join with the key they wrote down.
		"internal spaces": {"psk=correct horse battery\n", "correct horse battery"},
		"exactly 8":       {"psk=12345678\n", "12345678"},
		"exactly 63":      {"psk=" + strings.Repeat("k", 63) + "\n", strings.Repeat("k", 63)},
		// The last psk= line wins, which is what an operator editing the file twice
		// would expect.
		"last line wins": {"psk=first-one\npsk=second-one\n", "second-one"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ReadPSKFile(writePSK(t, tc.body))
			if !got.Found {
				t.Fatal("Found = false for a file that exists")
			}
			if got.Warning != "" {
				t.Errorf("Warning = %q for a valid file", got.Warning)
			}
			if got.PSK != tc.want {
				t.Errorf("PSK = %q, want %q", got.PSK, tc.want)
			}
		})
	}
}

// Property 4: a malformed file is ignored with a warning, and the AP still comes
// up open.
//
// This is the load-bearing behaviour. The file is edited by hand on a card in a
// reader, often on Windows; the realistic failure is a typo. A node that refuses
// to raise its setup AP over one is a node the operator cannot reach at all,
// which is strictly worse than one that comes up open and says so.
func TestReadPSKFileRejectsWithoutFailing(t *testing.T) {
	cases := map[string]string{
		"empty file":       "",
		"no psk line":      "ssid=something\n",
		"comment only":     "# psk=correct-horse\n",
		"no equals":        "psk correct-horse\n",
		"too short":        "psk=1234567\n",
		"empty value":      "psk=\n",
		"too long":         "psk=" + strings.Repeat("k", 64) + "\n",
		"control chars":    "psk=correct\x01horse\n",
		"tab in the value": "psk=correct\thorse\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got := ReadPSKFile(writePSK(t, body))
			if !got.Found {
				t.Error("Found = false for a file that exists")
			}
			if got.PSK != "" {
				t.Errorf("PSK = %q, want the AP to fall back to open", got.PSK)
			}
			if got.Warning == "" {
				t.Error("a malformed file was ignored silently; the operator needs to know why their key did nothing")
			}
			if !strings.Contains(got.Warning, "open") {
				t.Errorf("the warning does not say what happened instead: %q", got.Warning)
			}
		})
	}
}

// Property 5: no file at all is the normal case and warns about nothing.
func TestReadPSKFileAbsent(t *testing.T) {
	got := ReadPSKFile(filepath.Join(t.TempDir(), "not-there"))
	if got.Found || got.PSK != "" || got.Warning != "" {
		t.Errorf("ReadPSKFile of a missing file = %+v, want the zero value", got)
	}
}

// Property 6: an accepted passphrase is one the helper will also accept. A key
// file that parses here but is refused by APUp would raise no AP at all, which is
// the outcome this whole path exists to avoid.
func TestAcceptedPSKSatisfiesTheHelper(t *testing.T) {
	for _, body := range []string{
		"psk=12345678\n",
		"psk=correct horse battery\n",
		"psk=" + strings.Repeat("k", 63) + "\n",
	} {
		got := ReadPSKFile(writePSK(t, body))
		if got.PSK == "" {
			t.Fatalf("%q was rejected here", body)
		}
		if len(got.PSK) < minPSK || len(got.PSK) > maxPSK {
			t.Errorf("accepted a %d-character passphrase, outside WPA2's %d–%d", len(got.PSK), minPSK, maxPSK)
		}
	}
}
