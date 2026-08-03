package config

import (
	"regexp"
	"strings"
	"testing"
)

// A network with no dial prefix has to exist, or DMRGateway drops everything the
// prefix templates do not claim. These tests pin the fallback that guarantees
// one, and — just as importantly — the cases where nothing is guessed at.

// sectionFor returns the [DMR Network N] block whose Name= is name.
func sectionFor(t *testing.T, ini, name string) string {
	t.Helper()
	section := regexp.MustCompile(`(?m)^\[[^\]]+\]$`)
	locs := section.FindAllStringIndex(ini, -1)
	for i, loc := range locs {
		end := len(ini)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		block := ini[loc[0]:end]
		if strings.Contains(block, "\nName="+name+"\n") {
			return block
		}
	}
	t.Fatalf("no section named %q in:\n%s", name, ini)
	return ""
}

func model(nets ...Network) *Model {
	return &Model{
		General:  General{Callsign: "KN4OQW", ID: "3180202"},
		DMR:      DMR{ID: "3180202", ColorCode: "1"},
		Networks: nets,
	}
}

// The case that sent this branch: one BrandMeister network, nobody marked
// primary. Before the fallback the rendered file carried the prefix-2 template
// and no PassAll at all, so a plain TG 9 matched nothing and was dropped.
func TestSoleNetworkGetsTheCatchAllWithoutTheFlag(t *testing.T) {
	m := model(Network{Name: "BM_US", Type: NetBrandmeister, Address: "3103.master.brandmeister.network", Enabled: true})

	block := sectionFor(t, m.RenderDMRGateway(), "BM_US")
	for _, want := range []string{"PassAllPC0=1", "PassAllTG0=1", "PassAllPC1=2", "PassAllTG1=2"} {
		if !strings.Contains(block, want) {
			t.Errorf("the only network is missing %s, so traffic no rule claims is dropped:\n%s", want, block)
		}
	}
	// The prefix-2 template and the catch-all are alternatives, not a pair: if
	// the alternate block is still here the fallback picked the wrong branch.
	if strings.Contains(block, "PCRewrite3=1,2000001") {
		t.Errorf("still rendering the non-primary prefix-2 template:\n%s", block)
	}
	// Primary also drives the Location line; the two must not disagree.
	if !strings.Contains(block, "Location=1") {
		t.Errorf("promoted to primary but Location was left off:\n%s", block)
	}
}

// The clean case beside the failure case: an explicit flag is still what decides,
// and the fallback must not move it somewhere else.
func TestExplicitPrimaryIsHonouredOverTheFallback(t *testing.T) {
	m := model(
		Network{Name: "TGIF", Type: NetTGIF, Address: "tgif.network", Enabled: true},
		Network{Name: "BM_US", Type: NetBrandmeister, Address: "3103.master.brandmeister.network", Primary: true, Enabled: true},
	)

	ini := m.RenderDMRGateway()
	if !strings.Contains(sectionFor(t, ini, "BM_US"), "PassAllPC0=1") {
		t.Error("the network marked Primary did not get the catch-all")
	}
	if strings.Contains(sectionFor(t, ini, "TGIF"), "PassAllPC") {
		t.Error("a non-primary network was given the catch-all")
	}
}

// Two eligible networks is a real question about where unprefixed traffic should
// land, and guessing would silently move an operator's routing. Nothing is
// promoted; the prefix templates stand.
func TestAmbiguousMultiNetworkIsLeftAlone(t *testing.T) {
	m := model(
		Network{Name: "TGIF", Type: NetTGIF, Address: "tgif.network", Enabled: true},
		Network{Name: "BM_US", Type: NetBrandmeister, Address: "3103.master.brandmeister.network", Enabled: true},
	)

	if got := m.effectivePrimaryIndex(); got != -1 {
		t.Errorf("promoted network %d with two candidates; which one holds the catch-all is the operator's call", got)
	}
	if ini := m.RenderDMRGateway(); strings.Contains(ini, "PassAll") {
		t.Errorf("invented a catch-all for an ambiguous pair:\n%s", ini)
	}
}

// A custom network renders the operator's verbatim rewrites. networkRewrites
// answers that before it consults Primary, so promoting one could only ever
// change the Location line while leaving the routing untouched — which would be
// a lie about which network holds the catch-all.
func TestCustomAndXLXAreNotPromoted(t *testing.T) {
	for _, tc := range []struct {
		name string
		net  Network
	}{
		{"custom", Network{Name: "Raw", Type: NetCustom, Address: "example.net", Enabled: true,
			Rewrites: []string{"TGRewrite0=2,9,2,9,1"}}},
		{"xlx", Network{Name: "XLX950", Type: NetXLX, Enabled: true}},
		{"untyped legacy", Network{Name: "Legacy", Address: "example.net", Enabled: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := model(tc.net)
			if got := m.effectivePrimaryIndex(); got != -1 {
				t.Errorf("promoted a %s network to primary", tc.name)
			}
		})
	}
}

// A disabled network cannot carry traffic, so it cannot be the fallback answer
// either — otherwise the catch-all lands on a section rendered Enabled=0 and the
// enabled network keeps its dial prefix.
func TestDisabledNetworkIsNotTheFallback(t *testing.T) {
	m := model(
		Network{Name: "Off", Type: NetTGIF, Address: "tgif.network", Enabled: false},
		Network{Name: "BM_US", Type: NetBrandmeister, Address: "3103.master.brandmeister.network", Enabled: true},
	)

	if !strings.Contains(sectionFor(t, m.RenderDMRGateway(), "BM_US"), "PassAllPC0=1") {
		t.Error("the enabled network should hold the catch-all when the other is off")
	}
}

// But an operator who marked a network primary and then disabled it meant that,
// and the flag kept working that way before the fallback existed. Changing it
// here would move routing on a node mid-edit.
func TestExplicitPrimaryOnADisabledNetworkStillWins(t *testing.T) {
	m := model(
		Network{Name: "Off", Type: NetTGIF, Address: "tgif.network", Primary: true, Enabled: false},
		Network{Name: "BM_US", Type: NetBrandmeister, Address: "3103.master.brandmeister.network", Enabled: true},
	)

	if got := m.effectivePrimaryIndex(); got != 0 {
		t.Errorf("effectivePrimaryIndex = %d, want 0 — the explicit flag decides regardless of Enabled", got)
	}
}

// The fallback must not disturb the store: apply re-renders from the model, and
// a render that mutated it would make the second render differ from the first.
func TestFallbackDoesNotMutateTheStoredModel(t *testing.T) {
	m := model(Network{Name: "BM_US", Type: NetBrandmeister, Address: "3103.master.brandmeister.network", Enabled: true})

	first := m.RenderDMRGateway()
	if m.Networks[0].Primary {
		t.Error("rendering set Primary on the stored network")
	}
	if second := m.RenderDMRGateway(); second != first {
		t.Error("a second render differs from the first")
	}
}
