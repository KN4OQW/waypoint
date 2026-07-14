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
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/demo"
	"github.com/KN4OQW/waypoint/internal/dmrhosts"
	"github.com/KN4OQW/waypoint/internal/dstarhosts"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/m17hosts"
	"github.com/KN4OQW/waypoint/internal/mqtt"
	"github.com/KN4OQW/waypoint/internal/nxdnhosts"
	"github.com/KN4OQW/waypoint/internal/p25hosts"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/internal/ysfhosts"
	"github.com/KN4OQW/waypoint/ui"
)

// Version is stamped by the release build (-ldflags "-X main.Version=...").
var Version = "dev"

type server struct {
	hub        *hub.Hub
	demo       bool
	started    time.Time
	store      *store.Store
	storePath  string
	paths      config.Paths // where each daemon reads its generated INI (render targets)
	ysfHosts   string       // cached YSF reflector hostlist (JSON)
	p25Hosts   string       // cached P25 reflector (talkgroup) hostlist (JSON)
	nxdnHosts  string       // cached NXDN reflector (talkgroup) hostlist (JSON)
	dstarHosts string       // cached D-Star reflector hostlist (JSON)
	m17Hosts   string       // cached M17 reflector hostlist (space/tab text)
	dmrHosts   string       // cached DMR master hostlist (DMR_Hosts.txt, space/tab text)
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
	_ = s.store.RecordApply("api", map[string]any{"restarted": restarted})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"applied": true, "restarted": restarted})
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

func (s *server) restartUnits(units []string) ([]string, error) {
	var done []string
	for _, u := range units {
		if u == "" {
			continue
		}
		if out, err := exec.Command("systemctl", "restart", u).CombinedOutput(); err != nil {
			return done, fmt.Errorf("%s: %v: %s", u, err, strings.TrimSpace(string(out)))
		}
		done = append(done, u)
	}
	return done, nil
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

// events streams the hub over Server-Sent Events: backlog first, then live.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	ch, backlog, cancel := s.hub.Subscribe()
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

	for _, e := range backlog {
		if !send(e) {
			return
		}
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

func main() {
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
	ysf2dmrINI := flag.String("ysf2dmr-ini", "/home/pi-star/waypoint/etc/YSF2DMR.ini", "YSF2DMR.ini render target (rendered only when the bridge is enabled)")
	dmr2ysfINI := flag.String("dmr2ysf-ini", "/home/pi-star/waypoint/etc/DMR2YSF.ini", "DMR2YSF.ini render target (rendered only when the bridge is enabled)")
	ysf2nxdnINI := flag.String("ysf2nxdn-ini", "/home/pi-star/waypoint/etc/YSF2NXDN.ini", "YSF2NXDN.ini render target (rendered only when the bridge is enabled)")
	dmr2nxdnINI := flag.String("dmr2nxdn-ini", "/home/pi-star/waypoint/etc/DMR2NXDN.ini", "DMR2NXDN.ini render target (rendered only when the bridge is enabled)")
	nxdn2dmrINI := flag.String("nxdn2dmr-ini", "/home/pi-star/waypoint/etc/NXDN2DMR.ini", "NXDN2DMR.ini render target (rendered only when the bridge is enabled)")
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
	flag.Parse()

	st, err := store.Open(*storePath)
	if err != nil {
		log.Fatalf("config store: %v", err)
	}
	defer st.Close()

	s := &server{
		hub: hub.New(), demo: *demoMode, started: time.Now(),
		store: st, storePath: *storePath,
		paths: config.Paths{
			MMDVM: *mmdvmINI, DMRGateway: *dmrgwINI, YSFGateway: *ysfgwINI, DGIdGateway: *dgidgwINI,
			P25Gateway: *p25gwINI, NXDNGateway: *nxdngwINI, DStarGateway: *dstargwINI, M17Gateway: *m17gwINI,
			DAPNETGateway: *dapnetgwINI,
			YSF2DMR:       *ysf2dmrINI, DMR2YSF: *dmr2ysfINI, YSF2NXDN: *ysf2nxdnINI, DMR2NXDN: *dmr2nxdnINI, NXDN2DMR: *nxdn2dmrINI,
		},
		ysfHosts: *ysfHosts, p25Hosts: *p25Hosts, nxdnHosts: *nxdnHosts, dstarHosts: *dstarHosts, m17Hosts: *m17Hosts, dmrHosts: *dmrHosts,
	}
	if err := s.seedStore(); err != nil {
		log.Printf("config store seed skipped: %v", err)
	}
	if err := s.backfillDefaults(); err != nil {
		log.Printf("config store backfill skipped: %v", err)
	}

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

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.health)
	mux.HandleFunc("/api/events", s.events)
	mux.HandleFunc("/api/config", s.configView)
	mux.HandleFunc("/api/config/apply", s.configApply)
	mux.HandleFunc("/api/config/", s.configView) // PUT /api/config/{section}
	mux.HandleFunc("/api/ysf/reflectors", s.ysfReflectors)
	mux.HandleFunc("/api/p25/reflectors", s.p25Reflectors)
	mux.HandleFunc("/api/nxdn/reflectors", s.nxdnReflectors)
	mux.HandleFunc("/api/dstar/reflectors", s.dstarReflectors)
	mux.HandleFunc("/api/m17/reflectors", s.m17Reflectors)
	mux.HandleFunc("/api/dmr/masters", s.dmrMasters)
	mux.Handle("/", http.FileServerFS(ui.FS()))

	mode := "live, mqtt " + *broker
	if *demoMode {
		mode = "demo"
	}
	log.Printf("waypointd %s (%s) listening on http://%s", Version, mode, *addr)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
