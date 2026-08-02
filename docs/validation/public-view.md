# Public view — hardware validation

Bench run of the public dashboard (D1–D8) on the dev node.

| | |
|---|---|
| Node | Raspberry Pi 3 + MMDVM_HS_Dual_Hat, `kn4oqw`, armv7l, Raspbian 12 (bookworm) |
| Store | `/home/pi-star/waypoint/config.db` (diverges from production `/var/lib/waypoint/config.db`) |
| Build | cross-compiled `GOOS=linux GOARCH=arm GOARM=7`, installed over `/usr/local/lib/waypoint/bin/waypointd` |
| Serving | `waypointd` as root on `:443` (TLS) with `:80` redirecting; `mmdvmhost` inactive during the run |
| Date | 2026-08-02 |

Both databases were copied with the SQLite **online-backup API** before anything
else — never `cp`, which on a live WAL database captures a torn snapshot. The
node has no `sqlite3` binary and a stale package index, so the backup went
through Python's `sqlite3.Connection.backup()`, which is the same API the shell's
`.backup` command calls.

---

## Result summary

| # | Check | Result |
|---|---|---|
| 1 | Schema migrates on a real store | **pass** — v2 → v4, pre-migration copy written |
| 2 | Migrated node stays dark | **pass** — `enabled = 0` after migrating a node with real data |
| 3 | Disabled: every public route 404s from a LAN client | **pass** — 10/10 routes |
| 4 | Disabled: no CORS headers leak | **pass** |
| 5 | Enable → routes serve, CORS scoped correctly | **pass** |
| 6 | Reach card matches the running configuration | **FAILED — fixed in this branch** |
| 7 | Last heard shows callsign + mode + time only | **pass** — no BER/RSSI/duration despite seeded values |
| 8 | Counters | **pass** |
| 9 | Suppress list removes a station from list, counters **and** map | **pass** |
| 10 | Retention hides without deleting | **pass** — admin history intact |
| 11 | Logo upload strips EXIF/GPS | **pass** |
| 12 | SVG upload refused | **pass** — 400 |
| 13 | Hostile Markdown neutered | **pass** |
| 14 | Custom block sandboxed in a real browser | **pass** |
| 15 | Map: public snapped vs admin precise | **pass** |
| 16 | QR renders | **pass** |
| 17 | TLS 308 + health exception | **pass** |
| 18 | Rate limiter trips and recovers | **NOT REPRODUCED — see finding 2** |

Two findings, one fixed here and one left for a decision. Both are below.

---

## 1. Schema migration on a real store

The node was running a pre-public-view build at schema v2 with real operator
data. On first start of the new binary:

```
config store: migrated schema v2 → v4 (pre-migration copy: /home/pi-star/waypoint/config.db.pre-v2)
```

```
schema_version = 4
public tables: ['branding', 'heard_positions', 'public_links', 'public_nets',
                'public_suppress_list', 'public_view_settings']
enabled = 0
retention_hours = 24
admin.role = admin
```

`-rw-r--r-- root root 106496 config.db.pre-v2` — the pre-migration copy exists.

**`enabled = 0` is the one that matters.** A node with real data migrated two
schema versions forward and came up publishing nothing. The existing admin
credential picked up `role = admin` without touching the login.

## 2. Disabled state, from a LAN client (172.16.50.24 → 172.16.50.13)

```
ROUTE                      STATUS
/api/public/node           404
/api/public/status         404
/api/public/lastheard      404
/api/public/counters       404
/api/public/map            404
/public/                   404
/public/index.html         404
/public/assets/logo        404
/public/custom-block       404
/embed/lastheard           404
```

Zero `Access-Control-*` headers on any of them.

Worth recording as a before/after: on the **old** build these same paths returned
**401** — the auth gate refusing an unknown route. On the new build they return
**404**. A visitor can no longer tell "this node has the feature switched off"
from "this node has never had the feature", which is the whole point of D2.

## 3. Enabled

```
ROUTE                              STATUS  ACAO
/api/public/node                   200     *
/api/public/status                 200     *
/api/public/lastheard              200     *
/api/public/counters               200     *
/api/public/map                    200     *
/public/                           200     (none)
/public/vendor/leaflet/leaflet.js  200     (none)
/embed/lastheard                   200     *
```

CORS on the JSON API and the embed widget, and nowhere else.

## 4. Never-public fields

Three transmissions were seeded into the real events database carrying
`seconds=12.5, ber=0.4, rssi=-71`:

```json
{"available":true,"entries":[
  {"callsign":"KN4OQW","mode":"DMR","at":"2026-08-02T05:50:08.974Z"},
  {"callsign":"KK4WXT","mode":"DMR","at":"2026-08-02T05:49:08.974Z"},
  {"callsign":"W4RJM","mode":"YSF","at":"2026-08-02T05:48:08.974Z"}]}
```

Callsign, mode, timestamp. Nothing else survived the projection.

`available: true` also confirms the ID-database probe against the real
`/usr/local/etc/DMRIds.dat` (6.5 MB) rather than a fixture.

## 5. Suppression (D8)

Adding `KN4OQW` removed it from all three surfaces in one step:

```
lastheard: KK4WXT, W4RJM          (was 3 entries)
counters:  2 callsigns, 2 transmissions   (was 3/3)
map:       []                      (was 1 station)
```

## 6. Retention (D6)

With a station 3 h old and retention set to 1 h, the public list dropped it while
the admin history kept it:

```
public lastheard (1 h window):  KK4WXT, W4RJM   — N4OLD absent
admin events.db:                N4OLD rows = 1
```

Retention is a query bound, not a deletion policy, on real data.

## 7. Branding

A JPEG was built carrying an APP1 segment with
`GPSLatitude=30.6321 GPSLongitude=-87.0405 Make=BenchCamera Model=SECRET`,
uploaded to the node, and the **stored file** inspected with `strings`:

```
strings /home/pi-star/waypoint/branding/logo.png | grep -iE "GPSLatitude|GPSLongitude|BenchCamera|SECRET|Exif"
(no matches)

head -c 8 logo.png | od -An -tx1
 89 50 4e 47 0d 0a 1a 0a          ← PNG magic; a JPEG went in
```

SVG upload: `400 — publicview: logo must be a PNG or JPEG image: image: unknown format`

Served publicly with `content-type: image/png` and `x-content-type-options: nosniff`.

**Narrative.** Input:

```markdown
# KN4OQW bench
<script>alert(document.domain)</script>
[ok](https://example.org) and [bad](javascript:alert(1))
<img src=x onerror="fetch(1)">
```

Served:

```html
<h1>KN4OQW bench</h1>
<p><a href="https://example.org" rel="nofollow noreferrer noopener" target="_blank">ok</a> and bad</p>
```

Script gone, `onerror` gone, the `javascript:` link reduced to the plain text
`bad`, the legitimate link carrying `rel`. In the browser: **0 script elements**
under `#narrative`.

**Custom block.** Served with `content-security-policy: frame-ancestors 'self'`.
The operator's script was written to probe what it can reach, and the page was
loaded in headless Chromium against the node:

```
sandbox attr:  allow-scripts
sandbox probe: parent.document THREW SecurityError | cookie THREW SecurityError |
               localStorage THREW SecurityError
```

`allow-scripts` without `allow-same-origin`, on real hardware, over real TLS.

## 8. Map and the precision split (D3)

A fix was written straight into `heard_positions` at `30.63214, -87.04057` — the
shape a transport will produce.

```
GET /api/public/map
{"stations":[{"station":"KN4OQW","grid":"EM60lp",
              "lat":30.645833333333332,"lon":-87.04166666666666,
              "heard_at":"2026-08-02T05:50:08Z"}],
 "window_hours":24}
```

Grid centre, not the received fix. In the browser: **1 Leaflet container, 1
marker**, and neither `30.63214` nor `87.04057` appears anywhere in the delivered
page.

**No transport was attachable.** RFC-0018 is still `proposed` and none of
Meshtastic, MeshCore or LoRa APRS exists in the tree, so there is no code path
from a radio to `IngestPosition` yet. The position above was seeded directly.
Rendering and the precision split are validated; **ingestion from real hardware
is not, and cannot be until a transport lands.**

## 9. Page, QR and TLS

```
callsign shown: KN4OQW
QR rendered:    1 svg
no console errors        ← a CSP violation would appear here
```

TLS, from the LAN client:

```
ROUTE                    RESULT
/api/health              308 -> https://172.16.50.13/api/health
/api/public/node         308 -> https://172.16.50.13/api/public/node
/public/                 308 -> https://172.16.50.13/public/
/embed/lastheard         308 -> https://172.16.50.13/embed/lastheard
/                        308 -> https://172.16.50.13/

https://…/api/health     200 (unauthenticated — the gate exception holds)
```

The public routes get the same 308 as everything else, and the health exception
is in the auth gate rather than the TLS redirect, so on `:80` health redirects
too. That is the existing behaviour, unchanged.

---

## Finding 1 — the reach card did not match the running radio (**fixed here**)

The runbook step is "verify reach card matches actual configured MMDVMHost
frequency/CC/TS from the live config". It did not.

```
store:            color_code = ""
MMDVM-Host.ini:   ColorCode=1
reach card:       (colour code absent)
```

`render.go:580` writes `def(m.DMR.ColorCode, "1")`, so a blank store value
renders as CC 1 and **the radio is running CC 1**. The reach card read the store
and published nothing.

This is worse than a smaller answer. Someone programming a codeplug from the
public page would build it with no colour code and not get in. The page's job is
to say how to work the node, and it was quietly wrong about it.

Fixed by giving the default one home: `config.DefaultDMRColorCode`, read by both
`render.go` and the reach card, with `TestReachCardReportsTheEffectiveColourCode`
as the regression.

The same class of bug exists wherever the renderer defaults a blank value that
the reach card also publishes. Frequencies and timeslots do not default, so
today it is only the colour code — but a future `def(...)` on a published field
would reintroduce it silently. Worth a look if the reach card grows.

## Finding 2 — the rate limiter never engaged on this hardware (**decision needed**)

It could not be made to trip.

| Attempt | Result |
|---|---|
| 60 sequential requests | 60 × 200 |
| 150 concurrent (`xargs -P 30`) | 150 × 200 |
| 200 over one keep-alive connection | 200 × 200, 24.8 s |
| 400 over 10 parallel keep-alive connections | 400 × 200, 49.2 s |

**8.1 requests/second** is the ceiling — the Pi 3 is CPU-bound on TLS well below
the 10 req/s the limiter allows. The token bucket is correct (its unit tests
cover refill, burst, per-IP isolation and the sweep); it simply never fills on
this class of hardware, because the hardware is the limit first.

Loopback stayed exempt throughout: 60/60 × 200 from the node itself.

So on a Pi 3, the practical protection against a stranger hammering the public
API is the CPU, and the limiter is inert. That is not obviously wrong — but
`RateLimitPerSecond = 10` was chosen to be "far above legitimate use and far
below what hurts", and on the slowest supported hardware it is above what the
node can serve at all.

**Not changed here.** The right value depends on hardware the bench cannot speak
for: a Pi 4/5 or an x86 node serves far more, and lowering the constant to engage
on a Pi 3 could throttle legitimate club traffic on a faster one. Options, for
whoever decides:

1. Lower to ~5/s burst 15 — engages before saturation on a Pi 3, still ~5× a
   single visitor's load.
2. Scale from `runtime.NumCPU()` or a measured baseline.
3. Leave it, and document that on Pi-3-class hardware the CPU is the limit.

---

## Notes on the bench node's state

- The node was **re-claimed** during the run. Testing the authenticated branding
  endpoints needs a session, and the existing admin password was not available,
  so `waypointd reset-claim` was used under this box's break-anything license.
  **The previous admin account `cchance` was wiped and replaced with `bench`.**
  Re-claim it to whatever you want; nothing else depends on it.
- Both databases were backed up first to `/home/rescue/pv-validation/`.
- The public view is left **enabled** with bench content (a net, a link, a logo,
  a narrative, a custom block, one seeded position and four seeded
  transmissions). Turn it off in Settings → Public View, or restore the backup.
- `mmdvmhost` was inactive for the whole run, so no live RF traffic was observed.
  The last-heard and counter checks used events written into the real
  `events.db`, which is the same path the MQTT bridge writes. **Keying up
  through the hotspot was not performed** — the runbook's DMR keyup check is
  outstanding.
