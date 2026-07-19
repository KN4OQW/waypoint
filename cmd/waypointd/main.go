// waypointd is the Waypoint core daemon: config store, stack supervisor,
// hardware operations, and the REST/SSE API that serves the web UI.
//
// Current phase: event hub + dashboard, fed by the demo generator until the
// MQTT bridge to MMDVM-Host lands. Demo mode is always labeled in the API.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/auth"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/demo"
	"github.com/KN4OQW/waypoint/internal/dmrhosts"
	"github.com/KN4OQW/waypoint/internal/dstarhosts"
	"github.com/KN4OQW/waypoint/internal/events"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/lcd"
	"github.com/KN4OQW/waypoint/internal/lcd/hd44780"
	"github.com/KN4OQW/waypoint/internal/m17hosts"
	"github.com/KN4OQW/waypoint/internal/mqtt"
	"github.com/KN4OQW/waypoint/internal/netconfig"
	"github.com/KN4OQW/waypoint/internal/nxdnhosts"
	"github.com/KN4OQW/waypoint/internal/p25hosts"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/internal/ysfhosts"
	"github.com/KN4OQW/waypoint/ui"
)

// Version is stamped by the release build (-ldflags "-X main.Version=...").
var Version = "dev"

type server struct {
	hub       *hub.Hub
	demo      bool
	started   time.Time
	store     *store.Store
	storePath string
	evStore   *events.Store // persistent event history (RFC-0004); nil only in tests
	auth      *auth.Auth    // first-boot claim state machine + sessions (RFC-0002)
	paths     config.Paths  // where each daemon reads its generated INI (render targets)

	// Host/OS networking domain (docs/config-coverage.md §4). netKeyfileDir is
	// where the NetworkManager keyfile renderer writes waypoint-*.nmconnection;
	// netGuard runs the confirm-or-revert apply (a bad network change can strand
	// the node, so it is guarded, unlike the radio apply); netConfirmTimeout is the
	// rollback window handed to each apply.
	netKeyfileDir     string
	netConfirmTimeout time.Duration
	netBackend        string // "composite" (NM + keyfile) or "keyfile" (fallback)
	timesyncdConf     string // rendered systemd-timesyncd drop-in path (NTP direct apply)
	netGuard          *netconfig.Guard
	// Wi-Fi scan cache + timezone list cache (netScanMu guards both).
	netScanMu  sync.Mutex
	netScan    []netconfig.WiFiScanResult
	netScanAt  time.Time
	timezones  []string
	ysfHosts   string // cached YSF reflector hostlist (JSON)
	p25Hosts   string // cached P25 reflector (talkgroup) hostlist (JSON)
	nxdnHosts  string // cached NXDN reflector (talkgroup) hostlist (JSON)
	dstarHosts string // cached D-Star reflector hostlist (JSON)
	m17Hosts   string // cached M17 reflector hostlist (space/tab text)
	dmrHosts   string // cached DMR master hostlist (DMR_Hosts.txt, space/tab text)

	// Native LCD renderer lifecycle. The renderer captures its config at start, so
	// a config change (enable, geometry, pages) only reaches the panel when the
	// renderer is torn down and restarted — reloadLCD does that on apply. Guarded
	// because apply runs on an HTTP goroutine while the renderer runs on its own.
	lcdMu     sync.Mutex
	lcdCancel context.CancelFunc // stops the running renderer (nil when not running)
	lcdDone   chan struct{}      // closed when the stopped renderer has released the device
	lcdCfg    config.LCD         // config the running renderer was started with (for change detection)
}

// m17Reflectors serves the cached M17 reflector hostlist for the settings-page
// startup-reflector picker (GET /api/m17/reflectors).
func (s *server) m17Reflectors(w http.ResponseWriter, _ *http.Request) {
	refs, err := m17hosts.Reflectors(s.m17Hosts)
	if err != nil {
		refs = []m17hosts.Reflector{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(refs)
}

// dstarReflectors serves the cached D-Star reflector hostlist for the
// settings-page startup-reflector picker (GET /api/dstar/reflectors).
func (s *server) dstarReflectors(w http.ResponseWriter, _ *http.Request) {
	refs, err := dstarhosts.Reflectors(s.dstarHosts)
	if err != nil {
		// No cache yet (offline / first boot) → empty list, not an error.
		refs = []dstarhosts.Reflector{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(refs)
}

// nxdnReflectors serves the cached NXDN reflector (talkgroup) hostlist for the
// settings-page startup-TG picker (GET /api/nxdn/reflectors).
func (s *server) nxdnReflectors(w http.ResponseWriter, _ *http.Request) {
	refs, err := nxdnhosts.Reflectors(s.nxdnHosts)
	if err != nil {
		// No cache yet (offline / first boot) → empty list, not an error.
		refs = []nxdnhosts.Reflector{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(refs)
}

// p25Reflectors serves the cached P25 reflector (talkgroup) hostlist for the
// settings-page startup-TG picker (GET /api/p25/reflectors).
func (s *server) p25Reflectors(w http.ResponseWriter, _ *http.Request) {
	refs, err := p25hosts.Reflectors(s.p25Hosts)
	if err != nil {
		// No cache yet (offline / first boot) → empty list, not an error.
		refs = []p25hosts.Reflector{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(refs)
}

// dmrMasters serves the cached DMR master hostlist for the settings-page DMR
// master-server dropdowns (GET /api/dmr/masters).
func (s *server) dmrMasters(w http.ResponseWriter, _ *http.Request) {
	m, err := dmrhosts.Masters(s.dmrHosts)
	if err != nil {
		m = []dmrhosts.Master{} // no cache yet (offline / first boot) → empty list
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}

// ysfReflectors serves the cached YSF reflector hostlist for the settings-page
// startup-reflector picker (GET /api/ysf/reflectors).
func (s *server) ysfReflectors(w http.ResponseWriter, _ *http.Request) {
	refs, err := ysfhosts.Reflectors(s.ysfHosts)
	if err != nil {
		// No cache yet (offline / first boot) → empty list, not an error.
		refs = []ysfhosts.Reflector{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(refs)
}

// configView serves the node's configuration for the settings page from the
// authoritative store (RFC-0001) — the store is the read model, not the INIs.
func (s *server) configView(w http.ResponseWriter, r *http.Request) {
	// PUT /api/config/{section} writes one section; GET returns the view.
	if r.Method == http.MethodPut {
		s.configPut(w, r)
		return
	}
	m, err := config.Load(s.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m.View(s.storePath))
}

// isCrossBridge reports whether a section name is one of the cross-mode
// transcoding bridges, which write through the secret-preserving SetCrossBridge.
func isCrossBridge(section string) bool {
	switch section {
	case "ysf2dmr", "dmr2ysf", "ysf2nxdn", "dmr2nxdn", "nxdn2dmr":
		return true
	}
	return false
}

// configPut writes a single config section (PUT /api/config/{section}).
func (s *server) configPut(w http.ResponseWriter, r *http.Request) {
	section := strings.TrimPrefix(r.URL.Path, "/api/config/")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Networks are an array with secrets: use the password-preserving merge.
	if section == "networks" {
		if err := config.SetNetworks(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The D-Star gateway carries the ircDDB password, the same write-only secret
	// rule: a blank field keeps the stored one (see SetDStarGateway).
	if section == "dstargw" {
		if err := config.SetDStarGateway(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// POCSAG carries the DAPNET AuthKey, the same write-only secret rule: a blank
	// field keeps the stored one (see SetDAPNET).
	if section == "pocsag" {
		if err := config.SetDAPNET(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The native LCD driver validates its page geometry on save: a page may not
	// declare more lines than the panel has rows (SetLCD → ValidateLCD), so an
	// invalid page set is rejected here rather than silently clipped on the panel.
	if section == "lcd" {
		if err := config.SetLCD(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Event-history retention validates on save (retention_days must be >= 0;
	// 0 = keep forever), so route it through SetHistory rather than the generic
	// merge (RFC-0004).
	if section == "history" {
		if err := config.SetHistory(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Cross-mode bridges: YSF2DMR/NXDN2DMR carry a redacted DMR-master password, so
	// the same write-only-secret rule applies — a blank field keeps the stored one
	// (SetCrossBridge). Routing all five through it is uniform and harmless: the
	// no-secret bridges simply carry no password key.
	if isCrossBridge(section) {
		known, err := config.SetCrossBridge(s.store, section, body, "api")
		if !known {
			http.Error(w, "unknown config section: "+section, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	known, err := config.SetSection(s.store, section, body, "api")
	if !known {
		http.Error(w, "unknown config section: "+section, http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// configApply renders the store to the daemons' INI files and restarts the
// affected units (POST /api/config/apply). This is the store made authoritative:
// the files are regenerated wholesale from the model, never patched in place.
func (s *server) configApply(w http.ResponseWriter, _ *http.Request) {
	m, err := config.Load(s.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	targets := m.RenderTargets(s.paths)
	if err := m.WriteFiles(s.paths); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
		return
	}
	restarted, err := s.restartUnits(restartSet(targets))
	if err != nil {
		http.Error(w, "restart: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Stop any retired cross-mode bridge daemon (MMDVM_CM) still running from the old
	// per-bridge surface. The bridges no longer contribute a render target (RFC-0003
	// bus architecture supersedes them), so they are never restarted; stopping any
	// that are still active on every apply closes the stale-daemon-on-disable defect
	// by construction. Best-effort: a stop failure is logged, not fatal to the apply.
	stopped := s.stopUnitsIfActive(config.RetiredBridgeUnits())
	_ = s.store.RecordApply("api", map[string]any{"restarted": restarted, "stopped": stopped})
	// The native LCD driver renders no INI and restarts no unit, so it is absent
	// from targets/restarted — bring the panel in line with the applied config
	// here (a no-op unless the LCD section changed).
	s.reloadLCD(m)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"applied": true, "restarted": restarted, "stopped": stopped})
}

// restartSet is the deduped, ordered list of units to restart for a set of
// render targets. Two modes sharing a unit collapse to one restart.
func restartSet(targets []config.RenderTarget) []string {
	seen := map[string]bool{}
	var units []string
	for _, t := range targets {
		if t.Unit == "" || seen[t.Unit] {
			continue
		}
		seen[t.Unit] = true
		units = append(units, t.Unit)
	}
	return units
}

// systemctlRun invokes systemctl and returns its combined output. It is a package
// variable so tests can substitute a fake (there is no systemd under `go test`).
var systemctlRun = func(args ...string) ([]byte, error) {
	return exec.Command("systemctl", args...).CombinedOutput()
}

func (s *server) restartUnits(units []string) ([]string, error) {
	var done []string
	for _, u := range units {
		if u == "" {
			continue
		}
		if out, err := systemctlRun("restart", u); err != nil {
			return done, fmt.Errorf("%s: %v: %s", u, err, strings.TrimSpace(string(out)))
		}
		done = append(done, u)
	}
	return done, nil
}

// stopUnitsIfActive stops each unit that is currently active, skipping ones that
// are already inactive or not installed (`is-active` exits non-zero for both). A
// stop failure is logged and skipped rather than failing the apply — a lingering
// bridge that refuses to stop must not block a config change. Returns the units it
// actually stopped.
func (s *server) stopUnitsIfActive(units []string) []string {
	var stopped []string
	for _, u := range units {
		if u == "" {
			continue
		}
		if _, err := systemctlRun("is-active", "--quiet", u); err != nil {
			continue // inactive or unknown unit — nothing to stop
		}
		if out, err := systemctlRun("stop", u); err != nil {
			log.Printf("apply: stop %s: %v: %s", u, err, strings.TrimSpace(string(out)))
			continue
		}
		stopped = append(stopped, u)
	}
	return stopped
}

// seedStore imports the existing INI files into a fresh store on first run, so
// the store starts as an exact picture of what the node is already running.
func (s *server) seedStore() error {
	empty, err := s.store.IsEmpty()
	if err != nil || !empty {
		return err
	}
	m, err := config.Import(s.paths.MMDVM, s.paths.DMRGateway)
	if err != nil {
		return fmt.Errorf("seed import: %w", err)
	}
	if err := m.Save(s.store, "seed"); err != nil {
		return err
	}
	log.Printf("config store seeded from %s + %s", s.paths.MMDVM, s.paths.DMRGateway)
	return nil
}

// backfillDefaults writes defaults for sections added after this store was first
// seeded (a store created before YSF has no ysfgw row). It only fills absent
// sections, so it never overwrites a user's settings.
func (s *server) backfillDefaults() error {
	if _, ok, err := s.store.Get("ysfgw"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("ysfgw", config.DefaultYSFGateway(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled ysfgw defaults")
	}
	// P25 arrived after YSF: a store seeded before it lacks both the [P25] mode
	// params and the gateway section. A fresh store gets p25 from the import; an
	// older one needs both backfilled so Load never returns zero values.
	if _, ok, err := s.store.Get("p25"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("p25", config.DefaultP25(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled p25 defaults")
	}
	if _, ok, err := s.store.Get("p25gw"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("p25gw", config.DefaultP25Gateway(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled p25gw defaults")
	}
	// NXDN arrived after P25: same story — a store seeded before it lacks the
	// [NXDN] mode params and the gateway section, so backfill both.
	if _, ok, err := s.store.Get("nxdn"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("nxdn", config.DefaultNXDN(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled nxdn defaults")
	}
	if _, ok, err := s.store.Get("nxdngw"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("nxdngw", config.DefaultNXDNGateway(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled nxdngw defaults")
	}
	// D-Star arrived after NXDN: same story — a store seeded before it lacks the
	// [D-Star] mode params and the gateway section, so backfill both.
	if _, ok, err := s.store.Get("dstar"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("dstar", config.DefaultDStar(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled dstar defaults")
	}
	if _, ok, err := s.store.Get("dstargw"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("dstargw", config.DefaultDStarGateway(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled dstargw defaults")
	}
	// M17 arrived after D-Star: same story — a store seeded before it lacks the
	// [M17] mode params and the gateway section, so backfill both.
	if _, ok, err := s.store.Get("m17"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("m17", config.DefaultM17(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled m17 defaults")
	}
	if _, ok, err := s.store.Get("m17gw"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("m17gw", config.DefaultM17Gateway(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled m17gw defaults")
	}
	// Display arrived after M17: a store seeded before the Display surface lacks
	// the section, so backfill the display-free default (Display=None).
	if _, ok, err := s.store.Get("display"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("display", config.DefaultDisplay(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled display defaults")
	}
	// Cross-mode bridges arrived after Display: a store seeded before them lacks
	// each bridge section. Backfill the disabled defaults so Load never returns a
	// zero bridge and RenderTargets sees a real (off) Enable flag.
	for _, bf := range []struct {
		key string
		val any
	}{
		{"ysf2dmr", config.DefaultYSF2DMR()},
		{"dmr2ysf", config.DefaultDMR2YSF()},
		{"ysf2nxdn", config.DefaultYSF2NXDN()},
		{"dmr2nxdn", config.DefaultDMR2NXDN()},
		{"nxdn2dmr", config.DefaultNXDN2DMR()},
	} {
		if _, ok, err := s.store.Get(bf.key); err != nil || !ok {
			if err != nil {
				return err
			}
			if err := s.store.Set(bf.key, bf.val, "backfill"); err != nil {
				return err
			}
			log.Printf("config store: backfilled %s defaults", bf.key)
		}
	}
	// POCSAG + FM arrived after the cross-mode bridges: a store seeded before them
	// lacks both sections. Backfill their defaults so Load never returns a zero
	// value and the paging/analog panels render sane fields.
	if _, ok, err := s.store.Get("pocsag"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("pocsag", config.DefaultPOCSAG(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled pocsag defaults")
	}
	if _, ok, err := s.store.Get("fm"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("fm", config.DefaultFM(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled fm defaults")
	}
	// The native LCD driver section arrived after the cross-mode bridges: a store
	// seeded before it lacks the row, so backfill the disabled default (with its
	// starter pages) so Load never returns a zero LCD.
	if _, ok, err := s.store.Get("lcd"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("lcd", config.DefaultLCD(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled lcd defaults")
	}
	// Event-history retention arrived with the persistent event store (RFC-0004): a
	// store seeded before it lacks the row, so backfill the 7-day default so Load
	// never returns a zero (which would read as "keep forever") and the nightly
	// prune has a real window.
	if _, ok, err := s.store.Get("history"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("history", config.DefaultHistory(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled history defaults")
	}
	return nil
}

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Time    string `json:"time"`
	Uptime  string `json:"uptime"`
	Demo    bool   `json:"demo"`
	Detail  string `json:"detail,omitempty"`
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	detail := ""
	if s.demo {
		detail = demo.Banner()
	}
	_ = json.NewEncoder(w).Encode(healthResponse{
		Status:  "ok",
		Version: Version,
		Time:    time.Now().UTC().Format(time.RFC3339),
		Uptime:  time.Since(s.started).Round(time.Second).String(),
		Demo:    s.demo,
		Detail:  detail,
	})
}

// events streams the hub over Server-Sent Events as a pure live tail. Initial
// dashboard history is served separately by GET /api/history from the persistent
// event store (RFC-0004), so this handler no longer replays the hub's in-memory
// backlog to browser clients — doing so would double-render every event the
// client already fetched from /api/history. (The in-memory backlog still serves
// the LCD renderer, which subscribes to the hub directly, not through this path.)
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	ch, _, cancel := s.hub.Subscribe()
	defer cancel()

	send := func(e hub.Event) bool {
		b, err := json.Marshal(e)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			if !send(e) {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// history serves GET /api/history?since=&type=&limit= from the persistent event
// store (RFC-0004): the dashboard's initial render, replacing the old
// backlog-on-connect. It returns a JSON array of events newest-first, the same
// wire shape the SSE stream emits, so the client feeds them through the same
// reducer. since accepts an RFC-3339 timestamp or unix milliseconds; type filters
// one event type; limit is clamped by the store.
func (s *server) history(w http.ResponseWriter, r *http.Request) {
	if s.evStore == nil {
		http.Error(w, "history unavailable", http.StatusServiceUnavailable)
		return
	}
	q := events.HistoryQuery{Type: r.URL.Query().Get("type")}
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q.Since = t
		} else if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
			q.Since = time.UnixMilli(ms)
		} else {
			http.Error(w, "invalid since: want RFC-3339 or unix milliseconds", http.StatusBadRequest)
			return
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		q.Limit = n
	}
	evs, err := s.evStore.History(q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Never null: an empty history serializes as [] so the client always gets an
	// array to iterate.
	if evs == nil {
		evs = []hub.Event{}
	}
	_ = json.NewEncoder(w).Encode(evs)
}

// startLCD launches the native HD44780 renderer as a hub subscriber when the
// config enables it, returning whether it started. It replays the event backlog
// so the panel opens with current state, then drives the renderer from a ticker
// (at the scroll cadence) until its context is canceled. When disabled it does
// nothing. Device unavailability is never fatal to the daemon (design §7): a
// panel that fails to open falls back to a headless noop. Records the renderer's
// cancel/done handles and the config it started with so reloadLCD can stop it.
// The caller must hold lcdMu.
func (s *server) startLCD(parent context.Context, m *config.Model) bool {
	s.lcdCfg = m.LCD
	if !m.LCD.Enabled {
		return false
	}
	dev := newLCDDevice(m.LCD)
	r := lcd.NewRenderer(m.LCD, lcdInfo(m, Version, s.started), dev, func() string { return hostIPv4(net.InterfaceAddrs) })
	ch, backlog, unsub := s.hub.Subscribe()
	for _, e := range backlog {
		r.Handle(e)
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done) // signals the device is released (r.Run closes it on return)
		defer unsub()
		ticker := time.NewTicker(tickInterval(m.LCD))
		defer ticker.Stop()
		_ = r.Run(ctx, ch, ticker.C)
	}()
	s.lcdCancel, s.lcdDone = cancel, done
	log.Printf("lcd: renderer started on %s@%s (%sx%s, %d pages)", m.LCD.I2CBus, m.LCD.I2CAddress, m.LCD.Rows, m.LCD.Cols, len(m.LCD.Pages))
	return true
}

// stopLCD cancels the running renderer and waits for it to release the I2C device
// before returning, so a subsequent start reopens a free bus. No-op when nothing
// is running. The caller must hold lcdMu.
func (s *server) stopLCD() {
	if s.lcdCancel == nil {
		return
	}
	s.lcdCancel()
	<-s.lcdDone
	s.lcdCancel, s.lcdDone = nil, nil
}

// reloadLCD brings the renderer in line with the current config, restarting it
// only when the LCD section actually changed (so an unrelated apply never blinks
// the panel). This is what makes the panel reflect an edit-pages-then-apply flow
// without a daemon restart: the renderer captures its config at start, so a
// change requires a stop+start. Safe to call from the apply HTTP goroutine.
func (s *server) reloadLCD(m *config.Model) {
	s.lcdMu.Lock()
	defer s.lcdMu.Unlock()
	if s.lcdCancel != nil && reflect.DeepEqual(s.lcdCfg, m.LCD) {
		return // running with the same config — nothing to do
	}
	if s.lcdCancel == nil && !m.LCD.Enabled {
		s.lcdCfg = m.LCD // stopped and still disabled — record config, stay stopped
		return
	}
	s.stopLCD()
	s.startLCD(context.Background(), m)
}

// newLCDDevice opens the real HD44780 over the configured PCF8574 I2C backpack,
// falling back to a headless noop if the bus or panel is unavailable — device
// trouble is never fatal to the daemon (design §7).
func newLCDDevice(cfg config.LCD) lcd.LCDDevice {
	dev, err := hd44780.Open(cfg.I2CBus, cfg.I2CAddress)
	if err != nil {
		log.Printf("lcd: I2C %s@%s unavailable, disabled: %v", cfg.I2CBus, cfg.I2CAddress, err)
		return lcd.NoopDevice{}
	}
	return dev
}

// lcdInfo snapshots the config/health-derived tokens the renderer needs. Modes
// are the enabled modes' short keys (DMR, YSF, …) — compact for a narrow panel.
func lcdInfo(m *config.Model, version string, started time.Time) lcd.Info {
	var modes []string
	for _, md := range m.View("").Modes {
		if md.Enabled {
			modes = append(modes, strings.ToUpper(md.Key))
		}
	}
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	return lcd.Info{
		Callsign: m.General.Callsign,
		DMRID:    m.General.ID,
		Modes:    modes,
		Version:  version,
		Started:  started,
		Hostname: host,
		FreqRX:   m.Modem.RXFreqHz,
		FreqTX:   m.Modem.TXFreqHz,
	}
}

// tickInterval is the renderer's frame cadence: the scroll step, so marquees
// animate smoothly, floored so a bad value never spins the slow I2C bus too hard.
func tickInterval(cfg config.LCD) time.Duration {
	ms := 300
	if v, err := strconv.Atoi(strings.TrimSpace(cfg.ScrollSpeed)); err == nil && v > 0 {
		ms = v
	}
	if ms < 50 {
		ms = 50
	}
	return time.Duration(ms) * time.Millisecond
}

// hostIPv4 returns the node's first non-loopback IPv4 address, or "no-ip". The
// interface lister is injected so it is testable without touching real NICs.
func hostIPv4(list func() ([]net.Addr, error)) string {
	addrs, err := list()
	if err != nil {
		return "no-ip"
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
	}
	return "no-ip"
}

// Claimed reports whether the device has been claimed (RFC-0002), from the auth
// subsystem's cached, store-derived state. It is the state the HTTP gate serves
// its per-state route allowlist from.
func (s *server) Claimed() bool { return s.auth.Claimed() }

// newMux registers every route the daemon serves. It is separate from main so the
// gate integration tests exercise the exact route table the daemon runs, wrapped
// in the same s.auth.Gate. The claim/session endpoints are the only pre-auth API
// routes; every other route sits behind the gate and defaults to denied.
func (s *server) newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.health)
	mux.HandleFunc("/api/events", s.events)
	mux.HandleFunc("/api/history", s.history)
	mux.HandleFunc("/api/config", s.configView)
	mux.HandleFunc("/api/config/apply", s.configApply)
	mux.HandleFunc("/api/config/", s.configView) // PUT /api/config/{section}
	mux.HandleFunc("/api/ysf/reflectors", s.ysfReflectors)
	mux.HandleFunc("/api/p25/reflectors", s.p25Reflectors)
	mux.HandleFunc("/api/nxdn/reflectors", s.nxdnReflectors)
	mux.HandleFunc("/api/dstar/reflectors", s.dstarReflectors)
	mux.HandleFunc("/api/m17/reflectors", s.m17Reflectors)
	mux.HandleFunc("/api/dmr/masters", s.dmrMasters)
	// Host/OS networking domain (docs/config-coverage.md §4).
	mux.HandleFunc("/api/network/status", s.networkStatus)
	mux.HandleFunc("/api/network/wifi/scan", s.networkWiFiScan)
	mux.HandleFunc("/api/network/timezones", s.networkTimezones)
	mux.HandleFunc("/api/network/config", s.networkConfig)
	mux.HandleFunc("/api/network/apply", s.networkApply)
	mux.HandleFunc("/api/network/confirm", s.networkConfirm)
	mux.HandleFunc("/api/network/host/apply", s.networkHostApply)
	// First-boot claim + session endpoints (RFC-0002). These are the only routes
	// the gate serves before authentication (claim while unclaimed, session while
	// claimed); everything else above is behind the wall.
	mux.HandleFunc("/api/claim", s.auth.HandleClaim)
	mux.HandleFunc("/api/session", s.auth.HandleSession)
	mux.Handle("/", http.FileServerFS(ui.FS()))
	return mux
}

func main() {
	// Subcommands are dispatched before flag parsing: `waypointd reset-claim`
	// connects to the store directly and returns the device to claim mode, for an
	// operator with a shell on the box (RFC-0002 "Reset procedure (a)").
	if len(os.Args) > 1 && os.Args[1] == "reset-claim" {
		os.Exit(runResetClaim(os.Args[2:]))
	}

	addr := flag.String("addr", "127.0.0.1:8073", "listen address for the API and UI")
	demoMode := flag.Bool("demo", false, "publish synthetic traffic (no radio required); always labeled in /api/health")
	broker := flag.String("mqtt-broker", "127.0.0.1:1883", "MMDVM-Host MQTT broker host:port (live mode)")
	mqttName := flag.String("mqtt-name", "mmdvm", "MMDVM-Host [MQTT] Name (topic prefix)")
	mqttUser := flag.String("mqtt-user", "", "MQTT username (optional)")
	mqttPass := flag.String("mqtt-pass", "", "MQTT password (optional)")
	mmdvmINI := flag.String("mmdvm-ini", "/home/pi-star/waypoint/etc/MMDVM-Host.ini", "MMDVM-Host.ini render target (the file the daemon reads)")
	dmrgwINI := flag.String("dmrgateway-ini", "/home/pi-star/waypoint/etc/DMRGateway.ini", "DMRGateway.ini render target")
	ysfgwINI := flag.String("ysfgateway-ini", "/home/pi-star/waypoint/etc/YSFGateway.ini", "YSFGateway.ini render target")
	dgidgwINI := flag.String("dgidgateway-ini", "/home/pi-star/waypoint/etc/DGIdGateway.ini", "DGIdGateway.ini render target (used when DG-ID gateway is enabled)")
	p25gwINI := flag.String("p25gateway-ini", "/home/pi-star/waypoint/etc/P25Gateway.ini", "P25Gateway.ini render target")
	nxdngwINI := flag.String("nxdngateway-ini", "/home/pi-star/waypoint/etc/NXDNGateway.ini", "NXDNGateway.ini render target")
	dstargwINI := flag.String("dstargateway-ini", "/home/pi-star/waypoint/etc/dstargateway.cfg", "dstargateway.cfg render target")
	m17gwINI := flag.String("m17gateway-ini", "/home/pi-star/waypoint/etc/M17Gateway.ini", "M17Gateway.ini render target")
	dapnetgwINI := flag.String("dapnetgateway-ini", "/home/pi-star/waypoint/etc/DAPNETGateway.ini", "DAPNETGateway.ini render target (POCSAG paging gateway)")
	// The cross-mode bridge render-target flags (ysf2dmr-ini … nxdn2dmr-ini) are
	// retired with the per-bridge-daemon model (RFC-0003 bus architecture). No bridge
	// INI is rendered any more; apply stops any bridge daemon still running instead.
	ysfHosts := flag.String("ysf-hosts", "/home/pi-star/waypoint/etc/YSFHosts.json", "cached YSF reflector hostlist path")
	ysfHostsURL := flag.String("ysf-hosts-url", ysfhosts.DefaultURL, "YSF reflector hostlist source URL")
	p25Hosts := flag.String("p25-hosts", "/home/pi-star/waypoint/etc/P25Hosts.json", "cached P25 reflector hostlist path")
	p25HostsURL := flag.String("p25-hosts-url", p25hosts.DefaultURL, "P25 reflector hostlist source URL")
	nxdnHosts := flag.String("nxdn-hosts", "/home/pi-star/waypoint/etc/NXDNHosts.json", "cached NXDN reflector hostlist path")
	nxdnHostsURL := flag.String("nxdn-hosts-url", nxdnhosts.DefaultURL, "NXDN reflector hostlist source URL")
	// The D-Star cache path is the DStar_Hosts.json inside the gateway's HostsFiles
	// directory — the gateway reads it there directly (no separate copy).
	dstarHosts := flag.String("dstar-hosts", "/home/pi-star/waypoint/etc/DStar_Hosts.json", "cached D-Star reflector hostlist path")
	dstarHostsURL := flag.String("dstar-hosts-url", dstarhosts.DefaultURL, "D-Star reflector hostlist source URL")
	m17Hosts := flag.String("m17-hosts", "/home/pi-star/waypoint/etc/M17Hosts.txt", "cached M17 reflector hostlist path")
	m17HostsURL := flag.String("m17-hosts-url", m17hosts.DefaultURL, "M17 reflector hostlist source URL")
	dmrHosts := flag.String("dmr-hosts", "/usr/local/etc/DMR_Hosts.txt", "cached DMR master hostlist path (DMR_Hosts.txt)")
	dmrHostsURL := flag.String("dmr-hosts-url", dmrhosts.DefaultURL, "DMR master hostlist source URL")
	storePath := flag.String("store", "/home/pi-star/waypoint/config.db", "path to the SQLite configuration store")
	eventsPath := flag.String("events-store", "/home/pi-star/waypoint/events.db", "path to the SQLite event-history store (RFC-0004); a config.db sibling")
	nmKeyfileDir := flag.String("nm-keyfile-dir", "/etc/NetworkManager/system-connections", "directory for rendered NetworkManager keyfiles (waypoint-*.nmconnection)")
	netConfirmTimeout := flag.Duration("network-confirm-timeout", netconfig.DefaultConfirmTimeout, "confirm-or-revert rollback window for a network apply")
	netBackend := flag.String("network-backend", "composite", "network rollback backend: composite (NM D-Bus checkpoint + keyfile snapshot) or keyfile (fallback, no live-device rollback)")
	timesyncdConf := flag.String("timesyncd-conf", "/etc/systemd/timesyncd.conf.d/waypoint.conf", "rendered systemd-timesyncd drop-in for NTP servers")
	// The session cookie's Secure flag is gated on TLS being present: it stays off
	// until the TLS PR serves HTTPS and flips this default, so a pre-TLS build over
	// plain HTTP does not set a flag that would make the cookie unusable (RFC-0002).
	secureCookie := flag.Bool("secure-cookie", false, "set the session cookie Secure flag (enable once TLS is serving HTTPS)")
	flag.Parse()

	st, err := store.Open(*storePath)
	if err != nil {
		log.Fatalf("config store: %v", err)
	}
	defer st.Close()

	// Event-history store (RFC-0004). In demo mode it is in-memory so synthetic
	// traffic never accretes a persistent history on disk; live mode persists to the
	// events.db sibling of config.db.
	evPath := *eventsPath
	if *demoMode {
		evPath = ":memory:"
	}
	ev, err := events.Open(evPath)
	if err != nil {
		log.Fatalf("events store: %v", err)
	}
	defer ev.Close()

	s := &server{
		hub: hub.New(), demo: *demoMode, started: time.Now(),
		store: st, storePath: *storePath, evStore: ev,
		paths: config.Paths{
			MMDVM: *mmdvmINI, DMRGateway: *dmrgwINI, YSFGateway: *ysfgwINI, DGIdGateway: *dgidgwINI,
			P25Gateway: *p25gwINI, NXDNGateway: *nxdngwINI, DStarGateway: *dstargwINI, M17Gateway: *m17gwINI,
			DAPNETGateway: *dapnetgwINI,
		},
		ysfHosts: *ysfHosts, p25Hosts: *p25Hosts, nxdnHosts: *nxdnHosts, dstarHosts: *dstarHosts, m17Hosts: *m17Hosts, dmrHosts: *dmrHosts,
		netKeyfileDir: *nmKeyfileDir, netConfirmTimeout: *netConfirmTimeout, netBackend: *netBackend,
		timesyncdConf: *timesyncdConf,
	}
	// The confirm-or-revert guard for network applies (needs s.netKeyfileDir set).
	s.netGuard = s.newNetGuard()
	if err := s.seedStore(); err != nil {
		log.Printf("config store seed skipped: %v", err)
	}
	if err := s.backfillDefaults(); err != nil {
		log.Printf("config store backfill skipped: %v", err)
	}

	// First-boot claim state machine + sessions (RFC-0002). buildAuth also consumes
	// any boot-partition reset marker before the server starts serving, so a device
	// booted with a marker comes up unclaimed. A failure here is fatal: starting
	// with an unknown/inconsistent auth state could expose config surfaces.
	s.auth, err = buildAuth(st, *secureCookie)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}

	// Native LCD driver: paints a physical HD44780 from the live status plane when
	// the operator has enabled it. Disabled by default, so this is a no-op on a
	// headless node.
	if m, err := config.Load(s.store); err != nil {
		log.Printf("lcd: config load failed, renderer not started: %v", err)
	} else {
		s.lcdMu.Lock()
		s.startLCD(context.Background(), m)
		s.lcdMu.Unlock()
	}

	// Persist every hub event to the history store, and prune it nightly to the
	// operator's retention window (RFC-0004). Both run in demo and live mode — demo
	// simply persists into the in-memory store opened above. The prune reads the
	// retention setting from the config store each night, so an edit in Station
	// Settings takes effect without a restart.
	go events.Run(context.Background(), s.evStore, s.hub, events.DefaultFlushInterval, events.DefaultBatchSize)
	go events.RunPrune(context.Background(), s.evStore, 24*time.Hour, func() int {
		var h config.History
		if _, err := s.store.GetInto("history", &h); err != nil {
			return config.DefaultHistoryRetentionDays // fall back to the default window on a read error
		}
		return h.RetentionDays
	})

	if *demoMode {
		go demo.Run(context.Background(), s.hub)
	} else {
		go func() {
			if err := mqtt.Run(context.Background(), s.hub, mqtt.Options{
				Broker:   *broker,
				Name:     *mqttName,
				Username: *mqttUser,
				Password: *mqttPass,
			}); err != nil {
				log.Printf("mqtt bridge stopped: %v", err)
			}
		}()
		// Keep the reflector hostlists fresh for the gateways + pickers. The YSF
		// list honors the "UPPERCASE Hostfiles" toggle, read from the store each
		// refresh (both YSFGateway and DGIdGateway consume this same file).
		go ysfhosts.Run(context.Background(), *ysfHostsURL, *ysfHosts, 6*time.Hour, func() bool {
			var y config.YSFGateway
			if _, err := s.store.GetInto("ysfgw", &y); err != nil {
				return false
			}
			return y.UpperHostfiles
		})
		go p25hosts.Run(context.Background(), *p25HostsURL, *p25Hosts, 6*time.Hour)
		go nxdnhosts.Run(context.Background(), *nxdnHostsURL, *nxdnHosts, 6*time.Hour)
		go dstarhosts.Run(context.Background(), *dstarHostsURL, *dstarHosts, 6*time.Hour)
		go m17hosts.Run(context.Background(), *m17HostsURL, *m17Hosts, 6*time.Hour)
		go dmrhosts.Run(context.Background(), *dmrHostsURL, *dmrHosts, 6*time.Hour)
	}

	mode := "live, mqtt " + *broker
	if *demoMode {
		mode = "demo"
	}
	claimState := "claimed"
	if !s.auth.Claimed() {
		claimState = "UNCLAIMED (serving claim mode only)"
	}
	log.Printf("waypointd %s (%s, %s) listening on http://%s", Version, mode, claimState, *addr)
	srv := &http.Server{
		Addr: *addr,
		// The auth gate fronts the entire mux: it is the single seam that enforces
		// the claim state machine and session requirement, so no handler re-checks
		// auth (RFC-0002). A route absent from the gate's allowlist defaults to denied.
		Handler:           s.auth.Gate(s.newMux()),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
