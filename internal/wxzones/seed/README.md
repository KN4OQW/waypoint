# The shipped county table

`counties.txt` is the list the Weather panel's county picker searches. It is the
whole of it: nothing downloads or refreshes this at runtime, by design — see the
package comment in `../wxzones.go` for why, and the privacy argument in
`GOVERNANCE.md` principle 2 that makes it a gate rather than a preference.

Refresh it when cutting a release:

```sh
go run ./cmd/wxzoneseed          # rewrite it, and print the cross-check
go run ./cmd/wxzoneseed -check   # report staleness, change nothing
```

Read the diff before committing. A county appearing, disappearing or being
renamed is a real event a few times a decade; a hundred rows changing at once is
a capture that went wrong.

## Format

Pipe-delimited, sorted by SAME code, `#` comments at the top:

```
SAME  |UGC   |Name      |State|WFO
012113|FLC113|Santa Rosa|FL   |MOB
```

Pipe rather than comma because county names contain apostrophes and periods
everywhere and commas nowhere, so a format with no quoting rules cannot be parsed
two ways.

`Name` is the **NWS** name for the area, not always the county's legal name —
`Baltimore City`, `Mainland Monroe`, `Oahu in Honolulu`. That is the right one to
show, because it is the name that appears in the alert text the node reads out.

## Provenance

| Column | Source |
| --- | --- |
| `UGC`, `Name`, `State`, `WFO` | `https://api.weather.gov/zones?type=county` — the same service the alerts come from, and current: its records carry effective dates. |
| `SAME` | **Derived**: `"0"` + state FIPS + the UGC's three digits. |

The state FIPS table is taken from `https://www.weather.gov/source/nwr/SameCode.txt`
rather than typed out, so the mapping and the cross-check below rest on the same
source and a wrong digit cannot appear in only one of them.

Captured 2026-08-23. 3269 counties.

## Why SAME is derived, and the evidence that it is right

`api.weather.gov` does not publish the SAME code on a zone record, and SAME is
what the alert feed keys on (`wxalerts/nws/v1/same/<code>/#`). So it is derived —
and because a wrong derivation would subscribe a node to somebody else's county
silently, it was measured in two directions rather than reasoned about.

**Against NWS's own published SAME list.** 3246 of 3269 zones derive to a code
that list holds under the same county. All 23 disagreements are that file being
out of date, not the derivation being wrong — it was last modified 2016-10-03,
before Alaska's borough and census-area reorganisations (Chugach, Copper River,
Kusilvak, Hoonah-Angoon, Petersburg, Prince of Wales-Hyder, Skagway, Wrangell),
and it still lists Oglala Lakota, South Dakota under Shannon County's old code
`046113`. The remaining 14 are Federated States of Micronesia island zones. That
is why the file is used as a cross-check here and not as the source.

**Against live alerts, which is the check that matters.** A CAP alert carries
both geocodes for the same area, so the derivation can be tested against the
exact field the feed keys on. Every county UGC in every active alert derived to a
SAME code present in that alert's own SAME list: **132 of 132 pairs across 42
alerts**, sampled 2026-08-23 from `api.weather.gov/alerts/active`.

`cmd/wxzoneseed` re-runs the first check on every capture and prints it.
`TestSAMEAndUGCAgreeOnTheCountyCode` is the standing half: it asserts the
relationship holds for every shipped row, so a capture that breaks it fails in CI
rather than at a node subscribed to the wrong county.

## What this table does not cover

Marine zones and forecast zones are not here — only counties, which is what the
feature subscribes by. A county created after a node's release will not be in its
picker; `config.WXCounty` stores the name and state alongside the code so one
already chosen still displays correctly either way.
