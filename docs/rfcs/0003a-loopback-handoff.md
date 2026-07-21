# RFC-0003 Addendum A: Loopback Hand-off (closing D3)

**Status:** Proposed · **Extends:** RFC-0003 (Mode Buses) · **Relates:** RFC-0001 (Config Store), RFC-0016 (Bus LAN Peering)

## Summary

RFC-0003 gave each mode a fixed 127.0.0.1 loopback port pair and made the bus the *sole* consumer of it (§Motivation-2). On a node running the full stack that consumer is already taken — MMDVM-Host and the mode gateways own those ports — so an enabled bus cannot bind and the two cannot coexist. Both hardware validations (`docs/on-hardware-report.md`, findings **D3**) had to stop the live mode components and run the bus in isolation. This addendum specifies how an attached mode's loopback is **handed to the bus while the live stack keeps running**, per mode:

- **DMR — multiplex, never displace.** DMRGateway is already a multiplexer (one repeater ↔ *N* upstream networks). The bus becomes one more `[DMR Network N]`, routed by the existing rewrite machinery. MMDVM-Host's RF DMR and every upstream network stay live.
- **YSF / NXDN — displace in v1.** Their gateways are single-network with no multiplex seam, so a YSF/NXDN attachment makes the bus the mode's gateway and the stock gateway is not run while the attachment exists. Reflectors are unavailable on that mode for the duration; the UI says so in plain copy at attach time. A DG-ID-slot multiplex path for YSF is recorded in Open questions.

It also folds in the two supervision gaps D3 dragged along: **D5** (detach/disable cleanup + a stop path that handles a crash-looping unit) and **D2** (the `waypoint-bus@.service` template + boot enablement).

This is **implementation-free**: it fixes the render *contract* and the apply *ordering*. No code lands here.

## Motivation

The collision is physical, not cosmetic (RFC-0003 §Motivation-2). Ground truth from the bench (full citations in Appendix A):

- The **MMDVM-Host ↔ DMRGateway** link is the `62031/62032` pair — MMDVM-Host binds `62032` and sends to `62031`; DMRGateway binds `62031` and sends to `62032` (Appendix A.1). The current bus DMR endpoint binds `62032` (Appendix A.5), i.e. **MMDVM-Host's own listen port** — so an enabled DMR bus collides with MMDVM-Host and can only run with MMDVM-Host's DMR side stopped. That is the D3 collision, confirmed definitively.
- The field-proven MMDVM_CM bridge (`DMR2YSF`) reaches DMR by binding `62031` — i.e. it **displaces DMRGateway** (Appendix A.2), losing every upstream DMR network for the duration. Neither the current bus nor the CM bridge coexists with the whole stack; each displaces one side.

Isolation is acceptable for a validation run and unacceptable for a product: a node peers a bus *in order to keep operating normally otherwise*. D3 is the top-priority gap carried from Phase 1 through Phase 2.

## Design

### 0. Why this is still RFC-0001-clean

This addendum lets a **bus/attachment row influence other renderers' output**: a DMR attachment adds a `[DMR Network N]` to `DMRGateway.ini`; a YSF/NXDN attachment removes the stock gateway from the rendered unit set. That is not a departure from RFC-0001:

- **Render remains a pure function of the whole model.** `RenderDMRGateway` already reads `m.Networks` and `m.Routes` (Appendix A.5); it will additionally read `m.Buses`/`m.Attachments`. `RenderTargets` already reads `m.YSFGW.EnableDGId` to decide the YSF unit (Appendix A.4). Reading two more sections changes the output but not the property: same model in ⇒ same bytes out, no hidden state, no ordering dependence.
- **"One object, one target" was never "one object influences one target."** RFC-0001's rule is that each *rendered target* is produced deterministically from the model — not that each store object touches exactly one target. The renderer has always crossed object boundaries: a `Networks[]` row shapes a `[DMR Network N]` block; `YSFGW.EnableDGId` swaps the *entire* YSF unit between `YSFGateway` and `DGIdGateway` (Appendix A.4). Cross-object influence on unit presence is a **pre-existing** property of the render, not a new one this addendum invents.

So the contract change is: three renderers (`RenderDMRGateway`, `RenderTargets`, and the reserved-port allocation) gain the bus/attachment sections as inputs. Purity, determinism, and losslessness (RFC-0001 properties 1–3) are unchanged and re-asserted by the test contract (§6).

### 1. DMR — multiplex via a `[DMR Network N]` for the bus

DMRGateway routes MMDVM-Host's single DMR stream across *N* upstream networks by talkgroup, using `TGRewrite`/`PCRewrite`/`PassAllTG` rules (Appendix A.1). A DMR bus attachment renders as **one additional network**:

- **Bus endpoint.** The bus binds a **dedicated** loopback port from the reserved range (§5), never `62031/62032`. It presents on that port as the network endpoint DMRGateway dials — Homebrew/MMDVM master semantics; the exact login/auth handling is an implementation matter for the follow-up PR, not fixed here.
- **DMRGateway gains `[DMR Network <n+1>]`** with `Address=127.0.0.1`, `Port=<bus dedicated port>`, `Enabled=1`, and rewrite rules **derived from the attachment's TG params** (`DefaultTG`, `TGMap`) so exactly the bus's talkgroups route to it and nothing else. This reuses the existing `networkRewrites`/DMRRoute generator (Appendix A.5) — the bus is routed the same way BrandMeister or TGIF is.
- **Nothing is displaced.** MMDVM-Host's `[DMR Network]` (`62032/62031`) and DMRGateway's `[General]` repeater side are untouched; RF DMR and all upstream networks stay live. The DMR bus therefore has **no port-contention window** by construction (§Design-6).

Rendered outputs that change: `RenderDMRGateway` (append the bus network); the reserved-port allocation (§5). MMDVM-Host is unchanged.

### 2. YSF — displace in v1

YSFGateway is single-network (one MMDVM ↔ one reflector-network link on `3200/4200`; Appendix A.3). There is no multiplex seam, so a YSF attachment makes the **bus the YSF gateway**:

- The bus binds the mode's existing MMDVM-facing loopback (`4200`, the port MMDVM-Host already sends YSF to; Appendix A.5), and MMDVM-Host's `[System Fusion Network]` is **unchanged** — the bus is a drop-in on the same `3200/4200` pair. (The "repoint MMDVM at the bus" framing is a no-op here precisely because the bus reuses the gateway's ports; the operative change is the unit set. See the PR body's note where ground truth refined the stated default.)
- **The stock YSF gateway target is dropped** from the rendered unit set while the attachment exists (mirroring how `EnableDGId` already drops `YSFGateway` in favour of `DGIdGateway`; Appendix A.4). Its unit is stopped by the same apply loop that stops a displaced unit (§Design-7).
- **Reflectors are unavailable on YSF for the duration.** The attach-time UI must say so in plain copy, e.g. *"While YSF is attached to Bus A, YSF traffic goes to the bus, not to reflectors."* This is a decision the operator makes knowingly, not a silent side effect.

Rendered outputs that change: `RenderTargets` (drop the YSF/DGId gateway target when a YSF attachment exists). MMDVM-Host is unchanged.

### 3. NXDN — displace in v1

NXDNGateway is single-network (`14021/14020` to MMDVM, one `[Network]` upstream on `14050`; Appendix A.3) with no per-route multiplex. **Same shape as YSF, same UI copy:** the bus binds `14020`, the stock NXDN gateway target is dropped and its unit stopped, MMDVM-Host's `[NXDN Network]` is unchanged, and the UI states reflectors are unavailable on NXDN while attached.

Rendered outputs that change: `RenderTargets` (drop the NXDN gateway target when an NXDN attachment exists).

### 4. Reserved bus loopback range

The multiplex path (DMR now, DG-ID YSF later) needs a bus-owned port that never overlaps a stock consumer. The addendum **fixes a reserved range and requires it to be rendered, never hardcoded per bus**:

- Reserved range: **`62100–62199`** (adjacent to the DMR block, unused by any stock consumer — the occupied set is enumerated in Appendix A.6).
- waypointd allocates a **deterministic** port within the range per bus attachment that needs one (e.g. by stable bus index), so the render stays a pure function of the model (same model ⇒ same port). The specific allocation function is an implementation detail; the *range* and the *determinism requirement* are fixed here.
- Displacing attachments (YSF/NXDN v1) need **no** reserved port — they reuse the mode's existing loopback because only one consumer runs at a time.

### 5. Hand-off atomicity — one apply, ordered so no two processes contend

Attach/detach flows through the standard **render → apply** path as a single apply that changes, together: the affected gateway config(s), the rendered unit set, and the bus config. The supervisor restarts exactly the affected units. Ordering is defined per kind so no window exists where two processes hold one port:

- **Multiplex attach (DMR).** No shared port ⇒ **no ordering constraint**: write `DMRGateway.ini` (with the new network) and the bus config, then restart DMRGateway and start `waypoint-bus@<id>` — concurrently is fine. Detach reverses it (remove the network, restart DMRGateway, stop the bus); still no contention.
- **Displacing attach (YSF/NXDN).** The bus binds the gateway's port ⇒ **stop-before-start is mandatory**: (1) stop the stock gateway unit; (2) only once it has exited — the port is free — start `waypoint-bus@<id>`. Detach is the mirror: stop the bus, then start the stock gateway. Apply must serialize the pair (stop fully completes before start begins) for the displaced port; it must never issue them concurrently. This is the one place the apply ordering is load-bearing, and it is stated as a property the test contract checks (§6.4).

Everything is one apply so a partial state is never persisted: a crash mid-apply leaves the previous rendered set intact (RFC-0001 atomic write), and the next apply re-converges.

### 6. D5 folded in — detach/disable cleanup and a stop path that handles crash-loops

- **Detach/disable deletes the rendered bus config file.** When a bus is disabled or its last attachment removed, apply removes `waypoint-bus-<id>.json` (today `WriteFiles` leaves de-registered targets on disk). A stale config file must never outlive its unit.
- **The stop path handles a unit in `activating`/`failed`/crash-loop state, not only `active`.** The current stop is `is-active`-gated, so a unit that is crash-looping (exactly the D3 symptom) is skipped and never stopped. Apply must `stop` (and `disable`, §7) a de-registered bus/gateway unit regardless of its current sub-state.

### 7. D2 folded in — the templated unit and boot enablement

The `waypoint-bus@.service` template lands in **waypoint-stack** (separate PR, referenced here); this addendum fixes its contract:

- **`ExecStart`** runs `waypoint-bus -config <BusConfigDir>/waypoint-bus-%i.json -node <node-id> -dmrids <path>` (`%i` = bus id).
- **Dependencies express the topology.** For a **multiplex** (DMR) bus: `After=`/`BindsTo=` the daemons it rides — `waypoint-dmrgateway.service` (the bus is one of its networks). For a **displacing** (YSF/NXDN) bus: `After=waypoint-mmdvm.service` and `Conflicts=` the gateway unit it replaces (so systemd enforces the mutual exclusion the render expresses — the same `Conflicts=` pattern already installed for the CM bridges, `docs/on-hardware-report.md` §"Systemd `Conflicts=` installed").
- **`Restart=on-failure`** with a backoff, so a transient dependency start-order race self-heals rather than crash-looping forever (bounded by `StartLimit*`).
- **Apply enables/disables the instance for boot.** A rendered (enabled) bus target ⇒ `systemctl enable waypoint-bus@<id>`; a de-registered/disabled bus ⇒ `disable` (paired with the §6 `stop`). Boot then brings the bus back with the stack, unattended (the recovery `docs/on-hardware-report.md` row 8 proved at the process level, now at the OS level).

### 8. Test contract

CI enforces these as release-blocking properties, in the RFC-0001 / RFC-0003 §6 style (pure render + property-based + table-driven). They are written against the render/apply layer; the on-hardware coexistence rows are Prompt 18's.

1. **Render coordination (table-driven).** Over a table of `{local attachments on a bus} → {expected gateway deltas}`: a DMR attachment adds **exactly one** `[DMR Network N]` to `DMRGateway.ini` whose rewrites match the attachment's TG params, and removing it restores the file **byte-identically** to the no-bus render; a YSF (resp. NXDN) attachment removes the YSF/DGId (resp. NXDN) gateway target from the unit set and adds the bus unit, deterministically; disabling a bus **deletes** its rendered config file. (RFC-0001 properties 1–3 extended to the coordinated outputs.)
2. **Pure render under coordination.** For a randomized valid set of buses+attachments+networks, `RenderTargets` is byte-identical across repeated renders and unchanged by unrelated store edits — *including* the new cross-section reads (the DMR network entry and the dropped gateway targets are a pure function of the model).
3. **Port-collision impossibility (property).** Over randomized valid models, collect every rendered consumer's `127.0.0.1:port` (MMDVM-Host sections, every gateway, every bus loopback, every reserved-range allocation). Assert **no port is claimed by two consumers that are simultaneously in the rendered unit set** — a displacing bus and its gateway are never both present; a multiplex bus's reserved port is disjoint from all stock ports and from every other bus's allocation. This makes D3 a checked invariant, not a runtime discovery.
4. **Apply ordering (property/unit).** For a displacing attach/detach, the emitted apply plan **stops the displaced unit strictly before starting the displacing one** on the shared port (and the mirror on detach); for a multiplex attach, no such constraint is imposed. Asserted on the apply *plan* (the ordered stop/start/enable/disable list), so it needs no live processes.
5. **Cleanup + stop-state (unit).** Disabling a bus emits a plan that deletes its config file, `stop`s and `disable`s its unit, and the stop step is asserted to fire for a unit reported as `activating`/`failed`, not only `active` (D5).
6. **On-hardware coexistence (Prompt 18).** With the full live stack running: a DMR bus attaches and carries traffic while BrandMeister stays connected and RF DMR still keys (multiplex, nothing displaced); a YSF bus attaches, the YSF gateway stops, reflectors go away, the bus carries YSF, and detach restores the gateway — all through one apply each, no manual port surgery.

## Alternatives considered

- **DMR: displace DMRGateway like the CM bridge does (bind `62031`).** Rejected. It is the proven-in-the-field pattern (Appendix A.2) but it drops every upstream DMR network for the life of the attachment — the exact coexistence failure D3 is about. Multiplexing via `[DMR Network N]` reuses the machinery DMRGateway already runs for five networks and displaces nothing (§Design-1).
- **DMR: keep the current `62032` bind, just stop MMDVM-Host's DMR side.** Rejected. It trades one displacement (DMRGateway) for another (RF DMR) and still fails "keep the live stack running." Only multiplex satisfies the goal.
- **YSF/NXDN: give the bus a dedicated port and repoint MMDVM-Host at it (like DMR).** Rejected for v1 as pointless churn: with the gateway not running, the bus reuses the gateway's own loopback and MMDVM-Host needs no edit. A dedicated port buys nothing until the mode can *multiplex* (bus **and** reflectors at once), which YSF gets only via DG-ID (Open questions) and NXDN not at all today.
- **YSF: use DGIdGateway now, bus as one DG-ID route, reflectors on others (multiplex, not displace).** Deferred, not rejected — recorded as the YSF follow-up (Open questions 1). DGIdGateway genuinely multiplexes DG-ID slots (Appendix A.3), but wiring the bus as a slot is a larger change than v1 needs; displace ships the coexistence win for YSF now.
- **A dedicated reserved port per mode, hardcoded.** Rejected. Hardcoding re-introduces the fixed-port brittleness D3 came from and breaks with two buses. A rendered allocation from a fixed *range* keeps the render pure and scales to multiple buses (§Design-4).
- **Encode the mutual exclusion only in `Conflicts=`, not in the render.** Rejected as insufficient alone: `Conflicts=` stops the loser at the systemd layer, but the *render* must also not emit both consumers, or a byte-diff/health check would see a phantom target. The two work together (§Design-7) — render decides presence, `Conflicts=` enforces it at runtime.

## Open questions

1. **YSF multiplex via DG-ID slots.** DGIdGateway can route DG-IDs to different destinations (Appendix A.3). A follow-up could render the bus as one `[DGId=N]` route with reflectors on other DG-IDs, giving YSF coexistence *without* losing reflectors — the same win DMR gets from `[DMR Network N]`. Scope: a new gateway-selection path (bus forces DGId), DG-ID assignment policy, and UI. Deferred; v1 displaces.
2. **NXDN multiplex.** No stock NXDN multiplexer exists (Appendix A.3). Coexistence-with-reflectors for NXDN would need either an upstream NXDNGateway feature or a Waypoint-side fan — out of scope until there is field demand.
3. **DMR network protocol on the bus port.** `[DMR Network N]` dials a Homebrew/MMDVM master; the bus must accept that connection (login/auth). Whether the bus implements minimal Homebrew-master handling or DMRGateway grows a loopback/OpenBridge network type is an implementation choice for the follow-up PR, flagged so the reviewer weighs it.
4. **Reserved-range exhaustion.** `62100–62199` is 100 ports — far beyond any realistic bus count on one node, but the allocation function should fail loudly (not silently reuse) if it ever runs dry, and the range is documented so a future mode's ports do not land inside it.

---

## Appendix A — Ground truth (file/line citations)

Recorded before the Design was written, from the bench Pi's field-proven configs (`pi-star@172.16.50.13`, WPSD) and the waypoint renderers.

**A.1 — The MMDVM-Host ↔ DMRGateway link is `62031/62032`.** `MMDVM-Host.ini [DMR Network]`: `LocalAddress=127.0.0.1`, `LocalPort=62032`, `GatewayAddress=127.0.0.1`, `GatewayPort=62031` — MMDVM-Host **binds 62032**, sends to 62031. `DMRGateway.ini [General]`: `RptAddress=127.0.0.1`, `RptPort=62032`, `LocalAddress=127.0.0.1`, `LocalPort=62031` — DMRGateway **binds 62031**, sends to 62032. The two form the DMR loopback pair.

**A.2 — DMRGateway is a multiplexer; the CM bridge displaces it.** `DMRGateway.ini` carries `[DMR Network 1]`…`[DMR Network 5]`, each an outbound network `{Name, Address, Port=62031, Password, Id, Enabled, TGRewrite*/PCRewrite*/TypeRewrite*/SrcRewrite*/PassAllTG*/PassAllPC*}` (e.g. `[DMR Network 1] Name=BM_3102_United_States`, `Address=3102.master.brandmeister.network`, `Enabled=1`, `TGRewrite0=2,9,2,9,1`). The bridge `DMR2YSF.ini [DMR Network]` uses `RptAddress=127.0.0.1`, `RptPort=62032`, `LocalAddress=127.0.0.1`, `LocalPort=62031` — it **binds 62031**, i.e. takes DMRGateway's role and thus displaces it (matching the `dmr2ysf ↔ dmrgateway` 62031 `Conflicts=` in `docs/on-hardware-report.md`).

**A.3 — YSF/NXDN gateway multiplex capability.** `YSFGateway.ini` is single-network on `3200/4200` (one YSF/FCS reflector link). `DGIdGateway.ini` **multiplexes**: `[General] RptPort=3200 LocalPort=4200` (same MMDVM link) plus per-DG-ID routes `[DGId=0] Type=Gateway … Port=42025` / `[DGId=1] Type=Parrot … Port=42012` — DG-ID slots are independent routes, but it still occupies the single `3200/4200` pair (so it *replaces* YSFGateway, multiplexing internally). `NXDNGateway.ini` is single: `[General] RptPort=14021 LocalPort=14020` (MMDVM link) + one `[Network] Port=14050` upstream — no per-route multiplex.

**A.4 — The render already lets one field swap a gateway unit.** `internal/config/render.go:120–123` (`RenderTargets`): the YSF target is `YSFGateway` normally and swaps to `DGIdGateway` when `m.YSFGW.EnableDGId` — one store field changes which unit/renderer is present. Precedent for a bus attachment dropping the gateway target (§Design-0).

**A.5 — DMR bus binding + the DMRGateway renderer.** Bus DMR loopback = `{Bind:62032, Peer:62031}` (`internal/config/peering_render.go:150–158`, `busLoopbackFor`; mirrored in `cmd/waypoint-bus/endpoints.go`) — the bus binds `62032`, MMDVM-Host's own `LocalPort` (A.1), hence the collision. `RenderDMRGateway` (`internal/config/render.go:1289–1358`) iterates `m.Networks`, emitting one `[DMR Network n]` per network with `networkRewrites(net, m.Routes)` (`:1354`); this is where the bus network is appended (§Design-1).

**A.6 — Occupied loopback ports (for the reserved range).** From `internal/config/render.go` port constants (`:693–709`) and gateway renderers: `3200/4200` (YSF), `62030` (XLX), `62031/62032` (DMR), `32010/42020` (P25), `14020/14021/14050` (NXDN), `20010/20011` (D-Star), `17010/17011` (M17), `3800/4800` (POCSAG), `42000/42001/42012/42013/42025/42026` (YSF/DGId gateways), `42021` (NXDN parrot), `42500` (RFC-0016 peering), `13667`. `62100–62199` is clear (§Design-4).

**A.7 — Rendered outputs this addendum changes.** `RenderDMRGateway` (`render.go:1289`) — append the bus's `[DMR Network N]`. `RenderTargets` (`render.go:119`) — drop the YSF/DGId or NXDN gateway target when a displacing attachment exists; the reserved-range port allocation. `RenderMMDVM` (`render.go:419`) — **unchanged** in all three modes (DMR multiplex leaves it alone; YSF/NXDN displace reuses the mode's existing ports). The `waypoint-bus@.service` unit and enable/stop/disable apply steps land in waypoint-stack (D2/D5, §Design-6/7).
