// Command hostseed refreshes the hostlist copies shipped in the binary.
//
// The copies under internal/hostsrc/seed are the floor under every reflector,
// master and talkgroup picker — what a node shows when it has never reached the
// internet. They go stale, so this re-captures them from the sources recorded in
// seed/README.md and reports what moved.
//
// Run it when cutting a release, and read the diff before committing: these are
// third-party operational lists, and a bad capture ships a broken picker to every
// node. A source that is unreachable leaves its existing copy untouched rather
// than truncating it.
//
// The same capture list feeds the published copies at hostfiles.kn4oqw.com, so
// -out writes to a staging directory instead of the shipped ones. Publishing and
// seeding then cannot drift: one list of sources, one set of validity rules.
//
//	go run ./cmd/hostseed              # refresh the shipped copies in place
//	go run ./cmd/hostseed -check       # report staleness, change nothing (CI)
//	go run ./cmd/hostseed -out dist    # capture for publishing
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// capture is one shipped copy and where it comes from. Keep in step with the
// provenance table in seed/README.md.
var captures = []struct {
	File string
	URL  string
	// Shipped marks a copy that is embedded in the binary. An unshipped capture is
	// published but never committed: DMRIds.dat is 6.6 MB, which is a third of the
	// binary again, and it goes out of date continuously rather than slowly.
	Shipped bool
}{
	{"DMR_Hosts.txt", "https://www.pistar.uk/downloads/DMR_Hosts.txt", true},
	{"TGList_BM.txt", "https://www.pistar.uk/downloads/TGList_BM.txt", true},
	// Pinned to the same DStarGateway commit the stack pins the gateway binary to,
	// so the format matches the parser that reads it. Bump both together.
	{"DStar_Hosts.json", "https://raw.githubusercontent.com/g4klx/DStarGateway/612f388727a9bb47aaeaae3a89f5abff3152ed93/Data/DStar_Hosts.json", true},
	// The id<->callsign table every rendered gateway config points at. Published
	// only — see Shipped above.
	{"DMRIds.dat", "https://www.pistar.uk/downloads/DMRIds.dat", false},
}

// minSize guards against shipping a truncated list: an error page is small, a
// real hostlist is not.
const minSize = 1024

func main() {
	check := flag.Bool("check", false, "report what would change without writing")
	dir := flag.String("dir", "internal/hostsrc/seed", "directory holding the shipped copies")
	out := flag.String("out", "", "capture into this directory instead of -dir (for publishing); created if absent")
	flag.Parse()

	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "cannot create %s: %v\n", *out, err)
			os.Exit(1)
		}
		// Capturing to a staging directory is always a fresh write: there is no
		// existing copy to compare against, so "unchanged" is not a useful verdict.
		*dir = *out
	}

	client := &http.Client{Timeout: 60 * time.Second}
	var changed, failed, considered int
	for _, c := range captures {
		// Refreshing the shipped copies must not drag in the publish-only ones.
		if *out == "" && !c.Shipped {
			continue
		}
		considered++
		path := filepath.Join(*dir, c.File)
		old, _ := os.ReadFile(path)

		body, err := fetch(client, c.URL)
		if err != nil {
			fmt.Printf("  !! %-18s %v\n     keeping the existing copy (%d bytes)\n", c.File, err, len(old))
			failed++
			continue
		}
		if len(body) < minSize {
			fmt.Printf("  !! %-18s only %d bytes, refusing to ship it\n", c.File, len(body))
			failed++
			continue
		}
		if sum(old) == sum(body) {
			fmt.Printf("  ok %-18s unchanged (%d bytes)\n", c.File, len(body))
			continue
		}
		changed++
		if *check {
			fmt.Printf("  ~~ %-18s would change: %d -> %d bytes\n", c.File, len(old), len(body))
			continue
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			fmt.Printf("  !! %-18s write failed: %v\n", c.File, err)
			failed++
			continue
		}
		fmt.Printf("  ++ %-18s updated: %d -> %d bytes\n", c.File, len(old), len(body))
	}

	fmt.Printf("\n%d changed, %d failed, %d considered\n", changed, failed, considered)
	if failed > 0 {
		os.Exit(1)
	}
	if *check && changed > 0 {
		os.Exit(2) // "stale", distinct from "something broke"
	}
}

func fetch(c *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Waypoint hostseed")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

func sum(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
