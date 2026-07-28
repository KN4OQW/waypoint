package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/stackupdate"
	"github.com/KN4OQW/waypoint/internal/store"
)

// stackUpdater drives waypoint-stack .deb updates from the signed apt repo (D2 —
// waypointd is the only driver of stack updates). It wraps the health-gated engine
// (internal/stackupdate) with the store bookkeeping, an in-flight lock, and the
// private apt source dir that limits every apt call to the Waypoint origin.
type stackUpdater struct {
	sys        *stackupdate.OSSystem
	hist       *stackupdate.History
	store      *store.Store
	sourceFile string // path to the deb822 waypoint.sources (the only source apt sees)
	timings    stackupdate.Timings

	mu         sync.Mutex // serializes apply; check is read-mostly
	applying   bool
	lastResult string
}

// newStackUpdater assembles the controller. sourceFile is the waypoint.sources the
// image installs; if it is missing the stack updater still constructs (so the API
// reports "not configured" cleanly) but a check will error.
func newStackUpdater(st *store.Store, sourceFile string) (*stackUpdater, error) {
	hist, err := stackupdate.NewHistory(st)
	if err != nil {
		return nil, err
	}
	su := &stackUpdater{
		sys:        &stackupdate.OSSystem{Hist: hist},
		hist:       hist,
		store:      st,
		sourceFile: sourceFile,
		timings:    stackupdate.DefaultTimings(),
	}
	return su, nil
}

// ensureSourceDir writes the current waypoint.sources into a private directory
// containing nothing else, and points the engine at it — so `apt-get update` /
// `apt list` see ONLY the Waypoint origin (D2), never triggering an OS-wide
// refresh. Refreshed each check so an operator edit to the source is picked up.
func (su *stackUpdater) ensureSourceDir() error {
	if su.sourceFile == "" {
		return fmt.Errorf("stack updates not configured (no apt source file)")
	}
	content, err := os.ReadFile(su.sourceFile)
	if err != nil {
		return fmt.Errorf("read apt source %s: %w", su.sourceFile, err)
	}
	dir := filepath.Join(os.TempDir(), "waypoint-apt-src.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "waypoint.sources"), content, 0o644); err != nil {
		return err
	}
	su.sys.SourcesDir = dir
	return nil
}

// check refreshes the Waypoint source and returns the current stack update plan,
// caching the result (available + timestamp) in the store for the UI to read.
func (su *stackUpdater) check(ctx context.Context) (stackupdate.Plan, error) {
	if err := su.ensureSourceDir(); err != nil {
		return stackupdate.Plan{}, err
	}
	if err := su.sys.AptRefresh(ctx); err != nil {
		return stackupdate.Plan{}, err
	}
	out, err := su.sys.Upgradable(ctx)
	if err != nil {
		return stackupdate.Plan{}, err
	}
	plan := stackupdate.PlanFrom(stackupdate.ParseUpgradable(out))
	su.cacheAvailable(plan)
	return plan, nil
}

// cacheAvailable persists the available updates + a fresh last-check stamp so the
// UI can render availability without re-running apt on every page load. It
// read-modify-writes so the auto-apply throttle stamp is preserved.
func (su *stackUpdater) cacheAvailable(plan stackupdate.Plan) {
	st, _ := config.GetUpdateState(su.store)
	blob, _ := json.Marshal(plan.Updates)
	st.LastCheck, st.Available = time.Now().UTC(), blob
	if err := config.SetUpdateState(su.store, st, "update-check"); err != nil {
		log.Printf("stack update: cache available: %v", err)
	}
}

// apply re-checks (to pin the exact current candidates) and runs the transactional
// update, health-gated with automatic revert. Serialized: a second apply while one
// is running is refused. Runs to completion in-process — unlike the binary updater
// it restarts other services, not waypointd, so it survives its own work.
func (su *stackUpdater) apply(ctx context.Context) (stackupdate.Outcome, error) {
	su.mu.Lock()
	if su.applying {
		su.mu.Unlock()
		return stackupdate.Outcome{}, fmt.Errorf("a stack update is already in progress")
	}
	su.applying = true
	su.mu.Unlock()
	defer func() { su.mu.Lock(); su.applying = false; su.mu.Unlock() }()

	plan, err := su.check(ctx)
	if err != nil {
		return stackupdate.Outcome{}, err
	}
	if !plan.Available {
		return stackupdate.Outcome{}, fmt.Errorf("nothing to apply: %s", plan.Reason)
	}
	log.Printf("stack update: applying %d package(s): %s", len(plan.Updates), summarize(plan))
	out, err := stackupdate.Apply(ctx, plan, su.sys, su.timings)
	if err != nil {
		su.setLastResult("error: " + err.Error())
		return out, err
	}
	switch {
	case out.Confirmed:
		su.setLastResult("confirmed " + summarizeApplied(out.Applied))
		log.Printf("stack update: confirmed — %s", summarizeApplied(out.Applied))
	case out.Reverted:
		su.setLastResult("reverted: " + out.Reason)
		log.Printf("stack update: reverted to the prior versions — %s", out.Reason)
	}
	// Refresh the availability cache post-apply (a confirmed update clears it).
	if p, cerr := su.check(ctx); cerr == nil {
		_ = p
	}
	return out, nil
}

func (su *stackUpdater) setLastResult(s string) {
	su.mu.Lock()
	su.lastResult = s
	su.mu.Unlock()
}

func (su *stackUpdater) isApplying() bool {
	su.mu.Lock()
	defer su.mu.Unlock()
	return su.applying
}

func summarize(p stackupdate.Plan) string {
	parts := make([]string, len(p.Updates))
	for i, u := range p.Updates {
		parts[i] = fmt.Sprintf("%s %s→%s", u.Package, u.From, u.To)
	}
	return joinComma(parts)
}

func summarizeApplied(pkgs []stackupdate.PkgVer) string {
	parts := make([]string, len(pkgs))
	for i, p := range pkgs {
		parts[i] = p.Package + "=" + p.Version
	}
	return joinComma(parts)
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// --- API surface (behind the session wall) ---

type stackStatusResp struct {
	Configured bool                 `json:"configured"`
	Installed  map[string]string    `json:"installed"`
	Available  []stackupdate.Update `json:"available"`
	Prefs      config.ViewUpdate    `json:"prefs"`
	LastCheck  time.Time            `json:"last_check"`
	Applying   bool                 `json:"applying"`
	LastResult string               `json:"last_result,omitempty"`
	History    []stackupdate.Record `json:"history"`
	// Binary is the cached waypointd signed-manifest verdict (binaryAvailability),
	// so the panel can show a new release without a fetch on page load. Absent until
	// a check has run; independent of the stack fields above.
	Binary          json.RawMessage `json:"binary,omitempty"`
	LastBinaryCheck time.Time       `json:"last_binary_check"`
}

// stackStatus handles GET /api/update/stack: installed versions, cached available
// updates (stack and waypointd), the operator policy, and recent history —
// everything the Updates panel paints without triggering apt or a fetch. A node
// without the apt repo still gets the policy and the binary check, so the panel's
// update-policy controls work there too.
func (s *server) stackStatus(w http.ResponseWriter, r *http.Request) {
	st, _ := config.GetUpdateState(s.store)
	resp := stackStatusResp{
		LastBinaryCheck: st.LastBinaryCheck,
		Binary:          st.Binary,
	}
	if m, err := config.Load(s.store); err == nil {
		resp.Prefs = config.ViewUpdate{
			Channel:      m.Update.Channel,
			CheckEnabled: m.Update.CheckEnabled,
			AutoApply:    m.Update.AutoApply,
			QuietWindow:  m.Update.QuietWindow,
		}
	}
	if s.stack == nil {
		writeJSONStatus(w, http.StatusOK, resp)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if len(st.Available) > 0 {
		_ = json.Unmarshal(st.Available, &resp.Available)
	}
	resp.Installed, _ = s.stack.sys.InstalledVersions(ctx, stackupdate.AllPackages())
	resp.History, _ = s.stack.hist.Recent(20)
	resp.Configured = s.stack.sourceFile != ""
	resp.LastCheck = st.LastCheck
	resp.Applying = s.stack.isApplying()
	resp.LastResult = s.stack.lastResult
	writeJSONStatus(w, http.StatusOK, resp)
}

// stackCheck handles POST /api/update/stack/check: run apt against the Waypoint
// source now and return the available updates.
func (s *server) stackCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.stack == nil {
		http.Error(w, "stack updates not configured", http.StatusNotImplemented)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	plan, err := s.stack.check(ctx)
	if err != nil {
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"available": plan.Updates,
		"units":     plan.Units,
		"reason":    plan.Reason,
	})
}

// stackApply handles POST /api/update/stack/apply: start the transactional update
// in the background and return 202. The panel polls GET /api/update/stack for the
// result (applying → confirmed/reverted, and updated installed versions).
func (s *server) stackApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.stack == nil {
		http.Error(w, "stack updates not configured", http.StatusNotImplemented)
		return
	}
	if s.stack.isApplying() {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": "a stack update is already in progress"})
		return
	}
	// A stack update health-gates on MMDVM-Host being up, and both detection and
	// a firmware flash stop it — so an update running alongside either would see
	// the host go down, conclude it had broken the node, and roll itself back for
	// no reason (#19 / RFC-0019 §7).
	release, busy := s.hwOps.acquire("stack update")
	if busy != "" {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": busy + " is already running; only one hardware operation runs at a time",
		})
		return
	}
	go func() {
		defer release()
		// Detached from the request; its own timeout bounds the health gate.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if _, err := s.stack.apply(ctx); err != nil {
			log.Printf("stack update: apply failed: %v", err)
		}
	}()
	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"status": "started",
		"detail": "stack update running; poll /api/update/stack for the result",
	})
}

// --- periodic poller (D2 timing model) ---

// runUpdatePoller is every *unattended* update action on the node: (1) refreshing
// the stack availability cache, (2) the periodic signed-manifest check for
// waypointd itself, and (3) opt-in auto-apply inside the quiet window. It ticks
// more often than hourly so it reliably lands in the one-hour quiet window, but
// only runs a full check every checkEvery to keep apt calls (and fetches) cheap.
//
// All three are gated on the operator's check_enabled preference (#15): with
// automatic checks off this loop makes no outbound request at all.
func (s *server) runUpdatePoller(ctx context.Context, tick, checkEvery time.Duration) {
	if s.stack == nil && s.update == nil {
		return
	}
	lastCheck := s.pollUpdatesOnce(ctx, time.Time{}, checkEvery)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			lastCheck = s.pollUpdatesOnce(ctx, lastCheck, checkEvery)
		}
	}
}

// pollUpdatesOnce is one poller tick, returning the last-check stamp to carry into
// the next one (unchanged when this tick did not check). Split out from the loop so
// the privacy gate is testable without a ticker.
func (s *server) pollUpdatesOnce(ctx context.Context, lastCheck time.Time, checkEvery time.Duration) time.Time {
	m, err := config.Load(s.store)
	if err != nil {
		log.Printf("update poller: load config: %v", err)
		return lastCheck
	}
	// The privacy gate (#15 / GOVERNANCE.md principle 2). An operator who turned
	// automatic checks off gets NO unattended outbound request: not the manifest
	// fetch, not the apt refresh — and so no auto-apply either, since it exists only
	// to install what a check found. Everything the operator triggers by hand (the
	// CHECK NOW buttons, UPDATE NOW, the CLI modes) is untouched.
	if !m.Update.CheckEnabled {
		return lastCheck
	}
	now := time.Now()
	if now.Sub(lastCheck) < checkEvery {
		s.maybeAutoApply(ctx, m) // still evaluate the window against the cached availability
		return lastCheck
	}
	if s.stack != nil {
		if _, err := s.stack.check(ctx); err != nil {
			log.Printf("stack update: periodic check: %v", err)
		}
	}
	if s.update != nil {
		if _, err := s.binaryCheck(ctx); err != nil {
			log.Printf("update: periodic manifest check: %v", err)
		}
	}
	s.maybeAutoApply(ctx, m)
	return now
}

// maybeAutoApply applies stack updates automatically when the operator opted in and
// the current time is in the quiet window (at most once per local day). Off by
// default (D2). The throttle stamp (UpdateState.LastAutoApply) is written before
// the apply so a mid-apply crash cannot loop it. m is the caller's already-loaded
// policy, so one tick reads the store once.
func (s *server) maybeAutoApply(ctx context.Context, m *config.Model) {
	if s.stack == nil {
		return // nothing to auto-apply: this node has no apt stack
	}
	st, _ := config.GetUpdateState(s.store)
	var available []stackupdate.Update
	if len(st.Available) > 0 {
		_ = json.Unmarshal(st.Available, &available)
	}
	if !stackupdate.DueForAutoApply(time.Now(), st.LastAutoApply, m.Update.QuietWindow, m.Update.AutoApply, len(available) > 0) {
		return
	}
	// Stamp first (once-per-window guarantee even if the apply below fails).
	st.LastAutoApply = time.Now()
	if err := config.SetUpdateState(s.store, st, "auto-apply"); err != nil {
		log.Printf("stack update: auto-apply stamp: %v", err)
		return
	}
	// The unattended path takes the same token as the operator-driven one: an
	// auto-apply landing in the middle of a firmware flash is the worst version
	// of this collision, because nobody is watching it happen.
	release, busy := s.hwOps.acquire("stack update")
	if busy != "" {
		log.Printf("stack update: skipping auto-apply, %s is running", busy)
		return
	}
	defer release()

	log.Printf("stack update: auto-applying in quiet window (%s)", m.Update.QuietWindow)
	if _, err := s.stack.apply(ctx); err != nil {
		log.Printf("stack update: auto-apply failed: %v", err)
	}
}

// --- CLI modes (headless / cron / bench) ---

// runStackCheck implements `waypointd -update-stack-check`: refresh the Waypoint
// source and report available stack updates, changing nothing.
func runStackCheck(storePath, sourceFile string) {
	st, err := store.Open(storePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update-stack-check: open store: %v\n", err)
		os.Exit(2)
	}
	defer st.Close()
	su, err := newStackUpdater(st, sourceFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update-stack-check: %v\n", err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	plan, err := su.check(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update-stack-check: %v\n", err)
		os.Exit(2)
	}
	if !plan.Available {
		fmt.Printf("update-stack-check: %s\n", plan.Reason)
		os.Exit(0)
	}
	fmt.Printf("update-stack-check: %d update(s) available\n", len(plan.Updates))
	for _, u := range plan.Updates {
		fmt.Printf("  %s  %s → %s\n", u.Package, u.From, u.To)
	}
	os.Exit(0)
}

// runStackApply implements `waypointd -update-stack`: the transactional stack
// update (stop → install exact versions → restart → health-gate → confirm or
// revert), for a headless/cron caller and the bench. Runs standalone and exits.
func runStackApply(storePath, sourceFile string) {
	st, err := store.Open(storePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update-stack: open store: %v\n", err)
		os.Exit(2)
	}
	defer st.Close()
	su, err := newStackUpdater(st, sourceFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update-stack: %v\n", err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := su.apply(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update-stack: %v\n", err)
		os.Exit(1)
	}
	switch {
	case out.Confirmed:
		fmt.Printf("update-stack: confirmed — %s\n", summarizeApplied(out.Applied))
	case out.Reverted:
		fmt.Printf("update-stack: reverted to the prior versions — %s\n", out.Reason)
		os.Exit(1)
	}
	os.Exit(0)
}
