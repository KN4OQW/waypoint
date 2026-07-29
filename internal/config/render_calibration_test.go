package config

import (
	"strings"
	"testing"
)

// The [Modem] section is order-sensitive in a way the INI format gives no hint
// of, and blank per-mode levels are a trap. Both are pinned here because both
// fail silently on the air rather than loudly at save time.

func modemSection(t *testing.T, m *Model) []string {
	t.Helper()
	var out []string
	in := false
	for _, line := range strings.Split(m.RenderMMDVM(), "\n") {
		switch {
		case line == "[Modem]":
			in = true
		case strings.HasPrefix(line, "["):
			in = false
		case in && line != "":
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		t.Fatal("the rendered INI has no [Modem] section")
	}
	return out
}

// TestPerModeLevelsComeAfterTXLevel guards the ordering. MMDVM-Host's parser
// assigns TXLevel to every per-mode level as it reads that key, so a per-mode
// override above it is silently overwritten — the node transmits at the wrong
// deviation and the config file looks correct.
func TestPerModeLevelsComeAfterTXLevel(t *testing.T) {
	m := fixture()
	m.Modem.DMRTXLevel = "62"
	m.Modem.FMTXLevel = "44"

	lines := modemSection(t, m)
	txAt, dmrAt, fmAt := -1, -1, -1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "TXLevel="):
			txAt = i
		case strings.HasPrefix(l, "DMRTXLevel="):
			dmrAt = i
		case strings.HasPrefix(l, "FMTXLevel="):
			fmAt = i
		}
	}
	if txAt < 0 || dmrAt < 0 || fmAt < 0 {
		t.Fatalf("missing keys: TXLevel@%d DMRTXLevel@%d FMTXLevel@%d", txAt, dmrAt, fmAt)
	}
	if dmrAt < txAt || fmAt < txAt {
		t.Fatalf("per-mode levels rendered above TXLevel (TXLevel@%d, DMR@%d, FM@%d); the host would overwrite them", txAt, dmrAt, fmAt)
	}
}

// TestBlankPerModeLevelsAreOmitted guards the other half. The host parses these
// with atof, so "DMRTXLevel=" is not "unset" — it is zero deviation, a node
// that transmits nothing anyone can decode.
func TestBlankPerModeLevelsAreOmitted(t *testing.T) {
	m := fixture()
	m.Modem = Modem{Port: "/dev/ttyAMA0", DMRTXLevel: "62"} // one set, the rest blank

	for _, l := range modemSection(t, m) {
		if strings.HasSuffix(l, "=") {
			t.Errorf("rendered an empty value: %q", l)
		}
	}
	joined := strings.Join(modemSection(t, m), "\n")
	for _, key := range []string{"D-StarTXLevel", "YSFTXLevel", "P25TXLevel", "NXDNTXLevel", "POCSAGTXLevel", "FMTXLevel"} {
		if strings.Contains(joined, key) {
			t.Errorf("%s was rendered even though it is blank; blank means follow TXLevel", key)
		}
	}
	if !strings.Contains(joined, "DMRTXLevel=62") {
		t.Error("the one level that was set did not render")
	}
}

// TestNoM17TXLevelIsRendered records a fact about this stack's host fork: it has
// no M17TXLevel key, so M17 follows TXLevel. Rendering one would be a key the
// host silently ignores.
func TestNoM17TXLevelIsRendered(t *testing.T) {
	m := fixture()
	m.Modem.DMRTXLevel = "55"
	if strings.Contains(m.RenderMMDVM(), "M17TXLevel") {
		t.Error("rendered M17TXLevel, which this MMDVM-Host fork does not parse")
	}
}
