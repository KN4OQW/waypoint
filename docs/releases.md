# Releases and versioning

A Waypoint release is a **git semver tag**, and every surface that reports a
version derives from that one tag — so the CLI, the API, the dashboard, and the
GitHub release page always agree. This is [RFC-0015](rfcs/0015-tagged-releases-visible-versioning.md),
issue [#14](https://github.com/KN4OQW/waypoint/issues/14).

## One version, four surfaces

| Surface | How to read it | Source |
| --- | --- | --- |
| CLI | `waypointd -version` (or `--version`) | `main.Version` |
| API | `GET /api/health` → `version` | `main.Version` |
| Dashboard | footer (`waypointd <v>`) + settings "VERSIONS" card | `/api/health.version` |
| GitHub | the release page name | the git tag |

They agree by construction, not by copying: the release build stamps the tag into
`main.Version` (`-ldflags "-X main.Version=<tag>"`), `-version` and `/api/health`
both read that variable, and the UI footer binds to `/api/health.version`. A local
build reports `dev`; a CI build off `main` reports the short commit SHA; only a
**tagged** build reports a semver — which is exactly when the surfaces must line up.

## Cutting a release

```console
$ git tag -a v1.3.0 -m "v1.3.0"
$ git push origin v1.3.0
```

Pushing a `v*` tag runs [`.github/workflows/release.yml`](../.github/workflows/release.yml),
which:

1. **Builds** `waypointd` for `linux/amd64`, `linux/arm64`, and `linux/arm` (GOARM=6,
   the Pi Zero), each stamped with the tag and `CGO_ENABLED=0` (a static binary).
2. **Signs** each binary with minisign ([RFC-0013](rfcs/0013-signed-releases-verified-downloads.md)),
   emitting a `.minisig` beside it.
3. **Publishes** the binaries + signatures to a GitHub release named for the tag,
   with a **generated changelog** — GitHub composes the release body from the pull
   requests merged since the previous tag (no hand-maintained `CHANGELOG.md` to
   drift).
4. **Assembles + signs `update.json`** — the manifest the atomic updater fetches
   ([RFC-0014](rfcs/0014-atomic-updates.md)). Its per-artifact SHA-256 is computed
   over the actually-uploaded binaries, and the manifest itself is minisign-signed,
   so a node can verify *what to fetch* and *what it fetched* under one pinned key.

The manifest is published as a release asset, reachable at the stable
`releases/latest/download/update.json` URL the updater defaults to.

### Signing is optional (fork-safe)

Both the binary and the manifest signing steps are gated on the
`MINISIGN_SECRET_KEY` repository secret. Without it — a fork, or before a key is
configured — the workflow still completes and uploads **unsigned** artifacts with a
warning, rather than hard-failing. Configuring the key is in [docs/signing.md](signing.md).

## The update manifest

```json
{
  "version":     "v1.3.0",
  "min_version": "",
  "notes_url":   "https://github.com/KN4OQW/waypoint/releases/tag/v1.3.0",
  "artifacts": {
    "linux/amd64": { "url": ".../download/v1.3.0/waypointd-linux-amd64", "sha256": "…" },
    "linux/arm":   { "url": ".../download/v1.3.0/waypointd-linux-arm6",  "sha256": "…" },
    "linux/arm64": { "url": ".../download/v1.3.0/waypointd-linux-arm64", "sha256": "…" }
  }
}
```

Artifact keys are the node's `GOOS/GOARCH` — what the updater's `PlanUpdate` matches
against. `linux/arm` is the GOARM=6 build (its asset suffix is `arm6`). `min_version`
is emitted empty (no upgrade floor) until a release needs one — for example a store
migration the prior version cannot read back, which would set it to the oldest
still-upgradable release.

## Applying an update

See [docs/updates.md](updates.md): `waypointd -update-check` reports whether the
manifest offers a newer build, and `waypointd -update` (or `POST /api/update/apply`)
performs the verified, health-gated, atomically-swapped install with automatic
rollback.
