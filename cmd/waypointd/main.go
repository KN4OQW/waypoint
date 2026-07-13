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
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/mqtt"
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
	mmdvmINI  string // render target: the file MMDVM-Host reads
	dmrgwINI  string // render target: the file DMRGateway reads
	ysfgwINI  string // render target: the file YSFGateway reads
	p25gwINI  string // render target: the file P25Gateway reads
	ysfHosts  string // cached YSF reflector hostlist (JSON)
	p25Hosts  string // cached P25 reflector (talkgroup) hostlist (JSON)
	units     []string
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
	if err := m.WriteFiles(s.mmdvmINI, s.dmrgwINI, s.ysfgwINI, s.p25gwINI); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
		return
	}
	restarted, err := s.restartUnits()
	if err != nil {
		http.Error(w, "restart: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = s.store.RecordApply("api", map[string]any{"restarted": restarted})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"applied": true, "restarted": restarted})
}

func (s *server) restartUnits() ([]string, error) {
	var done []string
	for _, u := range s.units {
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
	m, err := config.Import(s.mmdvmINI, s.dmrgwINI)
	if err != nil {
		return fmt.Errorf("seed import: %w", err)
	}
	if err := m.Save(s.store, "seed"); err != nil {
		return err
	}
	log.Printf("config store seeded from %s + %s", s.mmdvmINI, s.dmrgwINI)
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
	p25gwINI := flag.String("p25gateway-ini", "/home/pi-star/waypoint/etc/P25Gateway.ini", "P25Gateway.ini render target")
	ysfHosts := flag.String("ysf-hosts", "/home/pi-star/waypoint/etc/YSFHosts.json", "cached YSF reflector hostlist path")
	ysfHostsURL := flag.String("ysf-hosts-url", ysfhosts.DefaultURL, "YSF reflector hostlist source URL")
	p25Hosts := flag.String("p25-hosts", "/home/pi-star/waypoint/etc/P25Hosts.json", "cached P25 reflector hostlist path")
	p25HostsURL := flag.String("p25-hosts-url", p25hosts.DefaultURL, "P25 reflector hostlist source URL")
	storePath := flag.String("store", "/home/pi-star/waypoint/config.db", "path to the SQLite configuration store")
	units := flag.String("units", "waypoint-mmdvm.service,waypoint-dmrgateway.service,waypoint-ysfgateway.service,waypoint-p25gateway.service", "comma-separated systemd units to restart on apply")
	flag.Parse()

	st, err := store.Open(*storePath)
	if err != nil {
		log.Fatalf("config store: %v", err)
	}
	defer st.Close()

	s := &server{
		hub: hub.New(), demo: *demoMode, started: time.Now(),
		store: st, storePath: *storePath,
		mmdvmINI: *mmdvmINI, dmrgwINI: *dmrgwINI, ysfgwINI: *ysfgwINI, p25gwINI: *p25gwINI,
		ysfHosts: *ysfHosts, p25Hosts: *p25Hosts,
		units: strings.Split(*units, ","),
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
		// Keep the reflector hostlists fresh for the gateways + pickers.
		go ysfhosts.Run(context.Background(), *ysfHostsURL, *ysfHosts, 6*time.Hour)
		go p25hosts.Run(context.Background(), *p25HostsURL, *p25Hosts, 6*time.Hour)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.health)
	mux.HandleFunc("/api/events", s.events)
	mux.HandleFunc("/api/config", s.configView)
	mux.HandleFunc("/api/config/apply", s.configApply)
	mux.HandleFunc("/api/config/", s.configView) // PUT /api/config/{section}
	mux.HandleFunc("/api/ysf/reflectors", s.ysfReflectors)
	mux.HandleFunc("/api/p25/reflectors", s.p25Reflectors)
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
