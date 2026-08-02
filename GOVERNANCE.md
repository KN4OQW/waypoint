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

**The RFC process is dormant until v1.** It is a mechanism for consulting a community, and consulting one that does not exist yet produces the form of consensus without the substance — a 14-day comment window that nobody comments in, "accepted by consensus" meaning one maintainer agreed with themselves. Running it in that state would teach contributors that the process is theatre, which is the harder thing to undo later.

So, pre-v1: architecture decisions are made in the open as issues and pull requests, and recorded in `docs/design/` and in the code comment that implements them. The existing numbered RFCs remain the design record and are cited throughout the code; nothing about them is retracted. Do not open new ones, and do not ask a contributor to.

It reactivates when there is a shippable v1 and a contributor base with a stake in the outcome — the point at which "open for comment" means something. At that point the process is: a document opened as a discussion in the [RFCs category](https://github.com/KN4OQW/waypoint/discussions/categories/rfcs), open for comment for at least 14 days, accepted by consensus of maintainers (supermajority if contested), with its status line edited in place as it moves from proposed to accepted.

**Dormant does not mean lowered.** Where a principle above names the RFC process as the only way to change something — principle 2's telemetry policy in particular — that remains true, and the cost of reactivating the process for it is part of the protection, not an obstacle to route around.

## AI-assisted triage

Waypoint uses Claude (Anthropic) via GitHub Actions as a triage and review assistant, funded by a maintainer's subscription quota:

- Issues and PRs from established contributors get an automated first read (reproduction check, duplicate detection) and a technical review pass (correctness, tests, accessibility).
- For new contributors, a maintainer mentions `@claude` to invoke the same review on your thread — this is quota protection against drive-by spam, not a trust judgment; expect it within the review SLA.
- Contributors with write access can mention `@claude` anywhere for interactive help.

**Boundaries:** the AI never merges, never closes, and never has the last word — maintainers do. Its comments are advisory. If AI triage is ever wrong or unhelpful, say so in the thread; that feedback tunes the prompts, which live in `.github/workflows/` and are themselves subject to PR review.

## Security

See [SECURITY.md](SECURITY.md). Private reporting via GitHub's vulnerability reporting; response SLA 72 hours; coordinated disclosure.

## Decision log

Significant decisions and their reasoning are recorded in `docs/design/` — short, dated entries, alongside the run logs that produced them. Future contributors deserve to know *why*, not just *what*. With RFCs dormant (above), this is where architecture reasoning lands pre-v1, so it carries more weight than the name suggests.
