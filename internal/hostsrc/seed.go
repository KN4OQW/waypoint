package hostsrc

import (
	_ "embed"
)

// The shipped copies. These are the floor under every list: a node that has never
// reached the internet still shows masters, talkgroups and reflectors rather than
// an empty dropdown, which is the state a first boot in a shack with no wifi
// lands in.
//
// They are a floor, not a source of truth. Restore only writes one when there is
// no cache at all, and the first successful download replaces it — a months-old
// shipped list must never win over a stale-but-real one. Refresh them with
// `go run ./cmd/hostseed` when cutting a release; see seed/README.md for where
// each came from.
//
// Only lists with a reachable upstream at capture time are shipped. YSF, P25,
// NXDN and M17 have none right now — every source for them resolved to a host
// that is refusing connections (#138) — so they have no floor until
// hostfiles.kn4oqw.com serves them, and their status says so.

//go:embed seed/DMR_Hosts.txt
var seedDMRHosts []byte

//go:embed seed/TGList_BM.txt
var seedDMRTalkgroups []byte

//go:embed seed/DStar_Hosts.json
var seedDStarHosts []byte

// List ids. These are the keys used for registration, status reporting and the
// seed lookup, so they are declared once here rather than spelled out per caller.
const (
	DMRHosts      = "dmr_hosts"
	DMRTalkgroups = "dmr_talkgroups"
	DStarHosts    = "dstar_hosts"
	YSFHosts      = "ysf_hosts"
	P25Hosts      = "p25_hosts"
	NXDNHosts     = "nxdn_hosts"
	M17Hosts      = "m17_hosts"
)

var seeds = map[string][]byte{
	DMRHosts:      seedDMRHosts,
	DMRTalkgroups: seedDMRTalkgroups,
	DStarHosts:    seedDStarHosts,
}

// Seed returns the shipped copy of a list, if it ships with one.
func Seed(name string) ([]byte, bool) {
	b, ok := seeds[name]
	if !ok || len(b) == 0 {
		return nil, false
	}
	return b, true
}

// HasSeed reports whether a list ships with a copy — i.e. whether it has a floor
// under it at all. The UI uses this to distinguish "no list yet, but one will
// appear when the network comes back" from "no list, and nothing to fall back on".
func HasSeed(name string) bool {
	_, ok := Seed(name)
	return ok
}
