# Waypoint Governance

This document is a set of commitments, written down *before* the project had users, because the problems it prevents killed or captured every hotspot platform before it.

## Principles

1. **Everything public.** Code, CI, releases, image builds, and decision-making all happen in public on GitHub. No private infrastructure sits in the critical path of building, releasing, or contributing.
2. **No telemetry. Ever.** Waypoint devices contact project infrastructure only to check for updates and refresh public host/ID databases, and both are user-disableable without losing anything else. Support is never conditioned on data collection. This policy can only be changed by the RFC process with a supermajority of maintainers — and the default answer is no.
3. **The review SLA.** Every pull request and every substantive issue receives a human maintainer response within **14 days**. "No, because…" is an acceptable response; silence is not. AI-assisted triage (below) provides a first technical read much faster, but the SLA is about humans.
4. **No single owner.** The project's stated goal is **at least three maintainers with full merge and release rights**, from at least two countries, within the first year. Bus-factor-one is treated as an incident, not a norm.
5. **Compete on quality only.** We do not disparage other projects, reuse their code, or scrape their infrastructure. Waypoint wins by being better, or it doesn't win.

## Roles

- **Contributor** — anyone who opens an issue or PR. No CLA; the DCO sign-off (`git commit -s`) is required.
- **Maintainer** — merge rights, release rights, security-report access. Granted by consensus of existing maintainers to contributors with a track record of quality and judgment (typically ≥5 merged non-trivial PRs and participation in reviews). Recorded in MAINTAINERS.md.
- **Emeritus** — maintainers who step back keep recognition, lose keys. Any maintainer inactive for 6 months moves to emeritus automatically (and is welcome back).

## RFCs

Changes that alter architecture, protocol/API contracts, security posture, or governance itself require an RFC: a markdown document PR'd to `docs/rfcs/`, open for comment for at least 14 days, accepted by consensus of maintainers (supermajority if contested). Small changes never need an RFC — when in doubt, open an issue and ask.

## AI-assisted triage

Waypoint uses Claude (Anthropic) via GitHub Actions as a triage and review assistant:

- New issues get an automated first read: reproduction check, affected-area labeling, duplicate detection.
- New PRs get an automated technical review: correctness concerns, test coverage, style drift — posted as comments for the author and human reviewers.
- Anyone can mention `@claude` in a thread for interactive help.

**Boundaries:** the AI never merges, never closes, and never has the last word — maintainers do. Its comments are advisory. If AI triage is ever wrong or unhelpful, say so in the thread; that feedback tunes the prompts, which live in `.github/workflows/` and are themselves subject to PR review.

## Security

See [SECURITY.md](SECURITY.md). Private reporting via GitHub's vulnerability reporting; response SLA 72 hours; coordinated disclosure.

## Decision log

Significant decisions and their reasoning are recorded in `docs/decisions/` — short, dated entries. Future contributors deserve to know *why*, not just *what*.
