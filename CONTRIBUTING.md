# Contributing to Waypoint

Thanks for being here. This project exists because community contributions to the incumbent hotspot platforms too often disappeared into a void — so the first rule is:

> **Every PR and substantive issue gets a human maintainer response within 14 days.** If we're going to say no, you'll hear the reasons.

## Where to start

- The [requirements register](https://github.com/KN4OQW/waypoint/issues?q=is%3Aissue+label%3Atype%3Arequirement) is the project's backlog — every item cites the real-world complaint that motivated it. Priorities: `P0` (MVP), `P1` (v1.0), `P2` (roadmap).
- Issues labeled `good-first-issue` are curated entry points.
- Feature-scale ideas: open an issue first. Nobody here will ask you to "seek permission"; we just want to help you aim before you invest. (The RFC process is dormant until v1 — see [GOVERNANCE.md](GOVERNANCE.md#rfcs) — so architecture-level changes are an issue and a PR too, not a separate ceremony.)

## Ground rules

- **DCO sign-off** on commits (`git commit -s`). No CLA.
- **Every behavior change ships with tests.** Not "the suite still passes" — that runs somebody else's tests. The question a reviewer asks is what your change asserts that nothing asserted before. Two tiers do different jobs: ordinary unit tests prove the code does what it meant to, and `test/tier2/` drives the **real pinned gateway daemons** against configs our renderers produced, which proves the daemon agrees those bytes are a valid configuration. If you change a renderer, ask whether Tier 2 can check the daemon still accepts the result.
- **A check that says something is wrong carries its evidence.** Cite the upstream source line or the protocol field width at the check itself, and include the passing cases beside the failing ones — a rule that fires on a working configuration is worse than no rule, because it teaches operators to ignore the panel. A rule you cannot ground gets left out rather than softened. `internal/config/mode_readiness.go` is the worked example.
- **The config round-trip guarantee** is enforced by tests — a PR that breaks losslessness will be caught by CI, not by a user losing their DMR password.
- **Accessibility is a merge gate for UI changes**: status must never be conveyed by color alone; interactive elements need keyboard/focus/ARIA coverage.
- **Copy style:** users manage *networks* and *modes*, not "config sections." Errors say what went wrong and what to do.
- Commit messages: imperative summary line; body explains why.

## AI triage

A Claude-powered workflow gives new issues and PRs a fast first technical read, and you can mention `@claude` in any thread to ask questions about the codebase, request a review pass, or get help reproducing a bug. It advises; humans decide. (Details and boundaries: [GOVERNANCE.md](GOVERNANCE.md#ai-assisted-triage).)

## Development quickstart

```sh
git clone https://github.com/KN4OQW/waypoint
cd waypoint
go build ./...     # daemon
go test ./...
go generate ./...  # refresh generated files; CI fails if any are stale
```

### Translations

UI strings live in flat JSON catalogs under `ui/static/locales/`, one file per
language, with `en-US.json` as the base every key originates from. Adding a
language is a new catalog plus `go generate ./...` to refresh
`ui/static/locales/index.json` — that index is generated from each catalog's
`_meta` block and must never be hand-edited.

The UI (`ui/`) lands in Phase 1 (Svelte). The g4klx stack builds live in [waypoint-stack](https://github.com/KN4OQW/waypoint-stack).

## Releases

Tagged, changelogged, signed. No rolling "trust the update button" releases — see the update-lifecycle requirements (`P0`) in the register.
