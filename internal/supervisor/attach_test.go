package supervisor

import (
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
)

func TestAttachmentsFromModel(t *testing.T) {
	m := &config.Model{
		Networks: []config.Network{
			{Name: "BM_3102", Type: config.NetBrandmeister, Address: "master.brandmeister.network", Port: "62031", Enabled: true},
			{Name: "TGIF", Type: config.NetCustom, Address: "tgif.network", Enabled: true},         // no port → default
			{Name: "DMRplus", Type: config.NetDMRPlus, Address: "dmrplus.example", Enabled: false}, // disabled
			{Name: "XLX950", Type: config.NetXLX, Address: "xlx950.example", Enabled: true},        // address is in a hostfile
			{Name: "Nameless", Type: config.NetCustom, Address: "", Enabled: true},                 // nothing to probe
		},
	}

	got := Attachments(m)
	if len(got) != 2 {
		t.Fatalf("expected the two probeable DMR masters, got %d: %+v", len(got), got)
	}
	if got[0].Name != "BM_3102" || got[1].Name != "TGIF" {
		t.Errorf("derivation is not in stable name order: %+v", got)
	}
	if got[0].Endpoint() != "master.brandmeister.network:62031" {
		t.Errorf("endpoint = %q", got[0].Endpoint())
	}
	if got[1].Port != dmrMasterDefaultPort {
		t.Errorf("a network with no port should take the renderer's default, got %q", got[1].Port)
	}
	for _, a := range got {
		if a.Unit != config.UnitDMRGateway {
			t.Errorf("%s: unit = %q, want the DMRGateway unit", a.Name, a.Unit)
		}
		if a.Kind != KindDMRMaster {
			t.Errorf("%s: kind = %q", a.Name, a.Kind)
		}
	}
}

// DAPNET exists as an attachment on exactly the nodes where the daemon does: its
// render target is gated on the POCSAG mode enable, so this must be too.
func TestAttachmentsDAPNETFollowsPOCSAG(t *testing.T) {
	off := Attachments(&config.Model{})
	for _, a := range off {
		if a.Kind == KindDAPNET {
			t.Fatal("DAPNET attachment derived with POCSAG off — its daemon is not even rendered")
		}
	}

	m := &config.Model{}
	m.Modes.POCSAG = true
	on := Attachments(m)
	if len(on) != 1 || on[0].Kind != KindDAPNET {
		t.Fatalf("expected one DAPNET attachment, got %+v", on)
	}
	// Blank server means the renderer wrote its default, so the probe must target
	// that same default rather than an empty host.
	if on[0].Endpoint() != dapnetDefaultServer+":"+dapnetPort {
		t.Errorf("endpoint = %q, want the rendered default", on[0].Endpoint())
	}
	if on[0].Unit != config.UnitDAPNETGateway {
		t.Errorf("unit = %q", on[0].Unit)
	}

	m.POCSAG.Server = "dapnet.example.org"
	if got := Attachments(m)[0].Endpoint(); got != "dapnet.example.org:"+dapnetPort {
		t.Errorf("a configured server should be probed, got %q", got)
	}
}

// Ordering is stable across derivations so a caller can diff two of them.
func TestAttachmentsStableOrder(t *testing.T) {
	m := &config.Model{
		Networks: []config.Network{
			{Name: "zeta", Address: "z.example", Enabled: true},
			{Name: "alpha", Address: "a.example", Enabled: true},
		},
	}
	m.Modes.POCSAG = true

	first := Attachments(m)
	for i := 0; i < 5; i++ {
		next := Attachments(m)
		for j := range first {
			if first[j] != next[j] {
				t.Fatalf("derivation %d differs at %d: %+v vs %+v", i, j, first[j], next[j])
			}
		}
	}
	// dapnet sorts before dmr-master by kind, then names ascend.
	if first[0].Kind != KindDAPNET || first[1].Name != "alpha" || first[2].Name != "zeta" {
		t.Errorf("unexpected order: %+v", first)
	}
}

func TestAttachmentsNilModel(t *testing.T) {
	if got := Attachments(nil); got != nil {
		t.Errorf("nil model should derive nothing, got %+v", got)
	}
}
