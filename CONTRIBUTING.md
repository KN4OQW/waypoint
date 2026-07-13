# Contributing to Waypoint

Thanks for being here. This project exists because community contributions to the incumbent hotspot platforms too often disappeared into a void — so the first rule is:

> **Every PR and substantive issue gets a human maintainer response within 14 days.** If we're going to say no, you'll hear the reasons.

## Where to start

- The [requirements register](https://github.com/KN4OQW/waypoint/issues?q=is%3Aissue+label%3Atype%3Arequirement) is the project's backlog — every item cites the real-world complaint that motivated it. Priorities: `P0` (MVP), `P1` (v1.0), `P2` (roadmap).
- Issues labeled `good-first-issue` are curated entry points.
- Feature-scale ideas: open an issue first (or an RFC for architecture-level changes — see [GOVERNANCE.md](GOVERNANCE.md#rfcs)). Nobody here will ask you to "seek permission"; we just want to help you aim before you invest.

## Ground rules

- **DCO sign-off** on commits (`git commit -s`). No CLA.
- **Tests accompany behavior changes.** The config round-trip guarantee in particular is enforced by tests — a PR that breaks losslessness will be caught by CI, not by a user losing their DMR password.
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
```

The UI (`ui/`) lands in Phase 1 (Svelte). The g4klx stack builds live in [waypoint-stack](https://github.com/KN4OQW/waypoint-stack).

## Releases

Tagged, changelogged, signed. No rolling "trust the update button" releases — see the update-lifecycle requirements (`P0`) in the register.
