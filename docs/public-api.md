# Public API and embeddable widget

A Waypoint node can publish a read-only page about itself, plus a small JSON API
and an embeddable widget. This document is for whoever is putting a node's
activity on a club website.

Everything here is **off by default**. Until an operator turns the public view on
in **Settings → Public View**, every route below answers `404`, and there is no
way to tell a node with the feature switched off from one that never had it. That
is deliberate: turning it on is a decision, and a node that has not made it
discloses nothing.

If you are replacing a `last_heard.php` scrape, skip to
[Replacing a Pi-Star scrape](#replacing-a-pi-star-scrape).

---

## Quick start

Put a node's last-heard list on your club's page:

```html
<iframe src="https://your-node.example/embed/lastheard?limit=10&status=1"
        title="K4SRC last heard"
        style="width:100%;max-width:420px;height:340px;border:0"
        loading="lazy"></iframe>
```

Or fetch the JSON and render it yourself:

```js
const r = await fetch("https://your-node.example/api/public/lastheard?limit=10");
const data = await r.json();

if (!data.available) {
  // The node's station database is missing or corrupt, so it withheld the list
  // rather than showing only the modes that resolve without it. Say so.
  console.warn(data.notice);
} else {
  for (const h of data.entries) {
    console.log(h.callsign, h.mode, h.at);
  }
}
```

CORS is open on `/api/public/*`, so this works from any origin.

---

## What is published, and what is not

The node publishes callsigns and nothing else about the people using it. These
fields **do not exist** in any public response, and there is no setting that adds
them:

- transmission duration, BER, RSSI, packet loss
- talkgroup or network for a heard station
- private-call destinations, and any destination that is a callsign rather than a
  group
- precise coordinates — a 6-character Maidenhead grid (≈5 × 3 km) is the finest
  location the node will emit
- software, daemon or firmware versions
- local or peer IP addresses, hostnames, ports
- any configuration value, and any control at all

The operator additionally controls each module and each reach-card field
separately, and can suppress specific callsigns from every public output. A field
whose toggle is off is **absent from the JSON**, not present and blank.

---

## Endpoints

Base: `https://your-node.example`

| Route | Returns |
|---|---|
| `GET /api/public/node` | reach card — how to work the node |
| `GET /api/public/status` | is it busy |
| `GET /api/public/lastheard` | recent callsigns |
| `GET /api/public/counters` | activity totals |
| `GET /embed/lastheard` | ready-made HTML widget |
| `GET /public/` | the node's own public page |
| `GET /public/assets/logo` | the operator's logo, if uploaded |
| `GET /public/custom-block` | operator HTML, for framing only |

`GET` and `HEAD` only. Everything else answers `405`.

### `GET /api/public/node`

Every field is optional and present only if the operator enabled it.

```json
{
  "callsign": "K4SRC",
  "rx_frequency": "438900000",
  "tx_frequency": "433900000",
  "color_code": "1",
  "slots": "TS1+TS2",
  "modes": ["DMR", "YSF", "M17"],
  "talkgroup": "TG 31123",
  "grid": "EM60lp",
  "power_line": "45 W into a DB420 at 140 ft AGL",
  "purpose_tags": ["club_net", "emcomm"],
  "purpose_freetext": "Open to all licensed amateurs.",
  "links": [{ "id": 1, "label": "Club website", "url": "https://example.org", "sort_order": 0 }],
  "nets": [{ "id": 1, "name": "Weekly Net", "schedule_text": "MON 20:00",
             "target": "TS2 / TG 31123", "note": "Visitors first", "sort_order": 0 }],
  "narrative_html": "<h1>K4SRC</h1><p>…</p>",
  "has_logo": true,
  "has_custom_block": false
}
```

Frequencies are **hertz, as configured** — divide by 1e6 for MHz.

`talkgroup` appears only while a transmission is up **and** its destination is a
group. A private call or a direct D-Star contact leaves it absent, because naming
the callee would publish a third party's identity as a side effect of somebody
calling them.

`narrative_html` is already rendered from Markdown and already sanitised by the
node. Insert it; do not re-render it.

Which toggle controls what:

| Field | Setting |
|---|---|
| `rx_frequency`, `tx_frequency` | Show frequencies |
| `color_code`, `slots` | Show colour code / timeslots |
| `modes` | Show mode |
| `talkgroup` | Show talkgroup |
| `grid` | Show grid square (**off by default**) |
| `power_line` | Show power line |
| `links` | Show links |
| `nets` | Show nets |

### `GET /api/public/status`

```json
{ "state": "idle", "last_activity_minutes": 7 }
```

`state` is `"idle"` or `"transmitting"`. `last_activity_minutes` is absent while
transmitting, and absent when nothing falls inside the retention window.

No callsign, no duration, no talkgroup — deliberately. "Someone is transmitting"
is what a visitor needs in order to decide whether to key up.

### `GET /api/public/lastheard`

Query: `limit` (default 25, max 100).

```json
{
  "available": true,
  "entries": [
    { "callsign": "KK4WXT", "mode": "DMR", "at": "2026-08-02T03:52:58Z" },
    { "callsign": "W4FLA",  "mode": "YSF", "at": "2026-08-02T03:52:49Z" }
  ]
}
```

**Always check `available`.** When the node's station ID database
(`DMRIds.dat`) is missing or corrupt, DMR, P25 and NXDN sources arrive as bare
numeric IDs and are dropped, while D-Star and YSF still resolve from the air. A
list built from what survives would be a confident wrong answer — a busy DMR
repeater looking as though it had been silent all day. So the node withholds the
whole list instead:

```json
{ "available": false, "notice": "station database missing or corrupt", "entries": [] }
```

Render the notice. Do not render an empty table.

Entries are newest first. Suppressed callsigns are absent, as are unresolvable
IDs. The window is the operator's retention setting (default 24 h, adjustable
1–168 h).

**This list is stations the node heard on RF** — not traffic arriving from a
network and played out locally. That is why `status` can report activity while
this list is empty: a node relaying a busy talkgroup is genuinely busy, but it has
not *heard* anyone. The two endpoints answer different questions and can disagree
without either being wrong.

### `GET /api/public/counters`

```json
{ "available": true,
  "counters": { "callsigns": 12, "transmissions": 47, "window_hours": 24 } }
```

Same `available` contract as `lastheard`, and for the same reason. `window_hours`
is reported even when unavailable — it describes the node's retention policy, not
the data.

Suppressed callsigns are excluded from **both** figures. A count that still moved
when a suppressed operator keyed up would tell anyone watching exactly when they
were on the air.

### `GET /embed/lastheard`

A self-contained HTML document with no JavaScript, no external requests, and no
dependency on your page's styles. Query parameters:

| Parameter | Default | Notes |
|---|---|---|
| `limit` | `10` | 1–50 |
| `status` | off | `status=1` prepends the status line |

`status=1` is a request, not an override: if the operator disabled the status
module, it stays absent.

It respects the last-heard toggle, so a node with the list turned off answers
`404` here too — publishing it through a different door would not be consent.

It follows the light/dark preference of whoever is viewing, via
`prefers-color-scheme`.

### `GET /public/custom-block`

Operator-authored HTML, served for **framing only**, and only in a sandbox. If
you embed it, use exactly:

```html
<iframe sandbox="allow-scripts" src="https://your-node.example/public/custom-block"></iframe>
```

Do not add `allow-same-origin`. Together with `allow-scripts` it returns the
document to the node's origin and removes the isolation entirely.

---

## Framing rules

| Route | `frame-ancestors` | Meaning |
|---|---|---|
| `/embed/*` | `*` | framed by anyone — this is the point of it |
| `/public/*` | `'self'` | the node's own pages, not embeddable elsewhere |
| the admin app | `'self'` | never embeddable |

The widget is the only thing designed to be framed by strangers. It is safe there
because it runs no script, submits no forms, and carries no session — there is
nothing inside it worth reaching by framing it.

---

## CORS

`/api/public/*` and `/embed/*` respond with:

```
Access-Control-Allow-Origin: *
Access-Control-Allow-Methods: GET, HEAD, OPTIONS
```

so a browser on any origin can fetch them. `OPTIONS` preflight answers `204`.

No other route carries `Access-Control-Allow-Origin` — enabling the public view
does not widen the authenticated API by even one header.

`Access-Control-Allow-Credentials` is deliberately **not** sent. Cookies are never
required, never read, and never honoured on these routes; with a wildcard origin
that header is how an anonymous API quietly becomes readable by any site a
logged-in operator happens to visit.

---

## Rate limits

Per IP: **10 requests per second, burst 30**. Requests from the node itself
(loopback) are exempt.

Over the limit returns `429` with `Retry-After: 1`. A page loading the four
endpoints and polling status every five seconds is far below it; a tight loop is
not.

`X-Forwarded-For` is ignored — the limit is keyed on the connecting address, so a
node behind a reverse proxy sees all traffic as one client. Rate-limit at the
proxy in that deployment.

---

## Caching

| Route | `Cache-Control` |
|---|---|
| `/api/public/*` | `no-store` — a cached last-heard list is a wrong one |
| `/embed/*` | `public, max-age=30` |
| `/public/` shell | `public, max-age=300` |
| `/public/assets/logo` | `public, max-age=300` |

If you poll, five seconds is plenty. Anything faster shows you nothing new and
will meet the rate limit.

---

## Replacing a Pi-Star scrape

If your club site fetches `last_heard.php` and parses it with a regex, this
replaces it. The scrape breaks whenever the upstream page changes, and it pulls a
full HTML page to extract a handful of rows.

**Least work** — swap the scrape for an iframe:

```html
<iframe src="https://your-node.example/embed/lastheard?limit=15"
        style="width:100%;max-width:420px;height:420px;border:0"
        title="Last heard on K4SRC" loading="lazy"></iframe>
```

**If you have your own styling**, fetch the JSON:

```html
<div id="heard">Loading…</div>
<script>
async function refresh() {
  const box = document.getElementById("heard");
  try {
    const r = await fetch("https://your-node.example/api/public/lastheard?limit=15");
    if (r.status === 404) { box.textContent = "This node does not publish activity."; return; }
    const d = await r.json();
    if (!d.available) { box.textContent = d.notice; return; }
    if (!d.entries.length) { box.textContent = "Nothing heard recently."; return; }
    box.replaceChildren(...d.entries.map(h => {
      const row = document.createElement("div");
      row.textContent = `${h.callsign} · ${h.mode} · ${new Date(h.at).toLocaleTimeString()}`;
      return row;
    }));
  } catch (e) {
    box.textContent = "Node unreachable.";
  }
}
refresh();
setInterval(refresh, 30000);
</script>
```

Three cases worth handling, and each means something different:

- **`404`** — the operator has not enabled the public view, or has turned that
  module off. Not an error; show nothing, or a quiet note.
- **`available: false`** — the node's station database is broken. Show `notice`.
  Do not show an empty list.
- **`429`** — you are polling too fast. Back off; 30 s is ample.

---

## Notes for operators

- **Nothing is published until you enable it.** Settings → Public View.
- **The grid square is off by default**, and there is nothing to derive it from
  automatically — set it yourself if you want it published. The node truncates to
  6 characters (≈5 × 3 km) regardless of what you enter.
- **Suppress list.** Add a callsign and it disappears from the last-heard list,
  the counters and the map. Matching is on the base callsign, so `N4ABC-7`,
  `N4ABC/M` and `n4abc` are all the same entry.
- **Retention** governs every public activity read. Lowering it hides older
  entries from the public surface immediately; it does not delete anything, and
  your own dashboard history is unaffected.
- **Custom HTML runs in a sandbox that cannot reach your node.** You own what you
  paste there — it can misbehave inside its own frame, and it cannot touch your
  session, your cookies, or the rest of the page.
