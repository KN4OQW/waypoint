// Package updater is Waypoint's transactional, health-gated update engine (RFC-0014
// / issue #13): a new release is verified (minisign, RFC-0013), staged, atomically
// swapped in with the old binary kept as a rollback, then health-checked — and
// reverted automatically if the new version does not come up healthy. A boot-time
// check reverts an update that was swapped but never confirmed (power pulled
// mid-update), so an update always completes or the prior version boots.
//
// Every side effect is behind the System interface, so the state machine is tested
// deterministically with a fake; the real os.go implementation is thin glue.
package updater

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Artifact is one architecture's binary in a manifest.
type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Manifest describes an available release. It is itself minisign-signed (verified
// on fetch), so an attacker cannot offer a downgrade or a malicious artifact URL.
type Manifest struct {
	Version    string              `json:"version"`
	MinVersion string              `json:"min_version"`
	NotesURL   string              `json:"notes_url"`
	Artifacts  map[string]Artifact `json:"artifacts"` // key "linux/arm", "linux/arm64", …
}

// Plan is the decision derived from a manifest for the running node.
type Plan struct {
	Available bool
	Version   string
	NotesURL  string
	Artifact  Artifact
	Reason    string // why not available/applicable, when Available is false
}

// PlanUpdate decides whether manifest applies to a node running `current` on
// `platform` (e.g. "linux/arm64"). A newer applicable version with an artifact for
// the platform is available; an equal/older version, a min_version above current,
// or a missing artifact is refused with a clear reason. A non-semver current (a
// dev build) is treated as older than any released version, so a dev node updates.
func PlanUpdate(current string, m Manifest, platform string) Plan {
	art, ok := m.Artifacts[platform]
	if !ok {
		return Plan{Reason: "no artifact for this platform (" + platform + ")"}
	}
	mv, err := parseVer(m.Version)
	if err != nil {
		return Plan{Reason: "manifest version is not valid: " + m.Version}
	}
	if m.MinVersion != "" {
		if minv, err := parseVer(m.MinVersion); err == nil {
			if cur, err := parseVer(current); err == nil && less(cur, minv) {
				return Plan{Reason: fmt.Sprintf("release %s requires at least %s (running %s)", m.Version, m.MinVersion, current)}
			}
		}
	}
	cur, curErr := parseVer(current)
	if curErr == nil && !less(cur, mv) {
		return Plan{Reason: "already up to date (" + current + ")"}
	}
	return Plan{Available: true, Version: m.Version, NotesURL: m.NotesURL, Artifact: art}
}

// Marker records an in-flight update so a power-loss can be recovered on boot.
type Marker struct {
	Version   string `json:"version"`    // the version being tried
	Rollback  string `json:"rollback"`   // path to the backed-up prior binary
	BootCount int    `json:"boot_count"` // boots into this update without confirmation
}

// Outcome is the result of Apply.
type Outcome struct {
	Confirmed bool   // the new version came up healthy and was committed
	Reverted  bool   // the new version failed and the prior one was restored
	Version   string // the version now running
	Reason    string // why it reverted, when Reverted
}

// System is every side effect the engine performs. The real implementation
// (os.go) does files/systemctl/http; tests inject a fake.
type System interface {
	StageBinary(data []byte) (stagePath string, err error) // write the new binary beside the live one
	BackupCurrent() (rollbackPath string, err error)       // copy the live binary aside
	Swap(stagePath string) error                           // atomic rename stagePath -> live
	Restart(ctx context.Context) error                     // restart the service
	Health(ctx context.Context) (version string, ok bool)  // probe the running node's health
	Restore(rollbackPath string) error                     // restore rollback -> live
	WriteMarker(Marker) error
	ReadMarker() (*Marker, error)
	ClearMarker() error
	Now() time.Time
}

// Apply performs the transactional install. The step ordering is the safety
// argument: nothing live is touched until the verified artifact is staged, the
// marker is durable before the swap, the swap is atomic, and the health check
// gates the commit with an automatic revert. Runs as a standalone invocation (so
// it survives the service restart it triggers).
func Apply(ctx context.Context, plan Plan, data []byte, sys System, healthTimeout, healthInterval time.Duration) (Outcome, error) {
	if !plan.Available {
		return Outcome{}, fmt.Errorf("updater: no update to apply: %s", plan.Reason)
	}
	stage, err := sys.StageBinary(data)
	if err != nil {
		return Outcome{}, fmt.Errorf("updater: stage: %w", err)
	}
	rollback, err := sys.BackupCurrent()
	if err != nil {
		return Outcome{}, fmt.Errorf("updater: backup: %w", err)
	}
	// Durable marker BEFORE the swap: if power is lost after the swap, the boot
	// check finds it and can revert to the rollback.
	if err := sys.WriteMarker(Marker{Version: plan.Version, Rollback: rollback}); err != nil {
		return Outcome{}, fmt.Errorf("updater: write marker: %w", err)
	}
	if err := sys.Swap(stage); err != nil {
		_ = sys.ClearMarker() // nothing changed on disk; don't leave a phantom in-flight marker
		return Outcome{}, fmt.Errorf("updater: swap: %w", err)
	}
	if err := sys.Restart(ctx); err != nil {
		return revert(ctx, sys, rollback, "restart failed: "+err.Error())
	}

	// Health-gate the commit.
	deadline := sys.Now().Add(healthTimeout)
	for sys.Now().Before(deadline) {
		if v, ok := sys.Health(ctx); ok && v == plan.Version {
			if err := sys.ClearMarker(); err != nil {
				return Outcome{}, fmt.Errorf("updater: confirmed but clearing marker failed: %w", err)
			}
			return Outcome{Confirmed: true, Version: plan.Version}, nil
		}
		select {
		case <-ctx.Done():
			return revert(ctx, sys, rollback, "canceled during health check")
		case <-time.After(healthInterval):
		}
	}
	return revert(ctx, sys, rollback, "new version did not become healthy within the timeout")
}

func revert(ctx context.Context, sys System, rollback, reason string) (Outcome, error) {
	if err := sys.Restore(rollback); err != nil {
		return Outcome{}, fmt.Errorf("updater: REVERT FAILED (%s): %w", reason, err)
	}
	if err := sys.Restart(ctx); err != nil {
		return Outcome{}, fmt.Errorf("updater: reverted binary but restart failed (%s): %w", reason, err)
	}
	_ = sys.ClearMarker()
	return Outcome{Reverted: true, Reason: reason}, nil
}

// BootCheck runs as the service's ExecStartPre. It closes the power-loss window: an
// update swapped but never confirmed leaves a pending marker; the FIRST boot into
// it increments the count and lets the new version try, the SECOND (still
// unconfirmed) boot reverts to the rollback. A confirmed update cleared its marker,
// so a healthy update is never reverted. maxBoots is the attempts before reverting
// (1 = revert on the second boot into an unconfirmed update).
func BootCheck(sys System, maxBoots int) (Outcome, error) {
	m, err := sys.ReadMarker()
	if err != nil {
		return Outcome{}, fmt.Errorf("updater: read marker: %w", err)
	}
	if m == nil {
		return Outcome{}, nil // no update in flight, nothing to do
	}
	if m.BootCount >= maxBoots {
		if err := sys.Restore(m.Rollback); err != nil {
			return Outcome{}, fmt.Errorf("updater: boot revert restore failed: %w", err)
		}
		_ = sys.ClearMarker()
		return Outcome{Reverted: true, Reason: "update did not confirm after " + strconv.Itoa(m.BootCount) + " boot(s); reverted to the prior version"}, nil
	}
	m.BootCount++
	return Outcome{}, sys.WriteMarker(*m)
}

// --- minimal semver ("x.y.z", optional leading v, ignores any pre-release/build) ---

type ver struct{ maj, min, patch int }

func parseVer(s string) (ver, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	// Drop any pre-release/build metadata after '-' or '+'.
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 1 || parts[0] == "" {
		return ver{}, fmt.Errorf("not a version: %q", s)
	}
	var v ver
	dst := []*int{&v.maj, &v.min, &v.patch}
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return ver{}, fmt.Errorf("not a version: %q", s)
		}
		*dst[i] = n
	}
	return v, nil
}

func less(a, b ver) bool {
	if a.maj != b.maj {
		return a.maj < b.maj
	}
	if a.min != b.min {
		return a.min < b.min
	}
	return a.patch < b.patch
}
