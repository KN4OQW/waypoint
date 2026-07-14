package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/store"
)

// A realistic WPSD/Pi-Star MMDVMHost export. Keys and values are transcribed
// from an actual Pi-Star / WPSD MMDVMHost config, including fields Waypoint does
// not model (Latitude/Longitude/Height/Description, per-mode extras) so the diff
// surfaces exactly what the importer drops.
const realMMDVM = `[General]
Callsign=W1ABC
Id=3161234
Timeout=240
Duplex=1
RFModeHang=300
NetModeHang=300
Display=None
Daemon=1

[Info]
RXFrequency=438800000
TXFrequency=431000000
Power=5
Latitude=40.015000
Longitude=-105.270000
Height=10
Location=Boulder CO
Description=Multimode Digital Voice
URL=https://www.qrz.com/db/W1ABC

[Log]
DisplayLevel=0
FileLevel=1
FilePath=/var/log/pi-star
FileRoot=MMDVM

[CW Id]
Enable=1
Time=10

[DMR Id Lookup]
File=/usr/local/etc/DMRIds.dat
Time=24

[Modem]
Protocol=uart
UARTPort=/dev/ttyACM0
UARTSpeed=115200
TXInvert=1
RXInvert=0
PTTInvert=0
TXDelay=100
DMRDelay=0
TXOffset=0
RXOffset=0
RXLevel=50
TXLevel=50
RFLevel=100
RXDCOffset=0
TXDCOffset=0
CWIdTXLevel=50
DMRTXLevel=50

[D-Star]
Enable=1
Module=B
SelfOnly=0
AckReply=1
ErrorReply=1

[DMR]
Enable=1
Beacons=0
ColorCode=1
SelfOnly=0
EmbeddedLCOnly=1
DumpTAData=1
Id=3161234

[System Fusion]
Enable=1
LowDeviation=0
RemoteGateway=0
SelfOnly=0
TXHang=4

[P25]
Enable=0
NAC=293
SelfOnly=0

[NXDN]
Enable=0
RAN=1
SelfOnly=0

[M17]
Enable=0
CAN=0
SelfOnly=0

[POCSAG]
Enable=1
Frequency=439987500

[FM]
Enable=0

[DMR Network]
Enable=1
LocalAddress=127.0.0.1
LocalPort=62032
GatewayAddress=127.0.0.1
GatewayPort=62031
Jitter=360
Slot1=1
Slot2=1
`

// A realistic WPSD DMRGateway.ini: a BrandMeister primary (PassAll catch-all,
// TG9990 Parrot) and a non-primary TGIF network with the generated prefix-5
// routing. Rewrite lines are digit-for-digit what dmrrewrites.go generates, so a
// clean import classifies both as typed networks.
const realDMRGW = `[General]
RptAddress=127.0.0.1
RptPort=62032
LocalAddress=127.0.0.1
LocalPort=62031
Timeout=10
Daemon=1

[Log]
DisplayLevel=0
FileLevel=1
FilePath=/var/log/pi-star
FileRoot=DMRGateway

[DMR Network 1]
Enabled=1
Name=BM_3161_United_States
Address=master.brandmeister.network
Port=62031
Password=bmpassword123
Location=1
Id=3161234
TGRewrite0=2,9,2,9,1
PCRewrite0=2,94000,2,4000,1001
TypeRewrite0=2,9990,2,9990
SrcRewrite0=2,4000,2,9,1001
PassAllPC0=1
PassAllTG0=1
PassAllPC1=2
PassAllTG1=2
Debug=0

[DMR Network 2]
Enabled=1
Name=TGIF_Network
Address=tgif.network
Port=62031
Password=tgifsecret456
Id=3161234
PCRewrite1=1,5009990,1,9990,1
PCRewrite2=2,5009990,2,9990,1
TypeRewrite1=1,5009990,1,9990
TypeRewrite2=2,5009990,2,9990
TGRewrite1=1,5000001,1,1,999999
TGRewrite2=2,5000001,2,1,999999
SrcRewrite1=1,9990,1,5009990,1
SrcRewrite2=2,9990,2,5009990,1
SrcRewrite3=1,1,1,5000001,999999
SrcRewrite4=2,1,2,5000001,999999
Debug=0
`

// knownUnmodeled is the allowlist of keys a real WPSD/Pi-Star export carries that
// Waypoint deliberately does not model yet (full modem calibration and structured
// location — both tracked "pending" in docs/pistar-parity.md). "General.Daemon" is
// not a drop but an intentional override: Waypoint renders Daemon=0 because
// systemd owns the process. Any drop/change OUTSIDE this set is a real parity
// regression and fails the test.
var knownUnmodeled = map[string]bool{
	"General.Daemon":    true, // intentional: systemd manages the process
	"Info.Latitude":     true, // pending — structured location not modeled
	"Info.Longitude":    true,
	"Info.Height":       true,
	"Info.Description":  true,
	"Modem.TXDelay":     true, // pending — full modem calibration (config-coverage #20)
	"Modem.DMRDelay":    true,
	"Modem.RFLevel":     true,
	"Modem.RXDCOffset":  true,
	"Modem.TXDCOffset":  true,
	"Modem.CWIdTXLevel": true,
	"Modem.DMRTXLevel":  true,
}

// TestParityRealRoundTrip drives a real WPSD/Pi-Star export through the actual
// Import -> Save -> Load -> Render pipeline (real SQLite store) and asserts that
// every operator-facing key in the managed sections round-trips byte-identically.
// It is the "real export" companion to TestLosslessRoundTrip (which round-trips a
// synthetic fully-populated fixture). Drops are allowed only for keys Waypoint
// explicitly does not model yet (knownUnmodeled); anything else fails.
func TestParityRealRoundTrip(t *testing.T) {
	dir := t.TempDir()
	mmPath := filepath.Join(dir, "mmdvmhost")
	dgPath := filepath.Join(dir, "dmrgateway")
	must(t, os.WriteFile(mmPath, []byte(realMMDVM), 0o600))
	must(t, os.WriteFile(dgPath, []byte(realDMRGW), 0o600))

	m, err := Import(mmPath, dgPath) // parse the real files into a Model
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(":memory:") // Save -> Load through the real store
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Save(s, "parity-test"); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}

	origMM, _ := ParseINI(strings.NewReader(realMMDVM))
	origDG, _ := ParseINI(strings.NewReader(realDMRGW))
	rendMM, _ := ParseINI(strings.NewReader(loaded.RenderMMDVM()))
	rendDG, _ := ParseINI(strings.NewReader(loaded.RenderDMRGateway()))

	assertRoundTrip(t, origMM, rendMM, mmManaged)
	assertRoundTrip(t, origDG, rendDG, dgManaged)
}

// mmManaged / dgManaged: the sections whose operator-facing keys the store owns
// and must round-trip. Keys outside these sections are fixed operational values
// (Log/MQTT/lookup paths) not part of parity.
var mmManaged = []string{"General", "Info", "Modem", "D-Star", "DMR", "System Fusion", "P25", "NXDN", "M17", "POCSAG", "FM", "DMR Network"}
var dgManaged = []string{"DMR Network 1", "DMR Network 2"}

func assertRoundTrip(t *testing.T, orig, rend *INI, sections []string) {
	t.Helper()
	for _, sec := range sections {
		os_ := orig.section(sec)
		if os_ == nil {
			t.Errorf("[%s] absent in original fixture", sec)
			continue
		}
		var keys []string
		for k := range os_ {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ov := os_[k]
			rv, present := lookupCI(rend.section(sec), k)
			if knownUnmodeled[sec+"."+k] {
				continue
			}
			switch {
			case !present:
				t.Errorf("[%s] %s DROPPED (orig=%q) — not in knownUnmodeled allowlist", sec, k, ov)
			case ov != rv:
				t.Errorf("[%s] %s CHANGED orig=%q rend=%q", sec, k, ov, rv)
			}
		}
	}
}

func lookupCI(m map[string]string, key string) (string, bool) {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
