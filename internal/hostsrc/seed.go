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
// DMRIds.dat ships no copy for a different reason from the rest: at 6.6 MB it is
// a third of the binary again, and it goes out of date continuously rather than
// slowly, so a shipped copy would be large and wrong. A node with no network
// resolves numeric IDs instead of callsigns.
//
// YSF, P25 and NXDN are captured from the classic text lists and converted to the
// JSON their gateways parse (internal/hostconv) before being shipped, because the
// source that served ready-made JSON is gone. M17 needs no conversion.

//go:embed seed/DMR_Hosts.txt
var seedDMRHosts []byte

//go:embed seed/TGList_BM.txt
var seedDMRTalkgroups []byte

//go:embed seed/DStar_Hosts.json
var seedDStarHosts []byte

//go:embed seed/YSFHosts.json
var seedYSFHosts []byte

//go:embed seed/P25Hosts.json
var seedP25Hosts []byte

//go:embed seed/NXDNHosts.json
var seedNXDNHosts []byte

//go:embed seed/M17Hosts.txt
var seedM17Hosts []byte

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
	DMRIds        = "dmr_ids"
)

var seeds = map[string][]byte{
	DMRHosts:      seedDMRHosts,
	DMRTalkgroups: seedDMRTalkgroups,
	DStarHosts:    seedDStarHosts,
	YSFHosts:      seedYSFHosts,
	P25Hosts:      seedP25Hosts,
	NXDNHosts:     seedNXDNHosts,
	M17Hosts:      seedM17Hosts,
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
