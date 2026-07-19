// Package dmrhosts fetches and caches the DMR master hostlist (DMR_Hosts.txt)
// that populates the settings-page DMR "master server" dropdowns — the same
// file Pi-Star/WPSD uses. Each line is whitespace-separated
// (Name  [Number]  Address  Password  Port); the network family is derived from
// the name prefix (BM_ → brandmeister, DMR+/FreeDMR_/FD_/HB_ → dmrplus,
// SystemX_ → systemx, TGIF → tgif), so the UI can group masters per section.
package dmrhosts

import (
	"context"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/hostfile"
	"github.com/KN4OQW/waypoint/internal/verifydl"
)

// DefaultURL is the WPSD-maintained DMR master hostlist (the same lineage as the
// box's /usr/local/etc/DMR_Hosts.txt). Overridable by flag; a deployment can
// instead point the cache path straight at an existing DMR_Hosts.txt.
const DefaultURL = "https://hostfiles.w0chp.net/DMR_Hosts.txt"

// Master is one DMR master server the operator can pick for a network.
type Master struct {
	Name     string `json:"name"`
	Category string `json:"category"` // brandmeister | dmrplus | systemx | tgif | other
	Address  string `json:"address"`
	Password string `json:"password"`
	Port     string `json:"port"`
}

// category maps a host name prefix to a network family (mirrors WPSD's grouping).
func category(name string) string {
	switch u := strings.ToUpper(name); {
	case strings.HasPrefix(u, "BM_"):
		return "brandmeister"
	case strings.HasPrefix(u, "DMR+") || strings.HasPrefix(u, "FREEDMR_") || strings.HasPrefix(u, "FD_") || strings.HasPrefix(u, "HB_"):
		return "dmrplus"
	case strings.HasPrefix(u, "SYSTEMX_"):
		return "systemx"
	case strings.HasPrefix(u, "TGIF"):
		return "tgif"
	default:
		return "other"
	}
}

// Fetch downloads the hostlist to path atomically. When a trusted key is
// configured (RFC-0013) the download is verified against its <url>.minisig before
// it replaces the cache — a tampered list is rejected and the previous cache kept.
// A failed fetch always leaves any previously-cached file intact.
func Fetch(ctx context.Context, url, path string, v verifydl.Verify) error {
	v.UserAgent = "Waypoint DMR hostlist"
	if v.HasPubKey && v.SigURL == "" {
		v.SigURL = url + ".minisig"
	}
	body, err := verifydl.Download(ctx, url, v)
	if err != nil {
		return err
	}
	// Wrap the downloaded list in the operator's local prepend/append hooks
	// (RFC-0005 / issue #2) so local masters survive every refresh. With no hooks
	// the file is written exactly as downloaded.
	return hostfile.WriteWithHooks(path, body, "dmrhosts")
}

// Masters reads the cached hostlist and returns the parsed masters, sorted by
// category then name. Malformed lines are skipped. The last three fields of a
// line are Address, Password, Port; the first is the Name (a numeric master
// column may sit between Name and Address, so anchor on the tail).
func Masters(path string) ([]Master, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Master
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		out = append(out, Master{
			Name:     f[0],
			Category: category(f[0]),
			Address:  f[len(f)-3],
			Password: f[len(f)-2],
			Port:     f[len(f)-1],
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Run fetches the hostlist once at startup and then every interval until ctx is
// canceled. Fetch failures are logged, not fatal — a hotspot may be briefly
// offline, and the cached file keeps working.
func Run(ctx context.Context, url, path string, interval time.Duration, v verifydl.Verify) {
	fetch := func() {
		if err := Fetch(ctx, url, path, v); err != nil {
			log.Printf("dmrhosts: fetch failed (using cached list if present): %v", err)
		} else {
			log.Printf("dmrhosts: updated %s", path)
		}
	}
	fetch()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fetch()
		}
	}
}
