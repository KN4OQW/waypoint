package config

import (
	"strings"
	"testing"
)

// The "2" that kept appearing in front of SMS ids.
//
// Across several bench sessions, BrandMeister's SMS service — private-call id
// 262993 — kept turning up as 2262993: on the radio's screen as the sender of an
// inbound message, and therefore as the destination when the operator hit reply.
// It was blamed on MMDVM-Host's Prefixes setting, on the radio's codeplug, and on
// BrandMeister. It was none of those.
//
// It is arithmetic, and it is this repository's own. A NON-primary BrandMeister
// network renders from the dial-prefix template with prefix 2 (dmrrewrites.go),
// and that template ends with
//
//	SrcRewrite4=2,1,2,2000001,999999
//
// which tells DMRGateway: on slot 2, a source id in the range starting at 1 and
// spanning 999999 ids is rewritten into the range starting at 2000001. Source id
// N becomes 2000000 + N. For 262993 that is exactly 2262993.
//
// The rewrite is correct and deliberate — it is how a prefixed network's traffic
// is echoed back into prefixed form, so the operator can reply by dialling the
// prefixed id. What was wrong was that BrandMeister was rendering as non-primary
// at all: the primary network is the one reached with NO dial prefix, and a node
// whose only network is BrandMeister has nothing else the catch-all could refer
// to. effectivePrimaryIndex now promotes a sole eligible network, and with the
// catch-all in place none of these prefix rules is emitted and ids pass through
// untouched.
//
// So the fix landed before this workstream did, and these tests pin the arithmetic
// so nobody has to rediscover it from a packet capture a third time.

// The rewrite that produced the 2, spelled out against the observed number.
func TestBrandmeisterPrefixArithmeticExplainsThe2262993(t *testing.T) {
	lines := alternateRewrites(dmrTemplates[NetBrandmeister])

	const want = "SrcRewrite4=2,1,2,2000001,999999"
	if !containsLine(lines, want) {
		t.Fatalf("the source rewrite is not %q; the explanation below no longer holds:\n%s",
			want, strings.Join(lines, "\n"))
	}

	// base is the first id of the rewritten range; a source id of 1 maps to it, so
	// id N maps to base-1+N.
	const base = 2000001
	for _, tc := range []struct {
		name string
		id   int
		want int
	}{
		{"BrandMeister's SMS service, the id that started this", 262993, 2262993},
		{"the other SMS id seen on the bench", 262995, 2262995},
		{"the operator's own radio", 3180202, 0}, // outside the range: untouched
		{"the first id in range", 1, 2000001},
		{"the last id in range", 999999, 2999999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := 0
			if tc.id >= 1 && tc.id <= 999999 {
				got = base - 1 + tc.id
			}
			if got != tc.want {
				t.Errorf("id %d rewrites to %d, want %d", tc.id, got, tc.want)
			}
		})
	}
}

// And the fix: a node whose only network is BrandMeister renders the catch-all,
// not the prefix template, even with the Primary flag clear. None of the rewrites
// that produce the 2 is emitted, so ids reach the radio as they were sent.
func TestASoleBrandmeisterNetworkEmitsNoPrefixRewrites(t *testing.T) {
	m := &Model{}
	m.Modes.DMR = true
	m.DMR.ID = "3180202"
	m.General.Callsign = "KN4OQW"
	// The flag deliberately left clear: this is the configuration the bench had.
	m.Networks = []Network{{Name: "BM", Type: NetBrandmeister, Enabled: true}}

	ini := m.RenderDMRGateway()
	// The prefixing rules specifically. The primary template has a SrcRewrite of
	// its own (4000..5000, reflector status) which is unrelated and must stay, so
	// this bans the prefix RANGE rather than the keyword.
	for _, banned := range []string{"2000001", "SrcRewrite4=", "TGRewrite2=2,2000001"} {
		if strings.Contains(ini, banned) {
			t.Errorf("the rendered gateway still carries %q; ids reaching the radio would be prefixed:\n%s",
				banned, ini)
		}
	}
	// The catch-all is what makes the prefix rules unnecessary...
	if !strings.Contains(ini, "PassAllPC") || !strings.Contains(ini, "PassAllTG") {
		t.Errorf("no catch-all was rendered:\n%s", ini)
	}
	// ...and the primary template's own source rewrite is untouched by any of this.
	if !strings.Contains(ini, "SrcRewrite0=2,4000,2,9,1001") {
		t.Errorf("the primary reflector-status rewrite went missing:\n%s", ini)
	}
}

// A node that really does have several networks still gets the prefix scheme, and
// still gets the 2. That is not a bug there: with more than one network, a dial
// prefix is the only way to say which one you meant.
func TestASecondNetworkRestoresThePrefixScheme(t *testing.T) {
	m := &Model{}
	m.Modes.DMR = true
	m.DMR.ID = "3180202"
	m.General.Callsign = "KN4OQW"
	m.Networks = []Network{
		{Name: "TGIF", Type: NetTGIF, Enabled: true, Primary: true},
		{Name: "BM", Type: NetBrandmeister, Enabled: true},
	}

	ini := m.RenderDMRGateway()
	if !strings.Contains(ini, "SrcRewrite4=2,1,2,2000001,999999") {
		t.Errorf("BrandMeister as a non-primary network lost its prefix rewrites:\n%s", ini)
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}
