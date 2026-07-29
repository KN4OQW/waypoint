# Atomic updates

Waypoint updates are **transactional and health-gated**: an update either
completes, or the device boots the version it was already running. There is no
state in which a `waypointd` update leaves the box unable to start — the failure
mode that plagues in-place Pi-Star/WPSD updates (update loops, hangs, and the
"stuck at initializing → factory reset" class). This is [RFC-0014](https://github.com/KN4OQW/waypoint/discussions/169),
issue [#13](https://github.com/KN4OQW/waypoint/issues/13).

## How an update happens

1. **Fetch + verify the manifest.** A signed JSON manifest (`update.json` +
   `update.json.minisig`) describes the latest release. It is verified against the
   pinned release key ([RFC-0013](https://github.com/KN4OQW/waypoint/discussions/168))
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

### Reverting across a schema migration

A release may raise the configuration store's schema version
([RFC-0001](https://github.com/KN4OQW/waypoint/discussions/155)). That puts a twist in the revert path
worth stating explicitly, because getting it wrong is the one way this engine can
leave a node that will not start:

1. The new build migrates `config.db` on its **first start** — after the swap, and
   after the applier wrote its marker. Before touching anything it writes a
   **pre-migration copy** beside the store (`config.db.pre-v<N>`, taken with
   `VACUUM INTO`, so it is a consistent database rather than a file copy).
2. That first start then **annotates the in-flight marker** with the copy's path.
   This is the handshake: the applier could not know the path, because the
   migration had not happened when it wrote the marker.
3. If the new version never becomes healthy, the revert restores the **binary and
   the store together**. Without the second half, a reverted older `waypointd`
   would meet a schema it refuses to open — the store's rollback-safety guard
   turning into a dead node.

Both revert paths do this: the applier's health-gate revert (which re-reads the
marker to pick up the annotation) and the boot check. The boot check is the better
of the two, because as `ExecStartPre` nothing has the store open. A restore also
clears `config.db-wal`/`-shm`, which belong to the database being replaced and
would otherwise be replayed onto the restored one.

The pre-migration copy is **left in place** after a revert, so recovery is
repeatable, and after a confirmed update, so an operator can still get back. If a
restore fails, the marker is deliberately **not** cleared — it is the only record
of where the copy lives.

A release that raises the schema version must also raise the manifest's
`min_version` to the first release shipping that version, so an older node updates
through it rather than jumping a migration it has no ladder for.

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

`waypointd` also checks on its own schedule (see [Automatic checks and the
opt-out](#automatic-checks-and-the-opt-out)), so no `systemd` timer is needed;
`waypointd -update-check` remains the way to ask from a shell or a cron of your
own.

## Automatic checks and the opt-out

Every `-update-poll-interval` (default 6h) the daemon checks whether a newer
release exists — the signed `waypointd` manifest and, on a node with the apt repo,
the stack packages — and caches the answer for the Updates tab. It only *reports*;
applying still waits for you unless auto-apply is on.

**One switch turns all of that off:** *Settings → Updates → Update policy →
Automatic update checks* (`check_enabled` in the `update` config section, on by
default). With it off the daemon makes **no unattended outbound request at all** —
no manifest fetch, no apt refresh — and since auto-apply exists only to install
what a check found, it stops too. Everything you trigger yourself is untouched:
**CHECK NOW**, **UPDATE NOW**, `GET /api/update/check`, `POST /api/update/stack/check`,
and the CLI modes all still work, and no other feature of the node changes. That is
[#15](https://github.com/KN4OQW/waypoint/issues/15), and it is the code half of
GOVERNANCE.md's no-telemetry commitment.

### What leaves the device

A check is a plain `GET` of a static file:

```
GET /KN4OQW/waypoint/releases/latest/download/update.json
User-Agent: Waypoint updater
```

No query string, no cookies, no request body, no per-device token, and a
`User-Agent` that is the same fixed string on every Waypoint in the world. Your
callsign, DMR/CCS7 ID, hostname, MAC, serial, current version, architecture and
channel are **not** sent: the manifest is downloaded first and compared against
them *locally*, in `PlanUpdate`, after the bytes are already on the box. The
server learns what any static-file host learns from a download — an IP address and
a timestamp — and nothing that identifies the node behind it. This is asserted by
tests (`TestCheckSendsNoDeviceIdentifier`, `TestUpdatePollerHonorsCheckEnabled`),
not just by policy, and it is verifiable from outside:

```console
$ sudo tcpdump -n -s0 -A 'tcp port 443 and host <release host>'
```

Support is never conditioned on any of this, and there is no other channel: no
crash reports, no usage counters, no "diagnostics" bundle, opt-in or otherwise.

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

## Two update paths

A Waypoint node updates two different things, kept deliberately separate:

1. **The OS** (Raspberry Pi OS / Debian base) — kernel, libc, openssl, and the
   rest of the distro. These are the distro's own security updates, applied by
   `unattended-upgrades` on the normal Debian schedule.
2. **Waypoint's own software** — the `waypointd` binary (the signed-manifest
   engine above) and the **waypoint-stack digital-voice daemons** (MMDVMHost,
   DMRGateway, the mode gateways), distributed as `.debs` from the signed apt
   repo at `https://kn4oqw.github.io/waypoint-stack`.

**waypointd is the only driver of the second path (D2).** `unattended-upgrades`
is configured (in the image) to never touch the Waypoint origin, so the stack is
only ever moved by waypointd's health-gated updater — never by an unattended
`apt` run that could restart the modem host mid-QSO with no rollback. The OS path
stays the distro's job; the Waypoint path gets the transactional, health-gated,
auto-reverting treatment.

## Stack-package updates (the waypoint-stack .debs)

The stack updater is the apt-backed sibling of the binary engine above. It keeps
the same **confirm-or-revert** contract, but the atomic unit is a *set of
versioned packages* installed by `apt`, not one binary swapped by `rename`.

### Detecting updates

The periodic check (reusing the update poll cadence) refreshes package lists
**limited to the Waypoint source** — never an OS-wide `apt-get update`:

```console
# apt-get update -o Dir::Etc::SourceList=/dev/null \
                  -o Dir::Etc::SourceParts=<dir with only waypoint.sources>
# apt list --upgradable        # (same source limit) → waypoint-* upgradable lines
```

The available updates are cached in the store and surfaced in the **Updates**
settings tab (installed versions, what is available, an Apply button).

### Applying an update

`Apply` runs a fixed, health-gated sequence — the ordering is the safety argument:

1. **Record** the currently-installed version of every affected package (the
   revert set) in the store.
2. **Stop** the affected services.
3. **`apt-get install`** the *exact* target versions
   (`waypoint-mmdvmhost=<ver> …`) — never a bare `upgrade`/`dist-upgrade`, so
   apt touches only the named packages.
4. **Restart** the services.
5. **Health-gate**: poll until healthy for several consecutive checks —
   **every affected unit is `active` AND MMDVMHost's modem is open**. The
   modem-open signal is ground-truthed: MMDVMHost **exits(1)** when its modem
   will not open (`MMDVM-Host.cpp` `createModem → return 1`), so a
   `waypoint-mmdvm.service` that will not stay cleanly `running` (SubState) is
   the real "modem did not open" signal — no log scraping. A brief healthy blip
   mid-restart does not confirm; the health must *sustain*.
6. **Confirm**, or on any failure (a failed install, a failed restart, or a
   health gate that never sustains) **revert**: reinstall the previous versions
   and restart.

### Revert relies on the repo keeping old versions

The revert step is `apt-get install <previous versions>` — which only works
because the apt repo **retains prior versions in `pool/`** (the repo carries
every published version forward). That retention is the apt-side half of the
rollback story; the health gate is the trigger. Every applied/previous pair and
its result (`confirmed`/`reverted`) is recorded in the `stack_update_history`
table for the audit and revert trail.

### Timing and auto-apply

- **Default: notify-and-click.** The Updates tab shows "update available"; the
  operator applies it explicitly. Nothing is applied automatically.
- **Opt-in auto-apply.** A setting (off by default) applies available updates
  automatically inside a **quiet window** (default 04:00 local), at most once per
  day. The poller ticks more often than hourly so it reliably lands in the
  one-hour window.
- **Automatic checks, opt-out.** The poll above is the only thing the node does on
  its own, and `check_enabled` (on by default) turns it off — including auto-apply,
  which rides on it. See [Automatic checks and the opt-out](#automatic-checks-and-the-opt-out).

### Channels

The update policy selects a **channel** (`stable` | `beta`, default `stable`),
persisted in the store like any other config. **For now the channel gates only
the signed `waypointd` binary manifest** — a node applies a manifest only when
its `channel` field matches the selected channel. The **apt stack repo serves
both channels from the same `bookworm` suite**, so selecting `beta` does not (yet)
change the apt source. Mapping a channel to a distinct apt suite (e.g. a
`bookworm-beta` suite, or a per-channel component) is a deliberate follow-up: the
setting and the manifest gate ship now so the channel is a real, persisted choice
the apt side can adopt later without a schema change.

### Triggering (shell + API)

```console
# Report available stack updates (changes nothing).
$ waypointd -update-stack-check

# Apply them: stop → install exact versions → restart → health-gate → confirm/revert.
$ waypointd -update-stack
update-stack: confirmed — waypoint-mmdvmhost=0~gitNEW+wp1, …
```

- `GET /api/update/stack` → installed versions, cached available updates, policy,
  recent history.
- `POST /api/update/stack/check` → run the source-limited apt check now.
- `POST /api/update/stack/apply` → start the health-gated apply (202; poll
  `GET /api/update/stack` for confirmed/reverted).

### Stack flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `-update-stack` | — | Apply available stack updates (health-gated, auto-revert) and exit. |
| `-update-stack-check` | — | Report available stack updates and exit. |
| `-apt-source-file` | `/etc/apt/sources.list.d/waypoint.sources` | The signed-repo deb822 source; the check limits apt to it (D2). |
| `-update-poll-interval` | `6h` | How often waypointd checks for updates (manifest + apt) and evaluates quiet-window auto-apply. Gated on `check_enabled` — off means no unattended request at all (#15). |

## What is not here yet

- **A/B image slots (Phase 3)** — the same verify/confirm/rollback state machine
  over boot slots instead of a binary, for the built-image layout ([#64]). The
  engine's swap/revert/confirm steps are already behind an interface so the A/B
  backend slots in without a rewrite.
- **Delta/partial updates** — the engine fetches a whole signed binary (~13 MB).

[#64]: https://github.com/KN4OQW/waypoint/issues/64
