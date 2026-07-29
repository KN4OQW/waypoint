# Translations

Waypoint's web UI reads its text from flat JSON message catalogs, one file per
language, in [`ui/static/locales/`](../ui/static/locales/). There is no build
step and no extraction toolchain: the browser fetches a catalog and the page
renders from it.

## How catalogs work

Each catalog is a flat object of dot-namespaced key → string, plus a reserved
`_meta`:

```json
{
  "_meta": { "name": "Deutsch", "tag": "de-DE", "reviewed": false },
  "status.connected": "verbunden"
}
```

- **`_meta.name` is the language's own name for itself.** It is what the picker
  shows, so a German speaker looks for *Deutsch*, not *German*.
- **`_meta.tag` must equal the filename** (`de-DE.json` → `de-DE`). A catalog is
  fetched by its filename; a mismatch would index a language the browser then
  fails to load, so the generator rejects it.
- **`_meta.reviewed`** is false until a native speaker has been through the
  catalog. The picker marks unreviewed languages so an operator knows what they
  are getting.

**`en-US.json` is the base.** Every key originates there. Other catalogs may be
partial: lookup falls back to en-US *key by key*, so a half-finished translation
renders translated where it has a string and English everywhere else — never a
blank. A key missing from both renders as the key itself, which is visible on
the page and greppable in the source.

**`index.json` is generated, never hand-edited.** `tools/genlocaleindex` builds
it from the catalogs' `_meta` blocks; run `go generate ./...` after adding or
renaming a catalog. CI regenerates and fails on any diff.

## Placeholders

A `{placeholder}` is substituted at render time. **The set of placeholders in a
translated string must match the English one exactly** — same names, no
additions, none dropped. They may be reordered freely; that is the point of
naming them.

```
"log.netStart": "{source} → {dest} from {network}"
```

A placeholder with no matching value is left on the page verbatim rather than
blanked, so a mistake is visible rather than silent.

Some strings carry inline markup (`<b>`, `<code>`) because the emphasis falls
inside the sentence. Keep the tags and translate around them.

## Adding a language

Platform-driven translation is the intended path — see the Weblate section once
it lands. This PR flow remains the offline fallback:

1. Copy `ui/static/locales/en-US.json` to `<tag>.json` (BCP-47, e.g. `pt-BR`).
2. Set `_meta` — native name, matching tag, `"reviewed": false`.
3. Translate the values. Leave `_meta` keys and placeholders alone.
4. `go generate ./...` to refresh `index.json`.
5. Open a PR. A catalog-only PR is reviewed as a catalog diff.

You do not need to translate everything. Anything you leave out falls back to
English, so a partial catalog is a useful contribution.

## What is not translated

Deliberately, and by policy:

- **Protocol and domain tokens** — mode names (DMR, D-Star, System Fusion, P25,
  NXDN, M17, POCSAG, FM), callsigns, talkgroup and DMR ID numbers, reflector
  names, INI section and key names, LCD template tokens like `{callsign}`, file
  paths and device names. These are the same words in every language, and an
  operator matching them against a radio's menu or another project's docs needs
  them unchanged.
- **The raw INI editors** on the Expert tab. Their content is configuration, not
  copy.
- **Text the daemon supplies over the API** — network link state ("logged in"),
  gateway detail lines, error strings from `/api/*`. These come from the server
  and from MMDVM-Host's own output; translating them needs a server-side
  catalog, which is a separate design.
- **Log lines**, which are developer-facing.
- **The LCD panel.** The HD44780 character ROM is not UTF-8 — `sanitizeASCII`
  exists precisely because of it — so accented text would render as `?`. Out of
  scope unless a per-charset ROM story appears.
- **Pre-auth screens** (the claim/login gate and the first-boot wizard). These
  are Go-generated pages that need a server-side catalog read; tracked
  separately.

## Reviewing a translation PR

A catalog-only change is reviewed as a catalog diff. The mechanical checks —
JSON validity, `_meta` correctness, placeholder parity, index freshness — belong
in CI rather than in a reviewer's head.

## Guard against regressions

`tools/ci/hardcoded-strings.sh` looks at the lines a branch *adds* to the scoped
frontend files and warns about ones that look like user-visible English going in
without a catalog key. It is a heuristic and a warning, not a gate; see the
script's header for what it cannot see.
