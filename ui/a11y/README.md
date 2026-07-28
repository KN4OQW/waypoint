# UI accessibility gate

Accessibility is a merge gate for Waypoint (issue #7). This harness runs
[axe-core](https://github.com/dequelabs/axe-core) against the live dashboard and
every settings/wizard panel, at both nav breakpoints, in all three display themes,
and fails on any WCAG 2.1 A/AA violation. CI runs it on every pull request
(`.github/workflows/a11y.yml`).

## Run it locally

From the repo root, build and start the daemon in demo mode (no radio needed):

```sh
go build -o waypointd ./cmd/waypointd
./waypointd -demo -addr 127.0.0.1:8073 -store /tmp/a11y.db
```

Then, in another shell:

```sh
cd ui/a11y
npm ci
npx playwright install chromium   # first run only
BASE=http://127.0.0.1:8073 npm run scan
```

A clean run prints `Accessibility gate passed.` and exits 0. Any violation is
printed with the offending element, the rule, and a `helpUrl`, and the process
exits non-zero.

### Env knobs

| Variable | Default | Purpose |
| --- | --- | --- |
| `BASE` | `http://127.0.0.1:8073` | URL of a running `waypointd -demo`. |
| `A11Y_THEMES` | `phosphor,amber,ice` | Themes to scan. |
| `PLAYWRIGHT_CHROMIUM` | *(unset)* | Explicit Chromium binary; omit to use Playwright's own. |
| `A11Y_USER` / `A11Y_PASS` | `a11y` / `a11y-scan-passphrase` | Credentials the scan claims the demo node with (or logs in with, if it is already claimed). |

The daemon gates the whole UI behind the RFC-0002 claim: an unclaimed node answers
every page with `{"error":"device is unclaimed"}`. The scan therefore claims (or
logs into) the node first — without that step axe scans the claim gate's JSON
instead of the app, and reports contrast-free "clean" pages that were never loaded.

## What it checks

- **Dashboard** (`/`) — live status, on-air, last-heard, networks, event log.
- **Settings / wizard** (`/settings.html`) — every top-level tab (`general`,
  `hardware`, `setup`, `lcd`, `station`, `modes`, `brandmeister`, `network`,
  `gateways`, `profiles`, `updates`, `expert`), with the Modes tab expanded into each of its
  eight mode sub-tabs (`dstar`, `dmr`, `ysf`, `p25`, `nxdn`, `m17`, `pocsag`, `fm`)
  so every per-mode panel is still walked.
- **Both nav topologies** — a desktop pass (1280px, grouped collapsible sidebar)
  and a mobile pass (390px, tile grid), plus the nav's own states: groups expanded,
  groups collapsed, and the tile grid itself.

For each page it also flips every off-state toggle on, so the "enabled" accent
styling is exercised too, and repeats the whole sweep per theme.
