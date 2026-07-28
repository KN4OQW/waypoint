package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/flash"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/minisign"
	"github.com/KN4OQW/waypoint/internal/modem"
	"github.com/KN4OQW/waypoint/internal/store"
)

// The firmware flashing surface (#19 / RFC-0019).
//
//	GET  /api/flash          → the catalog, what would be flashed and why, the
//	                           running or last job
//	POST /api/flash/catalog  → fetch the signed catalog now
//	POST /api/flash          → start a flash; 202 with a job id
//	GET  /api/flash/events   → byte-level progress, as Server-Sent Events
//
// # Why progress has two streams
//
// Everything published to the event hub is persisted to SQLite by
// internal/events. A 128 KB image written in 256-byte chunks is about five
// hundred progress ticks, and writing five hundred rows to a Raspberry Pi's SD
// card to animate a progress bar is exactly the kind of thing this project
// exists to be better than.
//
// So the split is deliberate: byte-level progress goes to an in-memory
// broadcaster that this SSE endpoint serves and nothing stores, while
// MILESTONES — started, succeeded, failed, with the firmware version before and
// after — go to the hub, where they land in history, on the dashboard and on
// the LCD like every other thing that happens to a node. Months later, "when
// did this node's firmware change, and to what" is answerable; "what percentage
// was it at four minutes in" is not, and should not be.

// defaultFirmwareURL is the signed firmware catalog. It is a separate release
// from waypointd's own so firmware can ship without a software release, and an
// operator on a private channel overrides it with -firmware-url.
const defaultFirmwareURL = "https://github.com/KN4OQW/MMDVM_HS/releases/latest/download/firmware.json"

// flashJobTimeout bounds a whole flash. A 128 KB image at 115200 baud is about
// twelve seconds to write and as long again to read back; the ceiling is
// generous enough for a slow catalog fetch on a poor link and short enough that
// a wedged board cannot hold the modem hostage indefinitely.
const flashJobTimeout = 10 * time.Minute

// flashJob is one flash, running or finished.
type flashJob struct {
	ID      string      `json:"id"`
	Variant string      `json:"variant,omitempty"`
	Stage   flash.Stage `json:"stage"`
	Done    int         `json:"done,omitempty"`
	Total   int         `json:"total,omitempty"`
	Detail  string      `json:"detail,omitempty"`
	Started time.Time   `json:"started"`
	Ended   *time.Time  `json:"ended,omitempty"`
	Error   string      `json:"error,omitempty"`
	// Before and After are the firmware version strings either side of the
	// flash. They are the whole point of the operation and the only evidence an
	// operator can actually read.
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// Running reports a job still in progress.
func (j *flashJob) Running() bool { return j != nil && j.Ended == nil }

// flasher owns the engine, the current job and the progress fan-out.
type flasher struct {
	eng *flash.Engine
	// source is the same object the engine flashes from, so the catalog the
	// panel shows and the catalog a flash is chosen out of can never be two
	// different things.
	source flash.Source
	hub    *hub.Hub
	store  *store.Store

	mu   sync.Mutex
	job  *flashJob
	subs map[chan flash.Progress]struct{}
	seq  int

	// cat caches the last successfully fetched catalog so the panel renders
	// without a network round trip on every page load.
	cat      *flash.Catalog
	catAt    time.Time
	catError string

	// keyed records whether a release key was configured. It is a flag rather
	// than a comparison against the zero key because a minisign public key holds
	// an ed25519 slice and slices do not compare.
	keyed bool
}

// newFlasher assembles the controller. A node with no release key still gets
// one — every call then fails with "no key configured", which is a far better
// answer than a panel that silently offers nothing.
func newFlasher(h *hub.Hub, st *store.Store, catalogURL string, key minisign.PublicKey, hasKey bool, cacheDir string) *flasher {
	src := &flash.HTTPSource{
		CatalogURL: catalogURL,
		PubKey:     key,
		CacheDir:   cacheDir,
		UserAgent:  "waypoint/" + Version,
	}
	f := &flasher{
		source: src,
		hub:    h,
		store:  st,
		subs:   map[chan flash.Progress]struct{}{},
		keyed:  hasKey,
	}
	if !hasKey {
		// Recorded rather than hidden: firmware verification is not optional
		// (RFC-0019 §2), so a node without the key cannot flash, and the panel
		// should say why rather than appearing broken.
		f.catError = "no release signing key is configured on this node, so firmware cannot be verified"
	}
	f.eng = &flash.Engine{
		Source: src,
		Holder: mmdvmHolder{},
	}
	f.source = src
	return f
}

// hasKey reports whether firmware can be verified at all on this node.
func (f *flasher) hasKey() bool { return f.keyed }

// --- progress fan-out ----------------------------------------------------

func (f *flasher) subscribe() (<-chan flash.Progress, func()) {
	ch := make(chan flash.Progress, 64)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	return ch, func() {
		f.mu.Lock()
		if _, ok := f.subs[ch]; ok {
			delete(f.subs, ch)
			close(ch)
		}
		f.mu.Unlock()
	}
}

// publish updates the job and fans the progress out.
//
// A subscriber whose buffer is full is skipped rather than waited for. A
// browser on a slow link must not be able to stall a write to a microcontroller
// — the flash is the real work and the progress bar is a courtesy.
func (f *flasher) publish(p flash.Progress) {
	f.mu.Lock()
	if f.job != nil && f.job.Ended == nil {
		f.job.Stage, f.job.Done, f.job.Total = p.Stage, p.Done, p.Total
		if p.Detail != "" {
			f.job.Detail = p.Detail
		}
	}
	for ch := range f.subs {
		select {
		case ch <- p:
		default:
		}
	}
	f.mu.Unlock()
}

// snapshot returns a copy of the current or last job.
func (f *flasher) snapshot() *flashJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job == nil {
		return nil
	}
	j := *f.job
	return &j
}

func (f *flasher) running() bool { return f.snapshot().Running() }

// --- the job ------------------------------------------------------------

// start begins a flash in the background, returning the new job.
func (f *flasher) start(req flash.Request, release func()) (*flashJob, error) {
	f.mu.Lock()
	if f.job.Running() {
		f.mu.Unlock()
		return nil, flash.ErrBusy
	}
	f.seq++
	job := &flashJob{
		ID:      strconv.Itoa(f.seq) + "-" + strconv.FormatInt(time.Now().Unix(), 10),
		Stage:   flash.StageChoosing,
		Started: time.Now().UTC(),
		Before:  req.Identity.Firmware,
	}
	f.job = job
	f.mu.Unlock()

	go func() {
		defer release()
		// The job's deadline is its own, not the request's: an operator closing
		// the tab must not abandon a modem mid-write.
		ctx, cancel := context.WithTimeout(context.Background(), flashJobTimeout)
		defer cancel()
		f.run(ctx, req, job)
	}()
	return job, nil
}

func (f *flasher) run(ctx context.Context, req flash.Request, job *flashJob) {
	f.event("flash_started", req.Identity.BoardID, "firmware flash started")

	res, err := f.eng.Flash(ctx, req, f.publish)

	now := time.Now().UTC()
	f.mu.Lock()
	job.Ended = &now
	if err != nil {
		job.Error = err.Error()
		job.Stage = flash.StageDone
	} else {
		job.Variant = res.Variant.ID
		job.Done, job.Total = res.Bytes, res.Bytes
		job.Detail = res.Variant.Describe()
		if res.After != nil {
			job.After = res.After.Firmware
		}
	}
	f.mu.Unlock()

	if err != nil {
		log.Printf("flash: %v", err)
		f.event("flash_failed", req.Identity.BoardID, err.Error())
		f.publish(flash.Progress{Stage: flash.StageDone, Detail: "failed: " + err.Error()})
		return
	}

	log.Printf("flash: wrote %s (%d bytes) in %s", res.Variant.ID, res.Bytes, res.Duration.Round(time.Second))
	f.event("flash_ok", req.Identity.BoardID, describeOutcome(res))
	f.adopt(res)
}

// adopt records the post-flash reality: the modem now runs different firmware,
// and the hardware panel should say so without the operator pressing Detect
// again to find out what they just did.
func (f *flasher) adopt(res flash.Result) {
	if res.After == nil || f.store == nil {
		return
	}
	st, err := config.GetHardwareState(f.store)
	if err != nil {
		return
	}
	st.Identity = res.After
	st.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	if err := config.SetHardwareState(f.store, st, "flash"); err != nil {
		log.Printf("flash: record the new firmware: %v", err)
	}
}

func describeOutcome(res flash.Result) string {
	if res.After != nil && res.After.Firmware != "" {
		if res.Before != "" && res.Before != res.After.Firmware {
			return fmt.Sprintf("firmware %s → %s (%s)", res.Before, res.After.Firmware, res.Variant.ID)
		}
		return fmt.Sprintf("firmware %s (%s)", res.After.Firmware, res.Variant.ID)
	}
	return fmt.Sprintf("wrote %s", res.Variant.ID)
}

// event publishes a milestone to the hub, where it is persisted and rendered
// like every other thing that happens to a node.
func (f *flasher) event(kind, board, detail string) {
	if f.hub == nil {
		return
	}
	f.hub.Publish(hub.Event{
		Time: time.Now().UTC(), Type: kind, Source: board, Detail: detail,
	})
}

// catalog returns the cached catalog, fetching it if there is none.
func (f *flasher) catalog(ctx context.Context, refresh bool) (flash.Catalog, error) {
	f.mu.Lock()
	cached, at := f.cat, f.catAt
	f.mu.Unlock()
	if cached != nil && !refresh && time.Since(at) < 6*time.Hour {
		return *cached, nil
	}

	cat, err := f.source.Catalog(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		f.catError = err.Error()
		if cached != nil {
			// A stale catalog beats none: the operator can still flash what they
			// already know about when the network is the thing that is broken.
			return *cached, nil
		}
		return flash.Catalog{}, err
	}
	f.cat, f.catAt, f.catError = &cat, time.Now(), ""
	return cat, nil
}

// --- HTTP ----------------------------------------------------------------

type flashVariantView struct {
	ID        string `json:"id"`
	Describe  string `json:"describe"`
	TCXOHz    int    `json:"tcxo_hz,omitempty"`
	TCXOLabel string `json:"tcxo_label,omitempty"`
	Duplex    bool   `json:"duplex"`
	Transport string `json:"transport"`
	Notes     string `json:"notes,omitempty"`
}

type flashView struct {
	// Available is false when this node cannot flash at all — no signing key, or
	// no catalog reachable and none cached.
	Available bool               `json:"available"`
	Version   string             `json:"catalog_version,omitempty"`
	Variants  []flashVariantView `json:"variants,omitempty"`
	CheckedAt time.Time          `json:"checked_at,omitempty"`

	// Match is what would be flashed if the operator pressed the button now.
	// Reason and Choices are the refusal, and the UI greys the button and shows
	// the reason rather than re-deriving the rules in JavaScript.
	Match   *flashVariantView `json:"match,omitempty"`
	Reason  string            `json:"reason,omitempty"`
	Choices []string          `json:"choices,omitempty"`

	// HostRunning tells the UI up front that flashing will take the node off the
	// air, so the confirmation says so instead of the operator finding out.
	HostRunning bool      `json:"host_running"`
	Job         *flashJob `json:"job,omitempty"`
}

func variantView(v flash.Variant) flashVariantView {
	return flashVariantView{
		ID: v.ID, Describe: v.Describe(), TCXOHz: v.TCXOHz,
		TCXOLabel: modem.TCXOLabel(v.TCXOHz), Duplex: v.Duplex,
		Transport: v.Transport, Notes: v.Notes,
	}
}

// firmwareCacheDir places the verified-image cache. Alongside the store by
// default, because that is the one directory on a node guaranteed to be
// writable and to survive a reboot — and a cached image is what makes a
// re-flash work on a node whose network is the thing that is broken.
func firmwareCacheDir(explicit, storePath string) string {
	if explicit != "" {
		return explicit
	}
	if storePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(storePath), "firmware")
}

// flashRoot dispatches GET /api/flash (the panel) and POST /api/flash (start).
// They share a path because they are the same noun: what the firmware is, and
// changing it.
func (s *server) flashRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.flashStatus(w, r)
	case http.MethodPost:
		s.flashStart(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// flashStatus serves GET /api/flash.
func (s *server) flashStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.flash == nil {
		writeJSON(w, flashView{Reason: "firmware flashing is not configured on this node"})
		return
	}
	writeJSON(w, s.flashSnapshot(r.Context(), false))
}

// flashCatalogRefresh serves POST /api/flash/catalog.
func (s *server) flashCatalogRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.flash == nil {
		http.Error(w, "firmware flashing is not configured on this node", http.StatusNotImplemented)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	v := s.flashSnapshot(ctx, true)
	if !v.Available {
		writeJSONStatus(w, http.StatusBadGateway, v)
		return
	}
	writeJSON(w, v)
}

// flashSnapshot builds the whole panel: catalog, verdict, job.
func (s *server) flashSnapshot(ctx context.Context, refresh bool) flashView {
	f := s.flash
	v := flashView{Job: f.snapshot()}

	active, _ := mmdvmHolder{}.Active(ctx)
	v.HostRunning = active

	if !f.hasKey() {
		v.Reason = "no release signing key is configured on this node, so firmware cannot be verified"
		return v
	}

	cat, err := f.catalog(ctx, refresh)
	if err != nil {
		v.Reason = err.Error()
		return v
	}
	v.Available, v.Version, v.CheckedAt = true, cat.Version, f.catAt
	for _, variant := range cat.Variants {
		v.Variants = append(v.Variants, variantView(variant))
	}

	st, err := config.GetHardwareState(s.store)
	if err != nil || st.Identity == nil {
		v.Reason = "no modem has been detected on this node yet — run detection first"
		return v
	}
	match, err := cat.MatchFor(*st.Identity)
	if err != nil {
		var me *flash.MatchError
		if errors.As(err, &me) {
			v.Reason, v.Choices = me.Reason, me.Choices
		} else {
			v.Reason = err.Error()
		}
		return v
	}
	mv := variantView(match)
	v.Match = &mv
	return v
}

// flashStart serves POST /api/flash.
func (s *server) flashStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.flash == nil {
		http.Error(w, "firmware flashing is not configured on this node", http.StatusNotImplemented)
		return
	}
	var body struct {
		VariantID string `json:"variant_id"`
		StopHost  bool   `json:"stop_host"`
	}
	_ = decodeJSON(r, &body)

	st, err := config.GetHardwareState(s.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if st.Identity == nil {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": "no modem has been detected on this node yet — run detection first",
		})
		return
	}

	// One hardware operation at a time. A flash stops MMDVM-Host for a minute,
	// and internal/stackupdate health-gates an update on MMDVM-Host being up —
	// so a stack update running concurrently would watch the host go down,
	// conclude it had broken the node, and roll itself back for no reason.
	release, busy := s.hwOps.acquire("firmware flash")
	if busy != "" {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": busy + " is already running; only one hardware operation runs at a time",
		})
		return
	}

	board, _ := modem.BoardByID(st.Identity.BoardID)
	s.flash.eng.LineConfig = flash.LinesForBoard(board)
	s.flash.eng.Reprobe = func(ctx context.Context) (*modem.Identity, error) {
		// MMDVM-Host is still stopped at this point — the engine restarts it only
		// after everything else — so this probes without arbitrating for a port
		// nothing currently holds.
		res, err := s.newDetector().Detect(ctx, false)
		if err != nil {
			return nil, err
		}
		return res.Identity, nil
	}

	job, err := s.flash.start(flash.Request{
		Identity:  *st.Identity,
		VariantID: body.VariantID,
		StopHost:  body.StopHost,
	}, release)
	if err != nil {
		release()
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"status": "started",
		"job":    job,
		"detail": "watch /api/flash/events for progress",
	})
}

// flashEvents serves GET /api/flash/events: byte-level progress as SSE.
//
// The first event is the current job, so a client that connects late — or
// reconnects after a dropped link — sees where things stand instead of waiting
// for the next chunk to be written.
func (s *server) flashEvents(w http.ResponseWriter, r *http.Request) {
	if s.flash == nil {
		http.Error(w, "firmware flashing is not configured on this node", http.StatusNotImplemented)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	ch, cancel := s.flash.subscribe()
	defer cancel()

	send := func(v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	if job := s.flash.snapshot(); job != nil {
		if !send(flash.Progress{Stage: job.Stage, Done: job.Done, Total: job.Total, Detail: job.Detail}) {
			return
		}
	}

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case p, ok := <-ch:
			if !ok || !send(p) {
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

// --- one hardware operation at a time ------------------------------------

// hwOps serialises the operations that take the modem, or the stack, away from
// the node: detection, firmware flashing, and stack updates.
//
// It refuses rather than queues. A caller told "a firmware flash is running"
// can decide what to do; a request that silently blocks for four minutes looks
// like a hung dashboard, and by the time it returns the operator has usually
// pressed the button twice more.
type hwOps struct {
	mu      sync.Mutex
	current string
}

// acquire takes the token, or reports what already holds it.
func (h *hwOps) acquire(name string) (release func(), busy string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current != "" {
		return nil, h.current
	}
	h.current = name
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			h.current = ""
			h.mu.Unlock()
		})
	}, ""
}

// busy names the running operation, if any.
func (h *hwOps) busy() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.current
}
