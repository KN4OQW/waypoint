package sysprov

import "testing"

// nmcli's terse output escapes colons inside values. An SSID containing one is
// unusual but legal, and getting it wrong shifts every column after it — so the
// signal strength and security of a network would be read out of the middle of
// its own name.
func TestScanParsesEscapedColonsInSSIDs(t *testing.T) {
	out := `*:my\:network:78:WPA2
 :plain:41:WPA1 WPA2`
	got := parseScan(out)
	if len(got) != 2 {
		t.Fatalf("parsed %d networks, want 2: %+v", len(got), got)
	}
	if got[0].SSID != "my:network" {
		t.Errorf("SSID = %q, want %q", got[0].SSID, "my:network")
	}
	if got[0].Signal != 78 {
		t.Errorf("signal = %d, want 78 — the columns shifted", got[0].Signal)
	}
	if got[0].Security != "WPA2" {
		t.Errorf("security = %q, want WPA2", got[0].Security)
	}
	if !got[0].InUse {
		t.Error("the in-use marker was not read")
	}
}

// Strongest first: the list is for picking a network from a phone, and the one
// most likely to be wanted should not be buried.
func TestScanSortsByStrength(t *testing.T) {
	got := parseScan(" :weak:12:WPA2\n :strong:91:WPA2\n :middling:55:WPA2")
	want := []string{"strong", "middling", "weak"}
	for i, w := range want {
		if got[i].SSID != w {
			t.Errorf("position %d = %q, want %q", i, got[i].SSID, w)
		}
	}
}

// One entry per network. An access point on both bands, or a mesh, beacons from
// several BSSes; the same name four times is a worse answer than once, and the
// strongest is the one worth offering.
func TestScanDeduplicatesBySSIDKeepingTheStrongest(t *testing.T) {
	got := parseScan(" :DeathStar:40:WPA2\n*:DeathStar:88:WPA2\n :DeathStar:12:WPA2")
	if len(got) != 1 {
		t.Fatalf("parsed %d entries, want 1: %+v", len(got), got)
	}
	if got[0].Signal != 88 {
		t.Errorf("kept signal %d, want the strongest (88)", got[0].Signal)
	}
	if !got[0].InUse {
		t.Error("the in-use marker was lost when deduplicating")
	}
}

// A hidden network beacons no name, so there is nothing to put in a list. The
// operator reaches it through the "does not broadcast its name" checkbox
// instead, and a row with a blank label would just be unclickable.
func TestScanDropsNamelessNetworks(t *testing.T) {
	got := parseScan(" ::62:WPA2\n :real:30:WPA2")
	if len(got) != 1 || got[0].SSID != "real" {
		t.Errorf("parsed %+v, want only the named network", got)
	}
}

// An empty security column means an open network. Reporting it as "" would make
// it indistinguishable from "we do not know", and the wizard warns on open
// networks — a warning that silently stops appearing is worse than none.
func TestScanNamesOpenNetworksExplicitly(t *testing.T) {
	got := parseScan(" :GuestWiFi:55:\n :other:50:--")
	for _, n := range got {
		if n.Security != "open" {
			t.Errorf("%q security = %q, want %q", n.SSID, n.Security, "open")
		}
		if !privhelperOpen(n.Security) {
			t.Errorf("%q does not read as open", n.SSID)
		}
	}
}

func privhelperOpen(sec string) bool { return sec == "open" || sec == "" }

// Garbage in must not panic or invent networks.
func TestScanIgnoresUnparseableLines(t *testing.T) {
	got := parseScan("\n\nnot-terse-at-all\n :ok:50:WPA2\n:::\n")
	if len(got) != 1 || got[0].SSID != "ok" {
		t.Errorf("parsed %+v, want only the well-formed row", got)
	}
}
