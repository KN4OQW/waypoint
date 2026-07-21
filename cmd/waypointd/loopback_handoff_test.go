package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/store"
)

// loopback_handoff_test.go asserts the RFC-0003 Addendum A apply contract on the
// apply PLAN — the ordered systemctl action set — with a faked systemctl (there is
// no systemd under `go test`): §8.4 stop-before-start for a displacing bus, §8.5
// cleanup + robust stop (activating/failed), and §7 boot enable/disable.

type op struct{ verb, unit string }

// applyRecorder runs one apply against a temp store/paths, recording every
// systemctl call in order. active names units is-active reports as running (any
// non-inactive word, e.g. "activating", proves the robust stop path).
func applyRecorder(t *testing.T, m *config.Model, activeState map[string]string) (ops []op, dir string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if len(m.Buses) > 0 {
		mustSet(t, st, "buses", m.Buses)
	}
	if len(m.Attachments) > 0 {
		mustSet(t, st, "attachments", m.Attachments)
	}

	orig := systemctlRun
	systemctlRun = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "is-active":
			state := activeState[args[len(args)-1]]
			if state == "" {
				state = "inactive"
			}
			return []byte(state + "\n"), nil
		case "list-units", "list-unit-files":
			return []byte(activeState["__orphans__"]), nil
		default:
			ops = append(ops, op{args[0], args[len(args)-1]})
			return nil, nil
		}
	}
	t.Cleanup(func() { systemctlRun = orig })

	dir = t.TempDir()
	s := &server{store: st, paths: config.Paths{
		MMDVM: dir + "/MMDVM-Host.ini", DMRGateway: dir + "/DMRGateway.ini",
		YSFGateway: dir + "/YSFGateway.ini", P25Gateway: dir + "/P25Gateway.ini",
		NXDNGateway: dir + "/NXDNGateway.ini", DStarGateway: dir + "/dstargateway.cfg",
		M17Gateway: dir + "/M17Gateway.ini", BusConfigDir: dir,
	}}
	if _, _, err := s.applyRender("test"); err != nil {
		t.Fatalf("applyRender: %v", err)
	}
	return ops, dir
}

func mustSet(t *testing.T, st *store.Store, key string, v any) {
	t.Helper()
	if err := st.Set(key, v, "test"); err != nil {
		t.Fatal(err)
	}
}

func indexOf(ops []op, verb, unitSub string) int {
	for i, o := range ops {
		if o.verb == verb && strings.Contains(o.unit, unitSub) {
			return i
		}
	}
	return -1
}

// TestApplyDisplaceStopsGatewayBeforeStartingBus is Addendum §8.4: a YSF bus binds
// the gateway's port, so the displaced gateway MUST be stopped before the bus
// starts (restart). With the YSF gateway running (active), the plan orders it.
func TestApplyDisplaceStopsGatewayBeforeStartingBus(t *testing.T) {
	m := &config.Model{
		Buses:       []config.Bus{{ID: "a", Enabled: true}},
		Attachments: []config.Attachment{{BusID: "a", Mode: config.ModeYSF}, {BusID: "a", Mode: config.ModeNXDN}},
	}
	ops, _ := applyRecorder(t, m, map[string]string{
		"waypoint-ysfgateway.service":  "active",
		"waypoint-nxdngateway.service": "active",
	})

	stopYSF := indexOf(ops, "stop", "ysfgateway")
	stopNXDN := indexOf(ops, "stop", "nxdngateway")
	startBus := indexOf(ops, "restart", "waypoint-bus@a")
	if stopYSF < 0 || stopNXDN < 0 || startBus < 0 {
		t.Fatalf("missing ops: stopYSF=%d stopNXDN=%d startBus=%d\n%v", stopYSF, stopNXDN, startBus, ops)
	}
	if stopYSF > startBus || stopNXDN > startBus {
		t.Fatalf("displaced gateway must stop BEFORE the bus starts; stopYSF=%d stopNXDN=%d startBus=%d", stopYSF, stopNXDN, startBus)
	}
	// And the bus is enabled for boot (§7), while the displaced gateways are DISABLED
	// so they do not race the bus for the loopback on reboot.
	if indexOf(ops, "enable", "waypoint-bus@a") < 0 {
		t.Fatalf("the enabled bus must be enabled for boot; ops=%v", ops)
	}
	if indexOf(ops, "disable", "ysfgateway") < 0 || indexOf(ops, "disable", "nxdngateway") < 0 {
		t.Fatalf("displaced gateways must be disabled for boot; ops=%v", ops)
	}
}

// TestApplyReconcilesOrphanedBusUnit is Addendum §6 / D5: a DELETED bus's row is
// gone from the model, so its still-running unit is found via systemd and stopped
// + disabled (else it would hold the mode's loopback and crash-loop a restored
// gateway). Here the model has NO buses but systemd reports waypoint-bus@ghost.
func TestApplyReconcilesOrphanedBusUnit(t *testing.T) {
	m := &config.Model{} // no buses: the ghost bus was deleted
	ops, _ := applyRecorder(t, m, map[string]string{
		"waypoint-bus@ghost.service": "active",
		"__orphans__":                "waypoint-bus@ghost.service loaded active running Waypoint bus ghost\n",
	})
	if indexOf(ops, "stop", "waypoint-bus@ghost") < 0 {
		t.Fatalf("a deleted bus's orphaned unit must be stopped; ops=%v", ops)
	}
	if indexOf(ops, "disable", "waypoint-bus@ghost") < 0 {
		t.Fatalf("a deleted bus's orphaned unit must be disabled for boot; ops=%v", ops)
	}
}

// TestApplyMultiplexNoGatewayStop is Addendum §8.4: a DMR bus multiplexes on a
// dedicated port, so no gateway is displaced and no stop-before-start is imposed —
// DMRGateway is restarted and the bus started, no DMR gateway stop.
func TestApplyMultiplexNoGatewayStop(t *testing.T) {
	m := &config.Model{
		Buses:       []config.Bus{{ID: "a", Enabled: true}},
		Attachments: []config.Attachment{{BusID: "a", Mode: config.ModeDMR, Slot: "2", DefaultTG: "91"}, {BusID: "a", Mode: config.ModeYSF}},
	}
	ops, dir := applyRecorder(t, m, map[string]string{"waypoint-ysfgateway.service": "active"})

	// DMRGateway is a restart target (it carries the bus network now), never stopped.
	if indexOf(ops, "stop", "dmrgateway") >= 0 {
		t.Fatalf("DMR multiplex must NOT stop DMRGateway; ops=%v", ops)
	}
	if indexOf(ops, "restart", "dmrgateway") < 0 {
		t.Fatalf("DMRGateway should be restarted to pick up the bus network; ops=%v", ops)
	}
	// The rendered DMRGateway.ini carries the bus network on the reserved port.
	ini, _ := os.ReadFile(filepath.Join(dir, "DMRGateway.ini"))
	if !strings.Contains(string(ini), "Name=Bus_a") || !strings.Contains(string(ini), "Address=127.0.0.1") {
		t.Fatalf("DMRGateway.ini missing the bus network:\n%s", ini)
	}
}

// TestApplyDisableCleansUpAndStopsCrashLoop is Addendum §8.5 / D5: disabling a bus
// deletes its rendered config, stops AND disables its unit, and the stop fires even
// when the unit is `activating` (crash-looping) — the case the old is-active gate
// skipped.
func TestApplyDisableCleansUpAndStopsCrashLoop(t *testing.T) {
	// First apply enables the bus and writes its config.
	m := &config.Model{
		Buses:       []config.Bus{{ID: "a", Enabled: true}},
		Attachments: []config.Attachment{{BusID: "a", Mode: config.ModeDMR, Slot: "2"}, {BusID: "a", Mode: config.ModeYSF}},
	}
	_, dir := applyRecorder(t, m, nil)
	cfg := filepath.Join(dir, "waypoint-bus-a.json")
	// Simulate the enabled-bus config being present (the first apply wrote it into a
	// throwaway dir); write a stale one plus an orphan to prove the sweep.
	_ = os.WriteFile(cfg, []byte("{}"), 0o644)
	orphan := filepath.Join(dir, "waypoint-bus-ghost.json")
	_ = os.WriteFile(orphan, []byte("{}"), 0o644)

	// Now the bus is disabled. Its unit is crash-looping (activating) — the robust
	// stop must still stop it.
	m.Buses[0].Enabled = false
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mustSet(t, st, "buses", m.Buses)
	mustSet(t, st, "attachments", m.Attachments)

	var ops []op
	orig := systemctlRun
	systemctlRun = func(args ...string) ([]byte, error) {
		if args[0] == "is-active" {
			if strings.Contains(args[len(args)-1], "waypoint-bus@a") {
				return []byte("activating\n"), nil // crash-looping
			}
			return []byte("inactive\n"), nil
		}
		ops = append(ops, op{args[0], args[len(args)-1]})
		return nil, nil
	}
	t.Cleanup(func() { systemctlRun = orig })

	s := &server{store: st, paths: config.Paths{
		MMDVM: dir + "/MMDVM-Host.ini", DMRGateway: dir + "/DMRGateway.ini",
		YSFGateway: dir + "/YSFGateway.ini", P25Gateway: dir + "/P25Gateway.ini",
		NXDNGateway: dir + "/NXDNGateway.ini", DStarGateway: dir + "/dstargateway.cfg",
		M17Gateway: dir + "/M17Gateway.ini", BusConfigDir: dir,
	}}
	if _, _, err := s.applyRender("test"); err != nil {
		t.Fatalf("applyRender: %v", err)
	}

	if indexOf(ops, "stop", "waypoint-bus@a") < 0 {
		t.Fatalf("a crash-looping (activating) disabled bus unit must be stopped; ops=%v", ops)
	}
	if indexOf(ops, "disable", "waypoint-bus@a") < 0 {
		t.Fatalf("a disabled bus unit must be disabled for boot; ops=%v", ops)
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("disabled bus config file must be deleted, still present: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("stale orphan bus config must be swept, still present: %v", err)
	}
}
