package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"github.com/KN4OQW/waypoint/internal/minisign"
	"github.com/KN4OQW/waypoint/internal/updater"
)

// writeJSONStatus writes v as JSON with an explicit status code.
func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	writeJSON(w, v)
}

// defaultUpdateURL is the signed release manifest. An operator on a private
// channel overrides it with -update-url and a matching -release-pubkey.
const defaultUpdateURL = "https://github.com/KN4OQW/waypoint/releases/latest/download/update.json"

// updateConfig is everything the update modes need: where the manifest lives and
// the key that signs it, plus the OS seams (binary path, unit, health URL, marker).
type updateConfig struct {
	url       string
	pubKey    minisign.PublicKey
	hasPubKey bool
	platform  string // runtime GOOS/GOARCH, e.g. "linux/arm64"
	sys       *updater.OSSystem
	timeout   time.Duration // health-confirm window
	interval  time.Duration // health poll interval
}

// newUpdateConfig assembles the config from the CLI flags. The health URL is a
// loopback self-probe of this node's own /api/health (RFC-0012 self-signed cert).
func newUpdateConfig(url, pubPath, binary, unit, marker, addr string, useTLS bool) updateConfig {
	cfg := updateConfig{
		url:      url,
		platform: runtime.GOOS + "/" + runtime.GOARCH,
		timeout:  90 * time.Second,
		interval: 2 * time.Second,
		sys: &updater.OSSystem{
			BinaryPath: binary,
			Unit:       unit,
			MarkerPath: marker,
			HealthURL:  healthProbeURL(addr, useTLS),
		},
	}
	if pubPath != "" {
		b, err := os.ReadFile(pubPath)
		if err != nil {
			log.Printf("update: cannot read release pubkey %s: %v (manifest unverified)", pubPath, err)
			return cfg
		}
		pk, err := minisign.ParsePublicKey(string(b))
		if err != nil {
			log.Printf("update: bad release pubkey %s: %v (manifest unverified)", pubPath, err)
			return cfg
		}
		cfg.pubKey, cfg.hasPubKey = pk, true
	}
	return cfg
}

// healthProbeURL turns the listen address into a loopback probe URL. A wildcard or
// unspecified host becomes 127.0.0.1 so we always probe ourselves, not the world.
func healthProbeURL(addr string, useTLS bool) string {
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return scheme + "://127.0.0.1:8073/api/health"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return scheme + "://" + net.JoinHostPort(host, port) + "/api/health"
}

// plan fetches + verifies the manifest and derives the decision for this node.
func (cfg updateConfig) plan(ctx context.Context) (updater.Plan, updater.Manifest, error) {
	m, err := updater.FetchManifest(ctx, cfg.url, cfg.pubKey, cfg.hasPubKey)
	if err != nil {
		return updater.Plan{}, updater.Manifest{}, err
	}
	return updater.PlanUpdate(Version, m, cfg.platform), m, nil
}

// runUpdateCheck implements `waypointd -update-check`: report whether a newer
// applicable release exists and exit, changing nothing.
func runUpdateCheck(cfg updateConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	plan, _, err := cfg.plan(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update-check: %v\n", err)
		os.Exit(2)
	}
	if !plan.Available {
		fmt.Printf("update-check: no update — %s\n", plan.Reason)
		os.Exit(0)
	}
	fmt.Printf("update-check: %s available (running %s)\n", plan.Version, Version)
	if plan.NotesURL != "" {
		fmt.Printf("  notes: %s\n", plan.NotesURL)
	}
	os.Exit(0)
}

// runUpdate implements `waypointd -update`: the full transactional install. It runs
// as a standalone process so it survives the service restart it triggers (the
// running daemon is a different process it restarts out from under itself).
func runUpdate(cfg updateConfig) {
	ctx := context.Background()
	plan, _, err := cfg.plan(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update: %v\n", err)
		os.Exit(2)
	}
	if !plan.Available {
		fmt.Printf("update: nothing to do — %s\n", plan.Reason)
		os.Exit(0)
	}
	fmt.Printf("update: fetching %s for %s…\n", plan.Version, cfg.platform)
	data, err := updater.DownloadArtifact(ctx, plan.Artifact, cfg.pubKey, cfg.hasPubKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update: download/verify failed (nothing changed): %v\n", err)
		os.Exit(1)
	}
	out, err := updater.Apply(ctx, plan, data, cfg.sys, cfg.timeout, cfg.interval)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update: FAILED: %v\n", err)
		os.Exit(1)
	}
	switch {
	case out.Confirmed:
		fmt.Printf("update: confirmed — now running %s\n", out.Version)
	case out.Reverted:
		fmt.Printf("update: reverted to the prior version — %s\n", out.Reason)
		os.Exit(1)
	}
	os.Exit(0)
}

// runUpdateBootCheck implements `waypointd -update-boot-check`, the service's
// ExecStartPre: revert an update that was swapped but never confirmed (power lost
// mid-update), so the unit then starts the prior version.
func runUpdateBootCheck(cfg updateConfig) {
	out, err := updater.BootCheck(cfg.sys, 1)
	if err != nil {
		// A boot-check failure must not wedge startup: log and let the unit start.
		fmt.Fprintf(os.Stderr, "update-boot-check: %v\n", err)
		os.Exit(0)
	}
	if out.Reverted {
		fmt.Printf("update-boot-check: %s\n", out.Reason)
	}
	os.Exit(0)
}

// --- API surface (behind the session wall) ---

// updateCheck handles GET /api/update/check: fetch + verify the manifest and
// report availability without changing anything.
func (s *server) updateCheck(w http.ResponseWriter, r *http.Request) {
	if s.update == nil {
		http.Error(w, "updates not configured", http.StatusNotImplemented)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	plan, _, err := s.update.plan(ctx)
	if err != nil {
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"current":   Version,
		"available": plan.Available,
		"version":   plan.Version,
		"notes_url": plan.NotesURL,
		"reason":    plan.Reason,
	})
}

// updateApply handles POST /api/update/apply: launch the standalone `-update`
// applier detached, so it outlives the service restart it performs, and return
// 202. The caller watches /api/health (and GET /api/update/check) for the result.
func (s *server) updateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.update == nil {
		http.Error(w, "updates not configured", http.StatusNotImplemented)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": "cannot locate own binary: " + err.Error()})
		return
	}
	cmd := exec.Command(exe, s.updateArgs...)
	// Detach: new session so a service restart of *this* process does not kill the
	// applier mid-swap. Its progress is observable via the systemd journal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": "cannot start updater: " + err.Error()})
		return
	}
	_ = cmd.Process.Release()
	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"status": "started",
		"detail": "update running; watch /api/health for the new version, or GET /api/update/check",
	})
}
