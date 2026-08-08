package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/bootcfg"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/modem"
	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/stackupdate"
)

// The hardware surface (#18): what modem is attached, what it says it is, and
// what this node has been configured to believe.
//
// Four routes, matching the four things an operator does:
//
//	GET  /api/hardware        → the last detection, the board table, the
//	                            configured hardware, the GPIO UART's
//	                            availability, and every disagreement between them
//	POST /api/hardware/detect → probe now; {"stop_host": true} authorises taking
//	                            the port from a running MMDVM-Host
//	POST /api/hardware/adopt  → write the last detection into the config,
//	                            optionally naming which candidate board it is
//	POST /api/hardware/uart   → free the GPIO serial port from Bluetooth and the
//	                            login console, so a fitted hat can be heard at all
//
// Detect and adopt are separate on purpose. Probing is a read of the world;
// writing the config is a change to the node. Fusing them would mean a button
// labelled "have a look" silently reconfigures a working node — and it would
// make the ambiguous case (several boards share one identity string) unanswerable,
// because there would be nowhere to put the answer.

// hardwareProbeTimeout bounds a whole detection run. The sweep is a handful of
// ports at a second or two each, and the ceiling exists so a wedged USB device
// cannot hold an HTTP handler open indefinitely.
const hardwareProbeTimeout = 60 * time.Second

// mmdvmHolder is the systemd side of modem.Holder: MMDVM-Host, the process that
// owns the modem port whenever the node is on the air. It exists here rather
// than in internal/modem so that package never imports systemd.
type mmdvmHolder struct{}

func (mmdvmHolder) Active(context.Context) (bool, error) {
	out, _ := systemctlRun("is-active", stackupdate.MMDVMUnit)
	// is-active exits non-zero for every non-active state, so the word is the
	// answer and the exit code is not. An absent unit reports "inactive" or
	// "unknown"; both mean nothing holds the port.
	switch strings.TrimSpace(string(out)) {
	case "active", "activating", "reloading":
		return true, nil
	}
	return false, nil
}

func (mmdvmHolder) Stop(context.Context) error {
	if out, err := systemctlRun("stop", stackupdate.MMDVMUnit); err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

func (mmdvmHolder) Start(context.Context) error {
	if out, err := systemctlRun("start", stackupdate.MMDVMUnit); err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

// boardView is one row of the picker.
type boardView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TCXOHz    int    `json:"tcxo_hz,omitempty"`
	TCXOLabel string `json:"tcxo_label,omitempty"`
	Duplex    bool   `json:"duplex"`
	Transport string `json:"transport"`
	Verified  bool   `json:"verified"`
	Note      string `json:"note,omitempty"`
}

// hardwareView is what GET /api/hardware serves.
type hardwareView struct {
	Detected   config.HardwareState     `json:"detected"`
	Boards     []boardView              `json:"boards"`
	Configured hardwareConfigured       `json:"configured"`
	Warnings   []config.HardwareWarning `json:"warnings"`
	// Panel is the last character-display detection (#136). It rides on the
	// hardware surface rather than a route of its own because this is the payload
	// the settings UI already loads on every visit, and the LCD tab's question —
	// "is there a display here that the config does not mention?" — is answered by
	// comparing these two records, not by a second fetch.
	Panel config.PanelState `json:"panel"`
	// UART is the GPIO serial port's availability on a Raspberry Pi. It is the
	// answer to the question detection cannot answer for itself: a hat on a
	// stock Raspberry Pi OS install is electrically fine and completely mute,
	// because Bluetooth owns the UART and a login console is sitting on it.
	// Omitted entirely on a host where that cannot be the problem.
	UART *bootcfg.Report `json:"uart,omitempty"`
}

// hardwareConfigured is the operator's answer, echoed back so the UI never has
// to cross-reference two endpoints to render one panel.
type hardwareConfigured struct {
	Port      string `json:"port"`
	UARTSpeed string `json:"uart_speed"`
	Board     string `json:"board"`
	BoardName string `json:"board_name,omitempty"`
	TCXOHz    string `json:"tcxo_hz"`
	TCXOLabel string `json:"tcxo_label,omitempty"`
}

func boardViews() []boardView {
	out := make([]boardView, 0, len(modem.Boards))
	for _, b := range modem.Boards {
		out = append(out, boardView{
			ID: b.ID, Name: b.Name, TCXOHz: b.TCXOHz, TCXOLabel: modem.TCXOLabel(b.TCXOHz),
			Duplex: b.Dual, Transport: b.Transport.String(), Verified: b.Verified, Note: b.Note,
		})
	}
	return out
}

// hardware serves GET /api/hardware.
func (s *server) hardware(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	v, err := s.hardwareSnapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

func (s *server) hardwareSnapshot() (hardwareView, error) {
	st, err := config.GetHardwareState(s.store)
	if err != nil {
		return hardwareView{}, err
	}
	m, err := config.Load(s.store)
	if err != nil {
		return hardwareView{}, err
	}
	cfg := hardwareConfigured{
		Port: m.Modem.Port, UARTSpeed: m.Modem.UARTSpeed,
		Board: m.Modem.Board, TCXOHz: m.Modem.TCXOHz,
	}
	if b, ok := modem.BoardByID(m.Modem.Board); ok {
		cfg.BoardName = b.Name
	}
	if hz, err := strconv.Atoi(m.Modem.TCXOHz); err == nil {
		cfg.TCXOLabel = modem.TCXOLabel(hz)
	}
	// A missing panel record is the never-looked state, not an error: this surface
	// predates panel detection, so every node upgraded into it has one.
	panel, err := config.GetPanelState(s.store)
	if err != nil {
		return hardwareView{}, err
	}
	return hardwareView{
		Detected:   st,
		Boards:     boardViews(),
		Configured: cfg,
		Warnings:   config.HardwareWarnings(m, st),
		Panel:      panel,
		UART:       uartReport(),
	}, nil
}

// uartReport diagnoses the GPIO serial port, or returns nil on a host where that
// question does not apply.
//
// The diagnosis runs on every view rather than being cached, because it is three
// small file reads and one systemctl call, and because the state it reports is
// exactly the state an operator changes out from under Waypoint — by editing
// config.txt themselves, or by rebooting after a repair.
func uartReport() *bootcfg.Report {
	r := bootcfg.Inspect(bootcfg.Options{UnitEnabled: unitEnabled})
	if !r.Applicable {
		return nil
	}
	return &r
}

// unitEnabled reports whether a systemd unit would start. Anything other than a
// clean disabled/masked/absent counts, because the states in between (static,
// indirect, generated) are all ones systemd will happily start a getty from.
func unitEnabled(unit string) bool {
	out, err := systemctlRun("is-enabled", unit)
	if err != nil && len(out) == 0 {
		return false
	}
	switch strings.TrimSpace(string(out)) {
	case "", "masked", "masked-runtime", "disabled", "not-found":
		return false
	}
	return true
}

// hardwareUART serves POST /api/hardware/uart: free the GPIO serial port for the
// modem.
//
// It is a separate, explicitly-invoked route rather than something detection does
// on its own. The repair edits the boot partition and masks a system service, and
// it needs a reboot to take effect — none of which belongs behind a button that
// says "look for my modem".
func (s *server) hardwareUART(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.prov == nil {
		// Demo mode, or a node whose helper is not running. Reporting the
		// problem without being able to fix it is still worth doing, so this is
		// a clean refusal rather than a crash.
		http.Error(w, "the privileged helper is not available on this node", http.StatusServiceUnavailable)
		return
	}
	before := uartReport()
	if before == nil {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": "this host has no Raspberry Pi boot partition, so there is no GPIO serial port to free",
		})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()

	resp, err := s.prov.EnableModemUART(ctx, privhelper.EnableModemUARTRequest{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"result": resp, "uart": uartReport()})
}

// hardwareDetect serves POST /api/hardware/detect.
func (s *server) hardwareDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		StopHost bool `json:"stop_host"`
	}
	// An empty body is a plain "look, but do not disturb anything".
	_ = decodeJSON(r, &body)

	// One hardware operation at a time (#19). Detection and a firmware flash want
	// the same port, and a stack update health-gates on MMDVM-Host being up while
	// both of the others stop it. Refusing beats queuing: "a firmware flash is
	// running" is something an operator can act on, where a request that blocks
	// for four minutes just looks like a hung dashboard.
	release, busy := s.hwOps.acquire("detection")
	if busy != "" {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": busy + " is already running; only one hardware operation runs at a time",
		})
		return
	}
	defer release()

	// The probe's deadline is its own, not the request's. A detection that
	// stopped MMDVM-Host has to be allowed to finish and restart it even if the
	// client hung up — the arbitration in internal/modem restarts on any exit,
	// but only if it is reached.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), hardwareProbeTimeout)
	defer cancel()

	res, err := s.newDetector().Detect(ctx, body.StopHost)
	if errors.Is(err, modem.ErrHostRunning) {
		// 409, not 500: the node is working exactly as designed and the caller
		// has a way to proceed. The UI turns this into "stop the modem and
		// detect anyway?".
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error":     err.Error(),
			"stoppable": true,
			"unit":      stackupdate.MMDVMUnit,
		})
		return
	}
	if err != nil && !errors.Is(err, modem.ErrNoModem) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// A run that found nothing is still stored. "We looked here and here, and
	// nothing answered" is the state an operator most needs to see, and losing
	// it means the panel goes blank exactly when it has something to say.
	st, _ := config.GetHardwareState(s.store)
	st.Identity, st.Scanned, st.Bootloader = res.Identity, res.Scanned, res.Bootloader
	st.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	if err := config.SetHardwareState(s.store, st, "api"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	v, err := s.hardwareSnapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// hardwareAdopt serves POST /api/hardware/adopt: write the last detection into
// the modem config. This is the crossing issue #136 is about — a detection that
// never reaches the configuration is a detection that did nothing.
func (s *server) hardwareAdopt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		BoardID string `json:"board_id"`
	}
	_ = decodeJSON(r, &body)

	st, err := config.GetHardwareState(s.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if st.Identity == nil {
		http.Error(w, "no modem has been detected on this node yet", http.StatusConflict)
		return
	}
	adoption, err := config.AdoptDetection(s.store, *st.Identity, body.BoardID, "api")
	if errors.Is(err, config.ErrAmbiguousBoard) {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error":      err.Error(),
			"candidates": st.Identity.Candidates,
		})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	st.AdoptedAt = time.Now().UTC().Format(time.RFC3339)
	st.AdoptedDescription = st.Identity.Description
	if err := config.SetHardwareState(s.store, st, "api"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	v, err := s.hardwareSnapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"adopted": adoption, "hardware": v})
}

// newDetector builds the probe with the node's real surroundings: systemd as the
// port holder, and the display port excluded from the sweep. That exclusion is
// the one that matters — a Nextion is a serial device on exactly the kind of
// port detection would otherwise walk into.
func (s *server) newDetector() *modem.Detector {
	var exclude []string
	if m, err := config.Load(s.store); err == nil {
		if p := m.Display.Port; p != "" && p != "None" && p != "modem" {
			exclude = append(exclude, p)
		}
	}
	return &modem.Detector{
		Scanner: modem.Scanner{Exclude: exclude},
		Holder:  mmdvmHolder{},
	}
}
