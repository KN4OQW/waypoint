# Accessibility

Accessibility is a **merge gate** for Waypoint, not a nicety. Blind and
low-vision operators are a significant part of the amateur-radio community, and
their fixes to the incumbent platforms were too often ignored (Pi-Star #144/#146,
#174–#176/#181). Waypoint treats a screen-reader-usable, keyboard-only-usable UI
as a correctness property — see [issue #7](https://github.com/KN4OQW/waypoint/issues/7).

## The contract

Every operator-facing surface (the dashboard and the settings/wizard) must hold:

1. **Status is never conveyed by color alone.** Every colored state indicator is
   backed by text, an icon, or a shape — the connection LED carries
   `connected`/`disconnected` text, mode tiles say `ENABLED`/`DISABLED`, network
   rows carry a `✓`, and toggle pills expose `aria-pressed`.
2. **Everything interactive is keyboard-operable.** Controls are real
   `<button>`/`<input>`/`<select>` elements (never click-only `<div>`/`<span>`),
   reachable by <kbd>Tab</kbd>, activated by <kbd>Enter</kbd>/<kbd>Space</kbd>,
   with a visible `:focus-visible` ring. A skip link jumps past the sidebar.
3. **Roles, names and states are exposed.** Form controls have associated labels
   (or `aria-label`); toggles use `aria-pressed`; the active nav tab uses
   `aria-current`; live regions (`role="status"`/`aria-live`) announce the
   on-air state, mode, connection changes, and save results.
4. **WCAG AA contrast in every theme.** Text and UI meet the 4.5:1 (small) /
   3:1 (large) thresholds in all three themes — `phosphor`, `amber`, `ice`.

## How it's enforced

- **CI** — the [`a11y` workflow](../../.github/workflows/a11y.yml) runs
  [axe-core](../../ui/a11y/) against the running daemon (dashboard + every
  settings/wizard tab, all themes) on every PR. A WCAG 2.1 A/AA violation fails
  the build.
- **Review** — the [PR template](../../.github/pull_request_template.md) makes
  every PR declare its accessibility impact.

## Running the checks locally

See [`ui/a11y/README.md`](../../ui/a11y/README.md). In short: build and run
`waypointd -demo`, then `cd ui/a11y && npm ci && npm run scan`.

## Testing with an actual screen reader

axe-core catches the machine-checkable failures, not all of them. Before landing
a substantial UI change, tab through it with the keyboard only, and — where you
can — listen to it with Orca (Linux), NVDA (Windows), or VoiceOver (macOS). The
automated gate is the floor, not the ceiling.
