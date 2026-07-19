# RFC-0010: Inline Talkgroup/Reflector Names + Searchable Pickers

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #8 (talkgroup/reflector names inline; searchable pickers)
- Depends on: the reflector/talkgroup hostlist refreshers (`internal/{ysf,p25,nxdn,dstar,m17}hosts`) and the dashboard event stream (RFC-0004/0008)

## Summary

Make identifiers legible. A DMR talkgroup or reflector number is meaningless at a
glance — "TG 3112" tells an operator nothing; "TG 3112 · Texas Statewide" tells
them everything. This RFC (1) adds the missing **DMR talkgroup name database** so
TG numbers resolve to names, (2) resolves TG/reflector IDs to names **inline on
the dashboard** (on-air, last-heard, event log), and (3) makes the DMR
routing/TG picker **typeahead-searchable** like the reflector pickers already are.

The reflector/TG *startup* pickers (YSF/P25/NXDN/D-Star/M17) already use native
`<input list><datalist>` typeahead, so the picker gap is specifically the DMR
routing surface, which is a bare text box today. The name gap is broader: nothing
resolves a DMR talkgroup number to a name anywhere, because there is no TG-name
source in the tree yet (the model carries a `TGListFile` path but nothing reads
a TG database).

The acceptance (#8): selecting a talkgroup by name (e.g. "TX statewide") takes
typing ≤ 5 characters, not scrolling a thousand-row dropdown.

## Motivation

Pi-Star #9 has been open since **2018** (with #143 alongside), and WPSD's inline
names are among its most-praised features. The reason is simple: a hotspot's
whole job is talkgroups and reflectors, and every incumbent surface shows them as
naked numbers. An operator reading their own last-heard log sees "TG 3112" and
has to go look it up. A newcomer configuring a startup talkgroup faces a dropdown
of thousands of numeric entries and gives up.

Waypoint already fetches and caches the *reflector* registers (YSF/P25/NXDN/…)
and serves them to searchable pickers — the plumbing pattern exists. Two things
are missing: a **DMR talkgroup** name source (DMR is the most-used mode and has
no name data here), and **name resolution at display time** so the dashboard
stops showing bare numbers. Both are small, contained additions on top of
existing patterns — which is why #8 is a `good-first-issue`.

## Design

### The DMR talkgroup name database

A new refresher `internal/dmrtg`, built exactly like `internal/dmrhosts` (fetch →
cache atomically → parse → serve), owns the TG-name list:

- `Fetch(ctx, url, path)` downloads a TG list to a cached file and swaps it in
  atomically (temp + rename), leaving the previous cache intact on failure —
  identical to the hostlist refreshers. `Run` refreshes on a slow ticker.
- `Talkgroups(path)` parses the cache into `[]Talkgroup{ID, Name}`. The parser is
  format-tolerant: each non-comment line is `<number><sep><name…>` where sep is
  `;`, `,`, tab, or run of spaces (covering the BrandMeister/WPSD `TGList` and
  `groups.txt` dialects). Malformed lines are skipped, never fatal.
- `Names(path)` returns an `map[string]string` (TG number → name) resolver the
  server side can use, and `DefaultURL` points at a maintained TG list
  (overridable by flag, like every hostlist source).
- Served at **`GET /api/dmr/talkgroups`** (JSON `[]Talkgroup`), the same shape and
  auth posture as `/api/dmr/masters` and the reflector endpoints. The daemon runs
  the refresher in live mode next to `dmrhosts.Run`, gated behind a `-dmr-tg-url`
  / cache-path flag pair.

DMR is the case that needs its own source; P25 and NXDN "reflectors" are already
talkgroup registers with names (`p25hosts`/`nxdnhosts` carry `Name`), and
YSF/M17/D-Star are named reflectors. So `dmrtg` is the one new data source; the
rest is resolution and UI over data that already exists.

### Inline name resolution on the dashboard

The dashboard is where bare numbers hurt most. `app.js` gains a small **name
index** built once at load from the lists it can already fetch — the DMR TG list
plus the P25/NXDN talkgroup and YSF/M17/D-Star reflector registers — keyed by
mode + identifier. Rendering then resolves:

- **On-air** and **last-heard**: a `dest` of "TG 3112" (DMR) renders as
  "TG 3112 · Texas Statewide"; a reflector dest renders with its reflector name.
- **Event log**: the same resolution in each log line.

Resolution is **display-only** — it never mutates the event schema (the events
still carry the raw `dest`, so history/persistence and the MQTT republish are
unchanged; RFC-0004/0008 stay intact). A name that is not in the index falls back
to the raw identifier, so a missing/stale TG list degrades to today's behavior,
never to a blank. The index refreshes when the lists load; there is no per-event
lookup cost beyond a map read.

Why client-side, not in the MQTT bridge: resolving at ingest would couple the
event producer to the TG database and bake a name into the persisted record that
goes stale when the list updates. Resolving at display keeps the record a pure
fact ("TG 3112 at time T") and lets the name track the current list — and any
other API consumer resolves the same way from `/api/dmr/talkgroups`.

### Searchable DMR talkgroup picker

The DMR **routing** surface's dialed-TG field is a bare `<input placeholder="dialed
TG">` today. It becomes an `<input list="dmr-tgs">` backed by a `<datalist>` of
the TG list — the same native typeahead the reflector pickers use — so typing a
few characters of the name or number filters instead of scrolling.

The one wrinkle: the field stores a TG **number** (routing needs the number), but
the operator wants to search by **name**. The datalist option `value` is the
searchable label `"3112 · Texas Statewide"`; a change handler extracts the leading
number for storage, so the operator types "Texas" (or "3112"), picks the row, and
the stored route TG is `3112`. This is a tiny, self-contained combobox helper
reused wherever a numeric TG is entered. Native datalist filters on the option
value, so putting both number and name in the value is what makes "type ≤ 5 chars"
work in every browser without a custom dropdown widget.

The existing reflector/startup-TG pickers keep their datalist; where their option
`value` is the bare id, a follow-up can widen it to include the name for the same
search-by-name benefit (noted in Open questions), but they already satisfy the
"searchable, not a thousand-row dropdown" half.

## The contract (test harness)

Automated (Go):

1. **TG-list parse.** Table-driven over the accepted dialects (`;`, `,`, tab,
   spaces; comments; blank lines; a numeric-name line) → the exact `[]Talkgroup`,
   malformed lines skipped, sorted by numeric ID.
2. **Resolver.** `Names` maps a known ID to its name and returns "" (caller falls
   back to the raw id) for an unknown ID.
3. **Endpoint.** `GET /api/dmr/talkgroups` returns the parsed list as JSON (`[]`,
   not null, when the cache is absent), behind the session wall.
4. **Refresher.** Fetch writes the cache atomically and a failed fetch leaves a
   previous cache intact (mirrors the `dmrhosts` guarantees), asserted with a
   fake HTTP source.

Manual (the #8 acceptance): on the dashboard, a DMR TG in on-air/last-heard/event
log shows its name inline; in DMR routing, selecting a talkgroup by name takes ≤ 5
keystrokes and no scrolling. Verified in the browser.

## Alternatives considered

- **Resolve names in the MQTT bridge / persist them.** Rejected — it couples the
  event producer to the TG DB and freezes a name into the record that goes stale
  on the next list update. Display-time resolution keeps the record a pure fact
  and the name always current. (RFC-0004/0008 event schema unchanged.)
- **Ship a bundled static TG list.** Rejected as the *source* — TG lists change;
  a fetch-and-cache refresher (like every other hostlist) keeps it current and
  offline-safe (the cache serves when the network is down). A bundled seed could
  be added later so a never-online node still has names, but the refresher is the
  mechanism.
- **A custom JS combobox widget for the pickers.** Rejected — native
  `<datalist>` already gives typeahead with zero dependencies and full a11y/mobile
  support, matching the offline-safe no-framework rule. The only custom bit is the
  number-extraction on change, which is a few lines.
- **A new DMR-TG store section the operator edits by hand.** Rejected — the TG
  name list is reference data (thousands of entries, maintained upstream), not
  node config; it belongs in a cached register like the reflector lists, not the
  config store. (Per-node custom TG names are a possible follow-up via the
  `TGListFile` the model already carries.)

## Open questions

1. **Search-by-name on reflector pickers.** The YSF/P25/NXDN/D-Star/M17 startup
   pickers are datalists whose option `value` is the bare id/name; widening the
   value to embed the description (so typing the country/room name filters) is the
   same trick as the TG picker and worth a fast-follow. Not required for #8's
   acceptance (they're already searchable, not thousand-row dropdowns).
2. **Per-node custom TG names.** The model carries a `TGListFile` per DMR network;
   honoring a node-local TG list (operator's own names) on top of the fetched one
   is a natural extension — deferred until asked.
3. **Bundled offline seed.** A never-online node has no names until the first
   fetch. Shipping a small seed TG list (like the pinned reflector bundles) would
   fix that; deferred, noted so the refresh-only gap is on record.
4. **DMR TG source URL.** The default points at a maintained list; if a
   community-preferred source emerges it is a one-line flag default change, not a
   design change.
