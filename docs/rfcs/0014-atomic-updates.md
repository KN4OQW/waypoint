# RFC-0014: Atomic Updates with Rollback

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #13 (atomic updates with rollback — an update completes or the old version boots)
- Depends on: RFC-0013 (the minisign verifier — an update artifact is verified before it is ever staged) and RFC-0012 (HTTPS — the health check probes the local `https://` endpoint)

## Summary

An update either **completes or leaves the previous version booting** — never a
half-installed brick. This RFC delivers the **Phase-1 (package) update engine**: a
new `waypointd` release is described by a signed **manifest**, **verified**
(minisign, RFC-0013) before it touches anything, staged, **atomically swapped**
into place with the old binary kept as a rollback, and then **health-checked**;
if the new version does not come up healthy, it is **automatically reverted**. A
**boot-time check** closes the power-loss window: an update that was swapped but
never confirmed reverts on the next boot, so *pulling power mid-update yields a
booting system running the prior version* — the acceptance. The engine's every
side effect is an injectable seam, so the state machine is exhaustively testable
without a real system, and the OS/systemd wiring is a thin, documented shell.
Phase 3 (image A/B slots) reuses the same manifest, verifier, and confirm/rollback
state machine over boot slots instead of a binary — a follow-up that ties to the
built-image work (#64).

## Motivation

The incumbent update story is the requirement's rap sheet: Pi-Star update **loops**
(t=3697) and **hangs** (p=21908), a whole **RO/RW filesystem-corruption class**
(#126, #64, sbin #34), and WPSD's *"stuck at initializing"* whose sanctioned fix is
a **factory reset** (RadioReference 480017). Every one of those is the same root
failure: an update that mutates the live system in place, non-atomically, with no
verified rollback — so an interruption (power, network, a bad build) leaves the
device in a state it cannot boot out of.

Waypoint already has the two hard prerequisites: a **verifier** (RFC-0013, so a
tampered or corrupt artifact is rejected before it is staged) and an **honest
health signal** (RFC-0008/0012 — `/api/status` and `/api/health` over HTTPS). The
missing piece is the transactional install itself: make the swap atomic, keep a
rollback, gate the new version on a health check, and make the whole thing
crash-safe with a boot-time confirm. That is a bounded, testable state machine.

## Design

### The update manifest

A release is described by a signed JSON manifest (`update.json`) at a configured
URL:

```json
{
  "version":   "1.4.0",
  "min_version":"1.0.0",
  "notes_url": "https://…/CHANGELOG.md#1-4-0",
  "artifacts": {
    "linux/arm/6":  { "url": "…/waypointd-linux-arm6",  "sha256": "…" },
    "linux/arm64":  { "url": "…/waypointd-linux-arm64", "sha256": "…" },
    "linux/amd64":  { "url": "…/waypointd-linux-amd64", "sha256": "…" }
  }
}
```

The manifest **itself is signed** (`update.json.minisig`, verified against the
bundled release key from RFC-0013), so an attacker cannot offer a downgrade or a
malicious artifact URL. Each artifact carries a SHA-256 the engine checks after
download, and the artifact's own `.minisig` is verified too — belt and braces:
the signed manifest authenticates *what to fetch*, the artifact signature
authenticates *what was fetched*. `min_version` lets a release refuse to apply on
top of a too-old base (a schema/migration floor).

### The transactional install (the state machine)

`Apply` runs a fixed sequence, and the ordering is the whole safety argument:

1. **Verify manifest** → reject a bad/older/incompatible manifest before anything
   is downloaded.
2. **Download + verify artifact** (SHA-256 + minisign) to a **staging path** next
   to the live binary. Nothing live is touched yet — a failure or power loss here
   leaves the running version wholly intact.
3. **Back up** the current binary (`waypointd.rollback`) and write a **pending
   marker** recording the version being tried and the rollback path. The marker is
   `fsync`'d before the swap, so it is durable.
4. **Atomic swap**: `rename(staging, live)` — a single rename on the same
   filesystem, which is atomic (the running process keeps its open inode; Linux
   permits replacing a running binary). Power loss is safe on both sides: the old
   *or* the new complete, signed binary is in place, never a torn file.
5. **Restart** the service (systemd), which now execs the new binary.
6. **Health-check** the new version: poll local `https://…/api/health` until it
   reports **status ok and the expected new version**, within a timeout. On
   success, **clear the pending marker** (commit) — the update is confirmed.
7. **Auto-revert on failure**: if the health check fails within the window, restore
   `waypointd.rollback` over the live path and restart — back to the prior version,
   with a clear logged reason.

### Boot-time confirm (the power-loss guarantee)

Steps 5–7 have a gap the acceptance targets directly: **power pulled after the swap
but before the health check confirms**. On the next boot the new (possibly bad)
binary would run with no one to revert it. The boot-time check closes it:

- `waypointd -update-boot-check` runs as the service's `ExecStartPre`. If a
  **pending marker** exists and its `boot_count` has already reached the limit
  (i.e. this is a *second* boot into an unconfirmed update — the first boot did not
  reach "confirmed"), it **reverts** to the rollback binary, clears the marker, and
  logs it — so the unit then starts the prior version. If the marker exists but the
  count is under the limit, it increments and lets the (still-unconfirmed) new
  version try once more.
- The running new binary clears the marker the moment its own startup passes the
  health check (step 6), so a *good* update confirms on first boot and the
  boot-check never reverts it.

The result: an update interrupted at *any* point degrades to a booting system —
the prior version if the new one never confirmed, the new version once it has.
This is the "pull power mid-update → prior version boots" acceptance, and it is a
property of the marker + atomic-rename ordering, tested as a state machine.

### Triggering + surfacing

- `GET /api/update/check` — fetch + verify the manifest and report whether a newer,
  applicable version exists (version, notes URL), without changing anything.
- `POST /api/update/apply` — run the transactional install; returns the outcome
  (confirmed new version, or reverted-with-reason). Behind the session wall.
- `waypointd -update` / `-update-check` — the same, for a headless/cron caller and
  the systemd timer that can offer updates. `-update-boot-check` is the boot hook.

Update checking is **opt-in and privacy-preserving** — it is a plain signed-manifest
fetch with no identifiers, and the periodic check is the subject of #15; this RFC
provides the mechanism and the manual trigger, #15 the opt-out policy around the
*automatic* check.

### What is deliberately not in Phase 1

- **A/B image slots (Phase 3).** The same manifest/verify/confirm-or-rollback state
  machine, but the atomic unit is a **boot slot** (two root partitions, a bootloader
  that flips on a confirmed flag and falls back on a failed boot). That depends on
  the built-image layout (#64) and the bootloader, so it is a follow-up; this RFC's
  engine is written with the swap/revert/confirm steps behind an interface so the
  A/B backend is a second implementation, not a rewrite.
- **Downgrade/arbitrary-version install.** The engine applies the manifest's
  version subject to `min_version`; picking an arbitrary older version is a separate
  (rarely-safe) operation.

## The contract (test harness)

The engine's side effects (download, backup, swap, restart, health probe, marker
I/O, clock) are injected, so the state machine is tested deterministically:

1. **Happy path.** A valid signed manifest + artifact ⇒ download, verify, swap,
   restart, health-check passes, marker cleared (confirmed). The live binary is the
   new one.
2. **Verify gate.** A manifest that fails signature/version checks, or an artifact
   whose SHA-256/signature fails, aborts **before any swap** — the live binary is
   untouched.
3. **Health-check revert.** The new version fails its health check within the
   window ⇒ the rollback binary is restored, the service restarted, the marker
   cleared, and a clear reason returned. Live binary is the old one.
4. **Boot-time revert (power-loss).** With a pending marker at the boot-count limit
   (simulating "swapped, powered off before confirm, booted again unconfirmed"),
   `-update-boot-check` reverts to the rollback and clears the marker — the prior
   version boots. Below the limit, it increments and does not revert.
5. **Atomic-swap safety.** The swap is a same-directory rename (asserted), and an
   injected failure *before* the rename leaves the old binary in place; *after* the
   rename the new binary is in place and the rollback copy exists for revert.
6. **Manifest parse/plan.** Table-driven: newer applicable version ⇒ update
   available; equal/older ⇒ none; `min_version` above current ⇒ refused with a
   clear reason; missing artifact for the running arch ⇒ refused.

Manual (the #13 acceptance): on the bench unit, trigger an update and pull power at
staged points (during download, during/after swap, during health check); the unit
comes back booting — the prior version until an update confirms, the new one after.

## Alternatives considered

- **In-place `cp` over the running binary, no rollback (the incumbent).** Rejected
  — it is the corruption class the requirement lists. A non-atomic overwrite with
  no verified rollback is exactly what bricks on interruption.
- **A/B slots for Phase 1 too.** Rejected as premature — slots need the image
  layout (#64) and bootloader integration that a package install on stock Raspberry
  Pi OS does not have. The package engine ships the guarantee now; the A/B backend
  slots in behind the same state machine for the image (Phase 3).
- **Health-check inside the new binary only (self-revert).** Rejected as the *sole*
  mechanism — a new binary that crashes on start cannot revert itself. The
  boot-time check (outside the new binary) is what makes a crash-on-start update
  recoverable; the in-binary health check is the fast-path confirm.
- **A package manager (`apt`/`.deb`) as the transaction.** Reasonable for the
  distro-package path and not precluded, but `apt` is not atomic across a power cut
  and does not give the health-gated auto-revert; the engine can wrap a `.deb`
  install as its "swap" step later. The primitive here is transport-and-format
  agnostic.
- **Sigstore/TUF for the update metadata.** Rejected for the same appliance reasons
  as RFC-0013: a signed manifest with a pinned key is offline-verifiable and
  sufficient; TUF's role separation is more than a single-maintainer channel needs.

## Open questions

1. **Bootloader hook for A/B (Phase 3).** The boot-count/confirm flag needs a
   bootloader that reads it (U-Boot `bootcount`, or a systemd-boot counter). Which,
   and how it is written from `-update-boot-check`, is the Phase-3 design tied to
   #64's image.
2. **Migration safety across an update.** The store migrates forward on start
   (RFC-0001) with a pre-migration backup; an update that ships a schema bump relies
   on that backup for the revert path to still open the old store. Worth an explicit
   test once a real migration exists.
3. **Delta/partial updates.** The engine fetches a whole binary. For a Pi on a slow
   link a binary delta would help; deferred — a whole signed artifact is simplest
   and safe, and the binary is ~13 MB.
4. **Update source trust on first boot.** The manifest URL + release key are build
   defaults; an operator on a private channel sets their own (`-update-url`,
   `-release-pubkey`). Rotating the release key mid-fleet is the RFC-0013 open
   question, inherited here.
