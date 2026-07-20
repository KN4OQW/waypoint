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
	"github.com/KN4OQW/waypoint/internal/dmrtg"
	"github.com/KN4OQW/waypoint/internal/dstarhosts"
	"github.com/KN4OQW/waypoint/internal/events"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/lcd"
	"github.com/KN4OQW/waypoint/internal/lcd/hd44780"
	"github.com/KN4OQW/waypoint/internal/m17hosts"
	"github.com/KN4OQW/waypoint/internal/minisign"
	"github.com/KN4OQW/waypoint/internal/mqtt"
	"github.com/KN4OQW/waypoint/internal/netconfig"
	"github.com/KN4OQW/waypoint/internal/nxdnhosts"
	"github.com/KN4OQW/waypoint/internal/p25hosts"
	"github.com/KN4OQW/waypoint/internal/status"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/internal/verifydl"
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
	evStore   *events.Store      // persistent event history (RFC-0004); nil only in tests
	auth      *auth.Auth         // first-boot claim state machine + sessions (RFC-0002)
	paths     config.Paths       // where each daemon reads its generated INI (render targets)
	agg       *status.Aggregator // live-status fold served by /api/status + WS (RFC-0008); nil only in some tests

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
	dmrTGs     string // cached DMR talkgroup-name list (RFC-0010)

	// Atomic-update surface (RFC-0014 / issue #13). update holds the manifest URL,
	// release key, and OS seams; updateArgs is the `-update` invocation the apply
	// endpoint launches detached. Both nil/empty disables the update API.
	update     *updateConfig
	updateArgs []string

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

// dmrTalkgroups serves the cached DMR talkgroup-name list for inline name
// resolution and the searchable TG picker (GET /api/dmr/talkgroups; RFC-0010).
func (s *server) dmrTalkgroups(w http.ResponseWriter, _ *http.Request) {
	tgs, err := dmrtg.Talkgroups(s.dmrTGs)
	if err != nil {
		tgs = []dmrtg.Talkgroup{} // no cache yet (offline / first boot) → empty list
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tgs)
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
	// Mode buses (RFC-0003): buses[] and attachments[] write through the attach-time
	// validator, not the generic merge — an invalid bus (dangling bus_id/credentials_ref,
	// a mode on two buses, a non-reframe mode set) is refused here so it can never be
	// persisted. The reason is the human-readable string the validator returns.
	if section == "buses" {
		if err := config.SetBuses(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if section == "attachments" {
		if err := config.SetAttachments(s.store, body, "api"); err != nil {
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
	restarted, stopped, err := s.applyRender("api")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"applied": true, "restarted": restarted, "stopped": stopped})
}

// applyRender is the store-to-daemons apply shared by a manual apply and a profile
// activation (RFC-0006): load the model, regenerate every INI wholesale, restart
// the affected units, stop retired bridge daemons, journal the apply, and bring
// the LCD in line. by attributes the journal entry ("api" for a manual apply,
// "profile:<name>" for an activation). Errors are returned already prefixed.
func (s *server) applyRender(by string) (restarted, stopped []string, err error) {
	m, err := config.Load(s.store)
	if err != nil {
		return nil, nil, err
	}
	targets := m.RenderTargets(s.paths)
	warnings, err := m.WriteFiles(s.paths)
	if err != nil {
		return nil, nil, fmt.Errorf("render: %w", err)
	}
	for _, wn := range warnings {
		log.Printf("overrides: %s", wn) // malformed fragment line — surfaced, never silently dropped
	}
	restarted, err = s.restartUnits(restartSet(targets))
	if err != nil {
		return nil, nil, fmt.Errorf("restart: %w", err)
	}
	// Stop any retired cross-mode bridge daemon (MMDVM_CM) still running from the old
	// per-bridge surface. The bridges no longer contribute a render target (RFC-0003
	// bus architecture supersedes them), so they are never restarted; stopping any
	// that are still active on every apply closes the stale-daemon-on-disable defect
	// by construction. Best-effort: a stop failure is logged, not fatal to the apply.
	// Also stop the daemon of any bus that is present but disabled (RFC-0003): an
	// enabled bus contributes a render target and is (re)started above; a disabled
	// bus contributes none, so its lingering waypoint-bus@<id> is stopped here.
	stopped = s.stopUnitsIfActive(append(config.RetiredBridgeUnits(), m.DisabledBusUnits()...))
	_ = s.store.RecordApply(by, map[string]any{"restarted": restarted, "stopped": stopped})
	// The native LCD driver renders no INI and restarts no unit, so it is absent
	// from targets/restarted — bring the panel in line with the applied config
	// here (a no-op unless the LCD section changed).
	s.reloadLCD(m)
	return restarted, stopped, nil
}

// profileSummary is the metadata a profile list/return carries — never the
// captured sections (which can hold secrets), so the list endpoint cannot leak.
type profileSummary struct {
	Name        string             `json:"name"`
	CreatedAt   string             `json:"created_at,omitempty"`
	UpdatedAt   string             `json:"updated_at,omitempty"`
	Fingerprint config.Fingerprint `json:"fingerprint"`
	Sensitive   []string           `json:"sensitive,omitempty"`
	Active      bool               `json:"active"`
}

func (s *server) summarize(p *config.Profile) profileSummary {
	active, _ := config.IsActive(s.store, p)
	return profileSummary{
		Name: p.Name, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		Fingerprint: p.Fingerprint, Sensitive: p.Sensitive, Active: active,
	}
}

// profilesView handles /api/profiles: GET lists saved profiles (metadata only),
// POST captures the current store as a named profile (RFC-0006 / issue #3).
func (s *server) profilesView(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := config.ListProfiles(s.store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := []profileSummary{}
		for _, p := range list {
			out = append(out, s.summarize(p))
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		name, ok := validProfileName(body.Name)
		if !ok {
			http.Error(w, "name must be 1–64 characters", http.StatusBadRequest)
			return
		}
		p, err := config.CaptureProfile(s.store, name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := config.SaveProfile(s.store, p); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		saved, _ := config.GetProfile(s.store, name)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, s.summarize(saved))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// profilesRouter handles /api/profiles/{name}, /{name}/activate, /{name}/export.
func (s *server) profilesRouter(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/profiles/")
	parts := strings.SplitN(tail, "/", 2)
	name := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	if name == "" {
		http.Error(w, "profile name required", http.StatusBadRequest)
		return
	}
	switch {
	case action == "activate" && r.Method == http.MethodPost:
		s.profileActivate(w, name)
	case action == "export" && r.Method == http.MethodGet:
		s.profileExport(w, name)
	case action == "" && r.Method == http.MethodDelete:
		removed, err := config.DeleteProfile(s.store, name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !removed {
			http.Error(w, "no such profile", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"deleted": true})
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// profileActivate writes a profile's sections atomically, then re-renders and
// restarts exactly like a manual apply (RFC-0006). Secrets are reconciled by
// ActivateProfile (a blank secret keeps the stored one).
func (s *server) profileActivate(w http.ResponseWriter, name string) {
	p, err := config.GetProfile(s.store, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if p == nil {
		http.Error(w, "no such profile", http.StatusNotFound)
		return
	}
	if err := config.ActivateProfile(s.store, p, "profile:"+name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	restarted, stopped, err := s.applyRender("profile:" + name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"activated": name, "restarted": restarted, "stopped": stopped})
}

// profileExport returns the scrubbed, fingerprinted export artifact for download.
func (s *server) profileExport(w http.ResponseWriter, name string) {
	p, err := config.GetProfile(s.store, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if p == nil {
		http.Error(w, "no such profile", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(name)+`.waypoint-profile.json"`)
	_ = json.NewEncoder(w).Encode(p.Export())
}

// profilesImport stores an export artifact as a profile (never activates).
// Secrets stay scrubbed; the operator re-enters them, or activation preserves the
// target node's current secrets. A name collision is 409 unless ?overwrite=1.
func (s *server) profilesImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var p config.Profile
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&p); err != nil {
		http.Error(w, "invalid profile artifact", http.StatusBadRequest)
		return
	}
	name, ok := validProfileName(p.Name)
	if !ok {
		http.Error(w, "profile has no valid name", http.StatusBadRequest)
		return
	}
	p.Name = name
	if r.URL.Query().Get("overwrite") != "1" {
		exists, err := config.ProfileExists(s.store, name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if exists {
			http.Error(w, "a profile named "+name+" already exists (use ?overwrite=1)", http.StatusConflict)
			return
		}
	}
	if err := config.SaveProfile(s.store, &p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	saved, _ := config.GetProfile(s.store, name)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, s.summarize(saved))
}

// validProfileName trims and bounds a profile name (1–64 chars after trim).
func validProfileName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return "", false
	}
	return name, true
}

// safeFilename reduces a profile name to a filesystem-safe export filename.
func safeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "profile"
	}
	return b.String()
}

// defaultNodeID derives a stable HA-discovery node id from the OS hostname
// (sanitized to a topic/id-safe token), falling back to "waypoint" when the
// hostname is unavailable or empty after sanitizing (RFC-0011).
func defaultNodeID() string {
	h, err := os.Hostname()
	if err != nil {
		return "waypoint"
	}
	var b strings.Builder
	for _, r := range strings.ToLower(h) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "waypoint"
	}
	return b.String()
}

// hostfileVerify builds the verification config for reference-data downloads
// (RFC-0013). With no key path it verifies nothing (plain fetch, today's default);
// with a key it verifies each list against its <url>.minisig. A key that fails to
// load is fatal only when verification was required, else it degrades to a warning.
func hostfileVerify(pubkeyPath string, require bool) verifydl.Verify {
	v := verifydl.Verify{Require: require}
	if pubkeyPath == "" {
		return v
	}
	b, err := os.ReadFile(pubkeyPath)
	if err != nil {
		log.Printf("hostfile verification: cannot read pubkey %s: %v (downloads unverified)", pubkeyPath, err)
		return v
	}
	pk, err := minisign.ParsePublicKey(string(b))
	if err != nil {
		log.Printf("hostfile verification: bad pubkey %s: %v (downloads unverified)", pubkeyPath, err)
		return v
	}
	v.PubKey, v.HasPubKey = pk, true
	return v
}

// runVerify implements `waypointd -verify <file> -verify-sig <file.minisig>
// -verify-pubkey <key>`: verify a signed artifact and exit 0/1 with a clear
// message. It is the operator/updater-facing entry to the same primitive the
// atomic updater (#13) uses before applying a release (RFC-0013).
func runVerify(file, sigPath, pubPath string) {
	pb, err := os.ReadFile(pubPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: read pubkey: %v\n", err)
		os.Exit(2)
	}
	pk, err := minisign.ParsePublicKey(string(pb))
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: bad pubkey: %v\n", err)
		os.Exit(2)
	}
	if sigPath == "" {
		sigPath = file + ".minisig"
	}
	if err := minisign.VerifyFile(pk, file, sigPath); err != nil {
		fmt.Fprintf(os.Stderr, "verify: REJECTED: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("verify: OK — %s is signed by the trusted key\n", file)
	os.Exit(0)
}

// overridesRoot returns the override drop-in root, or "" in demo mode so a demo
// run never merges a real node's overrides into its synthetic config (RFC-0005).
func overridesRoot(dir string, demo bool) string {
	if demo {
		return ""
	}
	return dir
}

// importScan reads an incumbent Pi-Star/WPSD card (mounted dir or uploaded files),
// maps it to a model, and returns the migration report plus a redacted preview —
// writing nothing (RFC-0007 / issue #4). The operator reviews before committing.
func (s *server) importScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	contents, names, platform, err := readImportInput(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m, report, err := config.Migrate(contents, names, platform)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// preview is the SAME redacted view the config API serves — secrets appear only
	// as has_* booleans, never in the scan response.
	writeJSON(w, map[string]any{"report": report, "preview": m.View(s.storePath)})
}

// importApply re-reads the incumbent input, maps it, and bulk-writes the model to
// the store in one transaction (RFC-0007). It does not restart daemons — the
// operator sees the imported config in the settings UI, then Applies to go live.
func (s *server) importApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	contents, names, platform, err := readImportInput(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m, report, err := config.Migrate(contents, names, platform)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := m.SaveAtomic(s.store, "import"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"imported": true, "report": report})
}

// readImportInput accepts either a multipart upload of incumbent config files
// (matched to roles by name) or a JSON body {"dir": "/mnt/…"} naming a mounted
// card. Both converge on the (contents, names, platform) triple Migrate consumes.
func readImportInput(r *http.Request) (contents map[string][]byte, names map[string]string, platform string, err error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			return nil, nil, "", fmt.Errorf("invalid upload: %w", err)
		}
		contents = map[string][]byte{}
		names = map[string]string{}
		for _, fhs := range r.MultipartForm.File {
			for _, fh := range fhs {
				role := config.RoleForFilename(fh.Filename)
				if role == "" {
					continue // not a recognized incumbent config file
				}
				f, oerr := fh.Open()
				if oerr != nil {
					return nil, nil, "", oerr
				}
				b, rerr := io.ReadAll(io.LimitReader(f, 4<<20))
				f.Close()
				if rerr != nil {
					return nil, nil, "", rerr
				}
				contents[role] = b
				names[role] = fh.Filename
			}
		}
		if len(contents) == 0 {
			return nil, nil, "", fmt.Errorf("no recognized Pi-Star/WPSD config files in the upload")
		}
		return contents, names, "unknown", nil
	}
	var body struct {
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		return nil, nil, "", fmt.Errorf("invalid body")
	}
	if strings.TrimSpace(body.Dir) == "" {
		return nil, nil, "", fmt.Errorf("provide a directory path (dir) or upload files")
	}
	return config.Locate(body.Dir)
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
	// Mode buses (RFC-0003) arrived after the LCD driver: a store seeded before them
	// lacks both sections. Backfill the empty defaults so Load never returns a nil
	// surprise; a fresh node starts with no buses.
	for _, bf := range []struct {
		key string
		val any
	}{
		{"buses", config.DefaultBuses()},
		{"attachments", config.DefaultAttachments()},
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

// overridesView serves GET /api/overrides: the override records that shape the
// current store's render — what the next Apply will actually write (RFC-0005 /
// issue #2). Read-only (overrides are edited on disk in v1); behind the same
// session wall as every config route. The response names the override root so the
// UI can tell the operator where fragments live, and surfaces any malformed-line
// warnings rather than dropping them silently.
func (s *server) overridesView(w http.ResponseWriter, _ *http.Request) {
	m, err := config.Load(s.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	applied, warnings, err := m.Overrides(s.paths)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if applied == nil {
		applied = []config.Applied{} // never null — the client always gets an array
	}
	if warnings == nil {
		warnings = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"dir":       s.paths.OverridesDir,
		"overrides": applied,
		"warnings":  warnings,
	})
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
	mux.HandleFunc("/api/status", s.statusView) // live status snapshot (RFC-0008)
	mux.HandleFunc("/api/ws", s.wsStream)       // WebSocket: events + status frames
	mux.HandleFunc("/api/config", s.configView)
	mux.HandleFunc("/api/config/apply", s.configApply)
	mux.HandleFunc("/api/config/", s.configView) // PUT /api/config/{section}
	mux.HandleFunc("/api/overrides", s.overridesView)
	mux.HandleFunc("/api/buses/validate", s.busesValidate)   // dry-run attach validator (RFC-0003 §2)
	mux.HandleFunc("/api/buses/migrate", s.busesMigrate)     // seed buses from the dormant bridges (§4)
	mux.HandleFunc("/api/profiles", s.profilesView)          // GET list, POST capture (RFC-0006)
	mux.HandleFunc("/api/profiles/import", s.profilesImport) // more specific than /api/profiles/
	mux.HandleFunc("/api/profiles/", s.profilesRouter)       // {name}[/activate|/export], DELETE
	mux.HandleFunc("/api/import/scan", s.importScan)         // preview an incumbent card (RFC-0007)
	mux.HandleFunc("/api/import/apply", s.importApply)       // commit the migration
	mux.HandleFunc("/api/ysf/reflectors", s.ysfReflectors)
	mux.HandleFunc("/api/p25/reflectors", s.p25Reflectors)
	mux.HandleFunc("/api/nxdn/reflectors", s.nxdnReflectors)
	mux.HandleFunc("/api/dstar/reflectors", s.dstarReflectors)
	mux.HandleFunc("/api/m17/reflectors", s.m17Reflectors)
	mux.HandleFunc("/api/dmr/masters", s.dmrMasters)
	mux.HandleFunc("/api/dmr/talkgroups", s.dmrTalkgroups) // TG name list (RFC-0010)
	// Host/OS networking domain (docs/config-coverage.md §4).
	mux.HandleFunc("/api/network/status", s.networkStatus)
	mux.HandleFunc("/api/network/wifi/scan", s.networkWiFiScan)
	mux.HandleFunc("/api/network/timezones", s.networkTimezones)
	mux.HandleFunc("/api/network/config", s.networkConfig)
	mux.HandleFunc("/api/network/apply", s.networkApply)
	mux.HandleFunc("/api/network/confirm", s.networkConfirm)
	mux.HandleFunc("/api/network/host/apply", s.networkHostApply)
	// Atomic-update endpoints (RFC-0014 / issue #13), behind the session wall.
	mux.HandleFunc("/api/update/check", s.updateCheck)
	mux.HandleFunc("/api/update/apply", s.updateApply)
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

	addr := flag.String("addr", "127.0.0.1:8073", "HTTPS listen address for the API and UI (plaintext when -tls=false)")
	useTLS := flag.Bool("tls", true, "serve HTTPS with a self-signed device cert (RFC-0012); set false only behind a TLS-terminating proxy")
	tlsDir := flag.String("tls-dir", "/home/pi-star/waypoint/tls", "directory holding the self-signed device cert/key (minted on first start)")
	httpRedirectAddr := flag.String("http-redirect-addr", "", "optional HTTP listener that 301-redirects to HTTPS, e.g. :80 (empty disables it)")
	acmeDomain := flag.String("acme-domain", "", "public hostname for a Let's Encrypt cert instead of self-signed (requires :80 + :443 reachable)")
	acmeEmail := flag.String("acme-email", "", "contact email for the Let's Encrypt account (optional)")
	acmeDir := flag.String("acme-dir", "/home/pi-star/waypoint/acme", "cache directory for Let's Encrypt certificates")
	demoMode := flag.Bool("demo", false, "publish synthetic traffic (no radio required); always labeled in /api/health")
	broker := flag.String("mqtt-broker", "127.0.0.1:1883", "MMDVM-Host MQTT broker host:port (live mode)")
	mqttName := flag.String("mqtt-name", "mmdvm", "MMDVM-Host [MQTT] Name (topic prefix)")
	statusPrefix := flag.String("status-topic-prefix", "waypoint/status", "MQTT prefix for the normalized status republish (RFC-0008)")
	haDiscovery := flag.Bool("ha-discovery", true, "publish Home Assistant MQTT discovery so entities appear with zero YAML (RFC-0011)")
	haPrefix := flag.String("ha-discovery-prefix", "homeassistant", "Home Assistant MQTT discovery prefix")
	nodeID := flag.String("node-id", defaultNodeID(), "device id + MQTT node segment for HA discovery (stable across restarts)")
	statusTick := flag.Duration("status-watchdog-tick", time.Second, "how often the status aggregator runs its stranded-transmission watchdog")
	probeInterval := flag.Duration("gateway-probe-interval", time.Second, "how often the supervisor probes gateway liveness (RFC-0008; keep < 2s for the #5 acceptance)")
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
	overridesDir := flag.String("overrides-dir", "/home/pi-star/waypoint/overrides.d", "root of operator override drop-ins: <dir>/<daemon>.d/*.conf merge last into each rendered INI (RFC-0005 / issue #2)")
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
	dmrTGs := flag.String("dmr-talkgroups", "/home/pi-star/waypoint/etc/TGList.txt", "cached DMR talkgroup-name list path (RFC-0010)")
	dmrTGsURL := flag.String("dmr-talkgroups-url", dmrtg.DefaultURL, "DMR talkgroup-name list source URL")
	hostfilePubkey := flag.String("hostfile-pubkey", "", "minisign public key (file path) to verify signed hostfile/TG downloads against (RFC-0013; empty = no verification)")
	requireSignedHostfiles := flag.Bool("require-signed-hostfiles", false, "reject any hostfile/TG download that is not verified (RFC-0013)")
	verifyFile := flag.String("verify", "", "verify a signed artifact against a minisign key and exit (RFC-0013); use with -verify-pubkey")
	verifySig := flag.String("verify-sig", "", "the .minisig for -verify (default <file>.minisig)")
	verifyPubkey := flag.String("verify-pubkey", "", "minisign public key (file path) for -verify")
	// Atomic-update engine (RFC-0014 / issue #13). The three mode flags each run and
	// exit; the rest configure the manifest source, release key, and OS seams.
	updateMode := flag.Bool("update", false, "run the transactional update (verify, stage, atomic swap, health-gated confirm-or-revert) and exit (RFC-0014)")
	updateCheckMode := flag.Bool("update-check", false, "report whether a newer signed release is available and exit; changes nothing")
	updateBootCheck := flag.Bool("update-boot-check", false, "ExecStartPre boot hook: revert an update swapped but never confirmed (power-loss safety) and exit")
	updateURL := flag.String("update-url", defaultUpdateURL, "signed update-manifest URL (RFC-0014)")
	releasePubkey := flag.String("release-pubkey", "", "minisign public key (file path) that signs the update manifest and artifacts (RFC-0013); empty = unverified (not recommended)")
	updateBinary := flag.String("update-binary", "/home/pi-star/waypoint/bin/waypointd", "path to the live waypointd binary the update swaps atomically")
	updateUnit := flag.String("update-unit", "waypointd.service", "systemd unit the updater restarts")
	updateMarker := flag.String("update-marker", "/home/pi-star/waypoint/update.marker", "in-flight-update marker path (power-loss recovery)")
	busConfigDir := flag.String("bus-config-dir", "/home/pi-star/waypoint/etc", "directory for rendered mode-bus configs (waypoint-bus-<id>.json), consumed by waypoint-bus@<id>.service (RFC-0003)")
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
	showVersion := flag.Bool("version", false, "print the waypointd version and exit (RFC-0015 / issue #14)")
	flag.Parse()

	// `waypointd -version` (or --version) prints the stamped version and exits, before
	// any daemon startup. It reads the same main.Version that /api/health reports and
	// the release tag stamps, so the CLI, the API, and the release page agree (#14).
	if *showVersion {
		fmt.Printf("waypointd %s\n", Version)
		os.Exit(0)
	}

	// `waypointd -verify <file> -verify-pubkey <key>` verifies a signed artifact and
	// exits, before any daemon startup (RFC-0013).
	if *verifyFile != "" {
		runVerify(*verifyFile, *verifySig, *verifyPubkey)
	}

	// The update modes (RFC-0014) each run as a standalone invocation and exit,
	// before any daemon startup: -update does the transactional install (surviving
	// the service restart it triggers), -update-check reports availability, and
	// -update-boot-check is the ExecStartPre power-loss revert.
	if *updateMode || *updateCheckMode || *updateBootCheck {
		cfg := newUpdateConfig(*updateURL, *releasePubkey, *updateBinary, *updateUnit, *updateMarker, *addr, *useTLS)
		switch {
		case *updateBootCheck:
			runUpdateBootCheck(cfg)
		case *updateCheckMode:
			runUpdateCheck(cfg)
		default:
			runUpdate(cfg)
		}
	}

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
		agg: status.New(status.DefaultTxTTL),
		paths: config.Paths{
			MMDVM: *mmdvmINI, DMRGateway: *dmrgwINI, YSFGateway: *ysfgwINI, DGIdGateway: *dgidgwINI,
			P25Gateway: *p25gwINI, NXDNGateway: *nxdngwINI, DStarGateway: *dstargwINI, M17Gateway: *m17gwINI,
			DAPNETGateway: *dapnetgwINI,
			BusConfigDir:  *busConfigDir,
			// Demo mode must never pick up a real node's overrides: point the layer at an
			// empty path so the render is emitted verbatim (RFC-0005).
			OverridesDir: overridesRoot(*overridesDir, *demoMode),
		},
		ysfHosts: *ysfHosts, p25Hosts: *p25Hosts, nxdnHosts: *nxdnHosts, dstarHosts: *dstarHosts, m17Hosts: *m17Hosts, dmrHosts: *dmrHosts, dmrTGs: *dmrTGs,
		netKeyfileDir: *nmKeyfileDir, netConfirmTimeout: *netConfirmTimeout, netBackend: *netBackend,
		timesyncdConf: *timesyncdConf,
	}
	// The confirm-or-revert guard for network applies (needs s.netKeyfileDir set).
	s.netGuard = s.newNetGuard()

	// Atomic-update surface (RFC-0014). The API endpoints reuse the same config the
	// CLI modes build; updateArgs is the detached `-update` invocation apply launches,
	// carrying the same manifest/key/seam flags so the child behaves identically.
	updCfg := newUpdateConfig(*updateURL, *releasePubkey, *updateBinary, *updateUnit, *updateMarker, *addr, *useTLS)
	s.update = &updCfg
	s.updateArgs = []string{
		"-update",
		"-update-url", *updateURL,
		"-update-binary", *updateBinary,
		"-update-unit", *updateUnit,
		"-update-marker", *updateMarker,
		"-addr", *addr,
		fmt.Sprintf("-tls=%t", *useTLS),
	}
	if *releasePubkey != "" {
		s.updateArgs = append(s.updateArgs, "-release-pubkey", *releasePubkey)
	}
	if err := s.seedStore(); err != nil {
		log.Printf("config store seed skipped: %v", err)
	}
	if err := s.backfillDefaults(); err != nil {
		log.Printf("config store backfill skipped: %v", err)
	}
	// Connection profiles table (RFC-0006 / issue #3). A failure here disables the
	// profiles surface but must not stop the daemon serving config.
	if err := config.InitProfiles(st); err != nil {
		log.Printf("profiles table init skipped: %v", err)
	}

	// The session cookie's Secure flag turns on automatically whenever the daemon
	// serves TLS (RFC-0012), so it can never drift out of sync with the transport;
	// with -tls=false the operator sets -secure-cookie iff their proxy speaks HTTPS.
	tlsServing := *useTLS || *acmeDomain != ""
	secureCookieOn := *secureCookie || tlsServing

	// First-boot claim state machine + sessions (RFC-0002). buildAuth also consumes
	// any boot-partition reset marker before the server starts serving, so a device
	// booted with a marker comes up unclaimed. A failure here is fatal: starting
	// with an unknown/inconsistent auth state could expose config surfaces.
	s.auth, err = buildAuth(st, secureCookieOn)
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

	// The status aggregator folds the event stream into the live status served by
	// /api/status + the WebSocket (RFC-0008). Runs in both demo and live mode.
	go s.agg.Run(context.Background(), s.hub, *statusTick)

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
		// Supervisor liveness probe: emits gateway_up/gateway_down so a killed or
		// restarted gateway shows truth within the probe interval — the #5 acceptance,
		// from systemd state (not log scraping). Live mode only (a demo runs no gateways).
		go s.runLivenessProbe(context.Background(), *probeInterval)
		// Republish the normalized status onto retained waypoint/status/# topics for
		// Home Assistant and other consumers (RFC-0008). Best-effort, live mode only.
		// When HA discovery is on, the publisher also carries the offline Last-Will +
		// online-on-connect availability (RFC-0011).
		prefix := *statusPrefix
		avail := ""
		if *haDiscovery {
			avail = status.AvailabilityTopic(prefix)
		}
		pub := mqtt.NewPublisher(mqtt.Options{Broker: *broker, Name: *mqttName, Username: *mqttUser, Password: *mqttPass}, avail)
		s.agg.OnChange(func(st status.Status) { status.Republish(st, prefix, pub.Publish) })
		// Home Assistant MQTT discovery (RFC-0011): publish a retained config for each
		// entity, pointing HA at the status topics — zero YAML. Configs are published
		// once per topic as the entity first appears (gateways/networks show up over
		// time); retained, so HA gets them whenever it connects.
		if *haDiscovery {
			haOpts := status.DiscoveryOptions{Prefix: *haPrefix, NodeID: *nodeID, StatePrefix: prefix, Version: Version}
			var seenMu sync.Mutex
			seen := map[string]bool{}
			publishDiscovery := func(st status.Status) {
				for _, d := range status.DiscoveryConfigs(st, haOpts) {
					seenMu.Lock()
					dup := seen[d.Topic]
					seen[d.Topic] = true
					seenMu.Unlock()
					if !dup {
						pub.Publish(d.Topic, d.Payload)
					}
				}
			}
			publishDiscovery(s.agg.Snapshot()) // the always-present mode/tx/feed entities now
			s.agg.OnChange(publishDiscovery)
		}
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
		// Verified reference-data downloads (RFC-0013): when a trusted key is
		// configured, dmrhosts/dmrtg verify each list against its <url>.minisig before
		// it replaces the cache; a tampered list is rejected and the cache kept.
		hostVerify := hostfileVerify(*hostfilePubkey, *requireSignedHostfiles)
		go dmrhosts.Run(context.Background(), *dmrHostsURL, *dmrHosts, 6*time.Hour, hostVerify)
		go dmrtg.Run(context.Background(), *dmrTGsURL, *dmrTGs, 24*time.Hour, hostVerify)
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
	// Serve HTTPS by default with the self-signed device cert (RFC-0012), plus the
	// optional HTTP→HTTPS redirect and the ACME path. -tls=false serves plaintext
	// for a node behind a TLS-terminating proxy.
	scheme := "https"
	if !tlsServing {
		scheme = "http"
	}
	log.Printf("serving %s on %s", scheme, *addr)
	log.Fatal(listenAndServe(srv, tlsOptions{
		enabled:      tlsServing,
		certDir:      *tlsDir,
		httpsPort:    portOf(*addr),
		redirectAddr: *httpRedirectAddr,
		acmeDomain:   *acmeDomain,
		acmeEmail:    *acmeEmail,
		acmeDir:      *acmeDir,
	}))
}

// portOf returns the port of a listen address ("127.0.0.1:8073" -> "8073"), or ""
// when it has none — used to build HTTP→HTTPS redirect targets.
func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}
