package config

import (
	"regexp"
	"strings"
	"testing"
)

// DMRNetworkOrder must match the sections RenderDMRGateway actually writes — the
// supervisor maps the daemon's positional "net1:conn net2:disc" reply onto names
// with it, so a drift of one silently attributes a dead link to the wrong network.
func TestDMRNetworkOrderMatchesRenderedSections(t *testing.T) {
	m := &Model{
		General: General{Callsign: "KN4OQW", ID: "3180202"},
		DMR:     DMR{ID: "3180202", ColorCode: "1"},
		Networks: []Network{
			{Name: "BM_US", Type: NetBrandmeister, Address: "3102.master.brandmeister.network", Primary: true, Enabled: true},
			{Name: "XLX950", Type: NetXLX, Enabled: true},                          // its own section, never a netN
			{Name: "TGIF", Type: NetTGIF, Address: "tgif.network", Enabled: false}, // disabled still holds a slot
			{Name: "DMRplus", Type: NetDMRPlus, Address: "dmrplus.example", Enabled: true},
		},
	}

	// The names the renderer wrote, in file order.
	ini := m.RenderDMRGateway()
	var rendered []string
	section := regexp.MustCompile(`(?m)^\[DMR Network (\d+)\]$`)
	for _, block := range section.Split(ini, -1)[1:] {
		for _, line := range strings.Split(block, "\n") {
			if name, ok := strings.CutPrefix(strings.TrimSpace(line), "Name="); ok {
				rendered = append(rendered, name)
				break
			}
		}
	}

	got := m.DMRNetworkOrder()
	if len(got) != len(rendered) {
		t.Fatalf("order has %d names but the INI has %d [DMR Network] sections:\n order:    %v\n rendered: %v",
			len(got), len(rendered), got, rendered)
	}
	for i := range got {
		if got[i] != rendered[i] {
			t.Errorf("net%d: order says %q, the INI says %q", i+1, got[i], rendered[i])
		}
	}
	if len(got) == 0 {
		t.Fatal("no networks derived — the fixture is not exercising anything")
	}
	// The XLX network is reported as "xlx" by the daemon, never as a netN slot.
	for _, n := range got {
		if n == "XLX950" {
			t.Error("XLX must not occupy a netN slot")
		}
	}
	// A disabled network still holds its position.
	if got[1] != "TGIF" {
		t.Errorf("a disabled network should still hold its slot: %v", got)
	}
}
