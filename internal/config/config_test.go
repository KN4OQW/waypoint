package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mmdvmFixture = `[General]
Callsign=KN4OQW
Id=3180202
Duplex=1

[Info]
RXFrequency=433900000
TXFrequency=438900000
Power=1

[Modem]
Port=/dev/ttyAMA0
UARTPort=/dev/ttyAMA0
RXOffset=0

[DMR]
Enable=1
ColorCode=1

[DMR Network]
Slot1=1
Slot2=1

[System Fusion]
Enable=0

[YSF]
`

const dmrgwFixture = `[DMR Network 1]
Name=BM_3102_United_States
Address=3102.master.brandmeister.network
Port=62031
Password="secret-do-not-leak"

[DMR Network 2]
Address=tgif.network
Port=62031
Enabled=0
`

func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseINIAccessors(t *testing.T) {
	ini, err := ParseINI(strings.NewReader(mmdvmFixture))
	if err != nil {
		t.Fatal(err)
	}
	if got := ini.Get("general", "callsign"); got != "KN4OQW" { // case-insensitive
		t.Fatalf("callsign = %q", got)
	}
	if !ini.Bool("DMR", "Enable") {
		t.Fatal("DMR Enable should be true")
	}
	if ini.Bool("System Fusion", "Enable") {
		t.Fatal("System Fusion Enable should be false")
	}
	if !ini.Has("DMR Network") || ini.Has("Nonexistent") {
		t.Fatal("Has() wrong")
	}
}

func TestReadViewShape(t *testing.T) {
	mm := writeFixture(t, "MMDVM-Host.ini", mmdvmFixture)
	dg := writeFixture(t, "DMRGateway.ini", dmrgwFixture)
	v := Read(mm, dg)

	if v.Errors != nil {
		t.Fatalf("unexpected errors: %v", v.Errors)
	}
	if !v.ReadOnly {
		t.Fatal("view should be read-only")
	}
	if v.General.Callsign != "KN4OQW" || v.General.RXFreqHz != "433900000" || v.General.ModemPort != "/dev/ttyAMA0" {
		t.Fatalf("general wrong: %+v", v.General)
	}
	if !v.DMR.Enable || v.DMR.ColorCode != "1" || !v.DMR.Slot1 {
		t.Fatalf("dmr wrong: %+v", v.DMR)
	}
	// modes: DMR enabled, System Fusion disabled
	got := map[string]bool{}
	for _, m := range v.Modes {
		got[m.Name] = m.Enabled
	}
	if !got["DMR"] || got["System Fusion"] {
		t.Fatalf("modes wrong: %+v", v.Modes)
	}
	if len(v.Networks) != 2 {
		t.Fatalf("want 2 networks, got %d", len(v.Networks))
	}
}

func TestReadRedactsPasswords(t *testing.T) {
	mm := writeFixture(t, "MMDVM-Host.ini", mmdvmFixture)
	dg := writeFixture(t, "DMRGateway.ini", dmrgwFixture)
	v := Read(mm, dg)

	var bm *Network
	for i := range v.Networks {
		if strings.HasPrefix(v.Networks[i].Name, "BM_3102") {
			bm = &v.Networks[i]
		}
	}
	if bm == nil {
		t.Fatal("BM network not found")
	}
	if !bm.HasPassword {
		t.Fatal("HasPassword should be true")
	}
	// The secret must never appear anywhere in the serialized view.
	for _, n := range v.Networks {
		if strings.Contains(n.Name+n.Address+n.Port, "secret-do-not-leak") {
			t.Fatal("password leaked into view")
		}
	}
}
