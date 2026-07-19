# RFC-0006: Connection Profiles

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #3 (connection profiles — save complete setups, switch in one click)
- Depends on: RFC-0001 (the configuration store — profiles are a named snapshot of a *subset* of the store's sections; this RFC refines RFC-0001 §Profiles into a concrete, tested mechanism)

## Summary

A **profile** is a named snapshot of the *mode-and-network* subset of the config
store. Saving one captures "what this node connects to and how" — which modes are
on, every gateway's settings, the DMR network list and routing — as a single
named object. Activating one writes that whole subset back **atomically**, then
regenerates the INIs and restarts the affected daemons: one action swaps the
node's entire connection setup. Profiles export to a JSON file (secrets scrubbed,
a hardware fingerprint attached) and import back, so an operator can carry
"BM DMR duplex" and "YSF simplex" between nodes or share a known-good setup.

The subset is deliberate. A profile captures the **mode/network namespace** and
*nothing else*: device identity (callsign, DMR ID, frequencies), modem
calibration (offsets, levels, invert flags), the LCD panel, station policy
(history retention), and auth **are never part of a profile** — so switching a
profile can never change the node's callsign, lose calibration, or lock the
operator out. That boundary is RFC-0001's ("switching can't brick access or lose
calibration"), made explicit and enforced by the section allow-list below.

The guarantee (#3 acceptance, enforced as a property test): two profiles switch
in well under 5 s on a Pi 3 **without touching any setting outside the profile
namespace**, and an exported profile re-imported into a fresh store and activated
renders byte-identical output for the captured sections — with secrets verified
absent from the export artifact.

## Motivation

Connection profiles are the single most-praised SharkRF openSpot feature in every
comparison review, and both incumbents lack them: on Pi-Star/WPSD, switching from
"DMR to Brandmeister" to "YSF to a room" is a manual tour through several config
pages, editing overlapping fields by hand, hoping nothing else moved. The mental
model the operator holds — *"I have two setups and I want to flip between them"* —
has no object in the system.

RFC-0001 chartered profiles as one of its three requirements (#1 round-trip, #2
override layer, #3 profiles) and sketched the design; #3 is the last of the three
still open. The store makes profiles clean to build for the same reason it made
the override layer clean: sections are already independent, typed, JSON rows
(`store.Set` "never touches any other key"), so "capture these sections, write
them back later" is a straight projection with no field-by-field surgery and no
risk to the sections a profile deliberately excludes.

## Design

### The profile namespace

A profile captures exactly these store sections — the mode enables plus every
per-mode and network section:

```
modes
dmr  dmrnet  networks  routes
ysf  ysfgw   p25  p25gw   nxdn  nxdngw
dstar dstargw m17  m17gw   pocsag  fm
ysf2dmr dmr2ysf ysf2nxdn dmr2nxdn nxdn2dmr
```

Excluded, permanently: `general` (callsign, DMR ID, RF frequencies — device
identity), `modem` (offsets/levels/invert — hardware calibration), `display`,
`lcd` (the physical panel), `history` (station retention policy), and auth (its
own tables, never a config section). The allow-list is a single source of truth
(`profileSections`), and a **round-trip test asserts it partitions
`Model.sections()` cleanly** — every store section is either in the profile
namespace or on the excluded list, so a future section added to the model forces
a conscious choice rather than silently landing in (or out of) profiles.

One nuance stated plainly: *simplex vs duplex* lives in `general.Duplex` +
`modem`, which are **excluded**. A profile therefore switches the connection
topology (modes, networks, gateways), not the RF-hardware duplex setting — an
operator sets duplex once for their board. The illustrative profile names in #3
("BM DMR duplex" / "YSF simplex") describe the setups an operator saved, not a
claim that the profile flips the modem into simplex. Promoting `Duplex` into the
namespace is a possible future refinement (Open questions), gated on the "never
touch calibration" rule.

### Storage: a `profiles` table

Profiles live in their own table in `config.db`, created and accessed through
`store.DB()` — the same pattern the auth subsystem uses for its credential/session
tables (`internal/auth/store.go`), and for the same reason: a profile is not a
config *setting* (it is not rendered, not in the config view, not itself
profile-able), so it does not belong in the `settings` key tree, but its lifecycle
is config-adjacent and single-writer, so it shares the config connection rather
than a separate file.

```sql
CREATE TABLE IF NOT EXISTS profiles (
  name       TEXT PRIMARY KEY,
  data       TEXT NOT NULL,      -- JSON: {fingerprint, sections{section: value}}
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

The stored `data` holds the captured sections **with their real secrets** — a
locally-saved profile is on the device, never leaves it, so activating it can
restore exactly what was saved. Secrets are scrubbed only at the *export*
boundary (below). This matches RFC-0001: "secrets never leave the device by
default … in-store profile switching retains them."

### Capture

`CaptureProfile(store)` reads each `profileSections` row verbatim (`store.Get`,
raw JSON) into a `Profile{Name, Fingerprint, Sections}`. It is a pure projection —
no rendering, no restart — so "Save current setup as 'BM DMR'" is cheap and
side-effect-free. A missing section (fresh store) is captured as absent, not as a
forced zero value, so re-activating never invents rows the store did not have.

### Activate — atomic, secret-preserving

`ActivateProfile(store, profile, by)` writes the profile's sections back to the
store in **one transaction** (`store.SetMany`, a new transactional multi-upsert),
so activation is all-or-nothing: a crash mid-switch leaves the previous profile
intact, never a half-merged hybrid. Sections the profile does not carry are not
written at all — the excluded namespace is untouched by construction, which is the
#3 "without touching any other setting" acceptance as a structural property.

Secrets are **reconciled before the write**, honoring the store's established
blank-keep convention (`internal/config/networks.go`): for each secret-bearing
field, a **blank** value in the profile means *keep the store's current secret*, a
**non-blank** value replaces it. So:

- A **locally-saved** profile carries the real secrets → activation restores them.
- An **imported** profile has scrubbed (blank) secrets → activation **preserves**
  whatever secret the target node already had, and the operator re-enters any that
  are genuinely new. An import can therefore never blank out a working password.

The secret fields are the same set the config write-path already treats as
write-only: `networks[].password`, `dstargw.ircddb_password`, `pocsag.auth_key`,
and the DMR-master passwords on the `ysf2dmr` / `nxdn2dmr` bridges. They live in
one registry (`profileSecretFields`) shared by scrub and reconcile, so the two can
never drift.

After the transactional write, activation runs the **same** render-and-restart
path as `POST /api/config/apply` (extracted into a shared `applyConfig` helper),
so a profile switch and a manual apply are the identical operation downstream —
one code path to reason about, one set of restart semantics. Target: well under
5 s on a Pi 3 (≈22 section upserts in one transaction + the render/restart that
apply already does).

### Export — scrubbed, fingerprinted

`GET /api/profiles/{name}/export` returns a JSON artifact:

```json
{
  "name": "BM DMR duplex",
  "fingerprint": { "rx_freq_hz": "438800000", "tx_freq_hz": "431000000", "modem_port": "/dev/ttyACM0" },
  "sensitive": ["networks[].password", "pocsag.auth_key"],
  "sections": { "modes": {…}, "networks": [{…, "password": ""}], … }
}
```

- **Secrets scrubbed.** Every field in `profileSecretFields` is blanked, and the
  keys actually scrubbed are listed in `sensitive` so an importing UI knows which
  credentials to prompt for. A test asserts no secret string survives into the
  artifact.
- **Hardware fingerprint.** The RF frequencies and modem port from the *current*
  store's `general`/`modem` (which are **not** part of the profile) travel as a
  fingerprint block, so importing a profile captured on a differently-tuned board
  can warn (the warning UI may follow; the field ships now, per RFC-0001's "the
  field exists in the schema even before the warning UI ships"). Board-family and
  TCXO frequency join this block when hardware detection (#18) lands — the schema
  reserves them.
- Minisign signing is out of scope here (RFC-0012/security follow-up); the artifact
  is a plain, diffable JSON file today.

### Import

`POST /api/profiles/import` accepts an export artifact and stores it as a profile
row (name from the artifact, collision → 409 unless `?overwrite=1`). Secrets stay
blank; the `sensitive` list is preserved so the UI can flag "these need
re-entry." Import is **store-only** — it never activates — so importing is safe
and reviewable before the operator commits to a switch.

### API surface

All behind the session wall (RFC-0002), no gate change:

- `GET  /api/profiles` — list: name, timestamps, fingerprint, and an `active` flag
  (true when every captured section already equals the store — computed, not
  stored, so it is always honest).
- `POST /api/profiles` `{name}` — capture the current store as a profile (create or
  overwrite).
- `POST /api/profiles/{name}/activate` — activate + render + restart; returns the
  restarted units, exactly like apply.
- `DELETE /api/profiles/{name}` — remove a profile (never touches the live config).
- `GET  /api/profiles/{name}/export` — the scrubbed artifact (download).
- `POST /api/profiles/import` — store an artifact as a profile.

### UI

A **Profiles** tab (simple-persona home, per architecture.md's "simple: wizard,
profiles, live activity"): the saved profiles as cards with **Activate** /
**Export** / **Delete**, the active one badged; a "Save current setup as…" field;
and an **Import** file control that flags any `sensitive` credentials needing
re-entry. Activation shows the same restart feedback as Apply. Status is conveyed
by text/badges, not color alone (a11y merge gate).

## The profile contract (test harness)

CI enforces these as release-blocking properties, in the RFC-0001 style — this is
RFC-0001 property 6 made concrete plus the #3 acceptance:

1. **Namespace partition.** `profileSections` ∪ `profileExcluded` == every key in
   `Model.sections()`, with no overlap. A section added to the model without being
   classified fails this test.
2. **Capture/activate round-trip.** Capture a store, mutate the profile sections,
   activate the captured profile ⇒ every profile section is byte-identical to the
   capture and the rendered INIs for those sections match; **every excluded
   section is untouched** (the #3 "without touching any other setting" property).
3. **Atomicity.** Activation writes all profile sections or none — asserted by
   routing through the single `SetMany` transaction (an injected mid-write failure
   leaves the store at the pre-activation state).
4. **Secret reconciliation.** A profile with a blank secret preserves the store's
   current secret on activate; a profile with a real secret restores it. An
   imported (scrubbed) profile never blanks a working password.
5. **Export scrub.** For a store with secrets set, the export artifact contains no
   secret string, and `sensitive` names exactly the scrubbed keys.
6. **Export/import round-trip (RFC-0001 property 6).** Export → import into a fresh
   store (with the target's own secrets pre-set) → activate ⇒ rendered output for
   the profile namespace is byte-identical to the source for all non-secret keys,
   and the target's secrets are preserved.
7. **Switch cost.** Activation issues one transaction of N section upserts (N =
   profile-section count), asserted at the store call level, so the "< 5 s"
   acceptance is a structural property, not a benchmark that flakes on CI hardware.

## Alternatives considered

- **Profile = the whole store (everything, including identity/calibration).**
  Rejected — it is the RFC-0001 hazard: switching a profile could change the
  callsign or wipe TCXO calibration, i.e. brick access or detune the radio. The
  mode/network allow-list is the safety property, not an incidental scoping choice.
- **Profiles as rows in the `settings` tree.** Rejected: a profile is not a
  setting (not rendered, not in the config view, not itself profile-able), and
  nesting snapshots of settings inside the settings tree invites a section that is
  accidentally profile-able-and-in-a-profile. A sibling table keeps the settings
  tree exactly what RFC-0001 says it is.
- **Non-atomic activation (write each section with the existing `Set*` helpers).**
  Tempting — it would reuse the tested secret-merge functions directly — but a
  crash between two of ~22 writes leaves a half-switched hybrid config, which is
  precisely the "Apply ate my settings" failure class Waypoint exists to remove.
  The transactional `SetMany` plus an in-memory secret reconcile keeps activation
  all-or-nothing; the reconcile shares `profileSecretFields` with export so the
  two never diverge.
- **Export secrets encrypted-in-place (so a profile is fully portable).** Rejected
  for v1 — key management is a security-posture decision (RFC-0002 lineage), and
  "secrets never leave the device by default" is the safer default. The `sensitive`
  list + re-entry flow is the honest alternative: the importer supplies the
  credentials for the node they are configuring.

## Open questions

1. **Duplex in the namespace.** `general.Duplex` (and the modem RF settings that a
   simplex/duplex change implies) are excluded to honor "never touch calibration."
   Some operators genuinely keep a simplex and a duplex setup. Promote `Duplex`
   (only) into the profile namespace, or leave RF entirely out and let the profile
   name carry the distinction? Leaning leave-out for v1; revisit if asked.
2. **Signed exports.** Minisign-signing the artifact (RFC-0001 mentioned "optionally
   minisign-signed") is deferred to the security-posture track. The artifact schema
   has room for a detached-signature sidecar without a format change.
3. **Fingerprint mismatch warning.** The fingerprint ships now; the import-time
   "this profile was captured on a 14.7456 MHz board, yours is 12.288 MHz" warning
   UI is a follow-up. Board-family/TCXO fields join the fingerprint with hardware
   detection (#18).
4. **Active-flag semantics.** `active` is computed by comparing captured sections to
   the live store. If an operator hand-edits one field of the active profile's
   sections, the profile reads as inactive (correct — the live config no longer
   matches the snapshot). Surface a "modified since activated" state, or keep the
   binary active/inactive? Leaning binary for v1.
