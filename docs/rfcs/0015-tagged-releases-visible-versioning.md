# RFC-0015: Tagged Releases, Changelogs, and Visible Versioning

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #14 (semver tags, generated changelogs, a version visible in the UI/API/CLI, release automation — `waypointd --version`, the dashboard footer, and the GitHub release page always agree)
- Depends on: RFC-0013 (release signing — the manifest and each artifact are minisign-signed) and RFC-0014 (the atomic updater — this RFC emits the `update.json` manifest the updater fetches)

## Summary

A Waypoint release is a **git semver tag**, and everything downstream agrees with
it: CI stamps the tag into the binary (`main.Version`), `waypointd -version` prints
it, `/api/health` reports it, the dashboard footer shows it, and the GitHub release
page is named for it — one version, four surfaces, no drift. The release workflow
already builds and signs the per-arch binaries on a `v*` tag (RFC-0013); this RFC
closes the remaining gaps: a **`-version` flag**, **generated release notes**
(GitHub's changelog from the merged PRs since the last tag), and — the connective
tissue to RFC-0014 — a **signed `update.json` manifest** published as a release
asset, so the atomic updater has something real to fetch. The acceptance is a
single invariant: the version you can read four ways is the same string.

## Motivation

The requirement's rap sheet is a versionless rolling release seen from outside:
Pi-Star #6 asked for tags/releases, and the *"Is Pi-Star dead?"* forum thread
(p=25040) is what a project with no visible version and no changelog looks like to
its users — you cannot tell what you are running, whether it is current, or what
changed. Waypoint already has the honest version *internally* (`main.Version`,
stamped by the release build and surfaced on `/api/health` and the UI), but three
things are missing to make it a real release lifecycle:

1. There is **no CLI way** to ask a binary its version — the acceptance names
   `waypointd --version` explicitly, and a headless operator (or the updater's own
   tooling) needs it without hitting the HTTP API.
2. A tag produces signed binaries but **no changelog** — a release page with a bare
   asset list is not "what changed."
3. The atomic updater (RFC-0014) fetches an `update.json` **manifest that nothing
   currently produces** — so the release automation must emit and sign it, or the
   whole update path is inert.

None of this is speculative: it is the last mile that turns "CI builds signed
binaries" into "there are releases users and machines can reason about."

## Design

### One version, stamped once

The single source of truth is the git tag. The release workflow already builds with
`-ldflags "-X main.Version=${GITHUB_REF_NAME}"`, so a `v1.3.0` tag yields a binary
whose `main.Version == "v1.3.0"`. Every other surface reads that one variable:

- **CLI:** `waypointd -version` (and `--version`, which Go's flag package accepts
  for the same flag) prints `waypointd <version>` and exits 0, before any daemon
  startup — the same early-exit shape as `-verify` (RFC-0013) and the update modes
  (RFC-0014).
- **API:** `/api/health` already returns `version` (unchanged). It is the pre-auth
  surface the UI footer reads, so the version shows even on the login screen.
- **UI:** the dashboard footer (`#foot-version`) and the settings "VERSIONS" card
  already bind to `/api/health.version` (unchanged) — so the UI agrees by
  construction, not by a second copy of the string.

A non-release (local) build still reports `dev`; CI's non-tag builds stamp the
short SHA (`ci.yml`, unchanged) so a `main` build reports something traceable. Only
a **tagged** build reports a semver — which is exactly when the four surfaces must
agree.

### Generated changelog

The release action gains `generate_release_notes: true` — GitHub composes the
release body from the pull requests merged since the previous tag, with
contributors and a full-changelog link. This is a zero-dependency changelog that
stays honest because it is derived from the same merge history the tag sits on
top of; there is no hand-maintained `CHANGELOG.md` to drift. (A conventional-commits
tool like `git-cliff` is a possible future refinement — see Alternatives — but the
GitHub-native notes meet the requirement without adding a build dependency.)

### The signed update manifest (the tie to RFC-0014)

After the per-arch build matrix uploads its signed binaries, a final `manifest`
job assembles `update.json` and publishes it (and its `.minisig`) as release
assets:

```json
{
  "version":     "v1.3.0",
  "min_version": "",
  "notes_url":   "https://github.com/KN4OQW/waypoint/releases/tag/v1.3.0",
  "artifacts": {
    "linux/amd64": { "url": "https://github.com/.../download/v1.3.0/waypointd-linux-amd64", "sha256": "…" },
    "linux/arm":   { "url": "https://github.com/.../download/v1.3.0/waypointd-linux-arm6",  "sha256": "…" },
    "linux/arm64": { "url": "https://github.com/.../download/v1.3.0/waypointd-linux-arm64", "sha256": "…" }
  }
}
```

- **Keys are the node's `GOOS/GOARCH`** — exactly what RFC-0014's `PlanUpdate`
  matches against (`linux/arm` covers the GOARM=6 Pi Zero build; the file suffix
  `arm6` is just the asset name). `amd64`/`arm64` map straight through.
- **`sha256`** is computed over each actually-uploaded artifact, so the manifest and
  the binaries cannot disagree.
- **The manifest is itself minisign-signed** with the same release key as the
  binaries (RFC-0013), so the updater verifies *what to fetch* (the manifest) and
  *what it fetched* (the artifact) under one pinned key. Signing is gated on the
  `MINISIGN_SECRET_KEY` secret exactly like the binary step — without it the job
  uploads an unsigned `update.json` and warns, so a fork never hard-fails.
- It is published under the stable `releases/latest/download/update.json` URL (the
  updater's built-in default, RFC-0014), because GitHub's "latest" release
  redirects there.

`min_version` is emitted empty (no upgrade floor) until a release actually needs
one; the updater treats empty as "no floor."

### What stays out

- **A hand-written `CHANGELOG.md` in the tree.** Rejected as drift-prone — the
  generated notes are derived from merge history and cannot fall out of sync.
- **Rewriting `main.Version` into a struct with build date/commit.** The acceptance
  is about *agreement*, not richness; a single string kept honest across four
  surfaces is the win. Build metadata is a cheap later addition behind the same
  variable.
- **A dedicated `/api/version` endpoint.** `/api/health` already carries the version
  and is the pre-auth surface the footer reads; a second endpoint would be a second
  thing to keep in sync for no gain.

## The contract (test + acceptance)

1. **`-version` prints the stamped version and exits 0.** With `main.Version` set,
   `waypointd -version` writes `waypointd <version>` and exits before touching the
   store or the network — testable by building with an `-ldflags` stamp and asserting
   stdout, and by construction it reads the *same* variable `/api/health` does.
2. **Agreement invariant (the #14 acceptance).** For a tagged build, the string from
   `-version`, from `/api/health.version`, and from the release tag are identical —
   because all three derive from `GITHUB_REF_NAME` → `main.Version`. The UI footer
   binds to `/api/health.version`, so it agrees transitively.
3. **Manifest correctness.** The emitted `update.json` parses as an RFC-0014
   `Manifest`, its `version` equals the tag, and each artifact's `sha256` matches the
   uploaded binary — so RFC-0014's `PlanUpdate` on a node running an older version
   returns "available" for that node's `GOOS/GOARCH`. (An `update.json` round-trip
   through the updater's `Manifest`/`PlanUpdate` is a unit test; the CI emission is
   validated by the workflow on the next real tag.)
4. **Fork-safe.** With no signing secret the release and manifest jobs still
   complete and upload unsigned assets with a warning — never a hard failure.

Manual (on a tag): push `vX.Y.Z`, confirm the release page is named `vX.Y.Z`, has
generated notes, carries the three signed binaries plus a signed `update.json`, and
that a node built from that tag reports `vX.Y.Z` from both `-version` and
`/api/health`.

## Alternatives considered

- **`git-cliff` / conventional-commits changelog.** A richer, categorized changelog
  (features/fixes/breaking) generated from commit prefixes. Reasonable and not
  precluded, but it adds a build dependency and presumes commit-message discipline;
  GitHub's PR-derived notes meet "generated changelogs" today with zero deps. This
  is the natural upgrade if the notes prove too coarse.
- **Version file in the tree (`VERSION`) as the source of truth.** Rejected — it
  duplicates the git tag and invites "tagged v1.3.0 but the file says v1.2.0" drift.
  The tag is the one fact; everything derives from it.
- **Publishing the manifest from a separate "release" repo or a Pages endpoint.**
  Rejected as premature — a GitHub release asset under the `latest` redirect is a
  stable, CDN-backed URL the updater already defaults to; a separate host is more
  moving parts for the same bytes.
- **Semantic-release / fully automated version bumping.** Rejected for a
  single-maintainer appliance — deciding the version and cutting the tag by hand is
  a feature (deliberate releases), not a chore worth automating away yet.

## Open questions

1. **`min_version` policy.** When a release ships a store migration that the prior
   version cannot read back (the RFC-0014 revert path), the manifest should set
   `min_version` to the oldest still-upgradable release. The trigger and the value
   are a per-release decision; this RFC emits it empty until the first such release.
2. **Pre-release/RC tags.** `v1.3.0-rc1` sorts (RFC-0014's semver drops the
   pre-release suffix, so an RC and its final compare equal). Whether RCs publish a
   manifest to the `latest` channel or a separate pre-release channel is a channel
   question shared with #15's opt-in checking.
3. **Release-notes provenance in the manifest.** `notes_url` points at the release
   page; if the changelog later moves to a docs site, the URL source changes but the
   field does not.
