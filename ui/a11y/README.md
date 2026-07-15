# UI accessibility gate

Accessibility is a merge gate for Waypoint (issue #7). This harness runs
[axe-core](https://github.com/dequelabs/axe-core) against the live dashboard and
every settings/wizard tab, in all three display themes, and fails on any WCAG
2.1 A/AA violation. CI runs it on every pull request (`.github/workflows/a11y.yml`).

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

## What it checks

- **Dashboard** (`/`) — live status, on-air, last-heard, networks, event log.
- **Settings / wizard** (`/settings.html`) — every tab (`general`, `setup`,
  `lcd`, `dmr`, `dstar`, `ysf`, `p25`, `nxdn`, `m17`, `pocsag`, `fm`, `modes`,
  `brandmeister`, `gateways`, `network`, `expert`).

For each page it also flips every off-state toggle on, so the "enabled" accent
styling is exercised too, and repeats the whole sweep per theme.
