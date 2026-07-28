package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/cal"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/modem"
	"github.com/KN4OQW/waypoint/internal/store"
)

// The calibration surface (#20 / RFC-0021).
//
//	GET  /api/cal           → what can be calibrated, and the last sweep
//	POST /api/cal/sweep     → start a sweep; 202 with a job id
//	POST /api/cal/cancel    → stop the running sweep
//	GET  /api/cal/events    → live BER, as Server-Sent Events
//	POST /api/cal/apply     → write a measured offset into the config
//	POST /api/cal/transmit  → one bounded transmit test (repeater workflow)
//	POST /api/cal/listen    → what the modem hears: levels, and whether the
//	                          signal arrived inverted (repeater workflow)
//
// Progress is split the way flashing splits it (RFC-0019 §8): per-frame BER goes
// to an in-memory broadcaster that nothing stores, and MILESTONES — started,
// finished, applied — go to the hub, where they persist. A sweep is several
// hundred progress updates; writing those to a Raspberry Pi's SD card to animate
// a chart is the thing this project exists to be better than.
//
// Measuring and applying are separate routes for the reason RFC-0021 §7 gives:
// they are different acts, and the operator sees the curve before anything about
// their node changes.

// calJobTimeout bounds a whole sweep, including time spent waiting for the
// operator to key up. It is generous because the waiting is the point, and
// bounded because the session holds the modem port — the node is off the air for
// as long as it runs.
const calJobTimeout = 15 * time.Minute

// calJob is one sweep, running or finished.
type calJob struct {
	ID       string      `json:"id"`
	Phase    cal.Phase   `json:"phase"`
	Started  time.Time   `json:"started"`
	Ended    *time.Time  `json:"ended,omitempty"`
	Error    string      `json:"error,omitempty"`
	Detail   string      `json:"detail,omitempty"`
	OffsetHz int         `json:"offset_hz"`
	BER      float64     `json:"ber_percent"`
	Frames   int         `json:"frames"`
	Step     int         `json:"step"`
	Steps    int         `json:"steps"`
	Result   *cal.Result `json:"result,omitempty"`
}

func (j *calJob) Running() bool { return j != nil && j.Ended == nil }

// calibrator owns the running sweep and the progress fan-out.
type calibrator struct {
	store *store.Store
	hub   *hub.Hub

	mu     sync.Mutex
	job    *calJob
	subs   map[chan cal.Progress]struct{}
	seq    int
	cancel context.CancelFunc
}

func newCalibrator(h *hub.Hub, st *store.Store) *calibrator {
	return &calibrator{store: st, hub: h, subs: map[chan cal.Progress]struct{}{}}
}

func (c *calibrator) subscribe() (<-chan cal.Progress, func()) {
	ch := make(chan cal.Progress, 64)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		if _, ok := c.subs[ch]; ok {
			delete(c.subs, ch)
			close(ch)
		}
		c.mu.Unlock()
	}
}

// publish updates the job and fans progress out. A subscriber whose buffer is
// full is skipped rather than waited for: a browser on a slow link must not
// stall a measurement someone is holding a PTT for.
func (c *calibrator) publish(p cal.Progress) {
	c.mu.Lock()
	if c.job.Running() {
		c.job.Phase, c.job.OffsetHz, c.job.BER = p.Phase, p.OffsetHz, p.BER
		c.job.Frames, c.job.Step, c.job.Steps = p.Frames, p.Step, p.Steps
		if p.Detail != "" {
			c.job.Detail = p.Detail
		}
	}
	for ch := range c.subs {
		select {
		case ch <- p:
		default:
		}
	}
	c.mu.Unlock()
}

func (c *calibrator) snapshot() *calJob {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.job == nil {
		return nil
	}
	j := *c.job
	return &j
}

// stop cancels a running sweep. The session's own deferred cleanup does the
// rest: unkey, idle the modem, close the port, restart MMDVM-Host.
func (c *calibrator) stop() bool {
	c.mu.Lock()
	cancel, running := c.cancel, c.job.Running()
	c.mu.Unlock()
	if running && cancel != nil {
		cancel()
		return true
	}
	return false
}

func (c *calibrator) event(kind, source, detail string) {
	if c.hub == nil {
		return
	}
	c.hub.Publish(hub.Event{Time: time.Now().UTC(), Type: kind, Source: source, Detail: detail})
}

// --- what can be calibrated ----------------------------------------------

// errNoCalTarget explains the one precondition an operator most often hits.
var errNoCalTarget = errors.New("no modem has been detected or configured on this node yet — run detection first")

// calTarget builds the session options from the node's own configuration.
//
// The frequencies come from here and from nowhere else. There is deliberately no
// way to pass one in through a request body (RFC-0021 §2): a text box that keys
// a transmitter on whatever is typed into it is not a feature, and MMDVMCal's
// equivalent exists only because it has no configuration to read.
func (s *server) calTarget() (cal.Options, error) {
	m, err := config.Load(s.store)
	if err != nil {
		return cal.Options{}, err
	}
	opt := cal.Options{
		Port:      m.Modem.Port,
		ColorCode: uint8(atoiDefault(m.DMR.ColorCode, 1)),
		TXLevel:   float32(atofDefault(m.Modem.TXLevel, 50)),
		RFLevel:   float32(atofDefault(m.Modem.RFLevel, 100)),
		Baud:      atoiDefault(m.Modem.UARTSpeed, 115200),
		Holder:    mmdvmHolder{},
	}
	opt.RXFreqHz = uint32(atoiDefault(m.Modem.RXFreqHz, 0))
	opt.TXFreqHz = uint32(atoiDefault(m.Modem.TXFreqHz, 0))

	// The protocol version decides which of the two incompatible SET_CONFIG
	// layouts the session writes. Detection already asked; the session asks
	// again when it opens, so this is only a starting guess.
	if st, err := config.GetHardwareState(s.store); err == nil && st.Identity != nil {
		if opt.Port == "" {
			opt.Port = st.Identity.Port
		}
		opt.Protocol = st.Identity.Protocol
		if st.Identity.Baud > 0 {
			opt.Baud = st.Identity.Baud
		}
	}
	if opt.Port == "" {
		return cal.Options{}, errNoCalTarget
	}
	return opt, nil
}

// calView is what GET /api/cal serves.
type calView struct {
	// Available is false when a sweep cannot be started at all, and Reason says
	// why in a sentence the UI shows instead of re-deriving the rules.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`

	Port      string `json:"port,omitempty"`
	RXFreqHz  uint32 `json:"rx_freq_hz,omitempty"`
	TXFreqHz  uint32 `json:"tx_freq_hz,omitempty"`
	Band      string `json:"band,omitempty"`
	ColorCode int    `json:"color_code,omitempty"`

	// Duplex reports a board with two radios. It changes nothing about the
	// sweep; it is here because the repeater workflow is the one that has never
	// been run against real hardware and the UI says so.
	Duplex bool `json:"duplex"`
	// RepeaterWorkflowVerified is false and says so on purpose (RFC-0021 §6).
	RepeaterWorkflowVerified bool `json:"repeater_workflow_verified"`

	// CurrentOffsetHz is what the node is configured for now — the number a
	// sweep is offering to replace.
	CurrentRXOffset string `json:"current_rx_offset,omitempty"`
	CurrentTXOffset string `json:"current_tx_offset,omitempty"`

	HostRunning bool                    `json:"host_running"`
	Last        config.CalibrationState `json:"last"`
	Job         *calJob                 `json:"job,omitempty"`
}

func (s *server) calStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	v := calView{RepeaterWorkflowVerified: false}
	if s.cal != nil {
		v.Job = s.cal.snapshot()
	}
	if st, err := config.GetCalibrationState(s.store); err == nil {
		v.Last = st
	}
	active, _ := mmdvmHolder{}.Active(r.Context())
	v.HostRunning = active

	if m, err := config.Load(s.store); err == nil {
		v.CurrentRXOffset, v.CurrentTXOffset = m.Modem.RXOffset, m.Modem.TXOffset
		v.ColorCode = atoiDefault(m.DMR.ColorCode, 1)
	}
	if hs, err := config.GetHardwareState(s.store); err == nil && hs.Identity != nil {
		v.Duplex = hs.Identity.Duplex
	}

	opt, err := s.calTarget()
	if err != nil {
		v.Reason = err.Error()
		writeJSON(w, v)
		return
	}
	v.Port, v.RXFreqHz, v.TXFreqHz = opt.Port, opt.RXFreqHz, opt.TXFreqHz
	v.Band = cal.BandName(opt.RXFreqHz)
	if opt.RXFreqHz == 0 {
		v.Reason = "this node has no receive frequency configured, so there is nothing to sweep around"
		writeJSON(w, v)
		return
	}
	v.Available = true
	writeJSON(w, v)
}

// --- the sweep -----------------------------------------------------------

func (s *server) calSweep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cal == nil {
		http.Error(w, "calibration is not configured on this node", http.StatusNotImplemented)
		return
	}
	var body struct {
		StopHost     bool `json:"stop_host"`
		CoarseSpanHz int  `json:"coarse_span_hz"`
		CoarseStepHz int  `json:"coarse_step_hz"`
		MinFrames    int  `json:"min_frames"`
	}
	_ = decodeJSON(r, &body)

	opt, err := s.calTarget()
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	opt.StopHost = body.StopHost

	s.cal.mu.Lock()
	if s.cal.job.Running() {
		s.cal.mu.Unlock()
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": "a calibration sweep is already running"})
		return
	}
	s.cal.mu.Unlock()

	// One hardware operation at a time. A sweep holds the modem port for
	// minutes and stops MMDVM-Host; a flash or a stack update overlapping it
	// would fight for the same port, and stackupdate health-gates on MMDVM-Host
	// being up — it would read a calibration as a broken update.
	release, busy := s.hwOps.acquire("calibration")
	if busy != "" {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": busy + " is already running; only one hardware operation runs at a time",
		})
		return
	}

	plan := cal.Plan{
		CoarseSpanHz: body.CoarseSpanHz,
		CoarseStepHz: body.CoarseStepHz,
		MinFrames:    body.MinFrames,
		Timeout:      calJobTimeout,
	}

	s.cal.mu.Lock()
	s.cal.seq++
	job := &calJob{
		ID:      strconv.Itoa(s.cal.seq) + "-" + strconv.FormatInt(time.Now().Unix(), 10),
		Phase:   cal.PhaseCoarse,
		Started: time.Now().UTC(),
		Detail:  "starting",
	}
	// The job's deadline is its own, not the request's: an operator closing the
	// tab must not abandon a session that holds the modem port.
	ctx, cancel := context.WithTimeout(context.Background(), calJobTimeout)
	s.cal.job, s.cal.cancel = job, cancel
	s.cal.mu.Unlock()

	go func() {
		defer release()
		defer cancel()
		s.runSweep(ctx, opt, plan, job)
	}()

	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"status": "started",
		"job":    job,
		"detail": "watch /api/cal/events, and key your radio when it asks",
	})
}

func (s *server) runSweep(ctx context.Context, opt cal.Options, plan cal.Plan, job *calJob) {
	s.cal.event("calibration_started", opt.Port, fmt.Sprintf("sweeping around %.4f MHz", float64(opt.RXFreqHz)/1e6))

	sess, err := cal.Open(ctx, opt)
	if err != nil {
		s.finishSweep(job, nil, err)
		return
	}
	defer sess.Close()

	res, err := sess.Sweep(ctx, plan, s.cal.publish)
	s.finishSweep(job, &res, err)
}

// finishSweep records the outcome in the job, in the store and on the event
// hub. It runs on every path, including the failures — a sweep that found
// nothing is still evidence about the board, and an operator who reloads the
// page should find out what happened rather than an empty panel.
func (s *server) finishSweep(job *calJob, res *cal.Result, err error) {
	now := time.Now().UTC()
	s.cal.mu.Lock()
	job.Ended = &now
	job.Result = res
	if err != nil {
		job.Error = err.Error()
	}
	job.Phase = cal.PhaseDone
	s.cal.mu.Unlock()

	st := config.CalibrationState{
		Result: res,
		RanAt:  now.Format(time.RFC3339),
		Mode:   "dmr",
	}
	if err != nil {
		st.Error = err.Error()
	}
	if hs, hErr := config.GetHardwareState(s.store); hErr == nil && hs.Identity != nil {
		st.BoardID, st.Firmware, st.UDID = hs.Identity.BoardID, hs.Identity.Firmware, hs.Identity.UDID
	}
	// Carry forward what was applied, if anything: applying is a separate act
	// and a later sweep does not un-apply an earlier answer.
	if prev, pErr := config.GetCalibrationState(s.store); pErr == nil {
		st.AppliedOffsetHz, st.AppliedAt = prev.AppliedOffsetHz, prev.AppliedAt
	}
	if sErr := config.SetCalibrationState(s.store, st, "calibration"); sErr != nil {
		log.Printf("cal: record the sweep: %v", sErr)
	}

	switch {
	case err != nil:
		log.Printf("cal: %v", err)
		s.cal.event("calibration_failed", "", err.Error())
	case res != nil && res.Best != nil:
		s.cal.event("calibration_ok", "", fmt.Sprintf(
			"best offset %+d Hz at %.3f%% BER over %d frames", res.Best.OffsetHz, res.Best.BER, res.Best.Frames))
	}
}

func (s *server) calCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cal == nil || !s.cal.stop() {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": "no calibration sweep is running"})
		return
	}
	writeJSON(w, map[string]any{"status": "cancelling"})
}

// calEvents streams progress as SSE. The first event is the current job's state,
// so a client that connects late — or reconnects after a dropped link — sees
// where things stand rather than waiting for the next frame to be decoded.
func (s *server) calEvents(w http.ResponseWriter, r *http.Request) {
	if s.cal == nil {
		http.Error(w, "calibration is not configured on this node", http.StatusNotImplemented)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	ch, cancel := s.cal.subscribe()
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
	if job := s.cal.snapshot(); job != nil {
		if !send(cal.Progress{Phase: job.Phase, OffsetHz: job.OffsetHz, BER: job.BER,
			Frames: job.Frames, Step: job.Step, Steps: job.Steps, Detail: job.Detail}) {
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

// --- applying ------------------------------------------------------------

// calApply writes a measured offset into the configuration.
//
// It refuses an offset that is not on the last sweep's curve. That is not
// bureaucracy: this route exists to record a MEASUREMENT, and an arbitrary
// number posted to it would be stored with the provenance of one. An operator
// who wants to set an offset by hand can — on the settings page, where it is
// plainly an edit rather than a measurement.
func (s *server) calApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		OffsetHz *int `json:"offset_hz"`
	}
	_ = decodeJSON(r, &body)

	st, err := config.GetCalibrationState(s.store)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if st.Result == nil || st.Result.Best == nil {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": "no sweep has produced an offset on this node yet",
		})
		return
	}

	offset := st.Result.Best.OffsetHz
	if body.OffsetHz != nil {
		offset = *body.OffsetHz
		if !measuredOffset(st.Result, offset) {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{
				"error": fmt.Sprintf("%+d Hz is not a frequency the last sweep measured; pick a point on the curve, or set the offset by hand on the settings page", offset),
			})
			return
		}
	}

	adoption, err := config.ApplyOffset(s.store, offset, "calibration")
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	st.AppliedOffsetHz, st.AppliedAt = &offset, now
	if err := config.SetCalibrationState(s.store, st, "calibration"); err != nil {
		log.Printf("cal: record the applied offset: %v", err)
	}
	if s.cal != nil {
		s.cal.event("calibration_applied", "", fmt.Sprintf("offset %+d Hz applied to RX and TX", offset))
	}
	writeJSON(w, map[string]any{
		"status":    "applied",
		"offset_hz": offset,
		"changed":   adoption.Changed,
		"detail":    "restart the stack, or apply the configuration, for MMDVM-Host to pick this up",
	})
}

// measuredOffset reports whether an offset is a point the sweep actually
// visited AND heard something on.
func measuredOffset(res *cal.Result, offset int) bool {
	for _, p := range res.Points {
		if p.OffsetHz == offset {
			return p.Heard
		}
	}
	return false
}

// --- the repeater workflow -----------------------------------------------

// calTransmit runs one bounded transmit test.
//
// This is the route that can key a radio, and every guard on it lives in
// internal/cal rather than here: the state must be one that transmits, the
// frequency comes from the config and must be in an amateur allocation, and the
// burst is bounded by a dead-man timer the handler cannot extend. What this
// layer adds is the arbitration — the same one-operation-at-a-time token a
// sweep and a flash take.
func (s *server) calTransmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Test     string `json:"test"`
		Seconds  int    `json:"seconds"`
		StopHost bool   `json:"stop_host"`
	}
	_ = decodeJSON(r, &body)

	state, ok := calTests[body.Test]
	if !ok {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("unknown transmit test %q", body.Test),
			"tests": calTestNames(),
		})
		return
	}

	opt, err := s.calTarget()
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	opt.StopHost = body.StopHost
	// Checked here as well as in the session so the refusal is a 400 with a
	// sentence, rather than a session that opens, stops MMDVM-Host, and only
	// then discovers the frequency is not one it may transmit on.
	if err := cal.CheckTXFrequency(opt.TXFreqHz); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	release, busy := s.hwOps.acquire("calibration transmit test")
	if busy != "" {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": busy + " is already running; only one hardware operation runs at a time",
		})
		return
	}
	defer release()

	d := time.Duration(body.Seconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), cal.MaxBurst+30*time.Second)
	defer cancel()

	sess, err := cal.Open(ctx, opt)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, modem.ErrHostRunning) {
			status = http.StatusConflict
		}
		writeJSONStatus(w, status, map[string]any{"error": err.Error()})
		return
	}
	defer sess.Close()

	if s.cal != nil {
		s.cal.event("calibration_transmit", opt.Port, fmt.Sprintf("%s on %.4f MHz", state, float64(opt.TXFreqHz)/1e6))
	}
	burst, err := sess.Transmit(ctx, state, d)
	if err != nil {
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"status": "done",
		"burst":  burst,
		"detail": fmt.Sprintf("transmitted %s for %s", state, burst.Duration.Round(time.Second)),
	})
}

// calListen reports what the modem hears — the receive half of the repeater
// workflow (RFC-0021 §6).
//
// It does not transmit. What it returns is the modem's own level report, and
// the useful field in it is Inverted: RX inversion is the ONE analog setting a
// board can simply be asked about, where every other one needs a deviation
// meter. A hotspot sends no level replies at all, and that is reported as such
// rather than as an empty panel.
func (s *server) calListen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Seconds  int  `json:"seconds"`
		StopHost bool `json:"stop_host"`
	}
	_ = decodeJSON(r, &body)

	opt, err := s.calTarget()
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	opt.StopHost = body.StopHost

	release, busy := s.hwOps.acquire("calibration level check")
	if busy != "" {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": busy + " is already running; only one hardware operation runs at a time",
		})
		return
	}
	defer release()

	d := time.Duration(body.Seconds) * time.Second
	if d <= 0 {
		d = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), d+30*time.Second)
	defer cancel()

	sess, err := cal.Open(ctx, opt)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, modem.ErrHostRunning) {
			status = http.StatusConflict
		}
		writeJSONStatus(w, status, map[string]any{"error": err.Error()})
		return
	}
	defer sess.Close()

	reports, err := sess.Listen(ctx, d)
	if errors.Is(err, cal.ErrNothingHeard) {
		writeJSON(w, map[string]any{
			"status": "nothing_heard",
			"detail": "the modem reported no levels. Full-size MMDVM boards report them while a signal is present; hotspot boards never do, so there is nothing to read here on a hat.",
		})
		return
	}
	if err != nil {
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	last := reports[len(reports)-1]
	writeJSON(w, map[string]any{
		"status":   "ok",
		"reports":  reports,
		"latest":   last,
		"inverted": last.Inverted,
		"detail": fmt.Sprintf("%d level reports; the signal arrived %s",
			len(reports), map[bool]string{true: "INVERTED — set RX Invert", false: "the right way up"}[last.Inverted]),
	})
}

// calTests are the transmit tests offered, named rather than numbered so a
// request body cannot ask for a modem state by integer.
var calTests = map[string]cal.State{
	"dmr_deviation":     cal.StateDMRCal,
	"dmr_test_pattern":  cal.StateDMRDMO1K,
	"dstar_tone":        cal.StateDStarCal,
	"low_frequency":     cal.StateLFCal,
	"pocsag_pattern":    cal.StatePOCSAGCal,
	"p25_test_pattern":  cal.StateP25Cal1K,
	"nxdn_test_pattern": cal.StateNXDNCal1K,
}

func calTestNames() []string {
	out := make([]string, 0, len(calTests))
	for k := range calTests {
		out = append(out, k)
	}
	return out
}

// --- small helpers --------------------------------------------------------

func atoiDefault(s string, d int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return d
}

func atofDefault(s string, d float64) float64 {
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return d
}
