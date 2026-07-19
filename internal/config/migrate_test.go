package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A stock Pi-Star 4.2.1 MMDVM-Host config (extension-less /etc/mmdvmhost), trimmed
// to the fields the migration must carry plus an [APRS] block for the unmapped
// check. DMR + YSF enabled; the rest off.
const piStarMMDVM = `[General]
Callsign=W1ABC
Id=3161234
Timeout=240
Duplex=1
RFModeHang=300
NetModeHang=300
Display=None

[Info]
RXFrequency=438800000
TXFrequency=431000000
Power=5
Location=Boulder CO

[Modem]
Protocol=uart
UARTPort=/dev/ttyACM0
UARTSpeed=115200
TXInvert=1
RXInvert=0
PTTInvert=0

[D-Star]
Enable=0

[DMR]
Enable=1
ColorCode=1
Id=3161234
EmbeddedLCOnly=1
DumpTAData=1

[System Fusion]
Enable=1

[P25]
Enable=0

[NXDN]
Enable=0

[M17]
Enable=0

[POCSAG]
Enable=0
Frequency=439987500

[FM]
Enable=0

[DMR Network]
Enable=1
LocalPort=62032
GatewayPort=62031

[APRS]
Enable=1
Server=euro.aprs2.net

[Remote Commands]
Enable=1
`

// A Pi-Star DMRGateway with a single stock Brandmeister network (clean routing).
const piStarDMRGateway = `[DMR Network 1]
Name=BM_United_States_3103
Enabled=1
Address=3103.master.brandmeister.network
Password=passw0rd
Port=62031
Id=3161234
TGRewrite0=2,9,2,9,1
PCRewrite0=2,4000,2,4000,1001
TypeRewrite0=2,9990,2,9990
SrcRewrite0=2,4000,2,9,1001
PassAllPC0=2
PassAllTG0=2
`

func writeCard(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	etc := filepath.Join(dir, "etc")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(etc, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Acceptance (Pi-Star 4.2.1): callsign, DMR/CCS7 ID, RX/TX frequency, and enabled
// modes/networks import intact from a mounted card.
func TestMigratePiStar(t *testing.T) {
	dir := writeCard(t, map[string]string{
		"mmdvmhost":      piStarMMDVM,
		"dmrgateway":     piStarDMRGateway,
		"pistar-release": "Version = 4.2.1\n",
	})

	contents, names, platform, err := Locate(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, report, err := Migrate(contents, names, platform)
	if err != nil {
		t.Fatal(err)
	}

	if m.General.Callsign != "W1ABC" {
		t.Errorf("callsign = %q, want W1ABC", m.General.Callsign)
	}
	if m.General.ID != "3161234" || m.DMR.ID != "3161234" {
		t.Errorf("IDs = general %q / dmr %q, want 3161234", m.General.ID, m.DMR.ID)
	}
	if m.Modem.RXFreqHz != "438800000" || m.Modem.TXFreqHz != "431000000" {
		t.Errorf("frequencies = rx %q tx %q", m.Modem.RXFreqHz, m.Modem.TXFreqHz)
	}
	if !m.Modes.DMR || !m.Modes.YSF || m.Modes.P25 || m.Modes.DStar {
		t.Errorf("modes wrong: %+v", m.Modes)
	}
	if len(m.Networks) != 1 || m.Networks[0].Name != "BM_United_States_3103" || !m.Networks[0].Enabled {
		t.Fatalf("networks wrong: %+v", m.Networks)
	}
	if m.Networks[0].Password != "passw0rd" {
		t.Errorf("network password not carried: %q", m.Networks[0].Password)
	}

	// Report: platform, found files, modes, and the APRS/Remote unmapped items.
	if report.Platform != "Pi-Star 4.2.1" {
		t.Errorf("platform = %q, want Pi-Star 4.2.1", report.Platform)
	}
	if !hasMode(report.Modes, "DMR") || !hasMode(report.Modes, "System Fusion") {
		t.Errorf("report modes = %v", report.Modes)
	}
	if !unmappedHas(report.Unmapped, "APRS") || !unmappedHas(report.Unmapped, "Remote Commands") {
		t.Errorf("expected APRS + Remote Commands in unmapped: %+v", report.Unmapped)
	}
	// The network is reported and enabled (whether it classified to a clean type or
	// was preserved verbatim as custom, it imported intact — the acceptance).
	if len(report.Networks) != 1 || !report.Networks[0].Enabled {
		t.Errorf("network not reported/enabled: %+v", report.Networks)
	}
}

// Acceptance (WPSD): .ini-named files and M17 present.
func TestMigrateWPSD(t *testing.T) {
	wpsdMMDVM := piStarMMDVM +
		"\n[M17]\nEnable=1\nCAN=7\n" // M17 on (overrides the earlier M17 block via last-wins parse)
	dir := writeCard(t, map[string]string{
		"MMDVM-Host.ini": wpsdMMDVM,
		"DMRGateway.ini": piStarDMRGateway,
		"M17Gateway.ini": "[General]\nSuffix=H\n[Network]\nStartup=M17-M17 C\nRevert=1\n",
		"wpsd-release":   "WPSD Version: 2024.10\n",
	})
	contents, names, platform, err := Locate(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, report, err := Migrate(contents, names, platform)
	if err != nil {
		t.Fatal(err)
	}
	if m.General.Callsign != "W1ABC" || m.General.ID != "3161234" {
		t.Errorf("identity not carried: %+v", m.General)
	}
	if !m.Modes.M17 {
		t.Error("M17 should be enabled from the WPSD card")
	}
	if m.M17GW.Startup != "M17-M17 C" {
		t.Errorf("M17 gateway startup not imported: %q", m.M17GW.Startup)
	}
	if got := fileStatus(report, roleM17Gateway); !got.Found || got.Name != "M17Gateway.ini" {
		t.Errorf("M17Gateway.ini not reported found: %+v", got)
	}
	if report.Platform != "WPSD 2024.10" {
		t.Errorf("platform = %q", report.Platform)
	}
}

// Locate matches names in both <dir> and <dir>/etc and reports a missing role.
func TestLocateVariantsAndMissing(t *testing.T) {
	dir := t.TempDir()
	// mmdvmhost at the root (not etc), dmrgateway absent.
	if err := os.WriteFile(filepath.Join(dir, "mmdvmhost"), []byte(piStarMMDVM), 0o600); err != nil {
		t.Fatal(err)
	}
	contents, names, _, err := Locate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := contents[roleMMDVM]; !ok || names[roleMMDVM] != "mmdvmhost" {
		t.Errorf("mmdvmhost at root not located: names=%v", names)
	}
	if _, ok := contents[roleDMRGateway]; ok {
		t.Error("dmrgateway should be absent")
	}
	m, report, err := Migrate(contents, names, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if m.General.Callsign != "W1ABC" {
		t.Error("migration should work with only mmdvmhost")
	}
	if fileStatus(report, roleDMRGateway).Found {
		t.Error("dmrgateway should report missing")
	}
}

// Mounted directory and uploaded role->bytes map produce an identical model.
func TestMigrateInputEquivalence(t *testing.T) {
	dir := writeCard(t, map[string]string{"mmdvmhost": piStarMMDVM, "dmrgateway": piStarDMRGateway})
	mountContents, mountNames, _, err := Locate(dir)
	if err != nil {
		t.Fatal(err)
	}
	mMount, _, err := Migrate(mountContents, mountNames, "unknown")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate an upload: map filenames to roles via RoleForFilename.
	upload := map[string][]byte{}
	upNames := map[string]string{}
	for name, content := range map[string]string{"mmdvmhost": piStarMMDVM, "DMRGateway.ini": piStarDMRGateway} {
		role := RoleForFilename(name)
		if role == "" {
			t.Fatalf("RoleForFilename(%q) did not classify", name)
		}
		upload[role] = []byte(content)
		upNames[role] = name
	}
	mUpload, _, err := Migrate(upload, upNames, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if mMount.General != mUpload.General || len(mMount.Networks) != len(mUpload.Networks) {
		t.Errorf("mounted vs uploaded models differ:\n mount %+v\n upload %+v", mMount.General, mUpload.General)
	}
}

// A hand-tuned network is preserved verbatim and flagged custom; a clean one is not.
func TestMigrateReportNetworksAndUnmapped(t *testing.T) {
	// A network whose routing does not match a clean type → custom-preserved.
	custom := `[DMR Network 1]
Name=MyWeirdNet
Enabled=1
Address=1.2.3.4
Password=x
Port=62031
Id=3161234
TGRewrite0=2,999,2,999,1
`
	dir := writeCard(t, map[string]string{"mmdvmhost": piStarMMDVM, "dmrgateway": custom})
	contents, names, _, err := Locate(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := Migrate(contents, names, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Networks) != 1 || !report.Networks[0].Custom {
		t.Errorf("hand-tuned network should be custom-preserved: %+v", report.Networks)
	}

	// A card WITHOUT APRS must not list APRS as unmapped.
	noAprs := writeCard(t, map[string]string{"mmdvmhost": "[General]\nCallsign=W1ABC\nId=1\n[DMR]\nEnable=1\n"})
	c2, n2, _, _ := Locate(noAprs)
	_, rep2, err := Migrate(c2, n2, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep2.Unmapped) != 0 {
		t.Errorf("no unmapped features expected, got %+v", rep2.Unmapped)
	}
}

func TestMigrateRequiresMMDVM(t *testing.T) {
	_, _, err := Migrate(map[string][]byte{roleDMRGateway: []byte(piStarDMRGateway)}, nil, "unknown")
	if err == nil {
		t.Error("Migrate without an MMDVM-Host config should error")
	}
}

// --- helpers ---

func hasMode(modes []string, name string) bool {
	for _, m := range modes {
		if m == name {
			return true
		}
	}
	return false
}

func unmappedHas(items []UnmappedItem, section string) bool {
	for _, it := range items {
		if it.Section == section {
			return true
		}
	}
	return false
}

func fileStatus(r *MigrationReport, role string) FileStatus {
	for _, f := range r.Files {
		if f.Role == role {
			return f
		}
	}
	return FileStatus{}
}
