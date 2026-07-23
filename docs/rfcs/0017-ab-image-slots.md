# RFC-0017: A/B Image Slots for Whole-System Atomic Updates

- Status: **proposed** (design only; adoption is gated on the trigger in [Adoption trigger](#adoption-trigger))
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #64 (built image with A/B boot slots — the Phase-3 update engine named by [RFC-0014](0014-atomic-updates.md) and [docs/updates.md](../updates.md))
- Depends on: RFC-0014 (the confirm-or-rollback state machine this reuses over a boot slot instead of a binary), RFC-0013 (signed, verified artifacts), and the built image (the Prompt-4/5 CustomPiOS module + release pipeline)

## Summary

A/B image slots make a **whole-system** OS update atomic: a new image is written
to the **inactive** of two root slots while the device keeps running from the
**active** one, the device is rebooted **once** into the new slot on a **one-shot
trial flag**, and if it does not come up healthy the very next boot returns to the
old slot — untouched, because it was never written to. This is the same
verify → swap → health-check → confirm-or-revert state machine [RFC-0014](0014-atomic-updates.md)
already ships for the `waypointd` binary, with the **atomic unit changed from a
file to a boot slot** and the swap/restart/confirm steps bound to the Raspberry Pi
bootloader's `tryboot` mechanism instead of `rename(2)` + systemd.

This RFC is a **design document with no implementation**. It also does not propose
building A/B now. Waypoint's launch image ships a **single writable root** — that
is decision **D6**, restated and defended below — and this RFC exists to (a) record
the design so it is ready when needed, and (b) state the **evidence** that would
justify paying for it. A/B supersedes D6 only when that evidence arrives.

## Motivation

### What the current design already mitigates

Waypoint did not defer *update safety* — it deferred *whole-system* update safety,
having already closed the two failure modes that actually bite:

- **The daemon cannot brick the box.** A `waypointd` update is a verified download,
  an atomic `rename(2)` with the old binary kept as a rollback, a health gate, and
  a boot-time revert of an update that swapped but never confirmed (RFC-0014). Pull
  power at any point and the prior version boots.
- **A bad stack daemon reverts without a reboot.** The waypoint-stack `.deb`s are
  installed at *exact* versions, health-gated (service active + MMDVMHost's modem
  open), and rolled back to the previous versions kept in the repo's `pool/`
  (RFC-0014 Phase 2). This is a package operation, not an in-place `cp` over a
  running binary — the incumbent's corruption class.
- **The SD card is written far less.** `journald` runs `Storage=volatile` (a RAM
  ring buffer, not `/var/log/journal`), and nothing in Waypoint reads the journal
  back — event history is SQLite, live status is MQTT/SSE. The dominant continuous
  writer on a hotspot is gone.

Those three cover the *software* Waypoint ships and the *wear* it generates.

### What residual risk remains

They do **not** make an arbitrary write to the one ext4 root filesystem atomic
across a power cut. Three residual exposures remain, and they are the ones A/B
exists to close:

- **Interrupted `apt`/`dpkg` transactions.** `apt-get install` is *not* atomic
  across a power loss. A cut while `dpkg` is unpacking a package or writing its
  status database can leave the package database half-applied — a state the
  health-gate does **not** address, because the health-gate reverts a *bad but
  installed* version, not a *dpkg transaction interrupted mid-write*. RFC-0014
  itself flags this: *"`apt` is not atomic across a power cut."* The stack updater
  narrows the window (stop services → install exact versions → restart), but the
  window is nonzero and it is on the live root.
- **ext4 corruption on power loss.** The single writable root is written by more
  than Waypoint — `apt`, the SQLite store, and the base OS's own housekeeping. A
  power cut during any of those can corrupt the root filesystem's metadata badly
  enough to not mount. `Storage=volatile` cut the *frequency* of writes; it did not
  make the remaining ones transactional.
- **Kernel, firmware, and base-OS updates have no atomic path.** The image
  deliberately holds `raspberrypi-kernel`, `raspberrypi-bootloader`, `linux-image-*`,
  and `raspi-firmware` out of `unattended-upgrades` — precisely because an in-place
  kernel/firmware bump on a single root is the "an update bricked my Pi" failure
  (RFC-0014's motivation cites Pi-Star update loops t=3697, hangs p=21908, an RO/RW
  corruption class #126/#64/sbin #34, and WPSD's factory-reset-to-recover
  RadioReference 480017). Holding them out is safe but *stranding*: when one of
  those genuinely must move, there is no health-gated, revertible way to move it.

A/B closes all three with one property: **the running root is never the thing
being written.** The update lands on the inactive slot; a power cut mid-update can
only leave a partial *inactive* slot, and you boot the *active* one, which nobody
touched.

## Survey

Three production systems solve exactly this, and one incumbent solves a *different*
problem often mistaken for it. The differences are the design's inputs, not trivia.

**Mender** is the clearest statement of the pattern. Its documentation is blunt
about why: *"The simplest and most robust way to update a device is to write a new
file system image directly to the flash partition … a dual redundant scheme (also
known as A/B scheme), ensuring that the device always returns to a working state on
failure."* The rollback is a bootloader property, not application logic: *"If
something causes the device to reboot before committing the update, the bootloader
knows that something went wrong and will roll back to the previous version by
flipping the active and inactive partitions back again."* Crucially, Mender
requires the rootfs to be **stateless** — *"To be updatable, a filesystem needs to
be stateless"* — with all mutable data *"stored … in a separate partition."* That
constraint is the whole reason a data partition is non-negotiable (below).
([docs.mender.io/overview/introduction](https://docs.mender.io/overview/introduction))

**RAUC** generalises it and is the reference for the *mechanism*. It models
updatable storage as **slots** (*"any partition, full device or volume that can be
updated"*), grouped into redundant **slot classes** and **slot groups**; it detects
the **active** slot from the kernel command line and installs into an **inactive**
one; and it abstracts the bootloader behind a **bootchooser** interface (Barebox,
U-Boot, GRUB, EFI, or a custom backend), with **`mark-good`** / **`mark-bad`**
commands that confirm a boot or trip fallback. Signing is not optional: *"A key
design decision of RAUC is that signing a bundle is mandatory."* RAUC's shape —
active/inactive slots, a bootloader abstraction, an explicit confirm signal — is
almost exactly RFC-0014's engine with "slot" substituted for "binary."
([rauc.readthedocs.io](https://rauc.readthedocs.io/en/latest/basic.html))

**Home Assistant OS** is the proof that this runs on our exact target. HAOS uses
**RAUC** with **kernel A/B + rootfs A/B** slot groups
(`slot.kernel.0 bootname=A` / `slot.rootfs.0`, `slot.kernel.1 bootname=B` /
`slot.rootfs.1`), a **shared boot partition**, and its RAUC status DB on that boot
partition — and on Raspberry Pi it drives the switch through the **`tryboot`**
bootloader (`bootloader=custom` with a `rpi-tryboot.sh` handler). That is the same
board family and the same bootloader primitive Waypoint would use; HAOS having
shipped it to a large fleet retires most of the "will tryboot A/B even work on a
Pi" risk.
([home-assistant/operating-system `buildroot-external/ota/system.conf.gtpl`](https://github.com/home-assistant/operating-system/blob/dev/buildroot-external/ota/system.conf.gtpl))

**Pi-Star's read-only root** is the incumbent that solves a *neighbouring* problem,
and getting the distinction right is why Waypoint did not copy it. Pi-Star mounts
its root **read-only** and remounts it **read-write only for the duration of an
update or config write**, then back to read-only — visible in `pistar-upgrade`,
which brackets its work with `mount -o remount,rw /` … `mount -o remount,ro /`.
([AndyTaylorTweet/Pi-Star_Binaries_sbin `pistar-upgrade`](https://github.com/AndyTaylorTweet/Pi-Star_Binaries_sbin/blob/master/pistar-upgrade))

What RO root **buys** is real: while idle, the root is not writable, so a power cut
during normal operation cannot corrupt it, and SD wear drops. What it **costs** is
the part that matters here: the protection is off for exactly the window that is
dangerous. An update is *when writes happen*, so `pistar-upgrade` remounts
read-write, does a non-atomic in-place upgrade, and remounts read-only — a power
cut in that window corrupts a now-writable root with **no rollback**, the same
brick A/B prevents. RO root also imposes a standing tax: every legitimate write
(config change, log, cache) either fails confusingly or must be wrapped in a
remount, and forgotten remounts are a perennial Pi-Star support category. **RO root
protects the idle state; A/B protects the update.** They are not substitutes, and
RO root is the weaker of the two for the failure Waypoint's motivation is about.

**Why Waypoint rejected RO root at launch (D6).** The launch image ships a **single
writable root** and does *not* adopt Pi-Star-style read-only root. The reasoning:
RO root's headline benefit — fewer SD writes, no idle corruption — is *mostly*
already delivered by `Storage=volatile` (the continuous writer removed) without the
standing remount tax, and its *update*-time protection is illusory (it remounts
read-write to update, so the dangerous window is unprotected). Adopting RO root
would have bought a modest, partly-redundant idle benefit at the cost of real
day-to-day friction, while leaving the actual residual risk — a non-atomic update
on the live root — **unsolved**. Spending that complexity is only worth it as part
of the design that *does* solve it: A/B, where the running root is genuinely never
written and RO-ness becomes a free consequence of the rootfs being a sealed,
verified image rather than a goal pursued for itself. **D6 is binding**: no RO root
at launch. **A/B supersedes D6** under the conditions in [Adoption trigger](#adoption-trigger),
and when it does, a read-only A/B rootfs arrives *with* atomicity, not instead of
it.

## Proposed design sketch

*(Design only. Sizes, exact partition types, and the bootloader-handler details are
illustrative and settle at implementation time.)*

### Partition layout

Four roles, mirroring Mender/HAOS, on one SD card:

| Partition | Contents | A/B? | Writable at runtime |
|---|---|---|---|
| `boot` (FAT) | firmware, `autoboot.txt`, the two slots' `config.txt`/kernel/initrd, the A/B trial+confirm state | shared | only by the updater / boot-check |
| `rootfs-a` | complete sealed OS image (base OS, waypointd, the stack) | slot A | **no** (read-only in normal operation) |
| `rootfs-b` | the other slot | slot B | **no** |
| `data` | `config.db`, TLS cert/key, claim state, the update marker, rendered INIs, reference caches | shared | **yes** |

The two rootfs slots are **interchangeable, self-contained images**; nothing
operator-specific lives in them, so either can be replaced wholesale. Everything the
operator *is* — identity, credentials, configuration, history — lives on `data`,
which no slot switch touches (see [The data partition](#the-data-partition-store-certs-claim-survive-a-slot-switch)).
The boot partition is shared and holds the small amount of A/B *state* the
bootloader and the boot-check read and write.

### Boot selection on Raspberry Pi (`tryboot` / `autoboot.txt`)

The Raspberry Pi bootloader provides exactly the primitive A/B needs, and it is the
one HAOS uses, so Waypoint does not invent a bootloader:

- **`autoboot.txt`** on the boot partition selects which partition boots. It
  supports `[all]` / `[tryboot]` conditional sections and a `boot_partition=N`
  directive, and is capped at 512 bytes. The **default** (`[all]`) section names the
  currently-committed slot; the **`[tryboot]`** section names the *other* slot and
  sets **`tryboot_a_b=1`**, which tells the bootloader to load the normal
  `config.txt`/kernel from the *other* partition when the trial flag is set (rather
  than the usual `tryboot.txt` override).
- **`reboot "0 tryboot"`** sets a **one-shot** trial flag that "is automatically
  cleared after use." That one-shot property *is* the rollback: if the trial boot
  hangs or the power is cut, the flag is already spent, so the **next** boot falls
  through to the `[all]` (old, committed) slot with no timer, no counter, no code
  to run.

([raspberrypi.com — `autoboot.txt`, `tryboot`, `tryboot_a_b`](https://www.raspberrypi.com/documentation/computers/config_txt.html))

The update sequence on this primitive:

1. Write + verify the new image into the **inactive** slot. The active slot is
   untouched; a failure or power loss here changes nothing.
2. Set `autoboot.txt`'s `[tryboot]` section to the inactive slot with
   `tryboot_a_b=1`, `fsync`, and `reboot "0 tryboot"`.
3. The device boots the inactive slot **once**. If it reaches a healthy state it
   **commits**: rewrite `autoboot.txt`'s `[all]` section to the new slot (now the
   default) and `fsync`.
4. If it does **not** commit — bad image, hang, or power cut — the spent one-shot
   flag means the next boot is the old `[all]` slot. No revert code runs; the
   default was never changed.

This is strictly *safer* than the binary engine's boot-count scheme, because the
"try once, fall back on the next boot" behaviour is enforced by firmware, not by a
`boot_count` the daemon must maintain.

### Mapping the RFC-0014 engine onto a slot backend

RFC-0014 was written so this is a **second backend, not a rewrite**: *"the engine's
swap/revert/confirm steps are already behind an interface so the A/B backend is a
second implementation."* The state machine is identical; only what each side effect
*does* changes:

| RFC-0014 step (binary backend) | A/B (slot backend) |
|---|---|
| Verify manifest + artifact (RFC-0013) | unchanged — verify the signed **image** for the target slot before writing |
| Stage: write verified binary beside the live one | write the verified image into the **inactive** slot |
| Back up: copy live binary to `.rollback` | **free** — the *active* slot *is* the rollback; nothing to copy |
| Write durable marker (version + rollback path), `fsync` before swap | write the durable marker to `data` + set `[tryboot]` in `autoboot.txt`, `fsync` |
| Swap: `rename(staging, live)` | `reboot "0 tryboot"` (the "swap" is a trial boot, not a rename) |
| Restart service (systemd execs new binary) | folded into the reboot above |
| Health-check the running version, then clear marker (commit) | health-check after the trial boot; on success rewrite `autoboot.txt` `[all]` to the new slot + clear marker (commit) |
| Auto-revert: restore `.rollback`, restart | **no revert code** — the spent one-shot flag boots the old slot; the boot-check just clears the stale marker |
| `-update-boot-check` (ExecStartPre): boot-count limit → revert | ExecStartPre: if the marker says "trial" and this boot is the *committed* slot (i.e. we fell back), the trial failed → clear the marker + log; if we are in the trial slot and healthy, arm the commit |

The health signal is the same one the stack updater already uses — the node's own
`/api/health` reporting `ok` plus MMDVMHost's modem open (RFC-0008/0012) — so
"healthy after a trial boot" is not a new definition.

### The data partition (store, certs, claim survive a slot switch)

The single most important property: **a slot switch is invisible to the operator's
device.** Both rootfs slots mount the shared `data` partition at the same path, so:

- `config.db` (RFC-0001 store), the TLS device cert + key (RFC-0012), and the claim
  state (RFC-0002 `meta.claimed_at` + admin credential) are read from `data`,
  never from a rootfs slot. Switching or rolling back a slot does not re-claim,
  re-mint a cert, or drop configuration.
- The update marker and the rendered daemon INIs live on `data` too, so an
  in-flight update's state survives the reboot it triggers.

This inherits one hazard RFC-0014 already named (open question 2): a **schema
migration across a slot bump**. If the new slot's `waypointd` migrates `config.db`
forward on first start and the trial then fails, the fallback slot runs an *older*
`waypointd` against a *migrated* store. The mitigation is the one RFC-0001 already
mandates — a **pre-migration backup** of the store — so the fallback opens the
pre-migration copy. A/B does not change this requirement; it makes testing it
non-optional, because a rollback across a migration is now a routine event, not an
edge case. (Whether the migration should instead be deferred until *after* commit
is an [open question](#open-questions).)

### A/B vs the apt-based stack updates: does A/B make `apt` obsolete?

This is the honest tension, and it must not be waved away. A read-only A/B rootfs
and the RFC-0014-Phase-2 `apt` stack updater **cannot both write the same files**:
if the stack binaries live on a sealed read-only rootfs slot, `apt` cannot install
into the running system at all. Three coherent resolutions, argued:

- **Bake the stack into the image; retire `apt` for the running system.** Stack
  updates become whole-image A/B updates. This is the cleanest safety story — one
  atomic unit, one update path, HAOS's model — but it trades away everything the
  packaged stack bought: a one-daemon security fix now means building, signing,
  shipping, and **downloading a whole ~600 MB image and rebooting**, instead of a
  ~200 KB `.deb` and a service restart. On a hotspot on a slow domestic uplink that
  is a real regression in *update latency*, and it couples a DMRGateway patch to a
  kernel-grade release cadence.
- **Keep the stack on the writable `data` partition.** The rootfs slots stay sealed
  and read-only; the stack binaries + the apt state live on `data`, so the
  granular, no-reboot, per-service health-gated `apt` path (RFC-0014 Phase 2)
  survives *unchanged*, and A/B covers the base OS + kernel + `waypointd`. This
  keeps both guarantees at the cost of a fuzzier boundary — "the OS is atomic, the
  stack is packaged" — and of the stack no longer being part of the verified image
  hash.
- **Rootfs slots stay read-write.** `apt` works as today and A/B still gives
  whole-image atomicity, but the rootfs is no longer a sealed verified image and
  you re-inherit ext4-corruption exposure on the live slot between updates.

**The RFC's leaning is the second: keep the stack on `data`, A/B the OS.** It
preserves the fast, granular, already-shipped stack-update path — which is most of
the *actual* update traffic on a hotspot — while giving the base OS, kernel, and
`waypointd` the whole-image atomicity they lack today. `apt` is **not** made
obsolete by A/B; the two cover different layers, and collapsing them into one
image would regress the common case to fix the rare one. This leaning is a design
recommendation, revisitable at implementation, not yet binding.

## Migration from the single-partition image

Devices flashed with the Prompt-4/5 image have **one writable root and no data
partition**. The A/B layout is a *different partition table* — two rootfs slots
plus a separate data partition — and there is no safe, power-fail-tolerant way to
repartition a live SD card in place under a running hotspot. So, stated plainly:

> **Moving an existing single-partition Waypoint device to A/B requires a
> reflash.** There is no in-place upgrade path, and this RFC does not invent one.

The reflash is not a data-loss event, because Waypoint already has the pieces to
make it a *restore*: RFC-0007 (config import) and the RFC-0002 claim model mean the
operator can **export their configuration before reflashing and import it after**,
re-claiming the freshly-flashed A/B device and importing the profile to land back
where they were. The identity (callsign, DMR ID) and secrets travel in that export
(secret-redaction rules from RFC-0001 apply — some credentials are re-entered).
Documenting a clean "export → reflash A/B → import" runbook is part of the adoption
work, not a reason to pretend an in-place migration is possible.

## Non-goals

- **Read-only root as an end in itself.** RO-ness is a *consequence* of a sealed
  A/B rootfs, not a goal; this RFC does not adopt Pi-Star-style remount-for-writes
  root on the single-partition image (that is D6, and it stands).
- **Full-disk / at-rest encryption.** RFC-0002 open question 3 tracks encrypting the
  separate config partition on the purpose-built image; a data partition is a
  *prerequisite* for that conversation but A/B does not require or design it, and the
  launch tier still lacks a TPM to key it to.
- **More than two slots (A/B/C), delta images, or fleet orchestration.** Two slots
  is the whole guarantee; a third buys nothing for a single-operator appliance.
  Delta/partial images (RFC-0014 open question 3) and any server-side deployment
  orchestration are explicitly out.
- **Changing the trust model.** Images are signed and verified with the existing
  RFC-0013 primitive; A/B introduces no new key, channel, or PKI.
- **Committing to *when* Waypoint builds this.** This RFC is the design and the
  trigger, not a schedule.

## Adoption trigger

A/B is a large build — a custom partitioned image, a bootloader handler, a slot
backend, a data-migration runbook, and a test rig that pulls power at staged points
across a *reboot*. Waypoint does not pay for that on speculation. It also, by the
**no-telemetry policy**, does **not collect update-failure or corruption telemetry**
from the field — so the trigger cannot be a metric the project will never have. It
must be *qualitative evidence*, and the bar is set here so the decision is not
re-litigated ad hoc:

A/B **supersedes D6 and moves to implementation** when **any** of the following
holds:

1. **Field brick reports against the Waypoint image specifically.** Credible,
   reproducible operator reports (not Pi-Star's — those *motivate* but do not
   *measure* Waypoint's rate) that a power cut during a Waypoint OS/`apt` update, or
   ext4 corruption on power loss, left a Waypoint device unbootable — enough of them
   (a small handful of independent reports) to establish it is a real failure mode
   on *this* image, not a hypothetical. Because there is no telemetry, these arrive
   as issues; the project should make it easy to report one.
2. **A forced non-atomic OS move.** The base OS, kernel, or firmware reaches a point
   where it genuinely must be updated in the field (a serious remote-exploitable
   kernel/firmware CVE on the target boards) and the current design's only honest
   answer is "reflash" — i.e. the held-back-from-`unattended-upgrades` packages stop
   being safely deferrable. At that point the in-place path's absence is a concrete
   gap, not a theoretical one.
3. **A write-hostile deployment target.** A decision to support hardware or media
   where power-loss corruption is materially more likely than on the current Pi + SD
   tier (e.g. an unattended/solar site, or media with worse power-loss behaviour),
   making the residual risk's *probability*, not just its severity, unacceptable.

Absent all three, the residual risk is **accepted and revisitable**, exactly as
RFC-0002 accepts plaintext-at-rest: stated plainly, with the design that closes it
already on the shelf. Building A/B before the evidence would be spending scarce
single-maintainer effort to out-plumb Pi-Star's successor on a failure Waypoint's
own image has not yet been shown to suffer — the opposite of the project's
"deliberately second on plumbing, first on payload" posture ([docs/architecture.md](../architecture.md)).

## Alternatives considered

- **Adopt Pi-Star-style read-only root instead of A/B.** Argued and rejected in the
  [Survey](#survey): RO root protects the idle state Waypoint has *mostly* already
  protected with `Storage=volatile`, leaves the update window — the actually
  dangerous one — unprotected and un-rollback-able, and taxes every legitimate
  write. It is a partial fix for the wrong half of the problem. If Waypoint wants
  read-only-ness, it should get it *for free* from a sealed A/B rootfs, not pursue
  it standalone.
- **A watchdog + `bootcount` scheme (U-Boot / Barebox style) instead of `tryboot`.**
  This is what RAUC uses on U-Boot/Barebox targets, and it is perfectly sound — but
  it means shipping and integrating a different bootloader (U-Boot) onto a platform
  whose *own* firmware already provides the one-shot trial primitive. `tryboot`'s
  one-shot flag is *simpler and safer* than a boot-counter (there is no counter to
  get wrong, and no code runs on the fallback path), it is maintained by Raspberry
  Pi, and HAOS already runs it in the field on this hardware. Adding U-Boot to get a
  weaker version of a primitive we already have would be gratuitous.
- **RAUC (or Mender, or SWUpdate) as the update engine, wholesale.** These are
  excellent and would work. Rejected as the *engine* for the same appliance reason
  RFC-0013 rejected TUF and RFC-0014 rejected a package manager as the transaction:
  Waypoint already has a small, exhaustively-tested, injectable-seam confirm/rollback
  state machine (RFC-0014) whose health signal is *Waypoint's own* modem-open check,
  not a generic "did it boot." Pulling in RAUC would mean re-expressing that
  Waypoint-specific health gate inside RAUC's handler model and taking a dependency
  and its update-format on the whole fleet, to replace ~a few hundred lines we
  control and test. We adopt RAUC's *shape* (slots, a bootloader abstraction, an
  explicit confirm) and, where sensible at implementation, its `tryboot` handler as
  a reference — but the state machine stays ours. (This is a leaning, not a
  foreclosure: if the slot backend grows toward RAUC's complexity, reusing RAUC's
  battle-tested handler is a legitimate implementation choice to revisit then.)
- **A/B the whole image including the stack, retiring `apt`.** Argued in
  [A/B vs the apt-based stack updates](#ab-vs-the-apt-based-stack-updates-does-ab-make-apt-obsolete):
  cleanest single-atomic-unit story, but it regresses the *common* update (a
  one-daemon fix) from a small `.deb` + restart to a whole-image download + reboot,
  coupling the fast-moving stack to a kernel-grade cadence. Rejected as the default
  in favour of keeping the stack on the `data` partition so both paths survive.
- **Do nothing; accept the residual risk permanently.** Rejected as a *permanent*
  stance but adopted as the *current* one: the risk is real but unquantified on
  Waypoint's own image, the mitigations already in place remove its most common
  triggers, and the fix is expensive. "Accept, with the design and the trigger
  written down" is the honest position — which is this RFC.

## Open questions

1. **Slot count vs. SD capacity and image size.** Two full rootfs slots plus a data
   partition roughly doubles the OS footprint on the card; whether the launch tier's
   typical SD sizes comfortably hold two ~2 GB slots + data, and whether slots should
   be sized to the shrunk image or to a growth margin, is a capacity question for the
   image build.
2. **Migration timing across a slot bump.** Should `waypointd` migrate `config.db`
   forward *before* the trial commits (fast, but a rollback then needs the
   pre-migration backup, per RFC-0001) or *only after* commit (a rollback never sees
   a migrated store, but the trial boot runs the new code against the old schema)?
   The binary engine sidesteps this by not repartitioning; the slot engine must
   choose. Leaning: migrate a *copy*, commit the migrated store only when the slot
   commits.
3. **Where exactly the A/B trial+confirm state lives.** `autoboot.txt` is 512 bytes
   and firmware-read; the richer marker (which version is on trial, the pre-migration
   backup pointer) belongs on `data`. The split between "firmware-visible one-shot
   selection" and "daemon-visible update marker" needs nailing down so the
   boot-check reads a consistent picture after any power-cut point.
4. **`tryboot` on the full target board range.** HAOS validates `tryboot` A/B on
   recent Pis; the exact minimum bootloader version and behaviour across the D1
   board set (Zero 2 W through Pi 4) must be ground-truthed on real hardware before
   committing, since a board that mis-handles the one-shot flag would defeat the
   whole guarantee.
