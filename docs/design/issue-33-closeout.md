# Issue #33 closing comment (draft)

Draft of the comment to post on [#33] before closing it. Kept in the repo rather
than posted straight away so the mapping below is reviewable in a PR — the
claim "this checkbox shipped in that PR" is the sort of thing worth checking
against the tree, not taking on trust.

**To use:** paste the section below the rule into the issue, then close it.

---

Per-mode config is complete. Every checkbox above is shipped, and the last two
genuine gaps — DMR's TG hold / fine-grained hangs, and POCSAG's missing daemon
binary — closed in #193 and KN4OQW/waypoint-stack#15.

The shape each mode was supposed to take is the shape it took: a store section,
a renderer for the gateway's INI, an enable-gated render target, a systemd unit,
and a settings tab. `docs/config-coverage.md` §2 now reads ✅ on all eight rows.

## Checkbox → PR

| Checkbox | Shipped in |
|---|---|
| **DMR** — `[DMR]`+`[DMR Network]` + DMRGateway | Store section, `[DMR]`/`[DMR Network]` renderer and the DMR tab: #35. Networks (add/remove, type, password, ESSID): #35. WPSD-style routing generation from network type, and static/dynamic TG via the per-network `Options` string: #41, with the Parrot regression pinned in #132. **TG hold + per-section hangs: #193.** |
| **YSF** — `[System Fusion]` + YSFGateway/DGIdGateway | End-to-end (store, renderer, hostlist, tab): #36. DG-ID / YCS gateway and the WPSD parity spec: #44. Parity gap G1 (mode params on the tab): #60. Hardware: DG-ID voice #143, live reflector link on both paths #147. |
| **D-Star** — `[D-Star]` + DStarGateway (ircDDB) | #39 — module, ircDDB login, startup reflector, DExtra/DPlus/DCS/XLX. |
| **P25** — `[P25]` + P25Gateway | #37 — reflector network, startup TG list, NAC. |
| **NXDN** — `[NXDN]` + NXDNGateway | #38 — reflectors, startup TG list, RAN. |
| **M17** — `[M17]` + M17Gateway | #40. Needed a host fork: upstream MMDVM-Host removed M17 in `1e2e0c74`, so waypoint-stack pins a fork that restores it (`docs/config-coverage.md` §2, "M17 required a host fork"). |
| **POCSAG** — `[POCSAG]` + DAPNETGateway | Config end-to-end (store, renderer, AuthKey as a write-only secret, tab): #51, with a crash-loop fix in #57. **Daemon pinned, built and packaged: KN4OQW/waypoint-stack#15.** |
| **FM** — `[FM]` + analog | #51 — CTCSS, timeout, kerchunk, audio levels, access mode. No gateway daemon exists for analog FM, so the `[FM]` section is the whole surface. |
| **Cross-mode gateways** (YSF2DMR, DMR2YSF, YSF2NXDN, DMR2NXDN) | **Deliberately superseded, not left undone** — see below. |

## The cross-mode gateway line was superseded, not dropped

This issue tracked the transcoding bridges as five more daemons under the same
per-mode pattern. They were built that way (#46, #48, #55) and then
**retired on purpose** in #60, because RFC-0003 replaced the per-bridge-daemon
model with a **named bus that modes attach to**: traffic entering from any
attached mode is converted and emitted to the others. That is an intentional
departure from WPSD's bridge model, and it is recorded as such in
`docs/config-coverage.md` §3.

Retiring rather than repairing was the call because the MMDVM_CM bridge surface
did not survive the rewrite and carried known defects (a stale daemon left
running on disable, a null view when disabled) that the bus architecture closes
by construction. Nothing was lost: the five bridge **store sections are retained
and dormant** — `SetCrossBridge`/`SetSection` still accept them and they
round-trip through Save/Load — so RFC-0003's migration can seed bus definitions
from the saved masters, passwords and TGs.

The bus work that replaced it: #88 (model + attach validator), #89 (renderer +
bridge migration), #90 (AMBE+2 reframing), #92 (`cmd/waypoint-bus`), #93 (UI),
#94 (Phase-1 hardware validation), #103 (loopback hand-off), #105 (MQTT events),
#110 (real over-the-air RF across a bus). LAN peering follows in RFC-0016 (#96,
#97, #98, #101).

So: the checkbox is not unfinished work. The `MMDVM_CM` binaries are not needed
and were never pinned in waypoint-stack.

## Follow-on, filed separately

`#193` scoped the hang-timer work to DMR, which is where this issue's checkbox
was. Upstream MMDVM-Host also accepts a per-section `ModeHang` in `[D-Star]`,
`[System Fusion]`, `[P25]`, `[NXDN]`, `[FM]` and their `[... Network]`
counterparts — YSF's RF-side `TXHang`/`ModeHang` pair is already modeled, the
rest are not. A uniform sweep across every mode is worth doing on its own terms
and is **not** part of #33: folding it in would have tripled the UI and catalog
surface for a control nobody has asked for per-mode yet. Suggested as a
standalone issue, "Model per-section ModeHang for the remaining modes", with the
same blank-omits-the-key rule #193 established so the `[General]` fan-out is
never severed.

Closing.

[#33]: https://github.com/KN4OQW/waypoint/issues/33
