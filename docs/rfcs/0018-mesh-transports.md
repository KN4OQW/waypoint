# RFC-0018: Mesh Transports and the Text/Position Plane

- Status: **proposed**
- Author: KN4OQW
- Comment window: **28 days from PR open** — deliberately doubled. This RFC asks
  questions only regional operators can answer (§Open questions 1–3), and the
  answers differ by continent. If you run Meshtastic, MeshCore, or LoRa APRS
  anywhere in the world, your input is requested even if you have never touched
  an MMDVM hotspot.
- Depends on: [RFC-0004](0004-event-history-store.md) (the event hub — text and
  position events ride the same seam voice events do),
  [RFC-0008](0008-status-pipeline.md) (the status pipeline and WebSocket the map
  and message views consume), [RFC-0001](0001-config-store.md) (store/render
  conventions for the new config sections)
- Relates to: [RFC-0003](0003-mode-buses.md) (mode buses — the "room" concept
  this borrows), [RFC-0016](0016-bus-lan-peering.md) (LAN peering — a future
  substrate for multi-node text, out of scope here)

## Summary

Waypoint gains a **text and position plane** alongside its voice plane, fed by
three **first-class LoRa transports** attached over USB:

1. **Meshtastic** — serial protobuf client to a companion-mode board
2. **MeshCore** — serial frame client to a companion-mode board
3. **LoRa APRS** — KISS over serial to a board running the CA2RXU TNC firmware

Plug a supported board (Heltec V3/V4, LILYGO, RAK, and kin) into the hotspot's
USB and Waypoint **autodetects which firmware it runs**, offers a
**per-transport configuration panel**, and bridges text messages and position
reports between the mesh side and the hotspot side — starting with DMR short
data (SMS), with positions rendered on a **local map tab**. Each transport is
independently enable-able; none is privileged over the others.

Two things this RFC deliberately does **not** do:

- **No firmware flashing.** Waypoint detects and configures; it never writes
  firmware to a board (§Alternatives).
- **No frequency defaults where none can be right.** The band question — 433 vs
  868 vs 915, and everything regional layered on top — is put to the community
  as this RFC's principal open question rather than decided by the author
  (§Open questions 1).

## Motivation

The hotspot is already the always-on, network-connected radio box in the shack.
Meanwhile, three LoRa ecosystems have grown up next to it — Meshtastic,
MeshCore, and LoRa APRS — each with its own hardware fleet, firmware, and
community, and each solving a piece of off-grid text and position reporting
that the digital-voice world handles poorly or not at all. Today, bridging any
of them to a hotspot means a second SBC, hand-rolled scripts, or nothing.

The boards are cheap ($25 Heltec-class), USB-attached, and speak documented (or
at least stable) serial protocols. The hotspot has a spare USB port. The gap is
purely software, and it is exactly the kind of integration the incumbents
avoid: it crosses ecosystem lines, it requires a runtime component rather than
a config template, and it demands care about regional band plans.

That last point is why this is an RFC and not an issue. **The author's own area
is contested ground:** part of the local community operates mesh on 915 MHz,
part on 433 MHz, and there is no coordinated plan. Other countries sit on 868,
or on national allocations that match none of the above. LoRa APRS centers on
433.775 MHz in much of the world, but not all of it. Meshtastic encodes region
presets; MeshCore and CA2RXU take explicit frequencies. Any default Waypoint
ships is wrong somewhere — possibly wrong *twenty miles from the author's
bench*. Rather than encode one locale's assumptions, this RFC states the
integration architecture (which is region-independent) and asks operators
worldwide to hash out the frequency/region UX (which is not).

### Why all three transports, first-class

The temptation is to pick a winner. It would be a mistake:

- **Meshtastic** has the largest installed base and the most mature serial
  admin surface — but its band usage is precisely what is contested locally.
- **MeshCore** is the lightweight newcomer with clean routing and growing
  adoption — a community Waypoint can meet early.
- **LoRa APRS** is the only one of the three that is *natively amateur*:
  callsign-addressed, plaintext by rule, Part 97-comfortable, and it feeds the
  position map without touching APRS-IS. In regions with thin coverage, a
  Waypoint node with a CA2RXU TNC board *is* the local infrastructure.

Three ecosystems, one abstraction: a transport that produces and consumes
`TextMessage` and `Position` events. The marginal cost of the second and third
transport behind that interface is small; the cost of guessing wrong about
which ecosystem a given region standardizes on is large. Waypoint's job is to
be a good citizen of whichever mesh its operator's community runs.

### What rides the plane

- **Text:** mesh DM/channel messages bridged to and from DMR short data (SMS)
  on the hotspot side, with a callsign registry mapping mesh node IDs to
  callsigns where the mesh protocol lacks them. Further modes (YSF GM) are
  future work gated on separate investigation.
- **Position:** mesh position packets and LoRa APRS position frames feed a
  runtime position store, rendered on a Leaflet map tab over the RFC-0008
  WebSocket. Display-only; nothing is persisted beyond a bounded ring, and
  nothing is uploaded anywhere (APRS-IS forwarding, if ever, is a separate,
  explicitly opt-in proposal).

### Detection and configuration, not flashing

All three ecosystems' common boards enumerate as generic USB-UART bridges
(CP2102 and similar), so the *port* tells you nothing. Waypoint identifies the
*firmware* by active probe — a Meshtastic `want_config` handshake, a MeshCore
device query, KISS framing — and presents the result with a manual override
for the cases probing cannot settle. Each transport then gets a configuration
panel scoped to what its protocol legitimately exposes over serial (region,
channels, owner identity on Meshtastic; the narrower equivalents elsewhere).

Firmware **flashing** was considered and rejected. The device matrix is the
trap: CA2RXU alone ships ~40 board-variant builds per release, each ecosystem
revises offsets and asset layouts on its own schedule, and every official
project already maintains a web flasher that does this well. A bricked board
mid-flash becomes Waypoint's bug regardless of whose offsets changed. Waypoint
instead tracks upstream releases *advisorily* — the Software page shows the
detected on-device version against the latest upstream tag, and links out to
the official flasher for that firmware. (All three projects publish GitHub
releases, so version checks are uniform and cheap.)

## Design

*(Decision-level; implementation detail follows in the runbook once the open
questions settle.)*

- **Transport interface:** each transport is a supervised runtime component
  producing/consuming `TextMessage` and `Position` events on the RFC-0004 hub.
  Store sections per transport under RFC-0001 conventions; enable/disable
  preserves data.
- **Autodetect:** udev-stable identification via `/dev/serial/by-id/`, active
  probe on attach, `firmware` field recorded per device with manual override.
  A probe never writes configuration — identification is read-only.
- **DMR short data:** BPTC(196,96) + ETSI short data codec in Go, validated
  against established open decoders as ground truth; bridged network-side (no
  MMDVM-Host changes).
- **Map:** Leaflet tab, positions over the existing WebSocket, bounded
  in-memory store.
- **Software page:** per-firmware latest-release polling (conditional requests;
  optional token), read-only.

## Alternatives considered

- **One blessed mesh ecosystem.** Rejected — see Motivation. Regional adoption
  differs; picking one imports one region's answer everywhere.
- **Firmware flashing from the UI.** Rejected — see Motivation. Detection and
  advisory version tracking capture most of the value at a fraction of the
  maintenance surface and none of the bricking liability.
- **APRS RF via the MMDVM modem.** Rejected — digital hotspot hardware cannot
  transmit 1200-baud AFSK; RF APRS requires an external FM radio and belongs to
  a different project. LoRa APRS via a dedicated board covers the intent.
- **Shipping regional frequency defaults.** Deferred to the community — that is
  this RFC's request for comment.

## Open questions — this is where your hat goes in the ring

1. **The band plan question (the big one).** What should Waypoint's
   frequency/region UX be, per transport, per region? Concretely: should
   Meshtastic configuration surface only the firmware's region presets, or also
   raw frequency-slot overrides? Should LoRa APRS default to 433.775 where that
   convention holds, and what governs where it does not? When a *local*
   community is split across bands (as the author's is, 915 vs 433), should
   Waypoint support multiple boards on different bands simultaneously as
   siblings — and if so, is that a common enough case to shape the UI around?
   **If your country or region has a working convention, document it in this
   thread.** The goal is a region table sourced from operators, not inferred
   from datasheets.
2. **Callsign registry policy.** Mesh node IDs are not callsigns. What
   verification, if any, should gate a mesh node's mapping to a callsign before
   its traffic is bridged toward RF modes?
3. **Bridging direction defaults.** Mesh→DMR, DMR→mesh, or bidirectional by
   default? Regional license conditions may constrain what should be on by
   default even where the architecture supports both.
4. **MeshCore's own KISS mode.** MeshCore firmware exposes a KISS modem
   feature. Is there operator interest in a MeshCore-as-TNC configuration, or
   is companion mode the only shape worth supporting in v1?
5. **Position privacy granularity.** The map is local-only by design. Should
   per-node position display be suppressible (a node opts out of the map while
   still bridging text)?
