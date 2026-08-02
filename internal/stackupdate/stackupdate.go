// Package stackupdate is waypointd's health-gated updater for the waypoint-stack
// Debian packages (MMDVMHost, DMRGateway, the mode gateways, …) distributed from
// the signed apt repo. It is the apt-backed sibling of the RFC-0014 binary update
// engine (internal/updater): the same confirm-or-revert contract, but the atomic
// unit is a set of versioned .debs installed by apt rather than one binary swapped
// by rename.
//
// Why a separate engine, not updater.System: updater's interface is binary-shaped
// (StageBinary(data)/Swap(stagePath)/Restore(rollbackPath) over a single artifact),
// which does not fit an apt transaction over a *set* of versioned packages. Per
// RFC-0014 ("the engine can wrap a .deb install as its swap step later … the
// primitive is transport-and-format agnostic") this backend keeps the same shape —
// a pure state machine over an injected side-effect interface (System), returning
// the same Confirmed/Reverted/Reason outcome — so it is exhaustively testable with
// a fake and the OS glue (apt/systemctl/store) stays a thin, documented shell.
//
// Apply's ordering is the safety argument, mirroring RFC-0014:
//
//	pre-flight the revert targets -> record intended change -> stop affected
//	services -> apt-get install the exact target versions (never a bare
//	dist-upgrade) -> restart -> health-gate (every affected unit active AND
//	MMDVMHost's modem open, sustained) -> confirm; or on any failure, apt-get
//	install the previous versions (kept in the repo's pool/) and restart ->
//	reverted, with a logged reason.
//
// The pre-flight step exists because the rest of that argument rests on an
// assumption this package cannot enforce: that the previous versions are still
// downloadable when the revert needs them. They were not (issue #221) —
// waypoint-stack's publish-apt.sh rebuilt the pool from only the current build,
// so the repo carried exactly one version per package, and a node whose update
// installed cleanly but failed its health gate could not go back:
//
//	stackupdate: REVERT FAILED (…): E: Version '0.1.0' for 'waypoint-stack' was not found
//
// leaving it stranded on the new version. That had stayed hidden because every
// earlier revert was a no-op — the install had failed *before* changing anything,
// so the revert reinstalled versions that were still installed. Only a revert
// following a *successful* install needs the pool. So Apply now proves the way
// back is installable before it takes a step it may need to undo, and refuses
// outright rather than discovering the dead end afterwards.
package stackupdate

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PackagePrefix is the namespace every waypoint-stack package shares. The apt
// check keys on it: only these packages are ever touched by this updater (D2 —
// waypointd is the only driver of stack updates, and it drives only its own
// packages, never the OS).
const PackagePrefix = "waypoint-"

// pkgUnit maps a stack package to the systemd unit it backs. A package with no
// long-running service — the parrots (on-demand echo) and the waypoint-stack
// metapackage — maps to "" and contributes no stop/start/health target.
var pkgUnit = map[string]string{
	"waypoint-mmdvmhost":     "waypoint-mmdvm.service",
	"waypoint-dmrgateway":    "waypoint-dmrgateway.service",
	"waypoint-ysfgateway":    "waypoint-ysfgateway.service",
	"waypoint-dgidgateway":   "waypoint-dgidgateway.service",
	"waypoint-p25gateway":    "waypoint-p25gateway.service",
	"waypoint-nxdngateway":   "waypoint-nxdngateway.service",
	"waypoint-dstargateway":  "waypoint-dstargateway.service",
	"waypoint-m17gateway":    "waypoint-m17gateway.service",
	"waypoint-dapnetgateway": "waypoint-dapnetgateway.service",
}

// MMDVMUnit is MMDVMHost's unit. The health gate always requires it active: a
// stack update must never leave the modem host down, and MMDVMHost exits(1) when
// its modem will not open (ground truth: MMDVM-Host.cpp createModem -> return 1),
// so a sustained-active waypoint-mmdvm.service is the real "modem open" signal.
const MMDVMUnit = "waypoint-mmdvm.service"

// UnitFor returns the systemd unit a package backs, or "" for a package with no
// long-running service.
func UnitFor(pkg string) string { return pkgUnit[pkg] }

// allPackages is every waypoint-stack package waypointd tracks — the daemons, the
// parrots, and the metapackage. Used to report installed versions in the UI.
var allPackages = []string{
	"waypoint-mmdvmhost",
	"waypoint-dmrgateway",
	"waypoint-ysfgateway",
	"waypoint-dgidgateway",
	"waypoint-ysfparrot",
	"waypoint-p25gateway",
	"waypoint-p25parrot",
	"waypoint-nxdngateway",
	"waypoint-nxdnparrot",
	"waypoint-dstargateway",
	"waypoint-m17gateway",
	"waypoint-dapnetgateway",
	"waypoint-stack",
}

// AllPackages returns every tracked stack package name (copied).
func AllPackages() []string { return append([]string(nil), allPackages...) }

// Update is one package with a newer candidate version available.
type Update struct {
	Package string `json:"package"`
	From    string `json:"from"` // installed version
	To      string `json:"to"`   // candidate version
}

// PkgVer is a package pinned to an exact version, as passed to `apt-get install
// name=version` — the only install form this engine uses (never a bare upgrade).
type PkgVer struct {
	Package string `json:"package"`
	Version string `json:"version"`
}

// Plan is the set of stack updates to apply and the units they affect.
type Plan struct {
	Available bool     `json:"available"`
	Updates   []Update `json:"updates"`
	Units     []string `json:"units"`  // affected systemd units, deduped, stable order
	Reason    string   `json:"reason"` // why nothing is available, when !Available
	// RequireMMDVM adds MMDVMHost's unit to the health gate even when the update
	// touched no package that backs it — a stack update must not leave the modem
	// host down. It is set by the caller rather than assumed here, because whether
	// this node is supposed to be running MMDVMHost at all is a config question
	// (config.ModemHostRuns): a node with every mode off, or with no modem port
	// set, correctly runs no modem host, and gating on a unit that is *meant* to
	// be stopped would fail every update on it forever.
	RequireMMDVM bool `json:"require_mmdvm"`
}

// PackageNames returns the package names in the plan, in order.
func (p Plan) PackageNames() []string {
	out := make([]string, len(p.Updates))
	for i, u := range p.Updates {
		out[i] = u.Package
	}
	return out
}

// Targets returns the exact package=version pairs to install (the candidates).
func (p Plan) Targets() []PkgVer {
	out := make([]PkgVer, len(p.Updates))
	for i, u := range p.Updates {
		out[i] = PkgVer{Package: u.Package, Version: u.To}
	}
	return out
}

// Outcome is the result of Apply, mirroring updater.Outcome.
type Outcome struct {
	Confirmed bool     `json:"confirmed"` // the new versions came up healthy and were committed
	Reverted  bool     `json:"reverted"`  // the update failed and the prior versions were restored
	Applied   []PkgVer `json:"applied"`   // the versions now installed (targets on confirm, previous on revert)
	Reason    string   `json:"reason"`    // why it reverted, when Reverted
	// Unrevertable records that this update ran with no working way back: the
	// pre-flight found the previous versions uninstallable and Policy.
	// AllowUnrevertable let it proceed anyway. Set on confirm and on revert
	// alike — on a revert it is the explanation for why the revert then failed.
	Unrevertable bool `json:"unrevertable,omitempty"`
}

// Policy is the operator-set behaviour Apply honours, distinct from Timings
// (which tunes the health gate rather than deciding anything).
type Policy struct {
	// AllowUnrevertable proceeds with an update whose revert targets are not
	// installable. Default false: no way back means no update, because a node
	// stranded on a broken stack is worse than a node that stayed on a working
	// one. It is an escape hatch for the operator who must take an update on a
	// node the pool cannot roll back — knowingly, and only for that run.
	AllowUnrevertable bool
}

// Apply result-code constants recorded in the history table.
const (
	ResultPending      = "pending"
	ResultConfirmed    = "confirmed"
	ResultReverted     = "reverted"
	ResultRevertFailed = "revert_failed"
)

// HistoryRow is one applied/previous version pair recorded for the audit and
// revert trail (RFC-0001 store bookkeeping).
type HistoryRow struct {
	Package string
	From    string
	To      string
	Result  string
}

// System is every side effect the stack engine performs — the apt/systemctl/store
// boundary. The real implementation (apt.go) shells out; tests inject a fake, so
// the state machine and the revert decision are covered without a real system.
type System interface {
	// InstalledVersions returns the installed version of each package ("" if a
	// package is not installed). This is the revert set captured before install.
	InstalledVersions(ctx context.Context, pkgs []string) (map[string]string, error)
	// Install installs exactly these package=version pairs — `apt-get install
	// name=version …`, never a bare upgrade or dist-upgrade (D2).
	Install(ctx context.Context, pkgs []PkgVer) error
	// CheckInstallable reports whether apt could install exactly these
	// package=version pairs right now, changing nothing. It is how Apply proves
	// the revert set is reachable before it installs anything (#221); an error
	// means at least one version is no longer in the pool, so there is no way
	// back. An empty set is installable by definition.
	CheckInstallable(ctx context.Context, pkgs []PkgVer) error
	// StopServices stops the given units before an install; StartServices restarts
	// them after. Empty unit lists are no-ops.
	StopServices(ctx context.Context, units []string) error
	StartServices(ctx context.Context, units []string) error
	// Healthy is one poll of the health gate: every unit in `units` is active AND
	// MMDVMHost's modem is open. It returns a human-readable detail when not ok.
	Healthy(ctx context.Context, units []string) (ok bool, detail string)
	// RecordHistory persists applied/previous version pairs (audit + revert trail).
	RecordHistory(rows []HistoryRow) error
}

// Timings configure the health gate. Defaults live in DefaultTimings; tests pass a
// zero PollInterval for instant, deterministic runs.
type Timings struct {
	SettleDelay   time.Duration // pause after restart before the first probe
	PollInterval  time.Duration // between health polls
	MaxPolls      int           // give up (revert) after this many polls without sustained health
	ConfirmChecks int           // consecutive healthy polls required to confirm (defeats a flap)
}

// DefaultTimings gates a stack update on ~6 s of sustained health within ~40 s.
// A crash-looping MMDVMHost (modem never opens) never sustains, so it reverts.
func DefaultTimings() Timings {
	return Timings{
		SettleDelay:   3 * time.Second,
		PollInterval:  2 * time.Second,
		MaxPolls:      20,
		ConfirmChecks: 3,
	}
}

// ParseUpgradable parses `apt list --upgradable` stdout into the stack updates it
// reports. Only waypoint-* packages are returned (the OS is never this updater's
// concern). Non-matching lines, the "Listing…" header, and apt warnings are
// skipped. Line shape:
//
//	waypoint-mmdvmhost/bookworm 0~gitNEW+wp1 armhf [upgradable from: 0~gitOLD+wp1]
func ParseUpgradable(out string) []Update {
	var ups []Update
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "[upgradable from:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name, _, ok := strings.Cut(fields[0], "/")
		if !ok || !strings.HasPrefix(name, PackagePrefix) {
			continue
		}
		newVer := fields[1]
		// The old version is the token after "from:", with its trailing ']' trimmed.
		var oldVer string
		for i, f := range fields {
			if f == "from:" && i+1 < len(fields) {
				oldVer = strings.TrimSuffix(fields[i+1], "]")
				break
			}
		}
		ups = append(ups, Update{Package: name, From: oldVer, To: newVer})
	}
	return ups
}

// PlanFrom builds a Plan from the parsed updates: the affected units (deduped,
// stable order) and availability. An empty update set is "already up to date".
func PlanFrom(updates []Update) Plan {
	if len(updates) == 0 {
		return Plan{Reason: "the stack is up to date"}
	}
	// Stable package order so the plan (and its units) is deterministic.
	sort.Slice(updates, func(i, j int) bool { return updates[i].Package < updates[j].Package })
	seen := map[string]bool{}
	var units []string
	for _, u := range updates {
		if unit := UnitFor(u.Package); unit != "" && !seen[unit] {
			seen[unit] = true
			units = append(units, unit)
		}
	}
	return Plan{Available: true, Updates: updates, Units: units}
}

// Apply performs the transactional stack update. The step ordering is the safety
// argument (see the package doc): the way back is proven installable and the
// previous versions are recorded before any change, and any failure — a failed
// install, a failed restart, or a health gate that never sustains — reverts to
// those previous versions from the repo's pool/.
func Apply(ctx context.Context, plan Plan, sys System, t Timings, pol Policy) (Outcome, error) {
	if !plan.Available {
		return Outcome{}, fmt.Errorf("stackupdate: nothing to apply: %s", plan.Reason)
	}
	prev, err := sys.InstalledVersions(ctx, plan.PackageNames())
	if err != nil {
		return Outcome{}, fmt.Errorf("stackupdate: read installed versions: %w", err)
	}
	// Prove the way back before taking a step that may need undoing (#221). This
	// runs before the pending history row as well as before any service or package
	// move, so a refused update leaves the node — and its audit trail — exactly as
	// it found them, rather than logging an in-flight update that never began.
	unrevertable := false
	if err := sys.CheckInstallable(ctx, revertSet(prev, plan.PackageNames())); err != nil {
		if !pol.AllowUnrevertable {
			return Outcome{}, fmt.Errorf("stackupdate: refusing to update — the currently installed "+
				"versions could not be reinstalled, so a failed update could not be rolled back: %w", err)
		}
		unrevertable = true
	}
	// Record the intended change up front: a durable applied/previous trail the
	// revert path (and an operator) can read even if this process dies mid-apply.
	_ = sys.RecordHistory(historyRows(prev, plan.Updates, ResultPending))

	if err := sys.StopServices(ctx, plan.Units); err != nil {
		return revert(ctx, sys, plan, prev, "stopping services failed: "+err.Error(), unrevertable)
	}
	if err := sys.Install(ctx, plan.Targets()); err != nil {
		return revert(ctx, sys, plan, prev, "apt install failed: "+err.Error(), unrevertable)
	}
	if err := sys.StartServices(ctx, plan.Units); err != nil {
		return revert(ctx, sys, plan, prev, "restart after install failed: "+err.Error(), unrevertable)
	}
	if reason := gate(ctx, sys, plan, t); reason != "" {
		return revert(ctx, sys, plan, prev, reason, unrevertable)
	}
	_ = sys.RecordHistory(historyRows(prev, plan.Updates, ResultConfirmed))
	return Outcome{Confirmed: true, Applied: plan.Targets(), Unrevertable: unrevertable}, nil
}

// gate polls the health signal until it is healthy for ConfirmChecks consecutive
// polls (sustained health — one healthy blip mid-restart never confirms) or MaxPolls
// is exhausted. It returns "" on success or a failure reason to revert on. When the
// plan requires it, the gate includes MMDVMHost's unit, so a modem that will not
// open — MMDVMHost exits(1), systemd crash-loops it — never sustains and the update
// reverts.
func gate(ctx context.Context, sys System, plan Plan, t Timings) string {
	if !sleep(ctx, t.SettleDelay) {
		return "canceled during settle"
	}
	gateUnits := plan.Units
	if plan.RequireMMDVM {
		gateUnits = withMMDVM(gateUnits)
	}
	consecutive := 0
	lastDetail := "no health probe ran"
	for i := 0; i < t.MaxPolls; i++ {
		ok, detail := sys.Healthy(ctx, gateUnits)
		if ok {
			if consecutive++; consecutive >= t.ConfirmChecks {
				return ""
			}
		} else {
			consecutive, lastDetail = 0, detail
		}
		if !sleep(ctx, t.PollInterval) {
			return "canceled during health check"
		}
	}
	return "the updated stack did not become healthy within the timeout: " + lastDetail
}

// revert restores the previous versions (kept in the repo's pool/) and restarts —
// back to the prior, known-good stack with a clear logged reason. unrevertable
// carries the pre-flight's verdict through to the outcome: when it is set the
// caller opted past a known-missing pool, so a REVERT FAILED here is the
// predicted consequence rather than a surprise.
func revert(ctx context.Context, sys System, plan Plan, prev map[string]string, reason string, unrevertable bool) (Outcome, error) {
	revertPkgs := revertSet(prev, plan.PackageNames())
	_ = sys.StopServices(ctx, plan.Units)
	if err := sys.Install(ctx, revertPkgs); err != nil {
		_ = sys.RecordHistory(historyRows(prev, plan.Updates, ResultRevertFailed))
		return Outcome{Unrevertable: unrevertable}, fmt.Errorf("stackupdate: REVERT FAILED (%s): %w", reason, err)
	}
	if err := sys.StartServices(ctx, plan.Units); err != nil {
		return Outcome{Unrevertable: unrevertable}, fmt.Errorf("stackupdate: reverted packages but restart failed (%s): %w", reason, err)
	}
	_ = sys.RecordHistory(historyRows(prev, plan.Updates, ResultReverted))
	return Outcome{Reverted: true, Reason: reason, Applied: revertPkgs, Unrevertable: unrevertable}, nil
}

// revertSet is the previous versions to reinstall: every planned package that had a
// prior installed version. A package with no prior version (a fresh install, which
// stack updates never are) is skipped — there is nothing to downgrade to.
func revertSet(prev map[string]string, names []string) []PkgVer {
	var out []PkgVer
	for _, name := range names {
		if v := prev[name]; v != "" {
			out = append(out, PkgVer{Package: name, Version: v})
		}
	}
	return out
}

// withMMDVM adds MMDVMHost's unit to the gate set when the plan requires it, even
// though the update itself may not have touched waypoint-mmdvmhost — a stack update
// must not leave the modem host down on a node that is meant to be running one.
func withMMDVM(units []string) []string {
	for _, u := range units {
		if u == MMDVMUnit {
			return units
		}
	}
	return append(append([]string(nil), units...), MMDVMUnit)
}

func historyRows(prev map[string]string, updates []Update, result string) []HistoryRow {
	rows := make([]HistoryRow, len(updates))
	for i, u := range updates {
		from := u.From
		if p, ok := prev[u.Package]; ok && p != "" {
			from = p // the actually-installed version wins over apt's reported "from"
		}
		rows[i] = HistoryRow{Package: u.Package, From: from, To: u.To, Result: result}
	}
	return rows
}

// sleep pauses for d, returning false if ctx is canceled first. d <= 0 returns
// immediately (tests pass 0 for instant, deterministic gate runs).
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
