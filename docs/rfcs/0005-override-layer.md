# RFC-0005: The Override Layer

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #2 (user customizations survive every update)
- Depends on: RFC-0001 (the configuration store — the override layer is the "human escape hatch" that RFC-0001 §Override layer specifies but does not build; this RFC pins the mechanics down to a tested contract and implements them)

## Summary

Every gateway INI Waypoint ships is a compiled output of the store (RFC-0001):
the renderers are pure functions, so an update that ships new renderer code
regenerates every file wholesale. That is the losslessness win — and it is also
the problem this RFC closes: a wholesale regenerate has **nowhere for a local
customization to live**. If the operator hand-edits `MMDVM-Host.ini` to add a key
the model does not carry, the next Apply overwrites it (the generated header even
says so). If they add local host entries to a downloaded hostlist, the next
refresh replaces the file.

The **override layer** is the answer RFC-0001 named but left unbuilt: a set of
on-disk drop-in fragments, owned by the human, that merge **last** into the
rendered output — after the store, after the renderer, and never touched by an
update. Two mechanisms, one guarantee:

- **INI overrides** — `overrides.d/<daemon>.d/*.conf` fragments merge into each
  rendered INI by section and key. A fragment replaces the keys it names, adds
  keys the model does not carry, and can delete a rendered default outright
  (`!unset`). Precedence among fragments is lexical filename order.
- **Host-file hooks** — text hostlists (`DMR_Hosts.txt`, `M17Hosts.txt`) get
  `<hostfile>.prepend.d/*` and `<hostfile>.append.d/*` hooks whose contents are
  concatenated around the downloaded base after every refresh. This is the
  first-class replacement for Pi-Star's `P25HostsLocal` grievance (sbin #11,
  open since 2018).

The guarantee (#2 acceptance, enforced as a property test): an update applied
over a system with overrides and local host entries reproduces **byte-identical
effective config** afterward, because the store re-renders deterministically and
the override merge re-applies deterministically on top of files an update never
writes.

## Motivation

Requirement #2 is one of the three RFC-0001 was chartered to implement (#1
round-trip, #2 override layer, #3 profiles), and it is the one still open. The
provenance is old and specific: "leave `P25HostsLocal` alone on update" has been
open on Pi-Star since **2018** (sbin #11); a general override/hook mechanism was
requested in sbin #39; WPSD's nightly tasks clobber local modifications *by
design*. The incumbent failure is architectural — the INI file is simultaneously
the UI's read model, the write target, and the daemon's input, so "your edit"
and "the tool's edit" are the same bytes and the tool always wins.

RFC-0001 already removed the first two roles: the store is the read model and the
write target; the INI is a disposable output. That is exactly what makes a clean
override layer *possible* — because the rendered file is regenerated from a known
pure function, a fragment that says "in section `[DMR]`, set `ColorCode=3`" has an
unambiguous meaning against an unambiguous base. There is no regex-rewrite of a
hand-edited file to get wrong. The override is applied to freshly-rendered text
whose every line the renderer produced.

The design constraint RFC-0001 set and this RFC inherits: overrides are the human
escape hatch, so they are **hand-edited disk files** and bypass the store's
JSON-Schema validation. What is guaranteed is not that their *content* is valid
(the daemon validates that when it reads the file, same as any hand-edit) but
that they **re-apply deterministically** and **do not interact with unrelated
store changes** — RFC-0001 property 5, made concrete and testable here.

## Design

### Directory layout

The override root is a sibling of the generated-INI directory, so it lives on the
same partition and survives the same way the store does. On the reference deploy
that root is `/home/pi-star/waypoint/overrides.d` (the RFC-0001 nominal path
`/etc/waypoint/overrides.d/` is the same idea; the daemon takes the real path from
a flag, `-overrides-dir`, defaulting to the sibling of the store). Under it:

```
overrides.d/
  mmdvm.d/            10-fixedmode.conf   50-extra-fm.conf     ← MMDVM-Host.ini
  dmrgateway.d/       10-local-rewrites.conf                   ← DMRGateway.ini
  ysfgateway.d/  dgidgateway.d/  p25gateway.d/  nxdngateway.d/
  dstargateway.d/  m17gateway.d/  dapnetgateway.d/
```

Each render target owns a **daemon key** (`mmdvm`, `dmrgateway`, `ysfgateway`, …)
that names its subdirectory. The key is a new field on `RenderTarget` alongside
its `Path`/`Unit`/`Render`, so a mode contributes its own override namespace the
same way it contributes its own render target and restart unit — issue #21's
gateway-plugin seam, extended one field. A daemon with no fragment directory, or
an empty one, renders exactly as today: **the override layer is inert until an
operator creates a fragment**, and default-off is the whole feature's posture.

The DG-ID / YSF split is handled for free: `RenderTargets` already swaps the whole
YSF target (file, unit, renderer) on `EnableDGId`, so it swaps the daemon key too
(`ysfgateway` ↔ `dgidgateway`). An operator's YSF overrides follow whichever
gateway is active, which is the correct and least-surprising behavior.

### INI fragment format and merge semantics

A fragment is an INI file in the daemons' own dialect (the format `internal/config/ini.go`
already parses): `[Section]` headers, `Key=Value` pairs, `#`/`;` comments. The
merge is defined against the **rendered base text** for that daemon — the exact
string the renderer produced this Apply — and is **line-preserving**: the base's
sections, key order, and (header) comments are kept; the fragment edits only what
it names.

For each fragment, in lexical filename order, for each `[Section] Key=Value`:

- **Key exists in the base section** → its value line is rewritten in place. The
  key keeps its position; only the value changes.
- **Key absent, section present** → the `Key=Value` line is appended after the
  last existing key line of that section.
- **Section absent** → a new `[Section]` block is appended at the end of the file
  with the fragment's keys.
- **`Key=!unset`** → the key's line is removed from the base entirely. `!unset`
  on a key the base does not have is a no-op. This is how an operator suppresses a
  *rendered default* (not merely overrides its value) — RFC-0001's stated
  deletion semantics.

**Precedence.** Fragments apply in ascending filename order (`10-` before
`50-`), so when two fragments touch the same section/key the later filename wins —
the `NN-name.conf` convention. Within one fragment, a later line wins over an
earlier one for the same key. The winner is what lands in the file **and** what
the UI reports as effective (below), so "what will actually take effect" is never
a guess.

**Match rules.** Section and key matching is case-insensitive (matching
`ini.go`'s accessors and the daemons' own readers); the base's original spelling
is preserved on a value rewrite. A fragment line with no `=` and no leading
comment marker is ignored with a counted warning rather than silently dropped
(the project's "no silent caps" rule).

The merge is a pure function `ApplyOverrides(base string, frags []Fragment)
(string, []Applied)` — same base and fragments ⇒ byte-identical output and the
same applied-report. Purity is what makes the update-survival property a test and
not a hope: an "update" is just a second render+merge, and determinism means it
lands on the same bytes.

### The applied-report (provenance and UI)

`ApplyOverrides` returns, alongside the merged text, a list of `Applied` records —
one per override that actually changed the base:

```go
type Applied struct {
    Daemon  string // "mmdvm", "dmrgateway", …
    Section string
    Key     string
    Old     string // rendered value; "" when the key was added
    New     string // effective value; "" when unset
    Unset   bool   // the override deleted a rendered key
    Added   bool   // key or section not present in the rendered base
    Source  string // winning fragment filename — the provenance
}
```

`Source` is the provenance RFC-0001 asked the override model to carry (`disk`
today; a `ui` origin is reserved — a UI-managed override would populate the same
record from a different loader without a schema change). The report drives the
read-only **Overrides** panel in the Expert tab: per daemon, each effective
override as *section · key · rendered → effective (or removed)* with the winning
fragment named. Visible, not fought — RFC-0001's "surfaced in the UI" made real.
It is also what `GET /api/overrides` serves.

### Host-file hooks

Hostlists are not INI, so they get concatenation hooks instead of a section merge
(RFC-0001). For a text hostlist at `…/DMR_Hosts.txt`:

```
DMR_Hosts.txt.prepend.d/*   ← concatenated, lexical order, BEFORE the base
DMR_Hosts.txt               ← the downloaded/cached base (refresher writes this)
DMR_Hosts.txt.append.d/*    ← concatenated, lexical order, AFTER the base
```

The refresher's existing atomic-write path (`dmrhosts.Fetch`, `m17hosts` …) is
wrapped: after the base is fetched to its cache path, if either hook directory is
non-empty the effective file is reassembled `prepend + base + append` and swapped
in atomically. With no hooks present the base is written exactly as today. This is
the direct answer to sbin #11 — an operator's local DMR masters / M17 reflectors
survive every nightly refresh because the refresher never writes into the hook
dirs, it only concatenates them.

**Scope for v1:** the *text* hostlists (`DMR_Hosts.txt`, `M17Hosts.txt`). The
JSON hostlists (YSF/P25/NXDN/D-Star are `…Hosts.json`) need a structure-aware
merge rather than raw concatenation; that is a documented follow-up (Open
questions), and D-Star already has a separate `CustomHostsfiles` directory the
gateway reads natively. No hostlist loses local entries silently: a JSON hostlist
with a stray hook directory logs that the hook is not yet honored.

### Where it wires in

- **Apply / `WriteFiles`.** Each target renders, then loads its `overrides.d/<daemon>.d`
  fragments, then merges, then writes atomically — the same `writeAtomic` (temp +
  rename, 0600) as today, so a crash mid-Apply still never leaves a half-written
  file. Rendering-then-merging keeps the renderer pure (it never sees overrides);
  the merge is a distinct, separately-tested stage on top.
- **Flag.** `-overrides-dir` (default the store's sibling `…/waypoint/overrides.d`),
  held on the `server` struct next to `paths`. In `-demo` mode it points at an
  empty temp dir so a demo never picks up a real node's overrides.
- **API.** `GET /api/overrides` returns the applied-report for the *current* store
  render — behind the same session wall as every other config route (RFC-0001 /
  RFC-0002; no gate change). Read-only: overrides are edited on disk by design in
  v1, and the endpoint reports what the next Apply will do.

## The override contract (test harness)

CI enforces these as release-blocking properties, in the RFC-0001 property-test
style. Together they are RFC-0001 property 5 ("override determinism") made
concrete, plus the #2 acceptance:

1. **Merge semantics.** Table-driven over the four cases (replace existing key /
   add key to existing section / add new section / `!unset` existing key) plus the
   edges (`!unset` a missing key = no-op; case-insensitive section+key match;
   malformed line counted not dropped): the merged text is exactly as specified
   and the base's untouched lines are byte-identical.
2. **Fragment precedence.** With two fragments setting the same section/key, the
   lexically-later filename wins in both the emitted file and the applied-report;
   within one fragment the later line wins.
3. **Isolation (RFC-0001 property 2, under overrides).** Randomized override
   fragments active ⇒ changing store key A never alters the rendered-plus-merged
   output for an unrelated key B outside A's section, for every daemon.
4. **Update survival (#2 acceptance).** Render+merge, then render+merge again over
   the same store and fragments ("apply an update"), ⇒ byte-identical effective
   files. Repeated for the host-file hooks: refresh, then refresh again over the
   same base + hook dirs ⇒ byte-identical assembled hostfile. This is the #2
   acceptance as an automated test.
5. **Inertness.** No fragment directory, empty directory, and directory of only
   comments/blank lines each ⇒ output byte-identical to the pure render. The
   feature is provably off until used.
6. **Applied-report fidelity.** Every record in the report corresponds to a real
   change in the emitted file (right `Old`/`New`/`Unset`/`Added`/`Source`), and
   every change in the emitted file has exactly one report record — so the UI and
   the file can never disagree about what took effect.
7. **Host-file hooks.** `prepend + base + append` in lexical order, atomic swap,
   base untouched when no hooks; a hook on a JSON hostlist is reported unhonored,
   not silently applied.

## Alternatives considered

- **Keep INIs authoritative, edit carefully (the incumbent).** Rejected — this is
  the #2 bug and two decades of Pi-Star/WPSD evidence. The whole point of RFC-0001
  was to stop the file being the source of truth; an override layer that edited
  the file in place would reintroduce the coupling.
- **Store the overrides in the store (UI-managed only).** Rejected *as the v1
  mechanism*, kept as a door. The update-survival guarantee is strongest when the
  human's escape hatch is a plain file an updater has no reason to touch; a
  store-only override would be back inside the thing updates rewrite. RFC-0001
  reserved a `ui` provenance so a UI-authored override can later feed the *same*
  merge and the *same* applied-report without a schema migration — this RFC keeps
  that reservation (the `Source`/provenance field) and implements the disk path
  first.
- **Re-serialize the rendered INI through a parsed model and merge structurally.**
  Rejected in favor of the line-preserving merge. Round-tripping through
  `ini.go`'s map loses key order and the header, so the "update produces identical
  bytes" property would depend on a canonical re-serializer staying in lockstep
  with the renderers. Editing the rendered lines in place makes the base's own
  output authoritative for everything the fragment does not name.
- **Patch-file (diff/patch) overrides.** Rejected — a context diff breaks the
  moment the renderer's surrounding lines change (i.e. exactly on the updates this
  feature exists to survive). Section/key addressing is stable across renderer
  changes in a way line-context is not.
- **JSON-hostlist concatenation.** Deferred, not rejected — raw-concatenating JSON
  produces invalid JSON. The text hostlists cover the headline grievance
  (`P25HostsLocal`/local DMR masters); JSON hooks get a structure-aware merge in a
  follow-up.

## Open questions

1. **JSON hostlist hooks.** YSF/P25/NXDN/D-Star hostlists are JSON. A
   structure-aware "local entries" merge (append objects to the `reflectors` /
   masters array, de-duped by key) is the natural shape, but the key semantics
   (does a local entry override a same-named downloaded one?) want their own small
   design. Deferred; v1 reports an unhonored hook rather than pretending.
2. **UI-authored overrides.** The `ui` provenance is reserved but no UI writes
   fragments yet. When it lands, does it write real `overrides.d` files (so the
   disk and UI paths stay one mechanism) or a store section that the loader
   projects into `Fragment`s? Leaning the former — one merge, one file layout, the
   updater-survival property unchanged.
3. **Validation feedback.** Overrides bypass schema validation by design, but the
   daemon *does* validate when it reads the file. Should the Overrides panel
   surface the post-Apply daemon load result (did MMDVM-Host accept the merged
   file?) so a bad override is visible in the UI rather than only in a unit log?
   Leaning yes, as a follow-up once the panel exists.
4. **Ordering of `!unset` vs. add across fragments.** Defined here as "last
   filename wins per key," so a later fragment can `!unset` a key an earlier one
   added, and a later fragment can re-add a key an earlier one unset. This is the
   intuitive lexical-precedence rule; flagged only so the interaction is on record.
