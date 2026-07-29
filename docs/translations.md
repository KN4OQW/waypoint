# Translating Waypoint

Waypoint's web UI reads its text from flat JSON message catalogs, one file per
language, in [`ui/static/locales/`](../ui/static/locales/). There is no build
step and no extraction toolchain: the browser fetches a catalog and the page
renders from it.

**Adding a language is a pull request that touches one new file.** No code
review is involved — a catalog-only change is reviewed as a catalog diff, with
the mechanical checks done by CI.

## How to add or fix a language

You need a GitHub account and a text editor. You do not need Go, Node, or a
running hotspot.

1. **Copy the base catalog.** `ui/static/locales/en-US.json` →
   `ui/static/locales/<tag>.json`, where `<tag>` is a BCP-47 tag such as
   `pt-BR`, `de-DE`, `it-IT`. If your language is already there, edit that file
   instead.

2. **Set `_meta`** at the top of the file:

   ```json
   "_meta": { "name": "Português (Brasil)", "tag": "pt-BR", "reviewed": false }
   ```

   - `name` is **your language's name for itself** — "Deutsch", not "German".
     It is what the language picker shows.
   - `tag` **must match the filename exactly**. A catalog is fetched by its
     filename, so a mismatch would list a language the browser then fails to
     load. CI rejects it.
   - `reviewed` is `false` until a native speaker has been through the whole
     file. The picker marks unreviewed catalogs so operators know what they are
     getting. Set it `true` only when you have genuinely read every string.

3. **Translate the values.** Leave the keys alone.

4. **Refresh the index** — `go generate ./...` from the repository root, which
   rewrites `ui/static/locales/index.json`. If you have no Go toolchain, say so
   in the pull request and a maintainer will run it; CI will tell you if it is
   stale.

5. **Open a pull request.** Title it `i18n: add <Language>` or
   `i18n: correct <Language>`.

**You do not have to translate everything.** Lookup falls back to English *key
by key*, so a partial catalog renders translated where it has a string and
English everywhere else — never a blank. Twenty strings is a useful
contribution. Come back for more later.

## The rules that matter

### Placeholders must survive exactly

A `{placeholder}` is substituted at render time. The set of placeholders in your
string must match the English one — same spelling, none added, none dropped.

```
en-US   "log.netStart": "{source} → {dest} from {network}"
de-DE   "log.netStart": "{source} → {dest} über {network}"
```

You may **reorder them freely** — that is why they are named rather than
numbered. Put them where your language's grammar wants them.

A placeholder with no matching value is printed literally rather than blanked,
so a typo shows up on screen instead of silently eating text. CI treats a
placeholder mismatch as a hard error, because it is the most common way a
translation breaks the UI.

### Amateur-radio terms follow the community, not the dictionary

Waypoint's users are licensed operators reading this screen with a radio manual
open beside them. Where a string contains a domain term — *talkgroup*,
*reflector*, *hotspot*, *simplex*, *duplex*, *color code*, *callsign*, *master*
— use the word your country's amateur community actually uses. If Pi-Star, WPSD
or the BrandMeister wiki already render it in your language, follow them.

A literal translation that no operator would recognise is worse than leaving the
English.

### Screen space is tight

The sidebar and the status bar are narrow, and the dashboard is expected to work
on a phone. Where a string is an ALL-CAPS label or a button, prefer the shorter
of two correct options. German and Finnish translators will feel this first.

### Inline markup stays

A few strings carry `<b>` or `<code>` because the emphasis or the literal falls
mid-sentence. Keep the tags and translate around them:

```
"Renders to <code>[DMR] SelfOnly</code>."
```

## What is not translated

Deliberately, and by policy:

- **Protocol and domain tokens** — mode names (DMR, D-Star, System Fusion, P25,
  NXDN, M17, POCSAG, FM), callsigns, talkgroup and DMR ID numbers, reflector
  names, INI section and key names, LCD template tokens like `{callsign}`,
  device paths (`/dev/ttyAMA0`) and hardware model names. These are the same
  words in every language, and an operator matching them against a radio's menu
  or another project's documentation needs them unchanged.
- **The raw INI editors** on the Expert tab. Their content is configuration.
- **Text the daemon supplies over the API** — network link state ("logged in"),
  gateway detail, error strings from `/api/*`. These come from the server and
  from MMDVM-Host's own output; translating them needs a server-side catalog,
  which is a separate design.
- **Log lines**, which are developer-facing.
- **The LCD panel.** The HD44780 character ROM is not UTF-8 — `sanitizeASCII`
  exists precisely because of it — so accented text renders as `?`. Out of scope
  unless a per-charset ROM story appears.
- **Pre-auth screens** (the claim/login gate and the first-boot wizard). These
  are Go-generated pages needing a server-side catalog read, tracked separately.

## Trying it before you send it

You do not need hardware. From a clone of the repository:

```sh
go build -o waypointd ./cmd/waypointd
./waypointd -demo -addr 127.0.0.1:8073 -tls=false \
            -store /tmp/wp.db -tls-dir /tmp/wp-tls
```

Open <http://127.0.0.1:8073>, claim the node with any username and password, and
pick your language from the selector under the theme swatches in the sidebar.
Walk the dashboard and every settings tab. Shrink the window to phone width and
walk them again — that is where a long label breaks a row.

The catalogs are embedded in the binary at build time, so rebuild after editing.

## How a translation PR is reviewed

The review bar is deliberately low, and mechanical:

1. CI validates the catalog — JSON parses, no duplicate keys, `_meta` correct,
   no keys that do not exist in `en-US`, placeholders match, index not stale.
2. A maintainer reads the diff.

That is all. There is no code to review, because a catalog is not code. This is
what makes a new language cheap enough to actually accept.

## For maintainers: keeping the source honest

`tools/ci/hardcoded-strings.sh` looks at the lines a branch *adds* to
`ui/static/*.html` and `ui/static/*.js` and warns about ones that look like
user-visible English going in without a catalog key. It is a heuristic and a
warning, not a gate; the script header says what it cannot see.

The completeness check that *does* work is the bracket walk: generate a catalog
whose every value is its own key in brackets, point the UI at it, and read the
pages. Anything still legible in English never made it out of the source.
