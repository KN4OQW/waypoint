<!-- Thanks for contributing to Waypoint. Keep the summary line imperative and
     explain *why* in the body. DCO sign-off (git commit -s) is required. -->

## What & why

<!-- What does this change do, and what problem does it solve? Link the issue. -->

Closes #

## How it was verified

<!-- Tests added/updated, manual steps, on-hardware checks — whatever applies. -->

- [ ] `go test ./...` passes
- [ ] Behavior changes are covered by tests

## Privacy (no telemetry)

<!-- GOVERNANCE.md principle 2 is a merge gate too (issue #15): a Waypoint device
     contacts project infrastructure only to check for updates and refresh public
     host/ID databases, both user-disableable, and support is never conditioned on
     data collection. Fill this in for every PR. -->

- [ ] **No new outbound request** — this PR does not make the device contact any
      host it did not already contact.

If it *does* add or change one, confirm each of these:

- [ ] It sends **no device identifier** — no callsign, DMR/CCS7 ID, hostname, MAC,
      serial, per-device token, cookie, or version/config detail, in the URL, the
      headers, or a body.
- [ ] It is **gated on an operator-visible off switch**, and turning that switch
      off breaks nothing else.
- [ ] It is **documented** where an operator can find it (docs/updates.md's "What
      leaves the device", or the equivalent for that subsystem).
- [ ] The gate and the absence of identifiers are **covered by a test**, not just
      by intent.

## Accessibility impact

<!-- Accessibility is a merge gate (issue #7). Fill this in for every PR.
     If this PR does NOT touch the UI (ui/, dashboard, settings/wizard), tick
     "No UI changes" and skip the rest. -->

- [ ] **No UI changes** — this PR does not touch `ui/` or any operator-facing surface.

If it *does* touch the UI, confirm each of these (the `a11y` CI job enforces them):

- [ ] Status is **never conveyed by color alone** (text, icon, or shape backs it up).
- [ ] Every new/changed interactive element is **keyboard-operable** (reachable by Tab, activated by Enter/Space) with a **visible focus** ring.
- [ ] Interactive elements expose an **accessible name and role/state** (labels, `aria-label`, `aria-pressed`/`aria-current`, etc.).
- [ ] Text and UI meet **WCAG AA contrast** in all themes (phosphor, amber, ice).
- [ ] The **axe-core `a11y` check is green** (dashboard + settings/wizard, all themes).

<!-- Notes on any a11y trade-offs or follow-ups: -->
