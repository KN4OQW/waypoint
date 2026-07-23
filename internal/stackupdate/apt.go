package stackupdate

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// OSSystem is the real System: apt for package moves, systemctl for the services,
// and the History table for the audit/revert trail. Like updater.OSSystem it is
// thin glue — the tested logic is the engine's state machine. Every exec is behind
// a swappable func so even this layer's command construction is unit-checkable.
type OSSystem struct {
	// SourcesDir holds ONLY the waypoint deb822 source, so the check's apt-get
	// update / apt list is limited to the Waypoint origin (D2 — waypointd never
	// triggers an OS-wide apt refresh). Empty falls back to the real sources.
	SourcesDir string
	Hist       *History

	// Run executes a command and returns combined output; Systemctl is the same for
	// systemctl. Both default to exec.CommandContext and are replaced in tests.
	Run       func(ctx context.Context, name string, args ...string) ([]byte, error)
	Systemctl func(ctx context.Context, args ...string) ([]byte, error)
}

// aptEnv forces non-interactive apt so an install never blocks on a prompt.
var aptEnv = []string{"DEBIAN_FRONTEND=noninteractive"}

func (s *OSSystem) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.Run != nil {
		return s.Run(ctx, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(cmd.Environ(), aptEnv...)
	return cmd.CombinedOutput()
}

func (s *OSSystem) systemctl(ctx context.Context, args ...string) ([]byte, error) {
	if s.Systemctl != nil {
		return s.Systemctl(ctx, args...)
	}
	return exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
}

// sourceLimit is the apt option pair that restricts a command to the Waypoint
// source only — SourceList=/dev/null disables the main list, SourceParts=<dir>
// enables only the deb822 sources in our dir (D2). Empty SourcesDir → no limit.
func (s *OSSystem) sourceLimit() []string {
	if s.SourcesDir == "" {
		return nil
	}
	return []string{
		"-o", "Dir::Etc::SourceList=/dev/null",
		"-o", "Dir::Etc::SourceParts=" + s.SourcesDir,
	}
}

// AptRefresh runs `apt-get update` limited to the Waypoint source (D2).
func (s *OSSystem) AptRefresh(ctx context.Context) error {
	args := append([]string{"update", "-qq"}, s.sourceLimit()...)
	if out, err := s.run(ctx, "apt-get", args...); err != nil {
		return fmt.Errorf("apt-get update: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Upgradable returns `apt list --upgradable` limited to the Waypoint source. Parse
// it with ParseUpgradable.
func (s *OSSystem) Upgradable(ctx context.Context) (string, error) {
	args := append([]string{"list", "--upgradable"}, s.sourceLimit()...)
	out, err := s.run(ctx, "apt", args...)
	if err != nil {
		return "", fmt.Errorf("apt list --upgradable: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// InstalledVersions returns each package's installed version ("" if not installed),
// from a single dpkg-query. dpkg-query exits non-zero when any named package is
// unknown, but still prints the known ones, so its output is parsed regardless.
func (s *OSSystem) InstalledVersions(ctx context.Context, pkgs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range pkgs {
		out[p] = ""
	}
	if len(pkgs) == 0 {
		return out, nil
	}
	args := append([]string{"-W", "-f=${Package} ${Version}\n"}, pkgs...)
	raw, _ := s.run(ctx, "dpkg-query", args...)
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			if _, want := out[f[0]]; want {
				out[f[0]] = f[1]
			}
		}
	}
	return out, nil
}

// Install installs exactly the given package=version pairs. --allow-downgrades so
// the revert path can move to an older pool/ version; the exact pins mean apt
// touches only these packages, never a dist-upgrade (D2).
func (s *OSSystem) Install(ctx context.Context, pkgs []PkgVer) error {
	if len(pkgs) == 0 {
		return nil
	}
	args := []string{"install", "-y", "--allow-downgrades", "--no-install-recommends"}
	for _, p := range pkgs {
		args = append(args, p.Package+"="+p.Version)
	}
	if out, err := s.run(ctx, "apt-get", args...); err != nil {
		return fmt.Errorf("%v: %s", err, lastLines(out, 4))
	}
	return nil
}

// StopServices stops the units (best effort per unit; a stop failure is surfaced).
func (s *OSSystem) StopServices(ctx context.Context, units []string) error {
	return s.act(ctx, "stop", units)
}

// StartServices restarts the units (restart, not start, so a unit already up from
// a partial run is cycled onto the new binary).
func (s *OSSystem) StartServices(ctx context.Context, units []string) error {
	return s.act(ctx, "restart", units)
}

func (s *OSSystem) act(ctx context.Context, verb string, units []string) error {
	for _, u := range units {
		if u == "" {
			continue
		}
		if out, err := s.systemctl(ctx, verb, u); err != nil {
			return fmt.Errorf("systemctl %s %s: %v: %s", verb, u, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// Healthy is one poll of the gate: every unit is active, and — the modem-open
// signal — MMDVMHost's unit is active with SubState=running (not auto-restart /
// failed). MMDVMHost exits(1) when the modem will not open, so a unit that is not
// cleanly running is the real "modem did not open" signal, no log scraping needed.
func (s *OSSystem) Healthy(ctx context.Context, units []string) (bool, string) {
	for _, u := range units {
		if u == "" {
			continue
		}
		if _, err := s.systemctl(ctx, "is-active", "--quiet", u); err != nil {
			return false, u + " is not active"
		}
		if u == MMDVMUnit {
			if sub := s.subState(ctx, u); sub != "running" {
				return false, fmt.Sprintf("%s SubState=%s (modem not open)", u, sub)
			}
		}
	}
	return true, ""
}

// subState reads a unit's systemd SubState ("running", "auto-restart", "failed", …).
func (s *OSSystem) subState(ctx context.Context, unit string) string {
	out, err := s.systemctl(ctx, "show", unit, "-p", "SubState", "--value")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// RecordHistory persists the version-pair rows (no-op if no history table wired).
func (s *OSSystem) RecordHistory(rows []HistoryRow) error {
	if s.Hist == nil {
		return nil
	}
	return s.Hist.Insert(rows)
}

// lastLines returns the last n non-empty lines of out, for compact error context.
func lastLines(out []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	var kept []string
	for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			kept = append([]string{lines[i]}, kept...)
		}
	}
	return strings.Join(kept, "; ")
}
