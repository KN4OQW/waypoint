# Native HD44780 LCD driver — design

**Status:** implemented (pure layers + UI + hardware seam) · the template system
below is the user-defined page model — pages are data, not code.
**Owner decision recap:** templated-token lines · a new `lcd` store section ·
design-doc-first (this file) before code.

## 0. Template system (the user-defined page model)

Pages are **data the operator authors**, not code. Each page is a name, an enable,
a hold duration, an optional `interrupt` flag, and up to *R* template lines. A
template line is literal text plus `{tokens}` (§5). The renderer is
**geometry-agnostic**: it expands a page against live state and truncates/pads each
line to the configured cols and the page to the configured rows, so the *same*
pages render on a 20×2 bench panel and a 20×4 alike. The one rule the type can't
express — a page must not declare more lines than the panel has rows — is enforced
at **save time** by `ValidateLCD` (the error names the geometry, e.g.
`page "Idle" has 3 lines but the panel is 20x2 (max 2 rows)`), never silently
clipped. `DefaultLCD` seeds an Idle / Activity / Network set, every page ≤2 lines
so the default set is valid at both geometries out of the box.

An `interrupt=true` page takes over the panel immediately on TX/RX and returns to
the rotation after the transmission plus `linger_secs`; interrupt pages are
excluded from the idle rotation (they show only during activity). With no operator
interrupt page defined, a synthesized fallback still surfaces activity. The pure
render contract is `renderPage(rows, cols, page, state, info, ip, now) → [R strings
of C cols]` — token expansion + truncation, no hardware, fully unit-tested with a
fake state.

## 1. Why this exists (and how it differs from `display`)

The `display` store section (shipped in the "Setup surface" PR) renders
MMDVM-Host's `[Display]`/`[HD44780]` INI keys for **WPSD parity**. On Waypoint's
own node those keys are **inert**: the forked MQTT-era MMDVM-Host has no
`[Display]` parser and drives nothing. There has never been a physical panel on a
Waypoint node.

This feature adds a **Waypoint-native LCD driver**: a component inside `waypointd`
that subscribes to the live status plane and paints an HD44780 itself, with pages
the operator defines. It is the *live* driver; `display` stays the *inert parity*
artifact. The two never share a store section (see §4).

Contrast worth noting: MMDVM-Host's `[HD44780]` had **no I2C-bus key** (the bus was
fixed by the driver). Our native driver opens the bus itself, so `lcd.i2c_bus` is
a **real** field here — the thing that didn't exist upstream.

## 2. Architecture

```
MMDVM-Host ──MQTT──▶ internal/mqtt.Consumer ──▶ internal/hub.Hub ──┬─▶ web dashboard (SSE)
                                                                    └─▶ internal/lcd.Renderer ──▶ LCDDevice ──I2C──▶ PCF8574 ──▶ HD44780
```

- **Runs in `waypointd`, not a new daemon.** The renderer is just another
  `hub.Subscribe()` consumer — it reuses the exact event stream the dashboard
  uses, no extra IPC, one binary. Started from `main.go` when `lcd.enabled`.
- **Two layers, split for testability:**
  - `internal/lcd` — pure logic: derived state, token expansion, page render →
    text buffer, rotation/scroll scheduling. No hardware, fully unit-tested.
  - `LCDDevice` interface — the only hardware seam. Real impl talks I2C; a fake
    impl captures `WriteLine` calls for tests; a `noop` impl logs and no-ops when
    the bus can't be opened (degrade gracefully, never crash — same posture as the
    gateway-restart failures).

## 3. Data plane → derived LCD state

The renderer folds the `hub.Event` stream into a small `lcdState` it can format.
Grounded in the real event shapes (`internal/demo/demo.go`, `internal/hub`):

| Event `Type` | Carries | Folds into |
|---|---|---|
| `mode` | `Mode` (e.g. `IDLE`, `DMR`) | `activeMode` |
| `link` | `Network`, `Detail` | `links[Network]` up/down |
| `rf_voice_start` / `net_voice_start` | `Mode`,`Slot`,`Source`,`Dest`,`Network` | `active` = keyed; direction RF/net |
| `rf_voice_end` / `net_voice_end` | + `Seconds`,`BER`,`RSSI` | `active` = idle; `lastHeard` = this |

Derived fields the tokens read:
- `active` (bool) + `activeDir` (`RX`/`TX-net`) + current `Source`/`Dest`/`Mode`
- `lastHeard`: `{call, tg, mode, ber, rssi, at}` from the last `*_voice_end`
- `activeMode`, `links`

## 4. Config schema — new `lcd` store section

Modeled like every other section (`model.go` struct + `sections()` entry +
`DefaultLCD()` seed/backfill in `main.go`, `View`/`ViewLCD` projection). No
secrets, so the view is a straight projection.

```go
type LCD struct {
    Enabled           bool      `json:"enabled"`
    I2CBus            string    `json:"i2c_bus"`       // e.g. /dev/i2c-1 (native driver picks the bus)
    I2CAddress        string    `json:"i2c_address"`   // PCF8574 backpack, hex e.g. 0x27
    Rows              string    `json:"rows"`          // 2 or 4
    Cols              string    `json:"cols"`          // 16 or 20
    ScrollSpeed       string    `json:"scroll_speed"`  // ms per scroll step for over-wide lines
    ActivityInterrupt bool      `json:"activity_interrupt"` // master switch for interrupt pages
    LingerSecs        string    `json:"linger_secs"`   // hold an interrupt page this long after key-up
    Pages             []LCDPage `json:"pages"`
}

type LCDPage struct {
    Enabled   bool     `json:"enabled"`
    Name      string   `json:"name"`
    Duration  string   `json:"duration"`  // seconds this page holds before rotating
    Interrupt bool     `json:"interrupt"` // take over on TX/RX; excluded from normal rotation
    Lines     []string `json:"lines"`     // one templated string per row; extra rows blank
}
```

`DefaultLCD()`: `Enabled=false`, `/dev/i2c-1`, `0x27`, 20×4, `ScrollSpeed=300`,
`ActivityInterrupt=true`, `LingerSecs=3`, and a three-page starter set — **Idle**
(`{callsign}`/`{freq_rx}`+`{time}`), **Activity** (`interrupt=true`;
`{mode}`+`{source}`/`{tg}`), **Network** (`{ip}`/`{hostname}`). Every page has ≤2
lines so the set is valid on the 20×2 bench panel and a 20×4. Writes to the section
go through `SetLCD` (merge + `ValidateLCD`); backfilled on older stores exactly like
`display`/`m17` were.

## 5. Token engine

A token is `{name}`. Expansion is pure `state → string`. Grounded token set (only
data that actually exists on the plane / in config):

| Token | Source | Idle/missing fallback |
|---|---|---|
| `{callsign}` `{dmr_id}` | config `general.callsign` / `.id` | — |
| `{ip}` | host lookup (first non-loopback v4) | `no-ip` |
| `{hostname}` | `os.Hostname()` | `-` |
| `{freq_rx}` `{freq_tx}` | config `modem.rx_freq_hz`/`.tx_freq_hz`, rendered MHz | `-` |
| `{time}` `{date}` `{uptime}` `{version}` | clock + health | — |
| `{mode}` | `activeMode` | `IDLE` |
| `{modes}` | enabled modes from config, joined | — |
| `{status}` | `Listening` when idle; `RX DMR TG91 W1ABC` when keyed | `Listening` |
| `{source}` `{tg}` | in-progress call when keyed, else `lastHeard` | `-` |
| `{rssi}` `{ber}` | `lastHeard` (measured at key-up — most recent) | `-` |
| `{lh_call}` `{lh_tg}` `{lh_mode}` | `lastHeard` | `-` |
| `{lh_ber}` `{lh_rssi}` `{lh_ago}` | `lastHeard` (`ago` = now−at) | `-` |

`{source}`/`{tg}` read the live call while keyed (who's talking now) and hold the
last contact once clear; `{rssi}`/`{ber}` have a data source only at key-up, so
they always reflect the most recent transmission. Every token, its meaning, and its
source is documented in the UI's collapsible **token reference** legend.

Rules:
- Unknown token → renders empty **and** the UI flags it on the page card (so a
  typo is visible, never a silent blank).
- Non-ASCII in a template is stripped/replaced (`?`) — HD44780 CGROM is not
  UTF-8. `sanitizeASCII` replaces any rune outside `0x20`–`0x7E`. Documented in the
  UI. **Confirmed on glass** (2026-07-14, docs/on-hardware-report.md): `°`/`—`
  render as `?`; ASCII descenders `g j p q y` render cleanly. **ROM-A00 caveat:** on
  a Japanese-ROM (A00) panel two *ASCII* code points differ — `\` (0x5C) shows as
  `¥` and `~` (0x7E) as `→` — a panel-ROM property, not the software; avoid `\`/`~`
  for portable output (A02 panels show them normally).
- Optional later: `{lh_rssi_bar}` using HD44780 CGRAM custom glyphs for a signal
  bar (8 programmable chars). Deferred; noted so the render buffer reserves it.

## 6. Page renderer

- **Render:** for page `p`, row `i` in `0..Rows-1`, expand `p.Lines[i]` (or blank),
  then window to `Cols`: fits → left-justify; over-wide → scroll.
- **Scroll:** an over-wide line advances one column per `ScrollSpeed` ms (wrap with
  a gap). The render loop ticks at `ScrollSpeed`; page changes at `Duration`.
- **Rotation:** cycle `enabled` pages in order; each holds `Duration` seconds. A
  single enabled page just stays put.
- **Activity interrupt:** on `*_voice_start`, if `ActivityInterrupt`, render a
  synthesized caller page (`{status}` + last-heard-style lines) until
  `*_voice_end` + a short linger, then resume rotation where it paused. This is
  the "who's talking right now" behavior operators expect from Pi-Star/WPSD.
- **Reload on apply:** the renderer captures its config at start, so a config
  change only reaches the panel when it is torn down and restarted. `POST
  /api/config/apply` calls `reloadLCD`, which restarts the renderer **only when the
  `lcd` section actually changed** (an unrelated apply never blinks the panel).
  This is what makes edit-pages-then-Apply update the glass without a daemon
  restart (validated on hardware 2026-07-14; the LCD section renders no INI and no
  unit, so without this it silently never took — see report finding F6).
- **Diffed writes:** keep the last text buffer; only `WriteLine` rows that
  changed, so the bus isn't hammered every tick (HD44780 over I2C is slow).

## 7. Hardware seam — `LCDDevice`

```go
type LCDDevice interface {
    Init(rows, cols int) error
    WriteLine(row int, text string) error // text already ≤ cols, ASCII
    Clear() error
    Close() error
}
```

- **Real impl** (`internal/lcd/hd44780`): PCF8574 4-bit protocol — pack each byte
  as two nibbles with the RS/E/backlight control bits, pulse E, over `/dev/i2c-N`
  via `ioctl(I2C_SLAVE)` + write, using `golang.org/x/sys/unix`. **Pure Go, no
  cgo** — matches the "nothing floats" build ethos. (`x/sys` is the one new dep;
  pin it in `go.mod`.)
- **Open failure is non-fatal:** log `lcd: I2C /dev/i2c-1@0x27 unavailable, disabled`,
  fall back to `noop`, keep serving the dashboard.
- **Fake impl** (`fakeDevice`): records `WriteLine(row,text)` for assertions.

## 8. UI — LCD tab (page builder)

New tab `lcd` (label "LCD", after Setup). `panelLCD()`:

- **Card "PANEL"** — Enabled toggle · I2C bus · I2C address · Rows (2/4) ·
  Columns (16/20) · Scroll speed · Activity-interrupt toggle · Interrupt linger (s).
  A collapsible **token reference** legend documents every token and its source.
- **Card per page** (reuse the routing-table add/remove pattern):
  ```
  ┌ ▲ ▼  Idle       [ENABLED] [ROTATE] dur [ 8 ]s  ✕ ┐
  │ row1  [ {callsign}   {mode}                    ] │
  │ row2  [ {freq_rx}    {time}                    ] │
  │ ┌ PREVIEW (20×2) ─────────┐                      │
  │ │ KN4OQW DMR              │  ← live, client-side  │
  │ │ 433.1250 15:04          │                      │
  │ └─────────────────────────┘                      │
  │ tokens: {callsign} {mode} {source} {tg} …        │  ← click to insert
  └──────────────────────────────────────────────────┘
  [ + ADD PAGE ]
  ```
  Per-page controls: **▲ ▼** reorder, **[ENABLED]** toggle, **[ROTATE]/[INTERRUPT]**
  toggle, hold duration, remove. Line-input count follows Rows. A **token palette**
  inserts at the caret. A live **preview** box renders the page client-side against
  a sample snapshot at the configured geometry — the JS mirrors the Go renderer
  (token expand → ASCII sanitize → truncate/pad). All controls are real
  buttons/inputs with `aria-label`/`aria-pressed`; the preview is a labeled region.
- PUT routes through `SetLCD` (merge + geometry `ValidateLCD`), so an invalid page
  set is rejected with a 400 naming the geometry rather than silently clipped.

## 9. Testing & validation

- **Pure logic (full coverage, table-driven):** token expansion + fallbacks;
  line truncation; scroll windowing; rotation scheduler; activity interrupt
  enter/exit; diffed-write suppression. Device behind `fakeDevice`.
- **Config:** round-trip + isolation, same properties as every other section.
- **Hardware — off-box, honest caveat:** the PCF8574 byte protocol **cannot be
  bench-validated here**; it needs the physical panel on the test box
  (`pi-star@172.16.50.13`). Plan: unit-tests green in CI, then flash the box,
  attach a 20×4 on a PCF8574, and confirm real output + tune timing. The `verify`
  step for the pure layers runs anywhere; the device layer is marked
  hardware-gated.

## 10. Open questions (resolve before/while building)

1. **Per-page enable vs delete-only** — proposed: keep an `enabled` bool (quick
   A/B without losing a page's lines). Agree?
2. **Backlight** — the PCF8574 backlight bit is free to toggle; auto-dim/off after
   an idle timeout? Proposed: v1 = always on; add an idle-off timer later.
3. **RSSI/BER bar glyphs (CGRAM)** — worth an `{lh_rssi_bar}` token, or keep it
   numeric for v1? Proposed: numeric v1, glyphs later.
4. **Where the driver's I2C address/geometry seeds from** — offer a "copy from the
   `display` HD44780 fields" convenience, or fully independent? Proposed:
   independent, with defaults; no coupling to the inert section.
5. **New dep `golang.org/x/sys`** — acceptable to add + pin? (Pure Go, ubiquitous,
   no cgo.)

## 11. Rough build order (once approved)

1. `internal/config`: `LCD`/`LCDPage` model, `DefaultLCD`, view, round-trip +
   isolation tests. *(no hardware)*
2. `internal/lcd`: derived state + token engine + page renderer against
   `fakeDevice`, full unit tests. *(no hardware)*
3. `cmd/waypointd`: start the renderer as a hub subscriber when enabled; `{ip}`
   host helper; backfill. *(no hardware)*
4. UI: `lcd` tab + page builder.
5. `internal/lcd/hd44780`: real PCF8574 device (`x/sys/unix`). *(hardware-gated —
   validate on the test box)*
