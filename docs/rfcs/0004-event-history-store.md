# RFC-0004: The Event History Store

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #9 (persistent last-heard + per-station history — the persistence half; the Home-Assistant MQTT-discovery topic scheme is a follow-up on top of this store)
- Resolves: #68 (last-heard / event history is per-browser-session only)
- Depends on: RFC-0001 (the configuration store — the events store mirrors its open/WAL/schema-version conventions but is a *separate* database), RFC-0003 (mode buses — a bus emits ordinary hub events, so bus traffic is persisted with no extra work)

## Summary

Every event the daemon learns — the ones already flowing through the hub (`internal/hub/hub.go`) — is persisted to a **separate SQLite database, `events.db`**, by a batched hub subscriber. A new `GET /api/history` endpoint serves that persistent record so any browser renders the same last-heard, networks, and event log regardless of when it connected or whether the daemon has restarted. The in-memory 200-event hub backlog stops being the source of dashboard history and reverts to what it is good at: the live-reconnect tail and the LCD renderer's warm start. Retention is an operator setting (default **7 days**), pruned nightly, and lives in a new **Station Settings** tab that a future callsign-beacon feature will share.

## Motivation

Today the dashboard's history is a fiction of the browser tab. The hub keeps a bounded in-memory ring (`backlogSize = 200`, `internal/hub/hub.go:29`) that the SSE handler (`cmd/waypointd/main.go`, `func (s *server) events`) replays on connect; `ui/static/app.js` then builds `state.lastheard`, `state.networks`, and the event-log table entirely in client JS from that stream. The consequences (#68):

- Open a second tab → its history starts from whatever the 200-event ring currently holds, nothing older.
- Restart `waypointd` → all history is gone; the ring is memory.
- Two browsers that connected at different times show different histories.
- There is no way to answer "who did this node hear yesterday."

This is also the standing shape of founding requirement #9 — *persistent last-heard with per-station history* — whose acceptance ("HA MQTT discovery picks up hotspot status entities with zero YAML") sits on top of a durable event record that does not exist yet. This RFC builds that record. The Home-Assistant-facing MQTT-discovery topic scheme is deliberately **out of scope here** and follows as its own change: it is a *publisher* that reads the same store, not part of the persistence contract.

The design constraint that shapes everything below: the target hardware is a Pi Zero W / Pi 3 booting off an SD card. The persistence layer must not write per-event fsyncs (SD wear) and must not add a second long-running daemon (memory) — which is exactly why the incumbent answer of "stand up InfluxDB" is the wrong tool here (see Alternatives).

## Design

### A separate `events.db`, not a table in the config store

Event history is high-churn and its retention is independent of configuration. Putting it in `config.db` would serialize every event insert against config writes on that database's single writer connection (`store.go` sets `SetMaxOpenConns(1)`), and would tangle a 7-day pruning window into the database whose entire value proposition (RFC-0001) is that it is small, authoritative, and never churned. So the events store is its **own** SQLite file, `events.db`, a sibling of `config.db`:

- New package `internal/events`, with `Open(path)` mirroring `store.Open` (`internal/store/store.go:30`): pure-Go `modernc.org/sqlite` driver (CGO-free armv6 cross-compile), `journal_mode=WAL`, `busy_timeout=5000`, and its **own** `meta(schema_version)` row so it migrates on its own cadence, independent of the config schema.
- One deliberate divergence from `store.go`: `synchronous=NORMAL`. Under WAL, `NORMAL` fsyncs at checkpoint rather than on every commit — a durability/wear trade the config store does not take (a lost config write is unacceptable; a lost last-second last-heard event on a power-cut is not). This is the knob that satisfies #68's "no per-event fsync" acceptance item.
- New daemon flag `-events-store` (default `/home/pi-star/waypoint/events.db`, the `config.db` sibling), opened in `main()` next to `store.Open`, handle held on the `server` struct, closed on shutdown.

The precedent for a subsystem owning tables of its own is already in the tree — the auth subsystem keeps its credential/session tables via `store.DB()` (`internal/auth/store.go:45`). We go one step further and give events their *own file* rather than sharing the config connection, because the churn and retention arguments above do not apply to auth's tiny, rarely-written tables.

### Schema

```sql
CREATE TABLE events (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  ts_ms    INTEGER NOT NULL,        -- event time, unix epoch milliseconds (UTC)
  type     TEXT NOT NULL,           -- rf_voice_start, rf_voice_end, net_voice_*, link, mode
  mode     TEXT,                    -- DMR, YSF, ...
  slot     INTEGER,
  source   TEXT,                    -- callsign or network name
  dest     TEXT,
  network  TEXT,
  seconds  REAL,
  ber      REAL,
  rssi     INTEGER,
  detail   TEXT
);
CREATE INDEX idx_events_ts     ON events (ts_ms);
CREATE INDEX idx_events_source ON events (source, ts_ms);
CREATE INDEX idx_events_type   ON events (type, ts_ms);
```

- The columns are exactly `hub.Event`'s fields (`internal/hub/hub.go:16`), so persistence is a straight projection and the history endpoint re-emits the identical wire shape the SSE stream and client already speak — no second event schema to keep in sync.
- **Time is stored as unix milliseconds (INTEGER), not RFC-3339 text.** Range scans (`WHERE ts_ms >= ?`) and the retention prune are integer comparisons on an indexed column; the API still speaks RFC-3339 (`hub.Event.Time` is a `time.Time`, marshaled as it is today) — the millis representation is an internal storage detail.
- `idx_events_source` is the per-station-history index #9 asks for: "who was this node hearing, newest first" is `WHERE source = ? ORDER BY ts_ms DESC`.

### Persistence subscriber (batched, off the publish lock)

`Hub.Publish` holds `h.mu` while it fans out to subscribers (`internal/hub/hub.go`), so persistence must never do a synchronous DB write inside that path. Instead the events package registers as an ordinary hub subscriber — `hub.Subscribe()`, the same seam the SSE handler and LCD renderer use — and runs a writer goroutine:

- Drain the subscription channel into an in-memory buffer.
- Flush on **either** a size threshold (e.g. 64 buffered events) **or** a timer (1–5 s, tunable), whichever comes first, as a **single transaction** (`INSERT` batch). Batching plus WAL plus `synchronous=NORMAL` is what keeps SD writes to a trickle under sustained traffic.
- On flush error, log and keep the buffer bounded (drop-oldest with a counter) — a wedged disk must never back-pressure the hub or grow memory without limit. A dropped-events count is surfaced, never silent (the project's "no silent caps" rule).

Started in `main()` near the demo/mqtt producer wiring, with the daemon context so it stops cleanly on shutdown (final flush on context cancel).

### Retention & the nightly prune

Retention is an operator preference, not a build constant, so it is a **store setting** — an ordinary `config.Model` section, read from the config store the same way the YSF hostlist refresher reads `UpperHostfiles` (`main.go`, `ysfhosts.Run` callback):

- New config section `history` → `type History struct { RetentionDays int }`, `DefaultHistory()` = `{ RetentionDays: 7 }`, wired into `Model.sections()`, `View`, and `backfillDefaults` (so a store seeded before this section gets the 7-day default, exactly as `display`/`p25`/etc. were backfilled).
- **Default 7 days.** `RetentionDays == 0` means *keep forever* (prune disabled) — a deliberate escape hatch for an operator who wants a permanent log and has the disk for it. Negative is rejected at save.
- A prune goroutine on a ~24 h ticker (the established recurring-work pattern — the hostlist `Run` loops in `main()`) reads the current `history.retention_days` each night and runs `DELETE FROM events WHERE ts_ms < ?` for the cutoff, then lets WAL checkpoint reclaim space. Bounded DB size under sustained traffic is thereby a tested property, not a hope (#68 acceptance).

### History API

New route `GET /api/history`, registered in `newMux` alongside `/api/events`. Because the auth gate is default-deny and passes every route through once a session authenticates (`internal/auth/handlers.go`, `gateClaimed`), the endpoint is behind the session wall with **no gate change** — same posture as the SSE stream.

- Query params: `since` (RFC-3339 or unix-ms; events at/after this time), `type` (filter one event type), `limit` (default 500, hard cap ~5000 so one request can never scan the whole retention window).
- Response: a JSON array of `hub.Event`, newest-first, identical wire shape to the SSE `data:` frames — so the client feeds history rows through the *same* reducer it already uses for live events.

### SSE and hub, after this change

The hub's in-memory backlog stays — it still serves two real needs: the **live-reconnect tail** (a browser that drops and reconnects catches the handful of events it missed) and the **LCD renderer's warm start** (`startLCD` replays the backlog so the panel opens showing current state, `main.go`). What changes is that the backlog is no longer the dashboard's *history*:

- `GET /api/events` becomes a **pure live tail** — it stops replaying the backlog to browser clients. (Safe: the LCD calls `hub.Subscribe()` directly, not through the SSE HTTP handler, so its warm start is unaffected.)
- `ui/static/app.js` gains `loadHistory()` — `fetch("/api/history?limit=500")`, replay the rows oldest→newest through the existing `handle()` reducer to seed `state.lastheard` / `state.networks` / the event-log table — called **before** `connect()`. History paints first; the SSE stream then live-tails on top.
- The fetch-then-connect ordering leaves a small overlap/gap window (events between the history snapshot and the SSE attach). For alpha this is acceptable; §Open-questions notes the tightening options (dedupe by `ts`+`source`, or a `since` handshake).

### Station Settings tab

Retention gets a home in the UI, and that home is deliberately built to hold more than retention. A new **Station Settings** tab (`TABS` entry in `ui/static/settings.js`, following the `lcd` tab's shape) surfaces `history.retention_days` today. A **tab may span more than one store section** — the General tab already spans `general`+`modem` — so the future callsign-beacon feature lands here as a sibling `beacon` section under the same tab without disturbing the retention field. The tab writes through the existing `PUT /api/config/{section}` merge path (`config.SetSection`), so it inherits RFC-0001's isolation guarantee for free.

## The persistence contract (test harness)

CI enforces these as release-blocking properties, in the RFC-0001 style (pure functions, property-based, table-driven where it fits):

1. **Round-trip fidelity.** For a randomized stream of valid `hub.Event`s published through the persistence subscriber, every event read back via the `History` query is field-for-field equal to what was published (modulo the ms-truncation of sub-millisecond time), in newest-first order. No event type is silently dropped.
2. **Durability across restart.** Events persisted, store closed and reopened (new process simulated by a fresh `Open` on the same file) ⇒ `History` returns them. This is the #68 "survives waypointd restart / host reboot" acceptance as an automated test.
3. **Batching does not lose the tail.** A flush triggered by context-cancel (shutdown) after a partial buffer ⇒ the buffered events are present on reopen. No event acknowledged to the subscriber is lost on a clean stop.
4. **Retention prune is correct and bounded.** With a fixture spanning more than the window, prune deletes exactly the events older than the cutoff and no others; `RetentionDays == 0` prunes nothing; DB row count is bounded by a synthetic sustained-traffic fixture after prune (#68 "DB size bounded" / "prune verified").
5. **No per-event fsync.** The store opens with `journal_mode=WAL` + `synchronous=NORMAL` and writes are batched in transactions — asserted at the DSN/PRAGMA level and by an insert-count-vs-transaction-count check (#68 "batched WAL writes confirmed").
6. **Endpoint filter/limit.** Table-driven over `{since, type, limit}` → asserts the `since` boundary is inclusive-at, the `type` filter is exact, `limit` caps the row count, and the response wire shape is byte-compatible with an SSE frame's `hub.Event` JSON.
7. **Config surface (RFC-0001 inheritance).** The `history` section round-trips through Save/Load, `backfillDefaults` fills the 7-day default into a store seeded without it, a negative `retention_days` is rejected at save, and editing `history` leaves every other section byte-identical (RFC-0001 property 2, isolation).

## Alternatives considered

- **Keep history client-side (the status quo).** Rejected — it is the bug (#68). History that lives in a browser tab is not history.
- **A dedicated TSDB (InfluxDB / Prometheus / VictoriaMetrics).** Rejected for this hardware. On a Pi Zero W a TSDB engine is a second long-running daemon with its own memory footprint and operational surface, sized for an event rate orders of magnitude above a single hotspot's. SQLite with a time index handles this rate trivially and adds no new process. The door stays open: if aggregation queries (per-talkgroup stats, heatmaps) ever outgrow SQLite, that is a *reporting* layer on top, revisited then — not a reason to pay for a TSDB now.
- **One table in `config.db` (reuse the config store via `store.DB()`).** Rejected: it serializes high-churn event inserts against config writes on the single writer connection, and couples a 7-day pruning/vacuum cycle to the database whose whole value is being small and never churned. A separate file isolates both the lock and the retention lifecycle. (Auth shares the config DB, but its tables are tiny and rarely written — the churn argument does not apply there.)
- **Write inside `Hub.Publish` (synchronous persistence).** Rejected: `Publish` holds the hub mutex while fanning out; a DB write there would put disk latency on the path every producer and subscriber shares. A batched subscriber decouples disk latency from the bus entirely.
- **RFC-3339 text timestamps (match `config.db`'s `now()`).** Rejected for the events table: range scans and prune run on every query and every night; integer-millis on an indexed column is the right storage type. The API boundary still speaks RFC-3339, so nothing downstream sees the difference.
- **Bake retention into a flag / build constant.** Rejected: it is an operator preference (disk vs. history depth), so it belongs in the store where the operator can change it and where it inherits the config surface's validation, journaling, and isolation — and it gives the Station Settings tab its first inhabitant.

## Open questions

1. **History/live seam.** Fetch-then-connect leaves a small gap/overlap between the `/api/history` snapshot and the SSE attach. Tighten with a client-side dedupe (`ts_ms`+`source`+`type`), or a `since` handshake where the SSE stream replays from the last-seen id? Leaning client-side dedupe for alpha — simplest, and duplicates are visually harmless in an event log that is already keyed by time.
2. **Per-station rollups for #9.** The `idx_events_source` index makes per-station queries cheap, but the HA-facing side of #9 may want a materialized `last_heard(source → latest event)` view for zero-scan status entities. Build it as a derived table maintained by the same subscriber, or compute on read? Deferred to the #9 MQTT-discovery follow-up, which is the consumer that decides.
3. **Beacon section shape.** This RFC only *reserves* the Station Settings tab for callsign beaconing. The beacon's model (interval, CW vs. voice, which modes, quiet hours) is out of scope and gets its own design when it lands — noted here only so the tab isn't built retention-only and rearranged later.
4. **Retention granularity.** Days is the operator-facing unit and 7 is the default. Is a raw-event-count cap (e.g. "keep at most N events") also worth exposing for the operator who cares about a hard disk bound more than a time window? Leaning no until asked — a time window is the intuitive model and the prune is bounded regardless.
5. **Backfill vs. demo noise.** In `-demo` mode the synthetic generator will persist synthetic events into `events.db` like any producer. Acceptable (demo is always labeled), but should demo mode use an in-memory / throwaway events store so a demo run doesn't accrete a persistent synthetic history? Leaning yes — open `:memory:` (or a temp path) for the events store when `-demo` is set, mirroring how demo traffic is already walled off elsewhere.
