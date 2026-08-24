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
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/auth"
	"github.com/KN4OQW/waypoint/internal/captive"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/demo"
	"github.com/KN4OQW/waypoint/internal/dmrhosts"
	"github.com/KN4OQW/waypoint/internal/dmrids"
	"github.com/KN4OQW/waypoint/internal/dmrtg"
	"github.com/KN4OQW/waypoint/internal/dstarhosts"
	"github.com/KN4OQW/waypoint/internal/events"
	"github.com/KN4OQW/waypoint/internal/hostsrc"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/idresolve"
	"github.com/KN4OQW/waypoint/internal/lcd"
	"github.com/KN4OQW/waypoint/internal/lcd/hd44780"
	"github.com/KN4OQW/waypoint/internal/m17hosts"
	"github.com/KN4OQW/waypoint/internal/messages"
	"github.com/KN4OQW/waypoint/internal/minisign"
	"github.com/KN4OQW/waypoint/internal/mqtt"
	"github.com/KN4OQW/waypoint/internal/netconfig"
	"github.com/KN4OQW/waypoint/internal/netwatch"
	"github.com/KN4OQW/waypoint/internal/notify"
	"github.com/KN4OQW/waypoint/internal/nxdnhosts"
	"github.com/KN4OQW/waypoint/internal/p25hosts"
	"github.com/KN4OQW/waypoint/internal/paths"
	"github.com/KN4OQW/waypoint/internal/peering"
	"github.com/KN4OQW/waypoint/internal/phonebook"
	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/provision"
	"github.com/KN4OQW/waypoint/internal/publicview"
	"github.com/KN4OQW/waypoint/internal/sdnotify"
	"github.com/KN4OQW/waypoint/internal/seed"
	"github.com/KN4OQW/waypoint/internal/status"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/internal/supervisor"
	"github.com/KN4OQW/waypoint/internal/tlscert"
	"github.com/KN4OQW/waypoint/internal/updater"
	"github.com/KN4OQW/waypoint/internal/verifydl"
	"github.com/KN4OQW/waypoint/internal/wizard"
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
	// authStore is the same store auth holds, kept here so the account-management
	// API can read and write accounts without going through the claim state
	// machine. One store, two callers with different jobs.
	authStore *auth.Store
	paths     config.Paths // where each daemon reads its generated INI (render targets)
	// mqttFlags is the command-line MQTT surface plus which of those flags the
	// operator typed explicitly. The store owns the data plane (#29), so this exists
	// only to let an explicit flag shadow it with a logged warning (D1 / system.go).
	mqttFlags mqttFlags
	// dp owns the live MQTT consumer + status republisher, reconfigured by apply when
	// the operator changes the data plane (dataplane.go). Nil in demo mode and in
	// tests, where reconfigure is a no-op.
	dp *dataPlane
	// relay owns the opt-in DMR loopback relay (dmrshim.go): the seam that lets the
	// node originate a data burst toward a radio. Always non-nil so callers need no
	// nil check on the owner; the relay INSIDE it is nil whenever the feature is
	// switched off, which is the default.
	relay *dmrRelay
	// msgs is the text-message service: it records what was sent and received and
	// transmits queued messages one at a time through the relay. Nil when there is
	// no event store to record into, and the API answers 503 rather than pretending.
	msgs *messages.Service
	// weather owns the alert subscription and the transmissions it produces.
	// Nil on a node where messaging is unavailable, since an alert with no way
	// to be sent is not worth subscribing for.
	weather *weatherService
	// listenAddr is the HTTPS address the daemon was told to serve on. It is shown
	// READ-ONLY on the System tab: it is deployment-owned via the packaged systemd
	// unit, and editing it live would move the UI out from under the browser doing
	// the edit (#29 scope amendment).
	listenAddr string
	agg        *status.Aggregator // live-status fold served by /api/status + WS (RFC-0008); nil only in some tests
	peering    *peering.Manager   // RFC-0016 LAN pairing manager (nil until initPeering runs / if it fails)
	// wiz is the first-boot setup wizard (docs/provisioning.md). Nil disables it
	// and the gate becomes a pass-through, which is what every test that builds a
	// server directly relies on.
	wiz *wizard.Wizard
	// ap is the setup access point and its captive portal, raised only while the
	// node is unprovisioned. Nil when the node is set up or the AP is disabled.
	ap *captive.Controller
	// apSession ties the AP to the listeners that make it useful, so a re-raise
	// after a failed join or a lost upstream brings the wizard back with it.
	apSession *apSession
	// apErr is why the setup access point is not up, surfaced through the wizard
	// so the failure is visible to whoever is setting the node up.
	apErrMu sync.Mutex
	apErr   string
	// certs owns the device certificate, so it can be reminted for the hostname
	// the operator chooses rather than the one the node booted with.
	certs *tlscert.Holder
	// prov is the privileged helper. waypointd is the unprivileged end of it: it
	// dials the socket and can ask for a fixed set of named operations, and holds
	// no capability the helper does not hand it one call at a time. Nil in tests
	// and in demo mode, where every caller degrades to "cannot repair, only
	// report" rather than to a panic.
	prov privhelper.Provisioner

	// Host/OS networking domain (docs/config-coverage.md §4). netKeyfileDir is
	// where the NetworkManager keyfile renderer writes waypoint-*.nmconnection;
	// netGuard runs the confirm-or-revert apply (a bad network change can strand
	// the node, so it is guarded, unlike the radio apply); netConfirmTimeout is the
	// rollback window handed to each apply.
	netKeyfileDir     string
	netConfirmTimeout time.Duration
	netBackend        string // "composite" (NM + keyfile) or "keyfile" (fallback)
	// setupAPIface is excluded from the route check the supervisor and netwatch
	// share: the setup access point's own route is not a way out, and counting it
	// would tell a node that had raised the AP that it was back online.
	setupAPIface  string
	timesyncdConf string // rendered systemd-timesyncd drop-in path (NTP direct apply)
	netGuard      *netconfig.Guard
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
	dmrIDs     string // cached DMR/NXDN id<->callsign table (DMRIds.dat), shared with every gateway

	// The opt-in public dashboard (D1-D8). publicStore is the settings and
	// operator-authored content; publicSvc answers the activity questions. Both are
	// nil in tests that do not exercise the public surface, and the gate treats nil
	// as "not enabled" so a partially-built server 404s rather than panicking.
	publicStore *publicview.Store
	publicSvc   *publicview.Service

	// The phonebook (identity and contact detail for the operators this node
	// knows). Its table is guaranteed by the schema version and it holds no
	// config, so it is attached beside publicStore rather than through it. Nil in
	// tests that do not exercise the surface, and the handlers answer 503 rather
	// than panicking on a partially-built server.
	phonebook *phonebook.Store
	// notifier turns things that happened into messages somebody receives
	// (notify.go). Nil until startNotify runs, and nil on a node with no event
	// store — the queue lives there.
	notifier *notify.Dispatcher
	// identity resolves a station's display name for the AUTHENTICATED dashboard:
	// the phonebook over DMRIds.dat (identity.go). Nil disables decoration
	// entirely, which is exactly what a node with no phonebook gets.
	identity *idresolve.Chain
	// talkerAlias injects DMRA frames naming the caller on network→RF calls
	// (talkeralias.go). Always non-nil so callers need no nil check; it emits
	// nothing until an operator picks a template.
	talkerAlias *taInjector

	// Atomic-update surface (RFC-0014 / issue #13). update holds the manifest URL,
	// release key, and OS seams; updateArgs is the `-update` invocation the apply
	// endpoint launches detached. Both nil/empty disables the update API.
	update     *updateConfig
	updateArgs []string

	// Stack-package updater (RFC-0014 Phase-2 / D2): waypointd-driven apt updates of
	// the waypoint-stack .debs, health-gated with automatic revert. Nil disables the
	// stack update API (e.g. no apt source configured).
	stack *stackUpdater

	// Firmware flashing (#19 / RFC-0019). Nil disables the flash API.
	flash *flasher
	// cal is the calibration engine's controller (#20 / RFC-0021): the running
	// sweep, its progress fan-out, and the transmit tests. It shares hwOps with
	// flashing and detection — all three take the modem away from the node.
	cal *calibrator
	// hwOps serialises everything that takes the modem or the stack away from the
	// node: detection, firmware flashing and stack updates. A flash stops
	// MMDVM-Host for a minute, and a stack update health-gates on MMDVM-Host being
	// up, so the two running together make a good update look like a broken one.
	hwOps hwOps

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

// hostlistStatus serves the supply state of every reflector, master and talkgroup
// list (GET /api/hostlists; #138).
//
// The pickers used to fail silently: a list that never downloaded and a list that
// is genuinely empty both rendered as an empty dropdown, with nothing on screen
// saying which. This is what lets the UI tell an operator that a list is the
// shipped copy, or that every source failed, and when it last succeeded.
func (s *server) hostlistStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.hostlistItems())
}

// hostlistItem is one list's supply state as the API reports it.
type hostlistItem struct {
	hostsrc.Status
	// HasSeed is a property of the build, not of any attempt, so it is attached
	// here rather than tracked as mutable status: it answers "will this list ever
	// show anything while offline?".
	HasSeed bool `json:"has_seed"`
	Stale   bool `json:"stale"`
}

// hostlistItems counts every cache and returns the supply state of each list.
// Counting happens here rather than trusting whatever a picker last parsed: a
// status fetched before any picker has opened would otherwise report zero entries
// for a perfectly good list, and the UI would call it empty.
func (s *server) hostlistItems() []hostlistItem {
	s.countHostlists()
	out := hostsrc.All()
	items := make([]hostlistItem, 0, len(out))
	for _, st := range out {
		items = append(items, hostlistItem{Status: st, HasSeed: hostsrc.HasSeed(st.Name), Stale: st.Stale(48 * time.Hour)})
	}
	return items
}

// hostlistRefresh downloads every reference list now (POST /api/hostlists/refresh).
//
// The scheduled refresh runs daily, which is right for lists that change slowly
// and wrong for the operator who has just fixed whatever was blocking the
// download — a node that booted before its wifi associated, a proxy that is now
// configured, a source that has come back. Without this the only ways to retry
// were to wait out the backoff or restart the daemon, and restarting a hotspot to
// pick up a hostlist takes it off the air to solve a settings-page problem.
//
// It runs the same fetch the scheduler runs, so there is one download path with
// one set of sources and one verification step. The reply is the refreshed supply
// state — the same shape GET returns — so the UI repaints from this one response
// rather than following up with another request.
func (s *server) hostlistRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !hostsrc.Refreshable() {
		// A demo node runs no refreshers at all, so there is nothing to ask for.
		http.Error(w, "this node does not download hostlists", http.StatusNotImplemented)
		return
	}
	// Bounded so a hung source cannot hold the request open indefinitely; generous
	// because this is eight lists over what may be a slow link, and the largest of
	// them (DMRIds.dat) is several megabytes.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	results := hostsrc.RefreshAll(ctx)
	var failed int
	for _, res := range results {
		if !res.OK && !res.Busy {
			failed++
		}
	}
	log.Printf("hostlists: operator-requested refresh finished — %d of %d lists updated", len(results)-failed, len(results))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Results []hostsrc.RefreshResult `json:"results"`
		Lists   []hostlistItem          `json:"lists"`
	}{results, s.hostlistItems()})
}

// countHostlists reparses each cache and records how many entries it holds.
// A parse error leaves the count at zero, which is the honest answer: whatever is
// on disk is not usable, and the status already carries the download error that
// most likely explains it.
func (s *server) countHostlists() {
	if m, err := dmrhosts.Masters(s.dmrHosts); err == nil {
		hostsrc.SetEntries(hostsrc.DMRHosts, len(m))
	}
	if t, err := dmrtg.Talkgroups(s.dmrTGs); err == nil {
		hostsrc.SetEntries(hostsrc.DMRTalkgroups, len(t))
	}
	if r, err := ysfhosts.Reflectors(s.ysfHosts); err == nil {
		hostsrc.SetEntries(hostsrc.YSFHosts, len(r))
	}
	if r, err := p25hosts.Reflectors(s.p25Hosts); err == nil {
		hostsrc.SetEntries(hostsrc.P25Hosts, len(r))
	}
	if r, err := nxdnhosts.Reflectors(s.nxdnHosts); err == nil {
		hostsrc.SetEntries(hostsrc.NXDNHosts, len(r))
	}
	if r, err := m17hosts.Reflectors(s.m17Hosts); err == nil {
		hostsrc.SetEntries(hostsrc.M17Hosts, len(r))
	}
	if r, err := dstarhosts.Reflectors(s.dstarHosts); err == nil {
		hostsrc.SetEntries(hostsrc.DStarHosts, len(r))
	}
	if t, err := dmrids.Load(s.dmrIDs); err == nil {
		hostsrc.SetEntries(hostsrc.DMRIds, t.Len())
	}
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

// dmrIDLookupLimit caps how many rows one lookup returns. It is generous enough
// that a real operator never meets it — the worst callsign in the August 2026
// export carries seventy IDs — and low enough that a picker stays a picker. The
// response says when it bit; the page does not silently show a prefix.
const dmrIDLookupLimit = 100

// dmrIDSearchLimit caps the type-ahead's answer. It is far smaller than
// dmrIDLookupLimit because the two are answering different questions: an exact
// callsign's IDs are a set the operator wants all of, while a prefix's matches
// are a window they are narrowing by typing. Fifty is more than fits on screen,
// so a longer list would only be scrolled past on the way to typing another
// letter — and the ranking (see dmrids.SearchCallsigns) is what makes the top of
// a truncated window the useful part rather than an arbitrary slice.
const dmrIDSearchLimit = 50

// dmrIDLookupResponse is GET /api/dmr/ids?callsign=… and ?prefix=…
type dmrIDLookupResponse struct {
	// Callsign is what was actually looked up: trimmed and upper-cased, and with a
	// portable suffix dropped when that was the only way to find anything. The page
	// shows it, so an operator who typed KN4OQW/P can see it answered on KN4OQW.
	// For a prefix search it echoes the normalized prefix that was searched.
	Callsign string `json:"callsign"`
	// Records is every issued ID behind that callsign, ascending — or, for a prefix
	// search, the ranked matching rows. Never null: an empty list is a legitimate
	// answer and the page renders it as "no match".
	Records []dmrids.Record `json:"records"`
	// Truncated reports that there were more matches than were returned.
	Truncated bool `json:"truncated,omitempty"`
	// Available reports whether the ID table is on disk at all. A node that has
	// never reached the internet has no table, and "no suggestions because there is
	// nothing to search" is a different thing to tell an operator than "your
	// callsign is not in the database" — the first is fixable from the Updates tab,
	// the second means going to radioid.net.
	Available bool `json:"available"`
	// MinPrefix is the shortest prefix a search will answer, sent only on the
	// prefix path. The page needs it to say "keep typing" instead of rendering an
	// empty list as "nobody matches", and carrying it in the answer is what stops
	// the number being written down twice — a copy in the JavaScript would be free
	// to drift from the constant that actually enforces it. Answering a short
	// prefix costs nothing to serve: the scanner returns before it opens the file.
	MinPrefix int `json:"min_prefix,omitempty"`
}

// dmrIDLookup answers "which DMR IDs are issued to this callsign" from the cached
// DMRIds.dat, so the settings page can offer the operator their own ID instead of
// sending them to radioid.net to copy it by hand (#140).
//
// It adds no outbound request. The table it reads is the one dmrids.Run already
// refreshes for the gateways' callsign resolution, under the same operator-visible
// off switch; this is a second reader of a file that was already there.
//
// The callsign is validated to a callsign shape before it reaches the scanner.
// That is not about the scanner — LookupCallsign only ever returns whole rows it
// matched exactly, so no query can make it emit something else — it is about not
// standing up an endpoint that walks a 6.6 MB file for arbitrary input.
//
// It also answers ?prefix=…, the type-ahead behind the callsign pickers. Same
// route rather than a second one because it is the same table, the same reader
// and the same answer shape; a separate endpoint would be a second place to
// decide what a row looks like on the wire, and the two would drift.
func (s *server) dmrIDLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if q := r.URL.Query(); q.Has("prefix") {
		s.dmrIDSearch(w, strings.TrimSpace(q.Get("prefix")))
		return
	}
	cs := strings.TrimSpace(r.URL.Query().Get("callsign"))
	if cs == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "callsign is required"})
		return
	}
	if !callsignShaped(cs) {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "not a callsign: " + cs})
		return
	}
	recs, truncated, err := dmrids.LookupCallsign(s.dmrIDs, cs, dmrIDLookupLimit)
	if err != nil {
		// An unreadable table is the node's problem, not the request's: report it
		// without naming the path, the same rule the public ID-database probe
		// follows.
		log.Printf("dmr id lookup: reading the id table failed: %v", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "the DMR ID table could not be read"})
		return
	}
	answered := strings.ToUpper(cs)
	if len(recs) > 0 {
		answered = recs[0].Callsign // the suffix fallback may have answered on the base call
	}
	if recs == nil {
		recs = []dmrids.Record{}
	}
	writeJSON(w, dmrIDLookupResponse{
		Callsign:  answered,
		Records:   recs,
		Truncated: truncated,
		Available: dmrIDTablePresent(s.dmrIDs),
	})
}

// dmrIDSearch answers the type-ahead: the ranked rows whose callsign starts with
// what has been typed so far.
//
// A prefix below dmrids.SearchPrefixMin is answered, not refused. It is not a bad
// request — it is somebody two letters into typing a callsign, and a 400 there
// would make the page render an error for the normal state of a picker being used
// correctly. The answer carries MinPrefix so the page can say "keep typing", and
// it costs nothing to serve because the scanner returns before opening the file.
//
// Anything that is not callsign-shaped IS refused, for the reason the exact
// lookup refuses it: not because the scanner could be made to emit something
// else, but so that a 6.6 MB file is not walked for arbitrary input. The shape
// check is looser here by exactly one thing — a prefix has no minimum length,
// since a length rule that rejected what somebody had typed so far would be the
// same mistake as the 400 above.
func (s *server) dmrIDSearch(w http.ResponseWriter, prefix string) {
	if !prefixShaped(prefix) {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "not a callsign prefix: " + prefix})
		return
	}
	recs, truncated, err := dmrids.SearchCallsigns(s.dmrIDs, prefix, dmrIDSearchLimit)
	if err != nil {
		// Same rule as the exact lookup: an unreadable table is the node's problem
		// and is reported without naming the path.
		log.Printf("dmr id search: reading the id table failed: %v", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "the DMR ID table could not be read"})
		return
	}
	if recs == nil {
		recs = []dmrids.Record{}
	}
	writeJSON(w, dmrIDLookupResponse{
		Callsign:  strings.ToUpper(prefix),
		Records:   recs,
		Truncated: truncated,
		Available: dmrIDTablePresent(s.dmrIDs),
		MinPrefix: dmrids.SearchPrefixMin,
	})
}

// prefixShaped is callsignShaped without the minimum length: the same letters,
// digits and single optional `/P`-style suffix, bounded the same way at the top,
// but accepting the one and two characters somebody has typed on the way to a
// whole callsign. An empty prefix is shaped — it is what an empty picker sends —
// and SearchCallsigns answers it with nothing.
func prefixShaped(p string) bool {
	base, suffix, hasSuffix := strings.Cut(p, "/")
	if !hasSuffix {
		base, suffix, hasSuffix = strings.Cut(p, "-")
	}
	if !alnum(base) || len(base) > 10 {
		return false
	}
	if hasSuffix && (!alnum(suffix) || len(suffix) > 4) {
		return false
	}
	return true
}

// dmrIDTablePresent reports whether there is an ID table to search. It stats
// rather than parses: the question is "has this node ever downloaded the table",
// and re-reading megabytes to answer it would undo the point of streaming.
func dmrIDTablePresent(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Size() > 0
}

// callsignShaped accepts what an amateur callsign can look like, loosely: letters
// and digits, optionally one `/P`-style portable or `-L` gateway suffix. The
// export's longest callsign is eight characters and its only punctuation is those
// two separators (12 rows out of 310,364), so the bounds are drawn wide of what
// the table actually holds rather than tight to it.
func callsignShaped(cs string) bool {
	base, suffix, hasSuffix := strings.Cut(cs, "/")
	if !hasSuffix {
		base, suffix, hasSuffix = strings.Cut(cs, "-")
	}
	if !alnum(base) || len(base) < 3 || len(base) > 10 {
		return false
	}
	if hasSuffix && (!alnum(suffix) || len(suffix) < 1 || len(suffix) > 4) {
		return false
	}
	return true
}

func alnum(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
			continue
		}
		return false
	}
	return true
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
	_ = json.NewEncoder(w).Encode(m.View(s.viewSources()))
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
	// The notify section carries the SMTP password, the same write-only secret
	// rule: a blank field keeps the stored one (see SetNotify).
	if section == "notify" {
		if err := config.SetNotify(s.store, body, "api"); err != nil {
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
	// The MQTT data plane carries the broker password, the same write-only secret
	// rule (D4): the View never returns it, so a blank field keeps the stored one.
	// SetMQTT also validates the port and the topic prefixes before the store row is
	// written, so an unusable data plane is refused at save rather than discovered
	// when the daemons fail to connect after an Apply.
	if section == "mqtt" {
		if err := config.SetMQTT(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Log levels validate to the 0..6 ladder the pinned daemons accept, so a typo is
	// refused rather than silently atoi()'d to 0 — which would turn a sink OFF, the
	// exact opposite of what an operator asking for more logging wants.
	if section == "logging" {
		if err := config.SetLogging(s.store, body, "api"); err != nil {
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
	// The weather section carries the feed password and a policy that has to be
	// coherent before it can transmit anything, so it is routed through SetWX
	// rather than the generic merge.
	if section == "wx" {
		if err := config.SetWX(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Station coordinates are validated as a pair -- a latitude without a
	// longitude is a half-finished edit, not a position.
	if section == "station_location" {
		if err := config.SetStationLocation(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if section == "history" {
		if err := config.SetHistory(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Station identification validates on save (the interval must be a whole
	// number of minutes in range when identification is enabled), so route it
	// through SetStationID rather than the generic merge.
	if section == "station_id" {
		if err := config.SetStationID(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The modem section validates on save: a board must be one Waypoint knows and
	// an oscillator must be a number, so neither reaches a renderer or a profile
	// fingerprint as nonsense (#18).
	if section == "modem" {
		if err := config.SetModem(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Software-update policy validates on save (channel enum + HH:MM quiet window),
	// so route it through SetUpdate rather than the generic merge (RFC-0014).
	if section == "update" {
		if err := config.SetUpdate(s.store, body, "api"); err != nil {
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
	// Zello bridging: both sections write through their validator so a channel can
	// never persist pointing at a bus or an account that does not exist, or at an
	// account that cannot do what the row asks. Accounts additionally blank-preserve
	// their two secrets — the View never carries them, so a panel saving any other
	// field would otherwise erase the token.
	if section == "zello_accounts" {
		if err := config.SetZelloAccounts(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if section == "zello_channels" {
		if err := config.SetZelloChannels(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Bus LAN peering (RFC-0016): peers carry write-only secrets (the pinned peer
	// cert + this node's peering key), so blank-preserve them like a network
	// password; remote attachments write through the peering validator. Both are
	// refused here rather than persisted invalid.
	if section == "peers" {
		if err := config.SetPeers(s.store, body, "api"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if section == "remote_attachments" {
		if err := config.SetRemoteAttachments(s.store, body, "api"); err != nil {
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
	// The RESOLVED model (D1): the store, with any explicitly-set command-line MQTT
	// flag written over it. Everything below — the INI renderers, the bus configs,
	// the override merge — therefore sees one already-reconciled data plane, and
	// effectivePaths carries the same answer to the bus-config render (D5).
	m, err := s.resolvedModel()
	if err != nil {
		return nil, nil, err
	}
	paths := s.effectivePaths(m)
	// D5: the data plane propagates everywhere or nowhere. Move waypointd's own
	// consumer and status republisher onto the new broker/topic roots BEFORE the
	// daemons are restarted onto them, so the renamed topic has a subscriber waiting
	// rather than a window where the modem publishes into the void and the dashboard
	// reports feed_down. A no-op when the operator changed something else.
	if s.dp != nil {
		s.dp.reconfigure(context.Background(), dataPlaneConfigFrom(m.MQTT))
	}
	targets := m.RenderTargets(paths)
	warnings, err := m.WriteFiles(paths)
	if err != nil {
		return nil, nil, fmt.Errorf("render: %w", err)
	}
	for _, wn := range warnings {
		log.Printf("overrides: %s", wn) // malformed fragment line — surfaced, never silently dropped
	}
	// D5 (RFC-0003 Addendum A §6): sweep rendered bus config files that are no longer
	// registered — a disabled/detached/deleted bus, or a stale file an earlier version
	// left. A config file must never outlive its unit.
	s.sweepStaleBusFiles(targets)

	// Addendum §5: stop-before-start. Everything that must give up a port stops FIRST,
	// so the displacing consumer (a bus that took a gateway's loopback, or a gateway
	// coming back on detach) never contends. The stop is robust to activating/failed
	// units (D5), not only active ones. Units:
	//   - retired MMDVM_CM bridges still lingering from the old surface;
	//   - disabled buses (an enabled bus is a restart target below);
	//   - gateways DISPLACED by a YSF/NXDN bus (dropped from targets — nothing else
	//     stops them, and their port must be free before the bus starts).
	//   - gateways BLOCKED by an unmet startup requirement (a POCSAG enable with no
	//     DAPNET AuthKey): also dropped from targets, and a daemon that would exit
	//     immediately must be stopped rather than left crash-looping from an earlier
	//     apply that had the value set.
	//   - and, since the render set is now gated on the mode enable, every managed
	//     unit the render did not ask for: a mode switched off must stop its daemon,
	//     not leave it running until the next reboot. BootDisableUnits is exactly
	//     that complement, so the stop set and the boot set cannot disagree.
	stopUnits := append(config.RetiredBridgeUnits(), m.DisabledBusUnits()...)
	stopUnits = append(stopUnits, m.BootDisableUnits(paths)...)
	// A DELETED bus's row cannot be enumerated from the model (DisabledBusUnits only
	// sees buses that still exist), so its lingering unit — which still holds the
	// mode's loopback and would make a restored gateway crash-loop — is found via
	// systemd and reconciled here (RFC-0003 Addendum A §6 / D5).
	orphans := s.orphanedBusUnits(m.RegisteredBusUnits())
	stopUnits = append(stopUnits, orphans...)
	stopped = s.stopUnits(stopUnits)

	// With the ports freed, (re)start exactly the rendered target set. A displacing
	// bus is a target here and binds the port the displaced gateway just released.
	restarted, err = s.restartUnits(restartSet(targets))
	if err != nil {
		return nil, nil, fmt.Errorf("restart: %w", err)
	}

	// D2 (Addendum §7): the render is the boot picture. Enable every registered bus
	// instance and every non-displaced gateway so they return after a reboot; disable
	// disabled buses and DISPLACED gateways so, on reboot, a displaced gateway does not
	// race the bus for the mode's loopback. Best-effort — a boot-persistence failure is
	// logged, never fatal to the apply.
	s.enableUnits(m.BootEnableUnits(paths))
	s.disableUnits(append(m.BootDisableUnits(paths), orphans...))

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
	writeJSON(w, map[string]any{"report": report, "preview": m.View(s.viewSources())})
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

// mqttBrokerFor is the broker rendered into a bus config: empty in demo mode (a
// demo node has no broker, so its bus configs carry no MQTT block), else the
// configured broker (D4).
func mqttBrokerFor(broker string, demo bool) string {
	if demo {
		return ""
	}
	return broker
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

// stopUnits stops each unit that is not already inactive or absent — robustly,
// including units in an activating/failed/deactivating state (RFC-0003 Addendum A
// §6 / D5). The prior `is-active --quiet` gate exited non-zero for a crash-looping
// unit (exactly the D3 symptom) and so skipped it; here the state string is
// inspected and anything other than "inactive"/"unknown" is stopped. A stop
// failure is logged and skipped rather than failing the apply — a lingering unit
// that refuses to stop must not block a config change. Returns the units stopped.
func (s *server) stopUnits(units []string) []string {
	seen := map[string]bool{}
	var stopped []string
	for _, u := range units {
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out, _ := systemctlRun("is-active", u) // exit code is non-zero for non-active states; read the word
		switch strings.TrimSpace(string(out)) {
		case "", "inactive", "unknown":
			continue // not running / not installed — nothing to stop
		}
		if out, err := systemctlRun("stop", u); err != nil {
			log.Printf("apply: stop %s: %v: %s", u, err, strings.TrimSpace(string(out)))
			continue
		}
		stopped = append(stopped, u)
	}
	return stopped
}

// orphanedBusUnits returns loaded/enabled waypoint-bus@ instances that are NOT in
// the registered set — a DELETED bus's lingering unit, which the model can no
// longer name (RFC-0003 Addendum A §6 / D5). Found via systemd so a removed bus's
// daemon is stopped and disabled even though its row is gone. Best-effort: if
// systemctl enumeration fails, returns nothing (the apply still converges the rest).
func (s *server) orphanedBusUnits(registered []string) []string {
	reg := map[string]bool{}
	for _, u := range registered {
		reg[u] = true
	}
	seen := map[string]bool{}
	var orphans []string
	scan := func(out []byte) {
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			u := fields[0]
			if strings.HasPrefix(u, "waypoint-bus@") && strings.HasSuffix(u, ".service") && !reg[u] && !seen[u] && u != "waypoint-bus@.service" {
				seen[u] = true
				orphans = append(orphans, u)
			}
		}
	}
	// Loaded instances (running/failed/activating) and installed-but-inactive ones.
	if out, err := systemctlRun("list-units", "--all", "--plain", "--no-legend", "waypoint-bus@*.service"); err == nil {
		scan(out)
	}
	if out, err := systemctlRun("list-unit-files", "--plain", "--no-legend", "waypoint-bus@*.service"); err == nil {
		scan(out)
	}
	return orphans
}

// enableUnits enables each unit for boot (idempotent). Best-effort: a failure is
// logged, never fatal — boot persistence must not block a config apply.
func (s *server) enableUnits(units []string) {
	for _, u := range units {
		if u == "" {
			continue
		}
		if out, err := systemctlRun("enable", u); err != nil {
			log.Printf("apply: enable %s: %v: %s", u, err, strings.TrimSpace(string(out)))
		}
	}
}

// disableUnits disables each unit for boot (idempotent), so a de-registered bus
// does not resurrect on reboot (RFC-0003 Addendum A §7). Best-effort.
func (s *server) disableUnits(units []string) {
	for _, u := range units {
		if u == "" {
			continue
		}
		if out, err := systemctlRun("disable", u); err != nil {
			log.Printf("apply: disable %s: %v: %s", u, err, strings.TrimSpace(string(out)))
		}
	}
}

// sweepStaleBusFiles deletes any waypoint-bus-*.json in the bus config dir that is
// not a currently rendered target (RFC-0003 Addendum A §6 / D5): a disabled,
// detached, or deleted bus's config, or a stale file an earlier version left. A
// config file must never outlive its unit. Best-effort: a remove failure is logged.
func (s *server) sweepStaleBusFiles(targets []config.RenderTarget) {
	dir := s.paths.BusConfigDir
	if dir == "" {
		return
	}
	keep := map[string]bool{}
	for _, t := range targets {
		if filepath.Dir(t.Path) == dir {
			keep[filepath.Base(t.Path)] = true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // dir absent on a node with no buses ever configured — nothing to sweep
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "waypoint-bus-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		if keep[name] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			log.Printf("apply: sweep stale bus config %s: %v", name, err)
		}
	}
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
	// Station identification: a store seeded before this section existed lacks the
	// row, and its zero value is Enable=false with a blank interval — which would
	// render [CW Id] Enable=0 and leave the node silent. Backfill the defaults
	// (identification on, 10 minutes) so an existing node starts identifying on the
	// next apply rather than inheriting a zero value as policy.
	if _, ok, err := s.store.Get("station_id"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("station_id", config.DefaultStationID(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled station ID defaults")
	}
	// Software-update policy (RFC-0014) arrived after the event store: a store
	// seeded before it lacks the row, so backfill the notify-and-click defaults
	// (stable channel, auto-apply off) so Load never returns a zero-value channel.
	if _, ok, err := s.store.Get("update"); err != nil || !ok {
		if err != nil {
			return err
		}
		if err := s.store.Set("update", config.DefaultUpdate(), "backfill"); err != nil {
			return err
		}
		log.Printf("config store: backfilled update defaults")
	}
	// The automatic-check opt-out (#15) arrived after the update policy itself, so a
	// row written before it has no check_enabled key — which would decode as false
	// and quietly stop a node from noticing releases. Seed it on; an operator who
	// already turned it off keeps that.
	wroteCheckEnabled, err := config.BackfillUpdateCheckEnabled(s.store)
	if err != nil {
		return err
	}
	if wroteCheckEnabled {
		log.Printf("config store: backfilled update check_enabled=true")
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
		{"peers", config.DefaultPeers()},
		{"remote_attachments", config.DefaultRemoteAttachments()},
		{"peering", config.DefaultPeering()},
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
	// SetupAP is the setup access point's state, or null once the node is
	// provisioned and the AP is gone.
	SetupAP *captive.Status `json:"setup_ap,omitempty"`
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
		// Health is reachable in every state, including from the setup access
		// point, so it is the one endpoint that can answer "is the AP up, and why
		// did it go away" while the node is still unprovisioned.
		SetupAP: s.setupAPStatus(),
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
		b, err := json.Marshal(s.decorateEvent(e))
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
	// Decorated on the way out, not on the way in (identity.go): the stored event
	// is untouched, and a corrected phonebook name fixes the history too.
	_ = json.NewEncoder(w).Encode(s.decorateEvents(evs))
}

// overridesView serves GET /api/overrides: the override records that shape the
// current store's render — what the next Apply will actually write (RFC-0005 /
// issue #2). Read-only (overrides are edited on disk in v1); behind the same
// session wall as every config route. The response names the override root so the
// UI can tell the operator where fragments live, and surfaces any malformed-line
// warnings rather than dropping them silently.
func (s *server) overridesView(w http.ResponseWriter, _ *http.Request) {
	m, err := s.resolvedModel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	applied, warnings, err := m.Overrides(s.effectivePaths(m))
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
	for _, md := range m.View(config.Sources{}).Modes {
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
	s.messagesRoutes(mux)                        // text messages (messages.go)
	s.weatherRoutes(mux)                         // weather alerts (weather.go)
	mux.HandleFunc("/api/overrides", s.overridesView)
	mux.HandleFunc("/api/buses/validate", s.busesValidate)     // dry-run attach validator (RFC-0003 §2)
	mux.HandleFunc("/api/buses/migrate", s.busesMigrate)       // seed buses from the dormant bridges (§4)
	mux.HandleFunc("/api/peering/discover", s.peeringDiscover) // RFC-0016 pairing (§3)
	mux.HandleFunc("/api/peering/initiate", s.peeringInitiate)
	mux.HandleFunc("/api/peering/confirm", s.peeringConfirm)
	mux.HandleFunc("/api/peering/cancel", s.peeringCancel)
	mux.HandleFunc("/api/peering/pending", s.peeringPending)
	mux.HandleFunc("/api/peering/peers", s.peeringPeers)
	mux.HandleFunc("/api/peering/revoke", s.peeringRevoke)
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
	mux.HandleFunc("/api/hostlists", s.hostlistStatus)
	mux.HandleFunc("/api/hostlists/refresh", s.hostlistRefresh)
	mux.HandleFunc("/api/dmr/talkgroups", s.dmrTalkgroups) // TG name list (RFC-0010)
	mux.HandleFunc("/api/dmr/ids", s.dmrIDLookup)          // callsign -> issued DMR IDs (#140)
	// Modem hardware: identity, detection, and adoption into the config (#18).
	mux.HandleFunc("/api/hardware", s.hardware)
	mux.HandleFunc("/api/hardware/detect", s.hardwareDetect)
	mux.HandleFunc("/api/hardware/adopt", s.hardwareAdopt)
	mux.HandleFunc("/api/hardware/uart", s.hardwareUART) // free the GPIO serial port
	// The character panel's half of the same split (#136): look for a display, and
	// write what was found into the lcd section on the operator's word.
	mux.HandleFunc("/api/lcd/detect", s.lcdDetect)
	mux.HandleFunc("/api/lcd/adopt", s.lcdAdopt)
	// Firmware flashing (#19 / RFC-0019). Byte-level progress is its own SSE
	// stream rather than hub events: the hub is persisted, and a progress bar is
	// not worth five hundred rows on an SD card.
	mux.HandleFunc("/api/cal", s.calStatus)
	mux.HandleFunc("/api/cal/sweep", s.calSweep)
	mux.HandleFunc("/api/cal/cancel", s.calCancel)
	mux.HandleFunc("/api/cal/events", s.calEvents)
	mux.HandleFunc("/api/cal/apply", s.calApply)
	mux.HandleFunc("/api/cal/transmit", s.calTransmit)
	mux.HandleFunc("/api/cal/listen", s.calListen)
	mux.HandleFunc("/api/flash", s.flashRoot)
	mux.HandleFunc("/api/flash/catalog", s.flashCatalogRefresh)
	mux.HandleFunc("/api/flash/events", s.flashEvents)
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
	// Stack-package update surface (RFC-0014 Phase-2 / D2): status, on-demand check,
	// and health-gated apply. Behind the session wall like the binary update routes.
	mux.HandleFunc("/api/update/stack", s.stackStatus)
	mux.HandleFunc("/api/update/stack/check", s.stackCheck)
	mux.HandleFunc("/api/update/stack/apply", s.stackApply)
	// First-boot claim + session endpoints (RFC-0002). These are the only routes
	// the gate serves before authentication (claim while unclaimed, session while
	// claimed); everything else above is behind the wall.
	mux.HandleFunc("/api/claim", s.auth.HandleClaim)
	mux.HandleFunc("/api/session", s.auth.HandleSession)
	// The opt-in public dashboard (D2/D7). These are the only anonymous routes
	// besides the two above; each 404s unless the operator enabled the feature, so
	// registering them costs a disabled node nothing but a not-found.
	s.registerPublicRoutes(mux)
	s.registerBrandingRoutes(mux)
	s.registerAdminMapRoutes(mux)
	s.registerPublicPanelRoutes(mux)
	// The phonebook (phonebook.go). Behind the session wall like every other
	// route above: its entries carry email addresses, and the gate's default-deny
	// is what keeps them off an anonymous response.
	s.registerPhonebookRoutes(mux)
	// Accounts, whoami and self-service password change (accounts.go). The first
	// is admin-only by the role mapping; the other two are reachable by every
	// authenticated account, and /api/password is the one route a must-rotate
	// account may use.
	s.registerAccountRoutes(mux)
	// Notification test-send and the parked list (notify.go).
	s.registerNotifyRoutes(mux)
	mux.Handle("/", s.rootHandler(http.FileServerFS(ui.FS())))
	return mux
}

// rootHandler decides what a bare "/" is (D7).
//
// With the public view enabled it is the node's public page for a visitor with no
// session, and the dashboard for a keeper who has one. With it disabled — the
// default — it is the dashboard, and an unauthenticated request never reaches
// here at all because the gate answered it with the login screen.
//
// "/index.html" is deliberately not covered: it is the admin entry that is never
// swapped, so enabling the public view can never leave an operator without a URL
// that reaches their own login screen. See serveRootPublicly.
func (s *server) rootHandler(app http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && s.publicRootEnabled() &&
			(s.auth == nil || !s.auth.HasSession(r)) {
			s.serveRootPublicly(w, r)
			return
		}
		app.ServeHTTP(w, r)
	})
}

// runWatchdog answers systemd's watchdog for as long as the daemon is healthy.
//
// "Healthy" is deliberately more than "this goroutine is scheduled". The ping is
// gated on the event hub answering, because a daemon whose hub has deadlocked is
// exactly the failure the watchdog exists to catch, and a ping that only proves
// the timer fired would keep such a node alive indefinitely.
func runWatchdog(ctx context.Context, s *server) {
	interval := sdnotify.WatchdogInterval()
	if err := sdnotify.Ready(); err != nil {
		log.Printf("waypointd: could not notify systemd: %v", err)
	}
	if interval <= 0 {
		return // no watchdog configured, or not running under systemd
	}
	log.Printf("waypointd: answering the systemd watchdog every %s", interval)

	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !s.healthy() {
				// Deliberately silent on the socket: withholding the ping is how the
				// watchdog is told, and systemd will restart us. The log line is for
				// whoever reads the journal afterwards.
				log.Printf("waypointd: WEDGED — withholding the watchdog ping so systemd restarts this daemon")
				continue
			}
			if err := sdnotify.Watchdog(); err != nil {
				log.Printf("waypointd: watchdog ping failed: %v", err)
			}
		}
	}
}

// healthy reports whether the daemon is doing its job, not merely running. It is
// what the watchdog ping is gated on.
func (s *server) healthy() bool {
	// The hub is the daemon's spine: every event, status frame, and WebSocket
	// message goes through it. If it does not answer, nothing else works either,
	// however alive the process looks from outside.
	return s.hub != nil && s.hub.Alive()
}

func main() {
	// Subcommands are dispatched before flag parsing: `waypointd reset-claim`
	// connects to the store directly and returns the device to claim mode, for an
	// operator with a shell on the box (RFC-0002 "Reset procedure (a)").
	if len(os.Args) > 1 && os.Args[1] == "reset-claim" {
		os.Exit(runResetClaim(os.Args[2:]))
	}
	// `waypointd reset-peer-identity` regenerates the node's peering keypair and
	// revokes every pairing (RFC-0016 §3, amended) — the whole-mesh reset for a
	// compromised node key or a re-homed box, same shell-on-device lineage.
	if len(os.Args) > 1 && os.Args[1] == "reset-peer-identity" {
		os.Exit(runResetPeerIdentity(os.Args[2:]))
	}

	addr := flag.String("addr", "127.0.0.1:8073", "HTTPS listen address for the API and UI (plaintext when -tls=false)")
	useTLS := flag.Bool("tls", true, "serve HTTPS with a self-signed device cert (RFC-0012); set false only behind a TLS-terminating proxy")
	tlsDir := flag.String("tls-dir", paths.TLSDir, "directory holding the self-signed device cert/key (minted on first start)")
	httpRedirectAddr := flag.String("http-redirect-addr", "", "optional HTTP listener that 301-redirects to HTTPS, e.g. :80 (empty disables it)")
	acmeDomain := flag.String("acme-domain", "", "public hostname for a Let's Encrypt cert instead of self-signed (requires :80 + :443 reachable)")
	acmeEmail := flag.String("acme-email", "", "contact email for the Let's Encrypt account (optional)")
	acmeDir := flag.String("acme-dir", paths.ACMEDir, "cache directory for Let's Encrypt certificates")
	demoMode := flag.Bool("demo", false, "publish synthetic traffic (no radio required); always labeled in /api/health")
	broker := flag.String("mqtt-broker", "127.0.0.1:1883", "MMDVM-Host MQTT broker host:port (live mode)")
	mqttName := flag.String("mqtt-name", "mmdvm", "MMDVM-Host [MQTT] Name (topic prefix)")
	statusPrefix := flag.String("status-topic-prefix", "waypoint/status", "MQTT prefix for the normalized status republish (RFC-0008)")
	busTopicPrefix := flag.String("bus-topic-prefix", config.DefaultBusTopicPrefix, "MQTT prefix for mode-bus events (D4); the bus daemons publish, waypointd consumes")
	haDiscovery := flag.Bool("ha-discovery", true, "publish Home Assistant MQTT discovery so entities appear with zero YAML (RFC-0011)")
	haPrefix := flag.String("ha-discovery-prefix", "homeassistant", "Home Assistant MQTT discovery prefix")
	nodeID := flag.String("node-id", defaultNodeID(), "device id + MQTT node segment for HA discovery (stable across restarts)")
	statusTick := flag.Duration("status-watchdog-tick", time.Second, "how often the status aggregator runs its stranded-transmission watchdog")
	probeInterval := flag.Duration("gateway-probe-interval", time.Second, "how often the supervisor probes gateway liveness (RFC-0008; keep < 2s for the #5 acceptance)")
	linkTTL := flag.Duration("link-ttl", status.DefaultLinkTTL, "how long a CONFIRMED network link stays claimed without a fresh confirmation; 0 disables. Only links something re-checks are subject to it (#22)")
	supervisorInterval := flag.Duration("supervisor-interval", supervisor.DefaultInterval, "how often the resilience supervisor evaluates each upstream attachment (#22)")
	supervisorRemediate := flag.Bool("supervisor-remediate", true, "let the resilience supervisor restart a gateway that has lost its upstream link; false observes and reports only (#22)")
	supervisorMaxRestarts := flag.Int("supervisor-max-restarts", supervisor.DefaultMaxRestarts, "global backstop: most gateway restarts the supervisor may perform inside -supervisor-restart-window")
	supervisorRestartWindow := flag.Duration("supervisor-restart-window", supervisor.DefaultRestartWindow, "the window -supervisor-max-restarts is counted over")
	// Distinct from the two above: those bound the restarts WAYPOINT performs.
	// These two watch the restarts SYSTEMD performs, which no budget of ours ever
	// sees — the failure mode where a daemon exits on its own and is restarted
	// forever while every health surface reads green.
	crashLoopThreshold := flag.Int("crashloop-threshold", supervisor.DefaultCrashLoopThreshold, "automatic systemd restarts inside -crashloop-window before a gateway is reported as crash-looping")
	crashLoopWindow := flag.Duration("crashloop-window", supervisor.DefaultCrashLoopWindow, "the window -crashloop-threshold is counted over")
	mqttUser := flag.String("mqtt-user", "", "MQTT username (optional)")
	mqttPass := flag.String("mqtt-pass", "", "MQTT password (optional)")
	mmdvmINI := flag.String("mmdvm-ini", paths.EtcDir+"/MMDVM-Host.ini", "MMDVM-Host.ini render target (the file the daemon reads)")
	dmrgwINI := flag.String("dmrgateway-ini", paths.EtcDir+"/DMRGateway.ini", "DMRGateway.ini render target")
	ysfgwINI := flag.String("ysfgateway-ini", paths.EtcDir+"/YSFGateway.ini", "YSFGateway.ini render target")
	dgidgwINI := flag.String("dgidgateway-ini", paths.EtcDir+"/DGIdGateway.ini", "DGIdGateway.ini render target (used when DG-ID gateway is enabled)")
	p25gwINI := flag.String("p25gateway-ini", paths.EtcDir+"/P25Gateway.ini", "P25Gateway.ini render target")
	nxdngwINI := flag.String("nxdngateway-ini", paths.EtcDir+"/NXDNGateway.ini", "NXDNGateway.ini render target")
	dstargwINI := flag.String("dstargateway-ini", paths.EtcDir+"/dstargateway.cfg", "dstargateway.cfg render target")
	m17gwINI := flag.String("m17gateway-ini", paths.EtcDir+"/M17Gateway.ini", "M17Gateway.ini render target")
	dapnetgwINI := flag.String("dapnetgateway-ini", paths.EtcDir+"/DAPNETGateway.ini", "DAPNETGateway.ini render target (POCSAG paging gateway)")
	overridesDir := flag.String("overrides-dir", paths.OverridesDir, "root of operator override drop-ins: <dir>/<daemon>.d/*.conf merge last into each rendered INI (RFC-0005 / issue #2)")
	// The cross-mode bridge render-target flags (ysf2dmr-ini … nxdn2dmr-ini) are
	// retired with the per-bridge-daemon model (RFC-0003 bus architecture). No bridge
	// INI is rendered any more; apply stops any bridge daemon still running instead.
	ysfHosts := flag.String("ysf-hosts", paths.EtcDir+"/YSFHosts.json", "cached YSF reflector hostlist path")
	ysfHostsURL := flag.String("ysf-hosts-url", ysfhosts.DefaultURL, "YSF reflector hostlist source URL (comma-separated; tried in order)")
	p25Hosts := flag.String("p25-hosts", paths.EtcDir+"/P25Hosts.json", "cached P25 reflector hostlist path")
	p25HostsURL := flag.String("p25-hosts-url", p25hosts.DefaultURL, "P25 reflector hostlist source URL (comma-separated; tried in order)")
	nxdnHosts := flag.String("nxdn-hosts", paths.EtcDir+"/NXDNHosts.json", "cached NXDN reflector hostlist path")
	nxdnHostsURL := flag.String("nxdn-hosts-url", nxdnhosts.DefaultURL, "NXDN reflector hostlist source URL (comma-separated; tried in order)")
	// The D-Star cache path is the DStar_Hosts.json inside the gateway's HostsFiles
	// directory — the gateway reads it there directly (no separate copy).
	dstarHosts := flag.String("dstar-hosts", paths.EtcDir+"/DStar_Hosts.json", "cached D-Star reflector hostlist path")
	dstarHostsURL := flag.String("dstar-hosts-url", dstarhosts.DefaultURL, "D-Star reflector hostlist source URL (comma-separated; tried in order)")
	m17Hosts := flag.String("m17-hosts", paths.EtcDir+"/M17Hosts.txt", "cached M17 reflector hostlist path")
	m17HostsURL := flag.String("m17-hosts-url", m17hosts.DefaultURL, "M17 reflector hostlist source URL (comma-separated; tried in order)")
	dmrHosts := flag.String("dmr-hosts", "/usr/local/etc/DMR_Hosts.txt", "cached DMR master hostlist path (DMR_Hosts.txt)")
	dmrHostsURL := flag.String("dmr-hosts-url", dmrhosts.DefaultURL, "DMR master hostlist source URL (comma-separated; tried in order)")
	dmrTGs := flag.String("dmr-talkgroups", paths.EtcDir+"/TGList.txt", "cached DMR talkgroup-name list path (RFC-0010)")
	dmrTGsURL := flag.String("dmr-talkgroups-url", dmrtg.DefaultURL, "DMR talkgroup-name list source URL (comma-separated; tried in order)")
	dmrIDs := flag.String("dmr-ids", dmrids.DefaultPath, "cached DMR/NXDN id<->callsign table (DMRIds.dat); the path every rendered gateway config points at")
	dmrIDsURL := flag.String("dmr-ids-url", dmrids.DefaultURL, "DMR id<->callsign table source URL (comma-separated; tried in order)")
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
	firmwareURL := flag.String("firmware-url", defaultFirmwareURL, "signed modem-firmware catalog URL (RFC-0019)")
	firmwareCache := flag.String("firmware-cache", "", "directory for verified firmware images; empty = alongside the store")
	updateBinary := flag.String("update-binary", paths.Binary, "path to the live waypointd binary the update swaps atomically")
	updateUnit := flag.String("update-unit", "waypointd.service", "systemd unit the updater restarts")
	updateMarker := flag.String("update-marker", paths.UpdateMarker, "in-flight-update marker path (power-loss recovery)")
	stackApplyMode := flag.Bool("update-stack", false, "apply available waypoint-stack .deb updates (health-gated, auto-revert) and exit (RFC-0014 / D2)")
	stackCheckMode := flag.Bool("update-stack-check", false, "report available waypoint-stack .deb updates and exit; changes nothing")
	aptSourceFile := flag.String("apt-source-file", "/etc/apt/sources.list.d/waypoint.sources", "deb822 source for the signed Waypoint apt repo; the stack check limits apt to it (D2)")
	updatePollInterval := flag.Duration("update-poll-interval", 6*time.Hour, "how often waypointd checks for stack/binary updates and evaluates quiet-window auto-apply (RFC-0014)")
	busConfigDir := flag.String("bus-config-dir", paths.EtcDir, "directory for rendered mode-bus configs (waypoint-bus-<id>.json), consumed by waypoint-bus@<id>.service (RFC-0003)")
	peeringDir := flag.String("peering-dir", paths.PeeringDir, "directory holding LAN-peering cert/key files (node.key, peer-*.crt) referenced by rendered bus peering blocks (RFC-0016)")
	peeringBootstrapAddr := flag.String("peering-bootstrap-addr", "0.0.0.0:42501", "listen address for the RFC-0016 pairing bootstrap channel (plain TCP; the short code authenticates the exchange)")
	storePath := flag.String("store", paths.StorePath, "path to the SQLite configuration store")
	// First-boot setup (docs/provisioning.md). The wizard is on by default: a node
	// flashed with dd has no hostname the operator chose, no account, and an
	// unlocked root, and Raspberry Pi Imager 2.x no longer offers to seed any of
	// them for a local image.
	setupWizard := flag.Bool("setup-wizard", true, "serve the first-boot setup wizard until the node is provisioned")
	provisionSocket := flag.String("provision-socket", privhelper.DefaultSocketPath, "Unix socket of the privileged provisioning helper")
	provisionMarker := flag.String("provision-marker", provision.DefaultPath, "path to the provisioned marker written when setup completes")
	setupProgress := flag.String("setup-progress", wizard.DefaultProgressPath, "path to the in-flight setup progress file")
	// The power-user fast path: a TOML on the boot partition provisions the node
	// without anyone touching the wizard, and without an access point being raised.
	seedPaths := flag.String("provision-seed", strings.Join(seed.DefaultPaths, ","),
		"comma-separated boot-partition provisioning files, searched in order (empty disables the fast path)")
	// The setup access point is how a node with no other network is reachable at
	// all. It is open by default (see internal/setupap) and comes down on a
	// network join, on setup completion, or after the window below with nobody
	// associated.
	setupAP := flag.Bool("setup-ap", true, "raise the setup access point while the node is unprovisioned")
	setupAPIface := flag.String("setup-ap-interface", "", "wireless interface for the setup access point (empty picks the first)")
	setupAPCountry := flag.String("setup-ap-country", "", "regulatory domain for the setup access point, e.g. US")
	setupAPWindow := flag.Duration("setup-ap-window", captive.DefaultAssociateWindow, "how long the setup access point waits for a client before coming down until the next boot")
	setupSessionIdle := flag.Duration("setup-session-idle", captive.DefaultIdleTimeout, "how long a setup session survives with no request from the holding device")
	// netwatch is the way back into a node that was set up months ago and has
	// since lost the network it was set up on — a replaced router, a renamed SSID,
	// a node carried somewhere else. It re-raises the setup access point and
	// touches no provisioning state.
	netwatchGrace := flag.Duration("netwatch-grace", netwatch.DefaultGrace, "how long the node tolerates having no route out before the setup access point comes back")
	netwatchInterval := flag.Duration("netwatch-interval", netwatch.DefaultInterval, "how often the route table is checked for a way out")
	eventsPath := flag.String("events-store", paths.EventsPath, "path to the SQLite event-history store (RFC-0004); a config.db sibling")
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

	// Relocate a pre-0.3 state tree before anything opens a path under it. This
	// runs on every invocation, including the ExecStartPre boot check, because
	// waypointd is the only component that reaches an installed node — its units
	// still name the old location and nothing can rewrite them (see
	// paths.Migrate). It is a no-op on an already-migrated or freshly flashed
	// node.
	//
	// Fatal on failure, and deliberately so: the failure modes are a conflict
	// nothing Waypoint does can produce, or a filesystem that would not let the
	// tree move. Carrying on would mean opening the compiled-in paths while the
	// operator's configuration sits somewhere else — a configured node silently
	// coming up on an empty store. Failing here puts the reason in the journal
	// with the node's data untouched.
	if mig, err := paths.Migrate(); err != nil {
		log.Fatalf("state migration: %v", err)
	} else if mig.Performed {
		log.Printf("state migration: %s", mig)
	}

	// The update modes (RFC-0014) each run as a standalone invocation and exit,
	// before any daemon startup: -update does the transactional install (surviving
	// the service restart it triggers), -update-check reports availability, and
	// -update-boot-check is the ExecStartPre power-loss revert.
	if *updateMode || *updateCheckMode || *updateBootCheck {
		cfg := newUpdateConfig(*updateURL, *releasePubkey, *updateBinary, *updateUnit, *updateMarker, *storePath, *addr, *useTLS)
		switch {
		case *updateBootCheck:
			runUpdateBootCheck(cfg)
		case *updateCheckMode:
			runUpdateCheck(cfg)
		default:
			runUpdate(cfg)
		}
	}

	// The stack update modes (RFC-0014 Phase-2 / D2) likewise run standalone and
	// exit: -update-stack-check reports available .deb updates, -update-stack applies
	// them health-gated with automatic revert. Both drive apt, limited to the
	// Waypoint source.
	if *stackApplyMode || *stackCheckMode {
		if *stackCheckMode {
			runStackCheck(*storePath, *aptSourceFile)
		}
		runStackApply(*storePath, *aptSourceFile)
	}

	st, err := store.Open(*storePath)
	if err != nil {
		log.Fatalf("config store: %v", err)
	}
	defer st.Close()

	// A schema migration just ran. If an update is in flight, tell its marker where
	// the pre-migration copy is: this start is the new version's first, and if it
	// never becomes healthy the revert (here or at the next boot-check) has to put
	// the store back too, or the restored older binary meets a schema it refuses to
	// open. This is the only moment that knows both facts.
	if from, backup, ok := st.Migrated(); ok {
		log.Printf("config store: migrated schema v%d → v%d (pre-migration copy: %s)", from, store.SchemaVersion, backup)
		if err := updater.RecordStoreBackup(*updateMarker, backup); err != nil {
			log.Printf("config store: could not record the pre-migration copy on the in-flight update marker: %v", err)
		}
	}

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
		agg:   status.New(status.DefaultTxTTL, *linkTTL),
		relay: &dmrRelay{},
		paths: config.Paths{
			MMDVM: *mmdvmINI, DMRGateway: *dmrgwINI, YSFGateway: *ysfgwINI, DGIdGateway: *dgidgwINI,
			P25Gateway: *p25gwINI, NXDNGateway: *nxdngwINI, DStarGateway: *dstargwINI, M17Gateway: *m17gwINI,
			DAPNETGateway: *dapnetgwINI,
			BusConfigDir:  *busConfigDir,
			PeeringDir:    *peeringDir,
			// D4: the broker each bus daemon publishes its events to, rendered into the
			// bus config. Empty in demo mode (no broker), so demo bus configs carry no
			// MQTT block and the daemon runs without publishing.
			MQTTBroker:     mqttBrokerFor(*broker, *demoMode),
			BusTopicPrefix: *busTopicPrefix,
			// Demo mode must never pick up a real node's overrides: point the layer at an
			// empty path so the render is emitted verbatim (RFC-0005).
			OverridesDir: overridesRoot(*overridesDir, *demoMode),
		},
		ysfHosts: *ysfHosts, p25Hosts: *p25Hosts, nxdnHosts: *nxdnHosts, dstarHosts: *dstarHosts, m17Hosts: *m17Hosts, dmrHosts: *dmrHosts, dmrTGs: *dmrTGs, dmrIDs: *dmrIDs,
		netKeyfileDir: *nmKeyfileDir, netConfirmTimeout: *netConfirmTimeout, netBackend: *netBackend,
		timesyncdConf: *timesyncdConf, setupAPIface: *setupAPIface,
		// D1: which MQTT flags the operator actually typed, so an explicit one can
		// shadow the store (with a logged warning) and an untouched one cannot.
		mqttFlags: newMQTTFlags(*broker, *mqttName, *mqttUser, *mqttPass, *statusPrefix, *busTopicPrefix),
		// Deployment-owned, displayed read-only on the System tab (#29 amendment).
		listenAddr: *addr,
	}
	// The confirm-or-revert guard for network applies (needs s.netKeyfileDir set).
	s.netGuard = s.newNetGuard()

	// Atomic-update surface (RFC-0014). The API endpoints reuse the same config the
	// CLI modes build; updateArgs is the detached `-update` invocation apply launches,
	// carrying the same manifest/key/seam flags so the child behaves identically.
	updCfg := newUpdateConfig(*updateURL, *releasePubkey, *updateBinary, *updateUnit, *updateMarker, *storePath, *addr, *useTLS)
	s.update = &updCfg

	// Firmware flashing (RFC-0019). It shares the release key with the updater —
	// the same key signs waypointd and the firmware images — but a separate
	// catalog URL, so a firmware release ships without a software release.
	s.flash = newFlasher(s.hub, s.store, *firmwareURL, updCfg.pubKey, updCfg.hasPubKey,
		firmwareCacheDir(*firmwareCache, *storePath))

	// Guided calibration (RFC-0021). It needs nothing configured to exist —
	// every precondition it has is reported through GET /api/cal rather than
	// deciding whether the surface is there at all.
	s.cal = newCalibrator(s.hub, s.store)
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
	// Stack-package updater (D2). A construction failure (e.g. no store table) only
	// disables the stack update API; the daemon still serves everything else.
	if su, err := newStackUpdater(st, *aptSourceFile); err != nil {
		log.Printf("stack updater disabled: %v", err)
	} else {
		s.stack = su
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
	s.auth, s.authStore, err = buildAuth(st, secureCookieOn, resetPaths{Marker: *provisionMarker, Progress: *setupProgress})
	if err != nil {
		log.Fatalf("auth: %v", err)
	}

	// The opt-in public dashboard (D1-D8). The store's tables are guaranteed by
	// the schema version, so there is nothing to create here; the service is handed
	// the same event history and status fold the authenticated API reads, and its
	// own far narrower projection of them.
	//
	// The ID-database probe is what lets the public last-heard list withhold itself
	// when DMRIds.dat is missing or corrupt, rather than quietly serving only the
	// D-Star and YSF traffic that resolves without it.
	s.publicStore = publicview.New(st)
	// The phonebook needs nothing but the store handle: no history, no live
	// status, no service in front of it. It is attached here so the surface exists
	// the moment the daemon serves, rather than being built per request.
	s.phonebook = phonebook.New(st)
	// The dashboard's display-name chain over that same phonebook (identity.go).
	s.identity = identityChain(s.phonebook)
	s.talkerAlias = &taInjector{}
	// Assigned through the interfaces rather than passed directly, so a build
	// without one of them hands the service a genuinely nil interface instead of a
	// non-nil one wrapping a nil pointer.
	var pubHistory publicview.History
	if s.evStore != nil {
		pubHistory = s.evStore
	}
	var pubLive publicview.Live
	if s.agg != nil {
		pubLive = s.agg
	}
	s.publicSvc = publicview.NewService(s.publicStore, pubHistory, pubLive).
		WithIDDatabase(publicview.DMRIDsProbe(*dmrIDs)).
		// The same chain the dashboard uses, but reached through an interface whose
		// only method returns a callsign (D3): the public page may publish that an
		// operator was heard, never the name or address the phonebook holds for
		// them. Passing s.identity here rather than a second chain is deliberate —
		// two chains could drift into disagreeing about who a station is.
		WithResolver(publicResolver(s.identity))
	// Anonymous by design, and the only routes that are. What they reach still
	// 404s unless the operator opted in (public.go).
	//
	// "/" joins them only while the public view is on, so the gate stops turning it
	// into a login screen and rootHandler can answer it per-request. "/index.html"
	// is never on this list: it is the admin entry that must keep reaching the
	// login screen whatever the public toggle says.
	// Bounded retention of other people's location data (D3). Hourly rather than
	// at startup only, because a node that runs for weeks would otherwise never
	// drop anything after its first minute.
	go runPositionPrune(s.publicStore, time.Hour, context.Background().Done())
	s.auth.AllowAnonymous(func(r *http.Request) bool {
		if IsPublicRoute(r.URL.Path) {
			return true
		}
		return r.URL.Path == "/" && r.Method == http.MethodGet && s.publicRootEnabled()
	})

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
	s.certs = tlscert.NewHolder(*tlsDir)
	s.certs.Logf = log.Printf
	// Demo mode is a dashboard with synthetic traffic and no radio — a laptop, a
	// CI runner, a screenshot. Gating it behind first-boot setup would mean the one
	// mode whose entire purpose is showing the dashboard showing a setup wizard
	// instead, and raising an access point on a machine that is not a node.
	if *demoMode && (*setupWizard || *setupAP) {
		log.Printf("waypointd: demo mode — first-boot setup and the setup access point are off")
		*setupWizard, *setupAP = false, false
	}

	// The helper client is built whether or not first-boot setup is enabled: the
	// modem-UART repair (#18) needs it on a node that finished setup long ago.
	s.prov = privhelper.NewClient(*provisionSocket)
	s.initSetup(setupOptions{
		Enabled:   *setupWizard,
		Socket:    *provisionSocket,
		Marker:    *provisionMarker,
		Progress:  *setupProgress,
		SeedPaths: strings.Split(*seedPaths, ","),
	}, st)
	s.initSetupAP(context.Background(), apOptions{
		Enabled:          *setupAP,
		Interface:        *setupAPIface,
		Country:          *setupAPCountry,
		Window:           *setupAPWindow,
		IdleTimeout:      *setupSessionIdle,
		NetwatchGrace:    *netwatchGrace,
		NetwatchInterval: *netwatchInterval,
	})
	s.initPeering(context.Background(), *peeringDir, *peeringBootstrapAddr)

	// Look for a panel the operator has wired but never declared (#136). After
	// initSetupAP, because the unprovisioned node's display belongs to the setup
	// screen and this stands down for it.
	if m, err := config.Load(s.store); err == nil {
		s.probeForPanel(m)
	}

	if *demoMode {
		go demo.Run(context.Background(), s.hub)
	} else {
		// The MQTT data plane is store-owned (#29): the consumer's topic roots, the
		// broker and the credentials come from the RESOLVED model (store, with any
		// explicitly-set flag written over it — D1), and applyRender reconfigures the
		// live connections when the operator changes them, so renaming the modem's
		// topic root in the System tab does not take the dashboard down until the next
		// daemon restart.
		dpModel, err := s.resolvedModel()
		if err != nil {
			log.Fatalf("mqtt: config load: %v", err)
		}
		logShadowed(s.mqttFlags.resolve(dpModel))
		// Supervisor liveness probe: emits gateway_up/gateway_down so a killed or
		// restarted gateway shows truth within the probe interval — the #5 acceptance,
		// from systemd state (not log scraping). Live mode only (a demo runs no gateways).
		go s.runLivenessProbe(context.Background(), *probeInterval, &supervisor.RestartWatch{
			Threshold: *crashLoopThreshold,
			Window:    *crashLoopWindow,
		})
		// Network resilience (#22): watch every upstream attachment the config
		// declares, keep the node honest about them, and restart a gateway that has
		// lost one and cannot get it back on its own. Live mode only — a demo node
		// has no masters to lose.
		go s.runSupervisor(context.Background(), supervisorOptions{
			Interval:      *supervisorInterval,
			Remediate:     *supervisorRemediate,
			MaxRestarts:   *supervisorMaxRestarts,
			RestartWindow: *supervisorRestartWindow,
			// Read per cycle, not captured: the data plane rebuilds this connection
			// when the store's broker moves (#29), and the supervisor has to follow.
			//
			// A closure, not the s.dp.commander method value: s.dp is assigned below
			// this call, and a method value binds its receiver where it is written.
			// Taking it here captured a nil *dataPlane for the life of the process, so
			// commander() returned nil on every cycle, the DMR link poll never ran, and
			// every attachment sat at "no evidence" forever while the status surface
			// reported it healthy. The closure resolves s.dp when the supervisor
			// actually asks, which is what the line above always meant.
			Commander: func() *mqtt.Commander { return s.dp.commander() },
		})
		// The DMR loopback relay (dmrshim.go). Live mode only: a demo node runs no
		// MMDVM-Host to sit between. It reconciles itself against the store on a
		// tick, so a node with the feature switched off — the default — starts it,
		// finds nothing to do, and costs one config read every fifteen seconds.
		go s.runDMRRelay(context.Background(), dmrRelayInterval)
		// The message service transmits through that relay, so it starts after it.
		// It costs a goroutine parked on an empty queue when nobody sends anything.
		s.startMessages(context.Background())
		// The notifier drains its queue on its own goroutine and subscribes to the
		// hub for the events worth telling somebody about. It starts after the
		// message service because an inbound message is one of those events, and
		// costs a goroutine parked on an empty queue on a node that notifies nobody.
		s.startNotify(context.Background())
		// The weather broadcast rides the message service, so it starts after it.
		// Like the relay it reconciles against the store rather than starting
		// once: a county added in the panel takes effect without a restart, and
		// a node with the feature off — the default — finds nothing to do.
		s.weather = newWeatherService(s)
		go s.weather.run(context.Background(), wxReconcileInterval)
		// Update poller (D2 / #15): periodically refresh the stack and waypointd
		// available-update caches and drive opt-in quiet-window auto-apply. Live mode
		// only. Ticks every 15 min so it reliably lands in the one-hour quiet window; a
		// full check runs only every updatePollInterval. Every outbound request it makes
		// is gated on the operator's check_enabled preference.
		if s.stack != nil || s.update != nil {
			go s.runUpdatePoller(context.Background(), 15*time.Minute, *updatePollInterval)
		}
		// Bring the consumer + status republisher (RFC-0008) and Home Assistant
		// discovery (RFC-0011) up on the resolved settings. Both follow later edits;
		// see dataplane.go for why the consumer restarts and the publisher does not.
		s.dp = &dataPlane{
			hub: s.hub, agg: s.agg,
			haDiscovery: *haDiscovery, haPrefix: *haPrefix, nodeID: *nodeID, version: Version,
			seen: map[string]bool{},
		}
		s.dp.start(context.Background(), dataPlaneConfigFrom(dpModel.MQTT))
		// Keep the reflector hostlists fresh for the gateways + pickers. Daily: these
		// lists change slowly, they are the largest things the node downloads, and
		// the upstreams ask not to be hammered. The YSF list honors the "UPPERCASE
		// Hostfiles" toggle, read from the store each refresh (both YSFGateway and
		// DGIdGateway consume this same file).
		go ysfhosts.Run(context.Background(), hostsrc.Split(*ysfHostsURL), *ysfHosts, 24*time.Hour, func() bool {
			var y config.YSFGateway
			if _, err := s.store.GetInto("ysfgw", &y); err != nil {
				return false
			}
			return y.UpperHostfiles
		})
		go p25hosts.Run(context.Background(), hostsrc.Split(*p25HostsURL), *p25Hosts, 24*time.Hour)
		go nxdnhosts.Run(context.Background(), hostsrc.Split(*nxdnHostsURL), *nxdnHosts, 24*time.Hour)
		go dstarhosts.Run(context.Background(), hostsrc.Split(*dstarHostsURL), *dstarHosts, 24*time.Hour)
		go m17hosts.Run(context.Background(), hostsrc.Split(*m17HostsURL), *m17Hosts, 24*time.Hour)
		// Verified reference-data downloads (RFC-0013): when a trusted key is
		// configured, dmrhosts/dmrtg verify each list against its <url>.minisig before
		// it replaces the cache; a tampered list is rejected and the cache kept.
		hostVerify := hostfileVerify(*hostfilePubkey, *requireSignedHostfiles)
		go dmrhosts.Run(context.Background(), hostsrc.Split(*dmrHostsURL), *dmrHosts, 24*time.Hour, hostVerify)
		go dmrtg.Run(context.Background(), hostsrc.Split(*dmrTGsURL), *dmrTGs, 24*time.Hour, hostVerify)
		// The id<->callsign table every gateway is configured to read. Nothing used
		// to download it, so the lookups silently resolved nothing (#138).
		// RunThen, not Run: after each successful refresh the phonebook's imported
		// entries are re-read against the new table, so a callsign RadioID reissued
		// against the same DMR ID reaches the operator's roster instead of sitting
		// stale until somebody notices. Entries the operator typed or has since
		// edited are not touched — see phonebook.Sync.
		//
		// This adds no outbound request: it runs on the table the refresher had
		// already downloaded, under the same operator-visible off switch. Turning the
		// ID-database refresh off turns this off with it, because there is then no
		// refresh for it to hang from.
		go dmrids.RunThen(context.Background(), hostsrc.Split(*dmrIDsURL), *dmrIDs, 24*time.Hour, hostVerify,
			func(path string) error { return s.syncPhonebookFromPublic(path) })
	}

	mode := "live, mqtt " + s.dataPlaneBroker(*broker)
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
		// The setup gate wraps the auth gate, so an unprovisioned node never
		// reaches the claim state machine at all: provisioning says what the box
		// is, claiming says who administers it, and the first has to happen first.
		Handler:           s.setupGate(s.auth.Gate(s.enforceRoles(s.newMux()))),
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
	// Tell systemd we are up, and start answering its watchdog.
	//
	// The watchdog is the only thing that can distinguish a wedged daemon from a
	// healthy one: a hotspot whose event loop has deadlocked still holds its
	// listener open and never exits, so Restart= never fires and the node is off
	// the air until somebody power-cycles it. See runWatchdog.
	go runWatchdog(context.Background(), s)
	log.Fatal(listenAndServe(srv, tlsOptions{
		enabled:      tlsServing,
		certDir:      *tlsDir,
		httpsPort:    portOf(*addr),
		redirectAddr: *httpRedirectAddr,
		certs:        s.certs,
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
