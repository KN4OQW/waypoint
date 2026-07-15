package netconfig

import (
	"reflect"
	"testing"
)

func TestParseWiFiScan(t *testing.T) {
	got := parseWiFiScan(readFixture(t, "wifi-scan.txt"))
	want := []WiFiScanResult{
		{SSID: "MyNetwork", Signal: 82, Security: "WPA2", InUse: true},
		{SSID: "NeighborNet", Signal: 47, Security: "WPA1 WPA2"},
		{SSID: "CoffeeShop:Guest", Signal: 31, Security: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scan parse:\n want %+v\n  got %+v", want, got)
	}
}

// ScanWiFi runs the command through the Runner seam.
func TestScanWiFiRunner(t *testing.T) {
	run := func(name string, args ...string) (string, error) {
		return readFixture(t, "wifi-scan.txt"), nil
	}
	res, err := ScanWiFi(run)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || res[0].SSID != "MyNetwork" || !res[0].InUse {
		t.Fatalf("ScanWiFi = %+v", res)
	}
}

// A hidden network (blank SSID) is dropped — it can only be joined via manual
// entry — and duplicate SSIDs collapse to the strongest, keeping the in-use flag.
func TestScanDedupAndHidden(t *testing.T) {
	got := parseWiFiScan(readFixture(t, "wifi-scan.txt"))
	for _, r := range got {
		if r.SSID == "" {
			t.Fatal("hidden (blank-SSID) network should be dropped from the list")
		}
	}
	// MyNetwork appears twice (82 in-use, 60) → one entry, strongest, in-use.
	n := 0
	for _, r := range got {
		if r.SSID == "MyNetwork" {
			n++
			if r.Signal != 82 || !r.InUse {
				t.Errorf("deduped MyNetwork = %+v, want signal 82 in-use", r)
			}
		}
	}
	if n != 1 {
		t.Errorf("MyNetwork should appear once, got %d", n)
	}
}
