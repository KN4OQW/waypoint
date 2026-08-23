package dmrids

import (
	"context"
	"log"
	"time"

	"github.com/KN4OQW/waypoint/internal/hostfile"
	"github.com/KN4OQW/waypoint/internal/hostsrc"
	"github.com/KN4OQW/waypoint/internal/verifydl"
)

// DefaultURL is the ordered source list for DMRIds.dat, the RadioID.net
// callsign<->ID export. The project's own copy leads; Pi-Star's is the fallback.
//
// This is the file every rendered gateway config already points at — MMDVM-Host's
// "DMR Id Lookup" and NXDNGateway's "Id Lookup" both name
// /usr/local/etc/DMRIds.dat (render.go) — and until now nothing in Waypoint ever
// downloaded it. The daemons were configured to read a file that did not exist,
// so callsign resolution silently produced nothing: last-heard showed bare
// numeric IDs, and the mode bus could not map a DMR/NXDN ID to a YSF callsign at
// all (RFC-0003 §3).
//
// DMRIds.dat rather than RadioID's users.json: it is 6.6 MB against 84 MB, and it
// is already the exact format every consumer parses, so nothing has to convert it.
const DefaultURL = "https://hostfiles.kn4oqw.com/DMRIds.dat,https://www.pistar.uk/downloads/DMRIds.dat"

// DefaultPath is where the gateways expect the table, and therefore where the
// refresher writes it. It is a rendered constant in every gateway config, not an
// operator choice, so the default has to match render.go exactly.
const DefaultPath = "/usr/local/etc/DMRIds.dat"

// Fetch downloads the table to path, trying each source in order. Local entries in
// the operator's prepend/append hooks survive the refresh (RFC-0005), which is how
// a club adds IDs that RadioID does not carry. A failed fetch leaves any existing
// file intact.
func Fetch(ctx context.Context, urls []string, path string, v verifydl.Verify) error {
	v.UserAgent = "Waypoint DMR ID table"
	body, _, err := hostsrc.Download(ctx, hostsrc.DMRIds, urls, v)
	if err != nil {
		return err
	}
	return hostfile.WriteWithHooks(path, body, "dmrids")
}

// Run fetches at startup and then every interval until ctx is canceled.
//
// There is no shipped copy to fall back on: the table is 6.6 MB, which is a third
// of the binary again, and unlike a hostlist it goes out of date continuously
// rather than slowly. A node that has never reached the internet resolves numeric
// IDs instead of callsigns, which is the behaviour it had before this existed.
//
// The interval is long because the table changes slowly and is the largest thing
// the node downloads; there is no benefit in pulling megabytes hourly.
func Run(ctx context.Context, urls []string, path string, interval time.Duration, v verifydl.Verify) {
	RunThen(ctx, urls, path, interval, v, nil)
}

// RunThen is Run with a hook that fires after each SUCCESSFUL refresh.
//
// It exists for the phonebook, whose imported entries are re-read against the new
// table so a reissued callsign reaches the operator's roster. The hook runs after
// the file is in place and only when the fetch actually replaced it, so a failed
// download — which leaves the previous table intact — does not re-scan a file
// nothing changed.
//
// The hook must not be slow or panic: it runs on the refresher's goroutine, and
// an error from it is logged rather than returned, because a phonebook sync that
// fails has not made the downloaded table any less valid.
//
// nil is the ordinary case and means Run.
func RunThen(ctx context.Context, urls []string, path string, interval time.Duration, v verifydl.Verify, after func(path string) error) {
	hostsrc.Register(hostsrc.DMRIds, "DMR ID / callsign table")
	hostsrc.Every(ctx, hostsrc.DMRIds, interval, func(ctx context.Context) error {
		if err := Fetch(ctx, urls, path, v); err != nil {
			log.Printf("dmrids: fetch failed (using cached table if present): %v", err)
			return err
		}
		if t, err := Load(path); err == nil {
			hostsrc.SetEntries(hostsrc.DMRIds, t.Len())
			log.Printf("dmrids: updated %s (%d ids)", path, t.Len())
		} else {
			log.Printf("dmrids: updated %s", path)
		}
		if after != nil {
			if err := after(path); err != nil {
				log.Printf("dmrids: post-refresh hook failed: %v", err)
			}
		}
		return nil
	})
}
