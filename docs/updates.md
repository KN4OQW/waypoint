# Atomic updates

Waypoint updates are **transactional and health-gated**: an update either
completes, or the device boots the version it was already running. There is no
state in which a `waypointd` update leaves the box unable to start — the failure
mode that plagues in-place Pi-Star/WPSD updates (update loops, hangs, and the
"stuck at initializing → factory reset" class). This is [RFC-0014](rfcs/0014-atomic-updates.md),
issue [#13](https://github.com/KN4OQW/waypoint/issues/13).

## How an update happens

1. **Fetch + verify the manifest.** A signed JSON manifest (`update.json` +
   `update.json.minisig`) describes the latest release. It is verified against the
   pinned release key ([RFC-0013](rfcs/0013-signed-releases-verified-downloads.md))
   before anything is downloaded, so a downgrade or a swapped artifact URL is
   rejected up front.
2. **Download + verify the binary.** The artifact for this node's architecture is
   fetched and checked against both its SHA-256 (from the signed manifest) and its
   own `.minisig`. Nothing on the live system is touched yet.
3. **Stage + back up.** The verified binary is written beside the live one, and the
   current binary is copied to `waypointd.rollback`. A durable **marker** records
   the version being tried and the rollback path — `fsync`'d *before* the swap.
4. **Atomic swap.** A single `rename(2)` on the same filesystem replaces the live
   binary. Power loss on either side is safe: the old *or* the new complete, signed
   binary is in place — never a torn file.
5. **Restart + health-gate.** The service restarts onto the new binary, and the
   updater polls the node's own `https://…/api/health` until it reports `status:
   ok` **and the expected new version**, within a timeout.
6. **Confirm or revert.** On a healthy new version the marker is cleared — the
   update is committed. If the new version does not become healthy in the window,
   the rollback binary is restored, the service restarted, and the marker cleared,
   with a logged reason. The box is back on the prior version.

### The power-loss guarantee

Steps 5–6 have one gap: power pulled *after* the swap but *before* the health
check confirms. The **boot-time check** closes it. `waypointd -update-boot-check`
runs as the service's `ExecStartPre`:

- If a pending marker exists and this is the **second** boot into an unconfirmed
  update (the first boot never reached "confirmed"), it reverts to the rollback
  binary and clears the marker — so the unit then starts the prior version.
- A *good* update clears its marker the moment its own startup passes the health
  check, so a healthy update confirms on the first boot and is never reverted.

The net guarantee: an update interrupted at any point degrades to a booting
system — the prior version if the new one never confirmed, the new version once it
has.

## Triggering an update

### From the shell

```console
# Is there a newer signed release for this node? (changes nothing)
$ waypointd -update-check -release-pubkey /path/to/waypoint-release.pub

# Apply it: verify, stage, atomic swap, health-gated confirm-or-revert.
$ waypointd -update -release-pubkey /path/to/waypoint-release.pub
update: fetching 1.4.0 for linux/arm64…
update: confirmed — now running 1.4.0
```

`-update` runs as a **standalone process** on purpose: it restarts the service out
from under the running daemon, so the applier must be a *different* process that
survives that restart.

### From the API (behind the session wall)

- `GET /api/update/check` → `{ "current", "available", "version", "notes_url", "reason" }`
  — fetch + verify the manifest and report availability without changing anything.
- `POST /api/update/apply` → `202 { "status": "started" }` — launches the detached
  `-update` applier (so it outlives the restart) and returns immediately. Watch
  `GET /api/health` (or re-poll `/api/update/check`) for the result.

## systemd integration

Two hooks make the guarantee hold across a reboot. The boot check must run
**before** the daemon starts, as `ExecStartPre`:

```ini
[Service]
# Power-loss revert: an update swapped but never confirmed is rolled back here,
# so the unit then starts the prior version. Non-fatal (leading '-') — a
# boot-check hiccup must never wedge startup.
ExecStartPre=-/home/pi-star/waypoint/bin/waypointd -update-boot-check \
  -update-binary /home/pi-star/waypoint/bin/waypointd \
  -update-marker /home/pi-star/waypoint/update.marker
ExecStart=/home/pi-star/waypoint/bin/waypointd -addr 0.0.0.0:443 …
```

An optional periodic check (opt-in, privacy-preserving — a plain signed-manifest
fetch with no identifiers; the opt-out policy is [#15](https://github.com/KN4OQW/waypoint/issues/15))
can be a `systemd` timer running `waypointd -update-check`, surfacing availability
without applying anything automatically.

## Flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `-update` | — | Run the transactional install and exit. |
| `-update-check` | — | Report whether a newer signed release is available and exit. |
| `-update-boot-check` | — | ExecStartPre power-loss revert hook. |
| `-update-url` | GitHub `releases/latest/.../update.json` | Signed manifest URL. |
| `-release-pubkey` | *(empty)* | minisign key that signs the manifest and artifacts. Empty = unverified (not recommended). |
| `-update-binary` | `…/waypoint/bin/waypointd` | Live binary the swap replaces atomically. |
| `-update-unit` | `waypointd.service` | systemd unit the updater restarts. |
| `-update-marker` | `…/waypoint/update.marker` | In-flight-update marker (power-loss recovery). |

## The manifest

```json
{
  "version":     "1.4.0",
  "min_version": "1.0.0",
  "notes_url":   "https://…/CHANGELOG.md#1-4-0",
  "artifacts": {
    "linux/arm":   { "url": "…/waypointd-linux-arm6",  "sha256": "…" },
    "linux/arm64": { "url": "…/waypointd-linux-arm64", "sha256": "…" },
    "linux/amd64": { "url": "…/waypointd-linux-amd64", "sha256": "…" }
  }
}
```

Artifact keys are the node's `GOOS/GOARCH`. `min_version` lets a release refuse to
apply on top of a too-old base (a schema/migration floor). A node running a
non-release (`dev`) build is treated as older than any release, so it updates.

## What is not here yet

- **A/B image slots (Phase 3)** — the same verify/confirm/rollback state machine
  over boot slots instead of a binary, for the built-image layout ([#64]). The
  engine's swap/revert/confirm steps are already behind an interface so the A/B
  backend slots in without a rewrite.
- **Automatic periodic checks with an opt-out** — the mechanism and manual trigger
  are here; the automatic-check policy is [#15](https://github.com/KN4OQW/waypoint/issues/15).
- **Delta/partial updates** — the engine fetches a whole signed binary (~13 MB).

[#64]: https://github.com/KN4OQW/waypoint/issues/64
