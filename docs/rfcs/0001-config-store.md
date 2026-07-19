# RFC-0001: The Configuration Store

- Status: **accepted** (2026-07-19)
- Author: KN4OQW
- Comment window: 14 days from PR open (elapsed; accepted by sole maintainer)
- Implements requirements: #1 (lossless round-trip), #2 (override layer), #3 (profiles)

## Summary

All Waypoint configuration lives in a single SQLite database owned by `waypointd`. Gateway INI files are deterministic, diffable *compiled outputs* of that store. Nothing ever parses a generated file back. This RFC defines the store's shape, the generation pipeline, the override layer, and the losslessness test contract.

## Motivation

The single largest complaint family across the incumbent platforms is configuration destruction: web forms POST → regex-rewrite INI files → unrelated settings vanish (Pi-Star #58, #72, #86, #98, #103, #132, #182–#185, #190). The root cause is architectural — the INI files are simultaneously the UI's read model, the write target, and the daemons' input, with no schema between them. The fix is to make the store authoritative and the files disposable.

## Design

### Store

- One SQLite file: `/var/lib/waypoint/config.db`, WAL mode, owned by `waypointd` (never edited by hand — the override layer is the human escape hatch).
- Two tables carry the design:
  - `settings(key TEXT PRIMARY KEY, value JSON, updated_at, updated_by)` — a typed key tree (`dmr.network.brandmeister.enabled = true`), validated against a versioned JSON Schema before write. Unknown keys are rejected, not silently kept.
  - `meta(schema_version, device_id, claimed_at, ...)`.
- **Schema migrations are explicit**: numbered Go migration functions; the daemon refuses to run on a future schema (rollback safety) and migrates forward automatically with a pre-migration backup file.
- Settings for **disabled modes are ordinary rows** — disabling DMR flips `dmr.enabled`, it deletes nothing. This makes the incumbent failure mode structurally impossible rather than carefully avoided.

### Generation pipeline

```
store  →  typed model (Go structs)  →  per-daemon renderers  →  staged dir  →  atomic swap
```

- Renderers emit INI text for each stack daemon (MMDVM-Host, DMRGateway, …) from the typed model. Rendering is a pure function: same store ⇒ byte-identical output.
- Generated files land in a staging directory, then swap into `/etc/waypoint/generated/` atomically (rename), then affected daemons restart via the supervisor. A header comment in every generated file names the source and warns that edits will be overwritten — and points at the override layer.
- The UI's "preview" (expert persona) shows the *rendered diff* before apply.

### Override layer

- `/etc/waypoint/overrides.d/<daemon>.d/*.conf` fragments merge **last** into the rendered output, keyed by INI section: an override section replaces keys it names, leaves the rest.
- **Precedence among fragments is lexical filename order** (the `10-name.conf` convention); when two fragments set the same section/key, the later filename wins. The UI's override view shows the effective winner per key.
- **Deletion is expressible**: a key set to the literal token `!unset` removes that key from the rendered output entirely (suppressing a rendered default, not just replacing its value).
- **Accepted risk, stated plainly**: override fragments are hand-edited disk files and bypass the store's JSON-Schema validation. They are the human escape hatch; the store's validation guarantees do not extend to their *content*. What is guaranteed — and tested (property 5) — is that overrides re-apply deterministically and don't interact with unrelated store changes.
- Overrides carry a provenance field in the daemon's model (`disk` today; `ui` reserved) so UI-managed overrides can later reuse the `applies` journal without a schema migration.
- Hostfile-style resources get `prepend.d`/`append.d` hooks instead (they aren't INI).
- Active overrides are surfaced in the UI (name, target, diff vs. rendered base) — visible, not fought. Updates never touch `/etc/waypoint/overrides.d`.

### Profiles

- A profile is a named, exported subset of the key tree (the `network.*` and `mode.*` namespaces by default), stored in a `profiles` table and exportable as a JSON file (optionally minisign-signed).
- **Secrets never leave the device by default**: schema keys carrying credentials are annotated `sensitive: true`, and sensitive keys are excluded from profile *export* (exported as a named placeholder requiring re-entry on import). In-store profile switching retains them. This annotation also drives redaction in UI diffs and logs.
- The export format carries a **hardware fingerprint block** (board family, TCXO frequency) from day one, so importing a 14.7456 MHz profile onto a 12.288 MHz board can warn — the field exists in the schema even before the warning UI ships.
- Switching a profile = transactional bulk write of that subset + regenerate + supervised restarts. Target: < 5 s on a Pi 3.
- Keys outside the profile's namespaces (device identity, auth, hardware calibration) are never part of a profile — switching can't brick access or lose calibration.

### API surface

- `GET/PUT /api/config/{key}` (typed, schema-validated), `POST /api/config/apply` (render+swap+restart, returns the diff it applied), `GET /api/config/preview`, profile CRUD + `POST /api/profiles/{name}/activate`.
- Every apply is journaled (`applies` table: who, when, diff) — the UI's "what changed" history and the debugging story for "it worked yesterday."

## The losslessness contract (test harness)

CI enforces, as release-blocking property tests:

1. **Round-trip**: randomized valid stores (property-based, covering every schema key) render → daemon-parse (using the daemons' own INI readers where feasible) → no semantic loss vs. the model.
2. **Isolation**: for random pairs (change key A, observe key B≠A): applying A never alters B's rendered output outside A's section.
3. **Disable/re-enable**: toggling any `*.enabled` off, applying unrelated changes, toggling back on ⇒ byte-identical section to the original.
4. **Migration**: every historical schema version's fixture DB migrates to head losslessly — and a migration interrupted at any step (crash-injection) leaves a system that boots on the pre-migration backup.
5. **Override determinism**: with randomized override fragments active (including same-key conflicts across fragments and `!unset` markers), properties 1–3 still hold for the non-overridden surface, and the override-affected keys re-render identically across repeated applies and unrelated store changes.
6. **Profile round-trip**: export → import into a fresh store → activate ⇒ rendered output for the exported namespaces is byte-identical, with `sensitive: true` keys verified absent from the export artifact.

## Alternatives considered

- **Keep INIs authoritative, edit carefully** — this is the incumbent design; two decades of evidence says careful isn't enough.
- **Plain-file store (YAML/TOML in git)** — attractive for hand-editing, but concurrent API writes, migrations, and journaling all get harder; the override layer preserves the hand-edit escape hatch with clearer semantics.
- **etcd/consul-style KV daemon** — absurd on a Pi Zero W; SQLite is the embedded standard and `waypointd` is the only writer.

## Open questions

1. Should overrides be expressible *in* the store (UI-managed) as well as on disk? Leaning yes, later — disk first for the update-survival guarantee. *(Partially settled in review: the override model carries a provenance field from day one so the UI path needs no migration.)*
2. Secrets **at rest** (encryption/keyring): deferred to RFC-0002 (security posture). *(Settled in review: secrets in transit are handled now — `sensitive: true` schema annotation, excluded from profile exports, redacted in diffs/logs.)*
3. ~~Whether profile export should include a hardware-fingerprint block~~ *(Settled in review: yes — the schema field ships with the export format; the warning UI may follow later.)*
