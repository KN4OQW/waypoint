package publicview

import (
	"os"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/dmrids"
)

// IDDBStatus is the health of the station ID database — the DMRIds.dat table that
// turns a DMR, P25 or NXDN source ID into a callsign.
//
// It matters to the public surface far more than to the admin one. The admin
// dashboard can show a raw ID and an operator reads it as "the lookup missed"; an
// anonymous page cannot, because a bare ID is not the callsign the page claims to
// be showing and is resolvable to a name and address through the public databases.
// So when the table is gone, the public list stops rather than degrading.
type IDDBStatus struct {
	// Available reports whether callsign resolution can be trusted.
	Available bool
	// Reason is a short operator-facing explanation when it cannot. It is written
	// here, never derived from a path or an OS error, so nothing about the
	// filesystem layout reaches an anonymous visitor.
	Reason string
}

// Reasons the public page may show. They are deliberately vague about the
// machine: "the station database" is something a visitor can understand, and it
// discloses nothing about where the file lives or why the read failed.
const (
	ReasonIDDBMissing = "station database missing or corrupt"
)

// idDBProbeTTL is how long a probe result is reused. The table is refreshed daily
// (dmrids.Run) and is several megabytes; re-reading it per anonymous request would
// be a denial-of-service handed to whoever finds the page.
const idDBProbeTTL = time.Minute

// DMRIDsProbe returns a health probe for the DMRIds.dat at path.
//
// The probe is cheap and cached. It stats the file on every call, which is enough
// to notice a deleted or replaced table immediately, and only re-parses when the
// file has actually changed or the cached verdict has aged out. A parse that
// yields no usable rows counts as corrupt: a present-but-empty table resolves
// nothing, which for the public surface is the same failure as a missing one.
func DMRIDsProbe(path string) func() IDDBStatus {
	var (
		mu     sync.Mutex
		cached IDDBStatus
		at     time.Time
		size   int64
		mod    time.Time
		primed bool
	)
	return func() IDDBStatus {
		mu.Lock()
		defer mu.Unlock()

		fi, err := os.Stat(path)
		if err != nil || fi.IsDir() || fi.Size() == 0 {
			cached = IDDBStatus{Reason: ReasonIDDBMissing}
			primed, at = true, time.Now()
			return cached
		}
		fresh := primed && fi.Size() == size && fi.ModTime().Equal(mod) && time.Since(at) < idDBProbeTTL
		if fresh {
			return cached
		}
		t, err := dmrids.Load(path)
		switch {
		case err != nil, t.Len() == 0:
			cached = IDDBStatus{Reason: ReasonIDDBMissing}
		default:
			cached = IDDBStatus{Available: true}
		}
		primed, at, size, mod = true, time.Now(), fi.Size(), fi.ModTime()
		return cached
	}
}

// alwaysAvailable is the probe used when no ID database was wired in. A node that
// was never told where the table lives is not a node whose table is broken, and
// the per-source filter still refuses to publish anything that is not
// callsign-shaped.
func alwaysAvailable() IDDBStatus { return IDDBStatus{Available: true} }
