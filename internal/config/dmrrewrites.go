package config

import (
	"fmt"
	"strconv"
	"strings"
)

// This file generates DMRGateway rewrite lines from a network's Type + Primary,
// mirroring WPSD's admin/configure.php so operators never hand-write routing.
// The templates are digit-for-digit from the WPSD generator source cross-checked
// against a real generated /etc/dmrgateway (see the wpsd-dmrgateway-templates
// note). DMRGateway matches rewrite keys by prefix and ignores the numeric
// suffix (Conf.cpp strncmp), and it evaluates every network's explicit rewrites
// before any PassAll (DMRGateway.cpp) — so a per-TG DMRRoute always wins over the
// primary's catch-all, and the primary's PassAll catches everything unclaimed
// (including the TG9990 Parrot).

// dmrTemplate describes how a non-primary DMR network is routed by dial prefix.
type dmrTemplate struct {
	prefix    int  // dial prefix: BM 2, DMR+ 8, cross-over 7, TGIF 5, SystemX 4
	reflector bool // has a single-digit local shortcut, a <prefix>4000 reflector-control PC line, and PC prefix-strip
	crossover bool // DMR2YSF/NXDN: minimal, TS2-only, range 999998, no Parrot
}

var dmrTemplates = map[NetworkType]dmrTemplate{
	NetDMRPlus: {prefix: 8, reflector: true},
	NetSystemX: {prefix: 4, reflector: true},
	NetTGIF:    {prefix: 5, reflector: false},
	NetDMR2YSF: {prefix: 7, crossover: true},
	// BrandMeister as a NON-primary uses prefix 2. DERIVED, NOT digit-verified
	// against a live current WPSD (selectable-primary path is behind an anti-bot
	// wall); BM is almost always primary. Confirm before relying on a local-only
	// BM room. See waypoint-local-dmr-room-future note.
	NetBrandmeister: {prefix: 2, reflector: true},
}

// primaryRewrites is the catch-all block for the single primary network (WPSD's
// BM-primary template): TG9 local, the 9990 group→private Parrot conversion, the
// 4000 reflector range, and PassAll on both slots and call types so any TG the
// prefix rules don't claim lands here. The TypeRewrite is what makes the TG9990
// Parrot echo — PassAll alone would pass it in the wrong call type.
func primaryRewrites() []string {
	return []string{
		"TGRewrite0=2,9,2,9,1",
		"PCRewrite0=2,94000,2,4000,1001",
		"TypeRewrite0=2,9990,2,9990",
		"SrcRewrite0=2,4000,2,9,1001",
		"PassAllPC0=1",
		"PassAllTG0=1",
		"PassAllPC1=2",
		"PassAllTG1=2",
	}
}

// alternateRewrites is the prefix-routed block for a non-primary network.
func alternateRewrites(t dmrTemplate) []string {
	p := t.prefix
	if t.crossover {
		// TS2-only local cross-over bridge; no Parrot, range 999998.
		return []string{
			fmt.Sprintf("TGRewrite0=2,%d000001,2,1,999998", p),
			fmt.Sprintf("SrcRewrite0=2,1,2,%d000001,999998", p),
			fmt.Sprintf("PCRewrite0=2,%d000001,2,1,999998", p),
		}
	}
	var out []string
	if t.reflector {
		// Single-digit local shortcut (dial the prefix digit → network TG9) and
		// the reflector-control private-call range (<prefix>4000 → 4000..5000).
		out = append(out,
			fmt.Sprintf("TGRewrite0=2,%d,2,9,1", p),
			fmt.Sprintf("PCRewrite0=2,%d4000,2,4000,1001", p),
		)
	}
	// 9990 Parrot private-call routing on both slots.
	out = append(out,
		fmt.Sprintf("PCRewrite1=1,%d009990,1,9990,1", p),
		fmt.Sprintf("PCRewrite2=2,%d009990,2,9990,1", p),
	)
	if t.reflector {
		// Private-call prefix-strip on both slots (plain networks like TGIF omit this).
		out = append(out,
			fmt.Sprintf("PCRewrite3=1,%d000001,1,1,999999", p),
			fmt.Sprintf("PCRewrite4=2,%d000001,2,1,999999", p),
		)
	}
	out = append(out,
		// 9990 group→private conversion on both slots.
		fmt.Sprintf("TypeRewrite1=1,%d009990,1,9990", p),
		fmt.Sprintf("TypeRewrite2=2,%d009990,2,9990", p),
		// Talkgroup prefix-strip on both slots.
		fmt.Sprintf("TGRewrite1=1,%d000001,1,1,999999", p),
		fmt.Sprintf("TGRewrite2=2,%d000001,2,1,999999", p),
		// Source rewrites: echo the returning Parrot/TG back into prefixed form.
		fmt.Sprintf("SrcRewrite1=1,9990,1,%d009990,1", p),
		fmt.Sprintf("SrcRewrite2=2,9990,2,%d009990,1", p),
		fmt.Sprintf("SrcRewrite3=1,1,1,%d000001,999999", p),
		fmt.Sprintf("SrcRewrite4=2,1,2,%d000001,999999", p),
	)
	return out
}

// networkRewrites returns the rewrite lines for one network: the verbatim custom
// escape hatch, the primary catch-all, or the type's prefix template — with any
// matching DMRRoute overrides appended as direct TGRewrites (which beat PassAll).
func networkRewrites(n Network, routes []DMRRoute) []string {
	var out []string
	switch {
	case n.Type == NetCustom && n.AutoRewrite:
		// Custom host with WPSD auto-rewrites: prefix-9 generated template.
		out = append(out, "WPSD_AutoRewrites=1")
		out = append(out, alternateRewrites(dmrTemplate{prefix: 9, reflector: true})...)
	case n.Type == NetCustom || n.Type == "":
		// Custom (manual), or a legacy network from a store predating typed
		// routing: render the stored rewrites verbatim so a binary upgrade never
		// wipes routing. The operator re-picks a type in the UI for generated routing.
		out = append(out, n.Rewrites...)
	case n.Primary:
		out = append(out, primaryRewrites()...)
	default:
		if t, ok := dmrTemplates[n.Type]; ok {
			out = append(out, alternateRewrites(t)...)
		}
	}
	return append(out, routeRewrites(n.Name, out, routes)...)
}

// routeRewrites renders the "tie this talkgroup to this gateway" overrides for
// the network named target as direct same-slot TGRewrites, indexed past any
// TGRewrite the template already emitted so nothing is shadowed in the file.
func routeRewrites(target string, existing []string, routes []DMRRoute) []string {
	next := nextIndex(existing, "TGRewrite")
	var out []string
	for _, r := range routes {
		if r.Network != target || strings.TrimSpace(r.TG) == "" {
			continue
		}
		slot := def(strings.TrimSpace(r.Slot), "2")
		tg := strings.TrimSpace(r.TG)
		out = append(out, fmt.Sprintf("TGRewrite%d=%s,%s,%s,%s,1", next, slot, tg, slot, tg))
		next++
	}
	return out
}

// nextIndex returns one past the highest numeric suffix among "key<N>=..." lines
// (0 if there are none), so appended rules never reuse an existing index.
func nextIndex(lines []string, key string) int {
	max := -1
	for _, l := range lines {
		eq := strings.IndexByte(l, '=')
		if eq <= 0 || !strings.HasPrefix(l[:eq], key) {
			continue
		}
		if n, err := strconv.Atoi(l[len(key):eq]); err == nil && n > max {
			max = n
		}
	}
	return max + 1
}
