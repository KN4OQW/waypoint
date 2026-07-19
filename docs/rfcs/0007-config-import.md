# RFC-0007: Config Import from Pi-Star / WPSD

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #4 (config import from existing Pi-Star / WPSD SD cards)
- Depends on: RFC-0001 (the configuration store — a migration is a one-time bulk write of an imported model, then the store is authoritative and the incumbent files are never read again)

## Summary

A one-time migration that reads an incumbent hotspot's config files — from a
**mounted SD card** or **uploaded files** — maps them into the Waypoint store, and
**reports anything it could not map**. The operator points Waypoint at their old
Pi-Star/WPSD card, reviews a scan (what was found, what will import, what won't
carry over), and commits. Zero reconfiguration for the settings that transfer;
honest, itemized visibility for the ones that don't.

The parsing core already exists: `internal/config/fromINI` builds a full `Model`
from parsed MMDVM-Host + gateway INIs (it is how a fresh store seeds from the
node's own files). This RFC adds the three things a *migration* needs on top of
that seed path: **locating** the incumbent files (their names and layout differ
from Waypoint's), a **migration report** (the "report anything unmappable"
requirement), and a **scan-then-apply** flow so the operator previews before the
store is touched.

The guarantee (#4 acceptance, enforced by test): a stock **Pi-Star 4.2.1** card
and a **current WPSD** card each import with callsign, DMR/CCS7 IDs, RX/TX
frequencies, and enabled networks intact.

## Motivation

Requirement #4 names the adoption barrier precisely: on the incumbent platforms a
version jump can force a *full reconfiguration* (forum.pistar.uk t=4911), and that
friction is exactly what keeps operators on an old release rather than moving. The
lever is the inverse — make switching *to* Waypoint cost nothing: read the card
they already have, carry everything that maps, and tell them plainly what doesn't
so there are no silent surprises after the switch.

Waypoint is unusually well-positioned to do this cleanly because of RFC-0001: the
store is the authoritative model and the INIs are disposable compiled outputs, so
"import" is not a risky in-place edit of a live config — it is a bulk write into a
fresh model that the operator previews first and that becomes authoritative only
on commit. The one place INIs are parsed *into* the model already exists and is
already tested (the seed path); migration reuses it rather than inventing a second
parser.

## Design

### Locating the incumbent files

Pi-Star and WPSD store their daemon configs in `/etc/` with lowercase,
extension-less names — `mmdvmhost`, `dmrgateway`, `ysfgateway`, `p25gateway`,
`nxdngateway`, `dstargateway`, `m17gateway`, `dapnetgateway`, `dgidgateway` —
which differ from Waypoint's `MMDVM-Host.ini` / `DMRGateway.ini` names. A
role→candidate-names table (`incumbentFiles`) maps each daemon role to the names
it may appear under (the Pi-Star lowercase form plus the `.ini`-suffixed WPSD/
generic variants). `Locate(dir)` searches, for each role, the given directory and
its `etc/` subdirectory (a mounted SD card's root partition presents the configs
at `<mount>/etc/…`) and returns the first match per role.

Platform detection is best-effort and informational: a `pistar-release` /
`WPSD-release` marker file (or a `.wpsd`/`pistar` token in the tree) sets the
report's `platform` field. Nothing downstream branches on it — the same
`fromINI` mapping serves both, because both emit the same g4klx daemon INI
dialect. Platform is surfaced so the operator sees "detected: Pi-Star 4.x", not so
the code forks.

### Two input modes, one migration

- **Mounted directory.** The operator inserts the old card (or a USB reader) and
  gives Waypoint the mount path; the server reads the located files. `Locate`
  does the walking.
- **Uploaded files.** The operator copies the handful of `/etc/*` config files off
  the card and uploads them (multipart). No whole-card image upload — the config
  files are a few KB each; a full SD image is gigabytes and unnecessary. Uploaded
  parts are matched to roles by the **same** candidate-name table, so an operator
  who uploads `mmdvmhost` and `dmrgateway` gets the same mapping as a mounted
  scan.

Both paths converge on `Migrate(contents map[role][]byte)` — the role→bytes map is
the single seam, so mounted and uploaded imports are identical from there on and
share all tests.

### Mapping: reuse `fromINI`

`Migrate` parses each located file with the existing `ParseINI` and calls
`fromINI(mm, dg, yg, dgid, pg, ng, xg, mg, dpg)` — the exact function the seed
path uses — so every field the seed path already maps (identity, frequencies,
modem, all eight modes, the DMR network list with WPSD-type classification and
verbatim-rewrite preservation, the gateways) is mapped by construction, with no
second mapping to keep in sync. A role with no file found is passed as `nil`,
exactly as the seed path handles absent gateway files (that daemon's section takes
its documented defaults). MMDVM-Host is the one **required** file — with no
`mmdvmhost` there is nothing to migrate, and `Migrate` returns an error the UI
surfaces as "this doesn't look like a Pi-Star/WPSD card."

### The migration report ("report anything unmappable")

`Migrate` returns a `MigrationReport` alongside the model:

- **`Platform`** — detected incumbent + release string, or "unknown".
- **`Files`** — per role: found (with the resolved name) or missing.
- **`Modes`** — which modes the import enables (the at-a-glance "did my modes come
  across").
- **`Networks`** — each imported DMR network with how it classified: a clean WPSD
  **type**, or **custom (verbatim routing preserved)** when its rewrites were
  hand-tuned and kept as-is rather than regenerated. This is the honest half of
  "unmappable" — a preserved-verbatim network is imported *and* flagged so the
  operator knows its routing wasn't normalized.
- **`Unmapped`** — incumbent **features Waypoint does not model**, detected by
  scanning the parsed INIs for known-but-unmodeled sections/keys: e.g. `[APRS]`
  reporting, `[Remote Commands]`, `[Mobile GPS]`, MMDVM-Host `[Lock File]`,
  NextionDriver blocks, and the retired cross-mode `*2*` daemons. Each entry names
  the file, the section, and a one-line "what it was" so the operator knows what to
  reconfigure natively rather than discovering it missing later. This list is a
  curated allow-known set, not a per-key diff — coarse enough to maintain, specific
  enough to act on. **No silent drops**: anything Waypoint doesn't carry is either
  in `Unmapped` or visibly a default in the preview.

### Scan, then apply

- `POST /api/import/scan` — input is either `{"dir": "/mnt/…"}` (mounted) or a
  multipart file set (uploaded). Returns `{report, preview}` where `preview` is the
  **redacted** model view (the same `View` the config API serves — secrets appear
  only as `has_*` booleans, never in the scan response). Writes nothing.
- `POST /api/import/apply` — same input, re-locates/re-parses, and on success
  **bulk-writes** the imported model to the store in one transaction
  (`store.SetMany` over the model's sections, RFC-0006's transactional writer), so
  a migration is all-or-nothing. Returns the report. The operator then reviews and
  hits Apply (the normal render+restart) — import populates the store; it does not
  itself restart daemons, so the operator sees the imported config in the settings
  UI before anything goes live.

Both are behind the session wall (RFC-0002), no gate change. Import is offered
prominently for a **fresh/unclaimed-then-just-claimed** node (the migration moment)
but is available any time; applying it over a configured store overwrites the
mapped sections (the operator previewed exactly what changes).

### UI

An **Import** surface (in the Expert tab's system area, and linked from a
first-run hint): choose *mounted path* or *upload files*, run **Scan**, and read
the report — platform, a found/missing file checklist, the mode summary, the
network list with type/custom badges, and the **Unmapped** list rendered as plain
"won't carry over: …" items. A **Preview** shows the resulting settings
(redacted). **Import** commits; a confirmation notes it overwrites current config
and that the operator applies afterward. Status is text/badges, not color (a11y
gate).

## The migration contract (test harness)

CI enforces these as release-blocking properties:

1. **Acceptance — Pi-Star 4.2.1.** A fixture of a stock Pi-Star 4.2.1 card's
   `mmdvmhost` + `dmrgateway` (+ gateways) migrates with callsign, DMR/CCS7 ID,
   RX/TX frequency, and every enabled mode/network intact — asserted field-by-field
   on the resulting model.
2. **Acceptance — WPSD.** The same, for a current WPSD fixture (its `.ini`-named
   files and M17 present).
3. **Location.** `Locate` finds the incumbent names in both `<dir>` and
   `<dir>/etc`, matches Pi-Star lowercase-no-ext and WPSD `.ini` variants, and
   reports a missing role as missing (not an error unless it's MMDVM-Host).
4. **Input equivalence.** The same fixture as a mounted directory and as an
   uploaded role→bytes map produces an identical model and report.
5. **Report fidelity.** `Networks` classifies a standard Brandmeister network as
   its type and a hand-tuned one as custom-preserved; `Unmapped` lists a known
   unmodeled section (e.g. `[APRS] Enable=1`) that is present, and does **not**
   list a section that isn't there.
6. **Apply is transactional and previewable.** `scan` writes nothing (store
   unchanged after a scan); `apply` writes exactly the mapped sections in one
   transaction and leaves excluded sections (auth, device identity beyond what the
   card carried) coherent; the scan response never contains a secret string.
7. **Round-trip with the seed path.** A model imported from a fixture, saved, and
   re-rendered produces INIs that re-parse to the same model (migration inherits
   RFC-0001 losslessness because it reuses `fromINI`).

## Alternatives considered

- **Whole-SD-image upload / auto-mount.** Rejected for v1 — a full card image is
  gigabytes and mounting arbitrary filesystem images server-side is a security and
  complexity burden. The config files are a few KB; a mounted path (for the
  operator who has the card in a reader) plus a small file upload covers the real
  workflow.
- **A second, migration-specific parser.** Rejected — `fromINI` already maps the
  full g4klx dialect and is losslessness-tested (RFC-0001). A separate parser would
  double the surface and drift. Migration adds *location* and *reporting* around
  the existing mapping, nothing that re-reads INI semantics.
- **Import straight into a live store (no preview).** Rejected — overwriting a
  config without showing what changes is the incumbent's sin. Scan-then-apply lets
  the operator see the report and the redacted preview before committing, and the
  commit is one transaction.
- **Branch on detected platform (Pi-Star vs WPSD code paths).** Rejected — both
  emit the same g4klx INI dialect, so one mapping serves both; platform is detected
  for the operator's information only. Fewer code paths, both covered by the same
  tests.
- **Silently default anything unmapped.** Rejected — "report anything unmappable"
  is the requirement. Unmodeled incumbent features are itemized in `Unmapped`;
  nothing is dropped without a line in the report.

## Open questions

1. **Auto-detecting a mounted card.** v1 takes a path (or upload). Scanning
   `/media`/`/mnt` for a partition that contains an `etc/mmdvmhost` and offering it
   automatically is a nice follow-up; the `Locate` primitive already does the file
   finding, so this is a UI/enumeration layer on top.
2. **Secrets on import.** Incumbent files carry DMR passwords / DAPNET AuthKey in
   cleartext; the migration imports them into the store (they were already on the
   operator's own card) but the scan **preview** redacts them like every other view.
   Should apply require the operator to re-confirm secret-bearing networks, or trust
   the card? Leaning trust-the-card (it's their own config) with the preview showing
   `has_password`, revisited if operators want a re-entry gate.
3. **DMRGateway `*2*` cross-mode configs.** The incumbent may run MMDVM_CM bridges;
   those are retired in Waypoint (RFC-0003 buses). v1 lists them under `Unmapped`
   so the operator knows to rebuild them as buses when that lands; auto-seeding bus
   definitions from the old bridge configs is a future tie-in with RFC-0003.
4. **Release-string parsing.** `Platform` reports the raw release marker; parsing it
   into a structured version (to warn on very old cards) is deferred.
