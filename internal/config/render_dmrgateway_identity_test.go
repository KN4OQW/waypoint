package config

import (
	"strings"
	"testing"
)

// The identity DMRGateway declares to its masters used to arrive over the wire and
// now comes out of its own INI, so these tests guard a section that did not exist
// before pin 2a3306d.
//
// The bar here is not "the keys are present". It is that what a master receives is
// unchanged from what a working node sent when MMDVM-Host still spoke DMRC —
// because the failure mode of getting this wrong is a node that connects happily
// and announces the wrong station.

// identityModel is a fully configured DMR node. Every value is distinct so a test
// that asserts the wrong key cannot pass by coincidence.
func identityModel() *Model {
	m := &Model{}
	m.Modes.DMR = true
	m.General.Callsign = "KN4OQW"
	m.General.ID = "3180202"
	m.General.Duplex = true
	m.General.Power = "25"
	m.Modem.RXFreqHz = "438800000"
	m.Modem.TXFreqHz = "438800000"
	m.DMR.ColorCode = "2"
	m.DMRNet.Slot1 = false
	m.DMRNet.Slot2 = true
	return m
}

// TestDMRGatewayDeclaresTheStationIdentity is the whole point of the [Info]
// section: every value MMDVM-Host used to pack into the DMRC message reaches the
// gateway through the file instead.
func TestDMRGatewayDeclaresTheStationIdentity(t *testing.T) {
	ini := identityModel().RenderDMRGateway()
	info := sectionOf(t, ini, "[Info]")
	for _, want := range []string{
		"Callsign=KN4OQW",
		"RXFrequency=438800000",
		"TXFrequency=438800000",
		"Power=25",
		"ColorCode=2",
		"Duplex=1",
		"Slot1=0",
		"Slot2=1",
	} {
		if !strings.Contains(info, want+"\n") {
			t.Errorf("[Info] is missing %q:\n%s", want, info)
		}
	}
}

// TestDMRGatewayIdIsRendered guards the key with no safe default. CConf starts
// m_id at 0 and nothing upstream validates it, so a missing Id does not fail — it
// announces DMR ID 0 to every master. There is no louder symptom to rely on.
func TestDMRGatewayIdIsRendered(t *testing.T) {
	ini := identityModel().RenderDMRGateway()
	gen := generalSection(t, ini)
	if !strings.Contains(gen, "Id=3180202\n") {
		t.Errorf("[General] Id is not rendered; the gateway would announce ID 0:\n%s", gen)
	}
}

// TestDMRGatewayIdPrefersTheDMRIdentity: a node whose DMR ID differs from the
// general station ID must log in with the DMR one, and it must be the SAME value
// the per-network blocks use. Two identities in one file is the bug this asserts
// against.
func TestDMRGatewayIdPrefersTheDMRIdentity(t *testing.T) {
	m := identityModel()
	m.DMR.ID = "3180299"
	ini := m.RenderDMRGateway()
	if !strings.Contains(generalSection(t, ini), "Id=3180299\n") {
		t.Errorf("[General] Id did not follow the DMR identity:\n%s", generalSection(t, ini))
	}
	m.Networks = []Network{{Name: "BM", Type: NetBrandmeister, Address: "bm.example", Enabled: true}}
	ini = m.RenderDMRGateway()
	if strings.Contains(ini, "Id=3180202") {
		t.Error("the general station ID leaked into DMRGateway.ini alongside the DMR ID")
	}
}

// TestDMRGatewayInfoKeySpelling is a regression guard on somebody else's typo.
// Upstream's own sample DMRGateway.ini shipped TXFRequency/RXFRequency in 61771e2
// while Conf.cpp compares "TXFrequency"/"RXFrequency" case-sensitively, so a
// renderer written from the sample transmits on 0 Hz and says nothing about it.
// The sample was corrected by 2a3306d; this keeps the correction from being
// undone by the next person who reads it.
func TestDMRGatewayInfoKeySpelling(t *testing.T) {
	ini := identityModel().RenderDMRGateway()
	for _, wrong := range []string{"TXFRequency", "RXFRequency"} {
		if strings.Contains(ini, wrong) {
			t.Errorf("rendered %q, which Conf.cpp does not parse; the frequency would be 0 Hz", wrong)
		}
	}
}

// TestDMRGatewayDoesNotDiscloseCoordinates.
//
// DMRGateway's [Info] accepts Latitude and Longitude, and DMRGateway.cpp packs
// them into the Homebrew config every master receives. The model has coordinates
// now (StationLocation), and they were entered to pick weather counties — not to
// be published. Rendering them here would turn a compatibility fix into a new
// disclosure, which is exactly the class of change CLAUDE.md's privacy gate and
// station_location.go's own comment say must be deliberate.
//
// This asserts the absence, so nobody adds them later for symmetry with the keys
// upstream's sample file shows.
func TestDMRGatewayDoesNotDiscloseCoordinates(t *testing.T) {
	m := identityModel()
	m.StationLocation = StationLocation{Latitude: 30.4383, Longitude: -84.2807}
	ini := m.RenderDMRGateway()
	for _, leak := range []string{"Latitude", "Longitude", "30.4383", "84.2807"} {
		if strings.Contains(ini, leak) {
			t.Errorf("DMRGateway.ini discloses the station position (%q):\n%s", leak, sectionOf(t, ini, "[Info]"))
		}
	}
}

// TestDMRGatewayIdentityDefaults: an unconfigured node must still render a
// parseable file rather than keys the daemon reads as garbage. Colour code and
// power carry the same defaults the MMDVM-Host renderer uses, so the two files
// cannot disagree about the same station.
func TestDMRGatewayIdentityDefaults(t *testing.T) {
	m := &Model{}
	m.Modes.DMR = true
	m.General.Callsign = "KN4OQW"
	m.DMR.ID = "3180202"
	info := sectionOf(t, m.RenderDMRGateway(), "[Info]")
	for _, want := range []string{"ColorCode=" + DefaultDMRColorCode + "\n", "Power=1\n", "Duplex=0\n"} {
		if !strings.Contains(info, want) {
			t.Errorf("[Info] is missing the default %q:\n%s", want, info)
		}
	}
}
