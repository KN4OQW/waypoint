# RFC-0003: Mode Buses

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Supersedes: the per-pair cross-mode bridge surface retired in `9f15099` (`internal/config/render.go` `RetiredBridgeUnits`, the dormant `ysf2dmr`/`dmr2ysf`/`ysf2nxdn`/`dmr2nxdn`/`nxdn2dmr` store sections)
- Depends on: RFC-0001 (the configuration store; buses are ordinary store sections and compiled render targets)

## Summary

A **bus** is a named, operator-created object (e.g. "Local Bus A") to which modes are **attached** (DMR → Bus A, YSF → Bus A). Voice entering the bus from one attached mode is converted and emitted to *every other* attached mode, with IDs, callsigns, and talkgroups translated to each destination's addressing. One bus replaces what used to be a hand-wired pile of pairwise `YSF2DMR`/`DMR2YSF`/… daemons: the operator names the endpoints and the plumbing is generated.

This RFC fixes the transport, the transcoding envelope (what may be attached together), the addressing/translation model, the store shape and its migration from the dormant bridge sections, the loop-prevention rules, and the test contract. It contains **no implementation** — the daemon internals are a follow-up.

## Motivation

The incumbent stacks (Pi-Star, WPSD) expose cross-mode transcoding as a grid of independent binaries from `juribeparada/MMDVM_CM` — one process, INI, and enable toggle per ordered pair. Waypoint mirrored that in its first cut (five `*2*` store sections, five renderers, five systemd units) and then retired it in `9f15099`, because the per-pair model has three defects the operator pays for:

1. **It is pairwise, not a topology.** An operator who wants DMR, YSF, and NXDN to hear each other must know to enable three separate bridges in the right directions. The mental model the operator actually holds — "these three talk to each other" — has no object in the system.
2. **The bridges collide on the single fixed loopback each mode has.** MMDVM-Host talks to each mode's gateway over one fixed port pair (`internal/config/render.go:434-452`: YSF `3200/4200`, P25 `32010/42020`, NXDN `14021/14020`, D-Star `20011/20010`, M17 `17011/17010`; DMR `62031/62032`). A bridge that terminates that pair is the *sole* consumer of it. Two bridges touching the same mode cannot both own its loopback — so the pairwise model does not compose into a hub even in principle.
3. **Duplicated secrets.** `YSF2DMR` and `NXDN2DMR` each carried their own DMR-master `Address`+`Password` (`internal/config/model.go:72-79,114-122`), duplicating credentials Waypoint already models once as `Networks[]` behind the local DMRGateway.

The bus is the missing object. It gives the operator the topology directly, owns each attached mode's loopback exactly once, and sources credentials from the existing model instead of re-entering them.

## Design

### 1. Transport & topology

**Decision: a single Waypoint-owned bus daemon per bus (`waypoint-bus@<id>.service`, a templated unit) that terminates each attached mode's loopback endpoint and hubs frames between them.** It sits where the CM bridges sat — on the g4klx loopback — but as a hub, not a leg.

The three candidates and why this one:

- **(a) Waypoint bus daemon on the loopback (chosen).** The daemon binds each attached mode's fixed loopback pair (the ports in `render.go:434-452`) and, per frame, reframes/transcodes and re-emits to the other attachments. Only this shape can hub N modes: the bus is the single consumer of each mode's one loopback endpoint, which is exactly the ownership the pairwise bridges fought over. It **reuses upstream engine code** rather than reimplementing DSP — the AMBE+2 reframe path is packet surgery (lift 49-bit AMBE+2 frames from the source superframe, repack into the destination's), and the cross-codec path links the same software vocoders MMDVM_CM uses (`md380_vocoder`, `imbe_vocoder`, `mbelib`; see §2). This honours the architecture rule (`docs/architecture.md`): add the layer upstream doesn't provide, don't fork the protocol implementations.

- **(b) MQTT-native router — rejected as infeasible for voice.** The pinned stack is MQTT-era, so this was worth checking against the code, not assumed. It fails: MMDVM-Host's MQTT plane carries only telemetry. The only `publish()` call sites are `log`, `json`, and `display-out` (`MMDVMHost/Log.cpp:86,104`; `MMDVMHost/MMDVM-Host.cpp:1227,3108` — `writeJSONMode`/`writeJSONMessage`). The voice data plane leaves over UDP: `CDMRNetwork::write` → `m_socket.write(...)` on a `CUDPSocket` (`MMDVMHost/DMRNetwork.cpp:356-364`), `CYSFNetwork` likewise (`MMDVMHost/YSFNetwork.cpp:104`). There is no AMBE/voice payload on any MQTT topic, so an MQTT router has nothing to route. MQTT remains the bus's *status* plane (§6, dashboard), never its media plane.

- **(c) Orchestrate existing MMDVM_CM binaries as pairwise legs — rejected as the architecture, adopted as the engine.** Running the stock binaries would mean an N-mode bus is N(N-1)/2 processes, each wanting exclusive ownership of a mode's single loopback pair — so three legs on a {DMR,YSF,NXDN} bus collide on the YSF and DMR loopbacks and cannot coexist (defect 2 above). The UX would also have to paper over process fan-out. We therefore reject the pairwise *topology* but keep faith with its *code*: the bus daemon is the MMDVM_CM reframing/vocoder logic re-hosted as a hub. A **2-attachment bus is the degenerate case** — one source, one destination, exactly one MMDVM_CM-equivalent leg — which is why migration from a saved bridge is lossless (§4).

Accepted cost, stated plainly: the bus daemon is new code on the media path, where the CM bridges were upstream code. We contain the risk by (i) reusing the upstream frame/vocoder routines verbatim, (ii) shipping only the reframe envelope first (§2), and (iii) a pure-render + simulated-traffic test contract (§6).

### 2. Transcoding reality — what may be attached together

Cross-checked against `juribeparada/MMDVM_CM` (the directory listing and the per-tool INIs/READMEs), not folklore. Two findings correct the received wisdom:

- **The AMBE+2 family is a re-frame, with no vocoder at all.** DMR, YSF **DN** (V/D mode 2, half-rate), and NXDN all carry AMBE+2 2450×1150. The corresponding tools have **no `[AMBE]`/vocoder section** in their INI — confirmed in `DMR2YSF/DMR2YSF.ini`, `NXDN2DMR/NXDN2DMR.ini`, and `YSF2DMR/YSF2DMR.ini`. Conversion is packet reframing; it costs no DSP and loses no audio.
- **Cross-codec conversion is done in *software*, not on a hardware dongle.** The prompt's premise ("without a hardware vocoder") is out of date for this fork. `DMR2P25/README.md`: *"performs software transcoding between IMBE 4400x2800(P25) and AMBE+2 2450x1150(DMR)"* via `imbe_vocoder` + `md380_vocoder`. `M172DMR/README.md`: *"performs software transcoding between ICodec2(M17) and AMBE+2 2450x1150(DMR)"* and notes `md380_vocoder` *"uses md380 firmware for vocoding, so this software needs to be run on an ARM based platform i.e. raspberri pi."* The repo README describes the fork as using *"md380 firmware to encode/decode AMBE+2 2450x1150 used by DMR/YSF/NXDN."* So a Pi **can** transcode DMR↔P25↔M17↔D-Star in software — at CPU cost, with double-transcode audio degradation, and using extracted proprietary firmware whose redistribution is a licensing question Waypoint must not paper over.

This yields two **attachment tiers**, and the UI validates at *attach time*, never at runtime:

| Tier | Codec relationship | Modes | Vocoder | Status |
|---|---|---|---|---|
| **Reframe** | shared AMBE+2 2450×1150 | DMR, YSF (DN), NXDN | none | **committed scope** |
| **Transcode** | distinct codecs | + D-Star (AMBE, older), M17 (Codec2), P25 (IMBE) | a licensed vocoder (§ hard constraint) | **deferred** to a later dedicated vocoder/encryption effort |

**Hard constraint — Waypoint distributes no extracted firmware.** The software-transcode path in MMDVM_CM works by loading a vocoder blob **extracted from TYT MD-380 radio firmware** (`md380_vocoder`), which is copyrighted and cannot be redistributed regardless of the underlying AMBE+2/IMBE patent status. Waypoint will **not** embed, bundle, build, or ship that blob. This is not an open question or a UI-consent checkbox — it is a line the project does not cross. Consequently the transcode tier, *if and when it ships*, may only obtain a vocoder by (a) a DVSI-licensed hardware device the operator owns (a ThumbDV/DVstick/AMBEserver — licensed silicon, no copyright question), or (b) a blob the operator supplies themselves, which Waypoint never carries. The whole transcode tier — modes, addressing depth, and the vocoder-sourcing decision above — is **deferred to a later dedicated effort** (planned alongside the encryption work), and is a non-goal for this RFC beyond reserving the model shape (§4) for it.

Rules the attach-time validator enforces (table-driven, §6):

1. **Committed scope is the reframe tier only.** A bus accepts any subset of {DMR, YSF, NXDN}. This is exactly what the five dormant bridge sections covered, so it ships with **no vocoder dependency, no firmware, no licensing question, and no per-frame CPU budget**. The reframe tier is self-sufficient: it is not blocked on, and does not depend on, the deferred vocoder effort.
2. **A converter path must exist.** Attachment is legal only if, for every pair of attached modes, a conversion path exists — a reframe (shared codec) today, or a licensed transcode later. MMDVM_CM's 16-tool inventory (`DMR2M17`,`DMR2NXDN`,`DMR2P25`,`DMR2YSF`,`DSTAR2YSF`,`M172DMR`,`M172YSF`,`NXDN2DMR`,`P252DMR`,`USRP2*`,`YSF2DMR`,`YSF2NXDN`,`YSF2P25`) bounds what is ever expressible: D-Star, notably, has only `DSTAR2YSF`, so D-Star could share a bus only with YSF, never directly with DMR/NXDN/P25/M17. The validator refuses any combination outside the committed reframe tier until the deferred effort lands; it does not fail at runtime.
3. **YSF attaches as DN.** VW (full-rate) frames are out of the reframe envelope; the bus handles the DN path the AMBE+2 family shares. YSF VW is an open question (§ Open questions), not a silent drop.

The UI presents attach as a picker that greys out any mode which would make the bus invalid given what is already attached, with the reason shown ("no converter for D-Star↔DMR"; "transcode tier disabled"). Invalid buses cannot be saved, so they cannot render an unstartable unit.

### 3. Addressing & translation

Translation is **per attachment**, because each attachment is one edge of the hub and needs its own view of the addressing.

- **DMR attachment.** Rides the **local DMRGateway loopback `62031/62032`** (as `DMR2YSF/DMR2YSF.ini` does: `[DMR Network] RptPort=62032 LocalPort=62031`), *not* its own upstream master. Translation params: `slot` (1|2), a `default_tg`/startup target (`[DMR Network] DefaultDstTG`/`StartupDstId` in the CM INIs), and an optional TG map (source-mode target → DMR TG). DMR ID ↔ callsign resolution reuses the station's `DMRIds.dat` lookup already wired for every gateway (`render.go` `DMR Id Lookup`/`Id Lookup` sections) — the bus adds no second lookup file.
- **YSF attachment.** DN mode; translation params: the reflector/DG-ID target and Wires-X passthrough posture, mirroring the `[YSF Network]` keys the CM tools expose. Callsign is carried natively (YSF is callsign-addressed); the DMR side resolves the callsign→ID via the shared lookup.
- **NXDN attachment.** Params: `[NXDN Network] Id`/`TG`/`DefaultID` (as `NXDN2DMR.ini` shows). NXDN shares the DMR ID space, so ID resolution reuses `DMRIds.dat` (matching `render.go`'s NXDN `Id Lookup` pointing at `DMRIds.dat`).

**Credentials: reuse the existing network secrets; a bus holds none of its own.** This is the decisive departure from the fat-bridge model. Because a DMR attachment rides the local DMRGateway, upstream authentication is already handled once by `Networks[]` (BrandMeister/TGIF/… `Password`, `internal/config/model.go` `Network`) and multiplexed by DMRGateway's generated routing (`render.go` `RenderDMRGateway`). The bus reaches a specific upstream by targeting the talkgroup/network the existing `DMRRoute`/`Network` machinery already routes — it never opens its own master and never stores a password. The dormant `YSF2DMR.Password`/`NXDN2DMR.Password` secrets therefore migrate by **pointing the bus's DMR attachment at the existing DMR network of that master** (§4), retiring the duplicate-secret smell (defect 3). If no matching network exists, migration flags it for the operator rather than silently minting a credential.

### 4. Model shape

Two new store sections (RFC-0001 rows; disabling a bus flips a bool, deletes nothing):

- `buses[]` — `{ id, name, enabled }`.
- `attachments[]` — `{ bus_id, mode, translation params (per §3), credentials_ref }`, where `credentials_ref` names an existing `Networks[]` entry (or gateway) rather than embedding a secret. A mode may appear in **at most one** attachment across all buses (enforced at write; see §5).

`RenderTargets` (`internal/config/render.go:85`) grows **one target per enabled *bus*** — `waypoint-bus@<id>.service` + its generated config — not one per leg. This keeps the hub as one unit (one supervised process, one restart on change) and keeps the render registry's "one mode/object contributes one entry" shape (issue #21 gateway-plugin seam). A bus with N attachments is still one target; the N endpoints are rows inside that target's rendered config. The renderer stays a pure function of the model (RFC-0001 property 1), so the same buses/attachments render byte-identically.

**Migration from the dormant bridge sections.** The five retired sections carry a complete, previously-working attachment pair each. A saved `YSF2DMR` (`internal/config/model.go:72-79`) seeds: one `Bus` (`enabled = YSF2DMR.Enable`), a **YSF attachment** (target TG → the DMR side's `StartupDstId`), and a **DMR attachment** whose `credentials_ref` is the `Networks[]` entry matching `YSF2DMR.Master` (or a flag if none). `DMR2YSF`, `YSF2NXDN`, `DMR2NXDN`, `NXDN2DMR` migrate the same way; the two that shared a DMR master (`YSF2DMR`, `NXDN2DMR`) may fold into the same bus if the operator wants DMR/YSF/NXDN to interoperate. Migration is one-way (sections stay dormant, not deleted, per RFC-0001's disable-preserves-data rule) and is covered by a render-equivalence property (§6.5).

### 5. Loop prevention & arbitration

A hub that re-emits every inbound frame to every other attachment is a feedback loop by construction. Four rules make it safe; all are pure functions of frame origin + bus state and are exercised by the simulated-traffic test (§6.4):

1. **Never emit to the source.** Each frame the bus reframes/transcodes carries an origin tag = the attachment it entered on; the fan-out iterates *other* attachments only. A frame is never re-emitted to the mode it came from.
2. **One active source per bus (arbitration).** The first attachment to key up acquires the bus's single source token; inbound voice from any other attachment is dropped while the token is held. The token releases after a hang-time of silence (the same hang the CM tools expose). This makes the bus half-duplex per transmission — correct for voice, and the only way to avoid two sources talking over each other into the same destinations.
3. **A mode attaches to at most one bus.** Enforced at write (§4). This structurally prevents cross-bus ping-pong: a frame cannot leave bus A on mode X and re-enter bus B, because X belongs to exactly one bus. The store rejects a second attachment of the same mode as a caller error (like `SetSection`'s unknown-field rejection, `model.go:533`).
4. **No self-loop through the local gateway.** Because a DMR attachment rides the shared local DMRGateway loopback, a bus-originated frame emitted to DMR must not be re-ingested by the bus as if it were RF/network traffic. The origin tag (rule 1) plus the source token (rule 2) cover this: while the bus holds the token for source X, it ignores the echo of what it just transmitted to DMR.

### 6. Test contract

CI enforces these as release-blocking properties, in the RFC-0001 style (pure render + property-based + table-driven):

1. **Pure render.** For a randomized valid set of buses+attachments, `RenderTargets` produces byte-identical output across repeated renders and is unchanged by unrelated store edits (RFC-0001 properties 1–2 extended to the two new sections). Each enabled bus contributes exactly one target/unit; a disabled bus contributes none and deletes no rows.
2. **Round-trip.** Model → rendered bus config → parse (with the daemon's own reader where feasible) → no semantic loss of any translation param; disable/re-enable a bus yields a byte-identical section (RFC-0001 property 3).
3. **Attachment-validity matrix (table-driven).** A table over `{set of attached modes} → {valid | invalid, reason}` asserts: reframe-tier subsets of {DMR,YSF,NXDN} are valid; any pair lacking an MMDVM_CM converter is invalid with the right reason (D-Star with anything but YSF; transcode-tier combos when that tier is disabled); a mode already attached elsewhere is invalid. The validator refuses to save invalid buses — so this is tested at the model boundary, never as a runtime failure.
4. **Loop prevention (simulated traffic).** A unit test drives synthetic frames into a hub model and asserts §5: (a) a frame is never emitted to its origin attachment; (b) with two simultaneous sources, exactly one holds the token and the other's frames are dropped until hang-time; (c) an echo of a bus-emitted frame on the shared DMR loopback does not re-enter the fan-out; (d) no configuration permits a frame to traverse two buses.
5. **Migration equivalence.** Every dormant bridge section, seeded into buses/attachments, renders a bus config semantically equivalent to what the retired bridge renderer would have produced (same endpoints, ports, targets, translation) — and seeds no credential the source section didn't carry.

## Alternatives considered

- **Keep the pairwise bridges, add a UI grouping.** Rejected: it papers a topology over N(N-1)/2 processes that collide on the single loopback per mode (§1c, §Motivation-2). The collision is physical, not cosmetic.
- **Ship software transcoding via `md380_vocoder` (extracted MD-380 firmware).** Rejected outright, and not as a cost trade-off: it would mean Waypoint redistributing a copyrighted firmware blob (§2 hard constraint). No CPU or UX argument reaches that question — the project does not ship extracted firmware. A future transcode tier sources its vocoder from licensed hardware or an operator-supplied blob only.
- **Ship the transcode tier from day one.** Rejected: beyond the firmware constraint above, it drags in a real per-frame CPU budget on a Pi Zero and audible double-transcode loss. The reframe tier covers exactly the dormant bridges' scope with none of that; the transcode tier is deferred to a later dedicated vocoder/encryption effort.
- **Let a bus open its own DMR master (the fat-bridge model).** Rejected: it duplicates credentials Waypoint already models as `Networks[]` and re-solves upstream routing that DMRGateway already generates. Riding the local gateway loopback reuses both (§3).
- **One systemd unit per leg.** Rejected: a bus is one object; one unit per bus keeps supervision, restart, and the render registry aligned with the operator's mental model (§4).

## Open questions

1. **YSF VW.** The reframe envelope is DN-only. Do we transcode VW (full-rate) into the AMBE+2 family, refuse it at attach, or pass it through untranslated to a YSF-only sub-fan? Leaning refuse-at-attach until the transcode tier lands.
2. **Transcode tier (deferred).** Settled as constraints, not open: Waypoint distributes no extracted firmware (§2), so the tier — when it lands as part of the later vocoder/encryption effort — sources its vocoder from a DVSI-licensed hardware device or an operator-supplied blob only. What remains genuinely open is that effort's *shape*: hardware-dongle path vs. operator-supplied-blob path (or both), and how the vocoder integrates with the encryption handling it will ship beside.
3. **Multi-source policy beyond first-wins.** §5's single-token arbitration is the safe default; a future priority/pre-emption policy (e.g. RF beats network) may be worth a param once buses are in field use.
4. **P25/M17/D-Star talkgroup translation depth.** The reframe tier's TG maps are well-defined; the transcode tier will need per-mode target semantics (P25 TG, M17 reflector+module, D-Star reflector) fleshed out when it lands.
