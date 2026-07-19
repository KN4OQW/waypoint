# RFC-0009: Responsive, Dark-Default, Themeable UI

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #6 (responsive, dark-mode-default web UI usable from a phone)
- Depends on: the existing embedded SPA (`ui/static`, the dashboard + settings pages) and the auth gate (RFC-0002), whose claim/login screens are still placeholders

## Summary

Make the whole UI usable from a phone: the dashboard and the settings page
responsive down to **360 px**, a genuine **light theme** alongside the
dark default, and — the piece that is currently a stub — real **first-run
screens** (claim + login) so a device can be set up entirely on a phone. Dark
stays the default; light is a mode the operator can switch to and that composes
with the existing accent themes.

The acceptance is manual and visual (setup wizard + live dashboard at 360 / 768 /
1280 px in both themes), so this RFC fixes the *contract* those manual checks
verify — the breakpoint set, the touch-target floor, the light-mode token set, and
the first-run screens — and the PR verifies it in a real browser at each width in
each theme.

## Motivation

Requirement #6's provenance is stark: a complete dark-mode PR sat **ignored on
Pi-Star for four years** (#173); "too small and not mobile responsive" (#141); the
2018 third-party Mobile Dashboard add-on existed *only* because the incumbent UI
wasn't phone-usable, and it is now abandoned. A hotspot is a device you configure
next to the radio — often with a phone, often with no desktop nearby. A UI that
assumes a 1280 px window fails the most common real setup moment.

Waypoint's UI is already a self-contained, offline-safe, accessible dark SPA (no
CDN, focus rings, skip links, WCAG-AA text), but three gaps keep it off the phone:

1. **One coarse breakpoint.** Both pages collapse their two-column grid at 900 px
   and stop there — nothing tunes the header, the settings tab rail, touch
   targets, or type for a 360 px screen.
2. **No light theme.** The three "themes" (phosphor/amber/ice) only swap the
   *accent*; all are dark. #173's four-year-ignored ask is a genuine light mode.
3. **No first-run UI.** Claim and login are `writePlaceholder` stubs that tell the
   operator to `curl POST /api/claim` — not a flow anyone completes on a phone.

## Design

### Breakpoint system

Three tiers, matching the acceptance widths, applied to both `index.html` and
`settings.html`:

- **≥ 1024 px (desktop):** the current two-pane layout (fixed sidebar + main),
  unchanged.
- **768 px (tablet):** the sidebar detaches from a fixed rail to a top bar; content
  is single-column; cards go full-width.
- **≤ 480 px, tuned at 360 px (phone):** the settings **tab rail becomes a
  horizontally-scrollable strip** (or a native `<select>` jump) instead of a
  vertical list that eats the viewport; the status bar wraps and shrinks; page
  padding drops from ~30 px to ~14 px; headings scale down; **every interactive
  control is ≥ 44 × 44 px** (WCAG 2.5.5 / Apple HIG touch target); tables get an
  `overflow-x: auto` wrapper so a wide row scrolls inside its card instead of
  blowing out the page width.

The page body never scrolls horizontally at any width — wide content (event log,
config tables, the generated-INI preview) scrolls inside its own container. This
is a hard rule the manual check verifies at 360 px.

### Theming: a mode dimension that composes with accent

Today `data-theme` conflates accent and (implicitly) dark. This RFC splits them
into two orthogonal dimensions so light composes with any accent:

- **`data-mode`** on `:root` — absent = **dark (default)**; `"light"` swaps the
  *structural* tokens (`--bg`, `--panel`, `--field`, the line/side/row colors, and
  the `--ink*/--label/--muted/--dim/--faint` text ramp) to a light palette.
- **`data-theme`** on `:root` — absent = phosphor green; `"amber"`/`"ice"` swap the
  *accent* tokens, in either mode.

Because everything downstream already reads the CSS variables, the entire UI
re-themes from these two attributes with **no per-component change**. Light mode
also darkens each accent (green/amber/blue) to a shade that clears **4.5:1** on a
white panel, so accent-colored text (callsign chip, breadcrumb, links) stays
AA-legible — the light palette is validated against the same contrast bar the dark
one already meets, not eyeballed.

The switcher gains a **Dark / Light** toggle above the accent swatches. Both
choices persist (`wp-mode`, `wp-theme` in `localStorage`) and apply on load before
first paint (an inline head script sets the attributes from storage, so there is no
dark-flash on a light-mode reload). First visit with no stored mode honors the
browser's `prefers-color-scheme` **once** as a hint, then the operator's explicit
choice wins forever after — "dark default" holds unless the OS says light and the
user hasn't chosen.

### First-run screens (claim + login)

The gate serves claim/login **pre-auth**, so these screens must be fully
self-contained (no gated asset, no CDN) — which they already must be for the
offline/security posture. This RFC replaces the two placeholder strings with real,
responsive, dark-default, themed pages:

- **Claim** (`/` while unclaimed): a single centered card — "Claim this device" —
  with username + password + confirm-password fields, inline validation (the
  8-char password floor the API enforces, matching passwords), a submit that
  `POST`s `/api/claim` and on 201 redirects to `/` (now claimed → the SPA loads
  behind the fresh session cookie). Errors from the API render inline, not as a raw
  JSON dump.
- **Login** (`/` while claimed but unauthenticated): the same card shape —
  username + password — `POST /api/session`, redirect to `/` on success, inline
  error (with the login damper's lockout message surfaced legibly) on failure.

Both are phone-first: one column, large touch targets, the same token set so they
match the app and respect light mode. They carry the viewport meta and the
inline theme-from-storage script so a returning operator sees their chosen theme.
Password fields never autofill-submit and are `type="password"`; the pages post
JSON (no credentials in a URL). No new endpoints — they drive the existing
`/api/claim` and `/api/session`.

### What stays out of scope

- No CSS framework, no build step — the UI is hand-written self-contained HTML/CSS
  by design (offline-safe single artifact). This RFC keeps that.
- No visual redesign of the desktop layout — the phone/light work is additive; the
  1280 px view is unchanged.
- The settings "wizard" persona (a guided first-config flow) is a later slice; #6's
  "first-run flow" is the **claim/login** gate, which is what blocks phone setup
  today.

## The contract (what the manual + automated checks verify)

Manual (the #6 acceptance), at **360 / 768 / 1280 px** in **dark and light**:

1. **Dashboard**: no horizontal page scroll; status bar readable; on-air, last-heard,
   networks, gateways, and event-log cards stack and stay within the viewport;
   wide tables scroll inside their card.
2. **Settings**: the tab rail is reachable and operable (scroll strip / select);
   every field and toggle is tappable (≥ 44 px); no control is clipped; the
   generated-INI/preview areas scroll internally.
3. **First-run**: claim and login complete end-to-end on a 360 px screen — fields
   focusable, validation legible, submit works, redirect lands on the app.
4. **Both themes**: text meets AA contrast in light and dark; the theme choice
   persists across reloads with no flash of the wrong mode.

Automated (Go, in this PR):

5. The gate serves a **real** claim page (contains the claim `<form>` and posts to
   `/api/claim`) while unclaimed, and a **real** login page (posts to
   `/api/session`) while claimed-but-unauthenticated — not the old placeholder
   copy. A guard test asserts the served HTML contains the form and the viewport
   meta, so the screens can't regress to a stub.

## Alternatives considered

- **A fourth "light" accent theme (light + green only).** Rejected — it wouldn't
  compose with amber/ice and would grow to six themes to cover the matrix. Two
  orthogonal attributes (`data-mode` × `data-theme`) is fewer tokens and covers
  every combination.
- **Pull in a CSS framework / utility library for responsiveness.** Rejected —
  breaks the offline-safe, no-CDN, single-artifact rule and adds weight to a Pi's
  tiny page. Hand-written media queries over the existing variable system are
  smaller and match the codebase.
- **Serve the SPA (not a placeholder) pre-auth and let it render claim/login.**
  Rejected — the SPA and its assets sit behind the auth wall on purpose (a fresh
  device leaks nothing). Self-contained claim/login pages keep that boundary while
  still being real screens.
- **Auto-switch to light purely from `prefers-color-scheme`.** Rejected as the
  default — #6 says *dark default*. `prefers-color-scheme` is honored only as a
  first-visit hint; the explicit toggle is authoritative and persists.

## Open questions

1. **Guided setup wizard.** #6 says "setup wizard"; this RFC delivers the
   first-run *gate* (claim/login) as the phone-blocking piece and treats a
   multi-step guided first-config (callsign → modem → first network) as a later
   slice on the now-responsive settings surface. Is the gate enough to call #6
   done, or should the first-config wizard land here too? Leaning: gate now, wizard
   as a fast-follow tracked separately.
2. **Reduced-motion.** The blink/glow animations are decorative; honoring
   `prefers-reduced-motion` to still them is a small addition worth doing alongside
   this — folded in if cheap, noted here otherwise.
3. **Container queries.** The card grid could use container queries instead of
   viewport media queries for finer control, but browser support on the
   older phones a ham might use is uneven; viewport breakpoints are the safe choice
   now.
