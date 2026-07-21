# RFC-0016: Bus LAN Peering

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Depends on: RFC-0003 (mode buses — the reframe envelope and hub this extends), RFC-0002 (the claim/pairing pattern and write-only secret store), RFC-0001 (the configuration store and render-target conventions)
- Relates to: RFC-0012 (HTTPS-by-default — the device cert this deliberately does *not* reuse), RFC-0008 (the MQTT status pipeline — the plane media deliberately does *not* transit)
- Resolves: issue #65 open question 4 ("can buses span multiple nodes on a LAN?"), which predates the 0004–0015 allocations

## Summary

A **peered bus** is an RFC-0003 mode bus whose attachments live on more than one Waypoint node on the same LAN. One node **owns** the bus (its home node); other nodes **join** it over a dedicated, authenticated point-to-point link and contribute their local modes as if they were attachments on the home node. Voice reframed on any participating node is fanned to the others, subject to the same loop-prevention and single-source arbitration RFC-0003 already defines — now carried across the wire with an origin/hop-count envelope so a frame can never loop back to its origin or re-enter a bus it has already crossed.

This is a LAN feature only. It adds no WAN/Internet peering, no NAT traversal, no owner failover, and no per-user ACLs (§Design 6). It changes nothing about the reframe envelope — the AMBE+2 family, no vocoder — it only lets the hub's inputs and outputs sit on different boxes.

The transport decision (dedicated TCP over mTLS, not the MQTT broker) is backed by a latency spike measured on the bench pair, not estimated; the table is in §Design 1.

## Motivation

RFC-0003 put the whole bus on one node because that is where MMDVM-Host and the gateways are. But operators run more than one node — a DMR hotspot in the shack, a YSF hotspot in the garage, a spare on the bench — and the natural question (issue #65 q4) is "can these hear each other without a reflector round-trip to the Internet?" Today the only answer is to point both at the same upstream talkgroup and pay the WAN latency and the dependency on a third-party reflector staying up. For two boxes ten metres apart on the same switch, that is absurd.

Peering makes the bus a LAN object. The home node's bus gains remote attachments; a keyed-up transmission on the garage YSF node is reframed locally, streamed to the shack node over the LAN, and emitted on DMR there — never leaving the building, never touching a reflector. The cost is a new authenticated link on the media path and the loop-prevention that N nodes require; the motivation is that this is the single most-requested bus extension and the LAN case is genuinely simple if we refuse to let it sprawl into a WAN mesh (§6).

## Design

### 1. Transport — dedicated TCP framing over mTLS, not the broker

**Decision: media flows over a persistent, length-prefixed TCP connection per peer, secured with mutual TLS. The MQTT broker carries status and control only; voice never transits it.** This mirrors RFC-0008's finding for the single-node case (MMDVM-Host's voice plane is UDP, never MQTT) and extends it across the LAN.

The premise was tested, not assumed. A throwaway spike (`experiments/peerspike/`) measured round-trip latency and jitter for 20 ms-cadence 55-byte (DMRD-sized) frames between the bench pair — the Pi 3 (`pi-star@172.16.50.13`, running the full stack) as the echo/broker node and the session host (`172.16.50.24`) as the client, on the same LAN switch — over (a) a persistent TCP+TLS connection and (b) the node's own mosquitto at QoS 0 and QoS 1. RTT was measured client-side against a single monotonic clock (no cross-host time sync); **one-way = RTT/2**. Results (representative run per transport; TCP and QoS 0 each held across three runs of 500–1000 frames, QoS 1 across two):

| Transport | one-way mean | median | jitter (σ) | p95 | p99 | max | loss |
|---|---|---|---|---|---|---|---|
| ICMP baseline | 0.26 ms | — | — | — | — | — | 0% |
| **TCP + TLS** | **0.64 ms** | 0.59 ms | 0.25 ms | 1.1 ms | 1.7 ms | 2.6 ms | 0% |
| MQTT QoS 0 | 1.10 ms | 0.98 ms | 0.40 ms | 1.9 ms | 2.5 ms | 5.1 ms | 0% |
| MQTT QoS 1 | 16.6 ms | 20.1 ms | 6.4 ms | 30.3 ms | 30.6 ms | 44.6 ms | 0% |

(Client: `paho.mqtt.golang v1.5.1` — the library Waypoint already ships. Broker: mosquitto with a temporary LAN listener, reverted after; the full stack ran throughout.)

Two things the numbers settle:

- **QoS 1 is disqualifying on latency alone.** At ~16 ms mean / 20 ms median one-way it consumes the *entire* 20 ms frame budget, with 6 ms of jitter and a 30 ms tail. The per-message PUBACK handshake at each hop serialises against the 20 ms publish cadence — the client cannot fire the next frame's ack cycle before the next frame is due. QoS 1 media is not viable.
- **QoS 0 is honestly within margin** — ~1.1 ms one-way, ~0.5 ms above raw TCP for the broker hop, comfortably inside 20 ms with no measured loss on a quiet LAN. So the decision does **not** rest on QoS 0 latency; it rests on coupling and backpressure, which the spike cannot measure but which are structural:
  - **The broker is a single point of failure and a shared queue.** Every node's status telemetry already flows through it (RFC-0008); putting media there couples voice liveness to broker health and to unrelated status volume. A dedicated socket per peer fails independently and carries only that peer's media.
  - **No per-peer flow control or backpressure.** QoS 0 is fire-and-forget: a slow or wedged subscriber is invisible to the publisher, and there is no signal to drop-at-source vs. pile-up. A framed TCP connection gives the owner a real write error the instant a peer stalls (needed for §4's "owner offline ⇒ bus down" and §5's cable-pull behaviour).
  - **Fan-out and topic coupling.** Broker media would need per-bus topics, retained-message hazards (a retained voice frame is nonsense), and shared-subscription semantics to avoid every node seeing every frame. A point-to-point link makes the origin/hop envelope (§5) trivial and keeps the status topic tree clean.

Frame format on the wire: a 2-byte big-endian length prefix + the envelope (§5) + the mode's existing loopback frame bytes (the same DMRD/YSFD/NXDND the local hub already speaks). The connection is persistent per peer; TLS handshake cost is one-time and off the per-frame path (the spike measured steady-state framed latency, not handshakes).

### 2. Node count — N-node protocol, optional v1 validator cap

**Decision: the wire protocol and frame envelope are N-node from the first byte; there is no two-node assumption anywhere in the framing, the envelope, or the store shape.** The origin/hop-count envelope (§5) already generalises to any number of nodes, and `peers[]` is a list. A future third node needs no protocol bump.

The v1 **UI and validator MAY cap** a bus to two participating nodes if that simplifies the first release (fewer pairing screens to test, a simpler "who is online" indicator). That cap is a **validator policy**, not a framing limit — stated in one place (`ValidateBuses`-adjacent, like RFC-0003's mode-set rules) and liftable by changing a constant. The RFC is explicit so nobody later mistakes the v1 cap for a protocol constraint.

### 3. Pairing — explicit, mutual, fingerprinted; mDNS with manual fallback

**Decision: peering is established by explicit mutual pairing, on the RFC-0002 claim-pattern lineage — a short numeric code shown on *both* dashboards, entered on one, confirmed on the other.** No pure trust-on-first-use.

- **Discovery:** each node advertises `_waypoint._tcp` over mDNS; the pairing screen lists discovered nodes by name. A **manual `host:port` entry** is always available for networks where mDNS is filtered.
- **Handshake:** on pairing, each side generates (if absent) a peering keypair and they exchange certificates; the short code authenticates the exchange (a MITM cannot know the code shown on the other node's screen). Each side **pins** the other's certificate.
- **Fingerprint:** the pairing screen additionally displays the peer certificate's fingerprint, so an operator who wants out-of-band verification has it. The short code is the primary defence; the fingerprint is confirmatory, not TOFU.
- **Storage:** exchanged certificates and the node's own peering key are stored via the **write-only secret pattern** (RFC-0002) — never returned in an API response, preserved on a blank write.
- **Revocation:** either side can revoke a pairing, which deletes the pinned cert and drops the connection immediately. Revocation is unilateral and takes effect without the peer's cooperation (the peer simply fails the next mTLS handshake).
- **One node keypair (ratified).** A node has **exactly one** peering keypair — its identity — generated once and reused for every pairing; trust lives in the **pairing records** (the pinned peer certs), not in per-peer keys. Pairing exchanges certificates and pins them; it does **not** mint a fresh local key. Revoking a peer removes *that pairing's* trust and never rotates the node's key, so revoking one peer cannot disturb any other pairing. (Hardware validation ran on this model; the `internal/peering` handshake/manager suite pins to it. The per-peer-key road is recorded in Alternatives.)
- **Resetting identity.** Rotating the node key is a deliberate, whole-mesh act, on the RFC-0002 *reset-claim* lineage: `waypointd reset-peer-identity` regenerates the node's peering keypair and thereby **invalidates every existing pairing at once** (every peer's next handshake fails until re-paired). It is the "my node key may be compromised / I'm re-homing this box" escape hatch, distinct from per-peer revocation. Implementation lands in Prompt 15.

### 4. Token ownership — the home node owns the token; no failover

**Decision: the bus's home node (the node that owns the `Bus` row) owns the single talk token for the whole cluster. If the owner is offline, the bus is down.** No leader election, no failover, no token hand-off in v1. Predictable beats clever.

Arbitration is unchanged from RFC-0003 §5 — one source at a time — but the token lives only on the owner. A remote node's inbound voice is a request the owner grants or drops exactly as if it were a local attachment; the owner's `bus_busy`/voice events (RFC-0003) are the cluster's source of truth.

Failure modes, stated plainly:

- **Owner reboots mid-QSO:** every peer's TCP connection drops. Remote nodes observe a closed connection, release any local play-out, and mark the bus **offline** (a status event, not a crash). When the owner returns, peers reconnect and the bus resumes. The in-flight transmission is lost; no frame is queued for replay (voice, not file transfer — §5).
- **Split LAN (owner reachable by some peers, not others):** peers that can reach the owner keep working; peers that cannot mark the bus offline. There is no partition in which two owners exist, because there is exactly one owner by construction — the split degrades reachability, never forks the token.
- **A non-owner peer drops:** the owner sees that peer's connection close, stops fanning to it, and continues serving the rest. The dropped peer's local modes simply leave the bus until it reconnects.

Owner failover is an explicit non-goal (§6); an operator who needs the bus to survive the owner rebooting should home it on the node with the best uptime.

### 5. Frame envelope — origin, hop count, and a play-out deadline

Every peered frame carries, ahead of the mode bytes:

- **origin node id** — the node the frame first entered the cluster on;
- **origin attachment id** — the attachment (mode) it entered on;
- **hop count** — incremented on each peer link crossed.

Loop prevention across peers, extending RFC-0003 §5 rules 1 and 3:

- **A frame is never emitted back toward its origin node/attachment.** The fan-out at each node skips any link that would return the frame to where it started.
- **A frame never re-enters a bus it has already traversed.** The origin-node tag plus hop count make a cycle detectable and dropped; combined with RFC-0003's "a mode attaches to at most one bus," there is no topology in which a frame loops.
- **Hop count is also a hard ceiling:** a frame exceeding `max_hops` (default equal to the peer count, so a valid frame never approaches it) is dropped and counted — a belt-and-suspenders backstop against a mis-paired ring.

**Play-out deadline / jitter buffer.** Peered voice is play-out-scheduled at the receiving node with a small jitter buffer; late frames are **dropped, not queued** (this is voice). The defaults are derived from §1's measurements: the TCP transport tail is p99 ≈ 1.7 ms, max ≈ 2.6 ms, so the wire contributes ~2 ms of variation against a 20 ms cadence. The dominant delay is play-out choice, not the link. Defaults:

- **jitter buffer: 40 ms (2 frames)** — ~20× the measured transport tail, headroom for OS scheduling on a low-end node (a Pi Zero was not in the test pair; the Pi 3 was, so the buffer is deliberately conservative);
- **per-frame deadline: 60 ms** — a frame arriving more than 60 ms after its nominal play-out slot is dropped and counted, never played late or queued.

Both are single constants, tunable once field data on weaker nodes exists (§Open questions).

### 6. Scope fences (non-goals — verbatim from issue #65)

- **no WAN/Internet peering or NAT traversal** — LAN only; peers are reached by LAN address or mDNS, never a public endpoint or a relay;
- **no owner failover** — one owner, no election, no hand-off (§4);
- **no per-user ACLs beyond node-level mTLS trust** — a paired node is trusted wholesale; there is no per-callsign or per-talkgroup authorization layer;
- **reframe tier only** — RFC-0003's envelope (DMR/YSF/NXDN, no vocoder) is unchanged by peering; peering moves reframe-tier frames between nodes, it does not add a transcode tier.

### Store shape (RFC-0001 rows)

Two new sections plus a reference:

- `peers[]` — one row per paired node: `{ id, name, host, port, fingerprint, cert_ref, enabled }`. `cert_ref` names a write-only secret entry holding the pinned peer certificate (and, for the local keypair, the node's own peering key); the certificate bytes never appear in a view. Disabling a peer (`enabled=false`) drops the link and preserves the pairing, exactly like RFC-0003 disabling a bus.
- `remote_attachments[]` — one row per local mode joined to a peer's bus: `{ peer_id, remote_bus_id, mode, <the RFC-0003 §3 translation params for that mode> }`. This is the joining node's declaration; the mode's addressing/TG translation is applied on emit exactly as a local attachment's is.
- On the **home node**, the `Bus` gains a `peers[]` link (the paired peer ids allowed to join). A bus with no peer links renders exactly as today (RFC-0003); a bus with ≥1 peer link additionally renders the peer-facing listener.

A mode may still appear in at most one attachment (local or remote) across all buses (RFC-0003 §5 rule 3), which the validator enforces across both sections.

### What renders on each side

- **Home node:** its existing `waypoint-bus-<id>.json` (RFC-0003) gains a `peers` block — the listen address and the set of paired peer ids permitted to connect, with their pinned-cert refs. The daemon opens the mTLS listener in addition to the local loopback endpoints.
- **Joining node:** a remote-attachment config — the peer's `host:port`, the `remote_bus_id`, the local mode and its translation params, and the local mTLS client cert ref. The daemon dials the peer and presents the local mode as an attachment on the remote bus.

Both are pure functions of the store (RFC-0001 property 1); disabling a peer or a remote attachment yields a byte-identical render to before it was added, and re-enabling restores it (the RFC-0003 §6.2 property, extended).

### Security posture

Peering uses **mutual TLS with a keypair separate from RFC-0012's HTTPS device certificate.** They are not shared, for reasons of trust anchor and lifecycle:

- The **HTTPS cert** (RFC-0012) authenticates the node to *browsers*. It is user-facing, may be an ACME/Let's Encrypt cert or the self-signed device cert, and rotates on that plane's schedule. Anything that trusts it can reach the web UI.
- The **peering cert** authenticates the node to *other nodes* in a private, operator-established mesh. Its trust is pinned pairwise at pairing (§3), not derived from any public CA, and it grants access to the media plane — a strictly higher privilege than "render the dashboard."

Sharing one keypair across both would couple browser-facing rotation to the peer mesh (rotating the HTTPS cert would break every pairing) and would leak media-plane trust to anything that trusts the web cert. Separate keypairs, separate write-only store entries, separate rotation; revoking a pairing never touches HTTPS and vice versa. Both live under the RFC-0002 write-only secret rule.

## Test contract

CI-internal properties (pure/simulated, RFC-0003 §6 style):

1. **Envelope round-trip.** A frame constructed with `{origin node, origin attachment, hop count}` parses back to the same envelope; `max_hops` overflow is dropped, never panics.
2. **Cross-peer loop prevention (simulated).** In a simulated N-node hub, a frame is never emitted toward its origin node/attachment, never re-enters a bus it has traversed, and a deliberately mis-paired ring drops on hop-count ceiling — the RFC-0003 §6.4 properties extended across peers, driven by synthetic frames with no sockets.
3. **Pairing handshake + revocation (unit).** A pairing exchange over a mutual-TLS pair with a matching short code succeeds and pins both certs; a mismatched code fails closed; revocation on either side deletes the pinned cert and the next handshake fails. The peer certificate never appears in any view (grep the API surface, per the RFC-0002 pattern).
4. **Store/render equivalence.** `peers[]` + `remote_attachments[]` render deterministically; disabling then re-enabling a peer yields a byte-identical render; a bus with no peer links renders exactly as RFC-0003.
5. **Play-out deadline.** A frame arriving past its per-frame deadline is dropped and counted, never played late or queued (unit test over the jitter-buffer scheduler).

CI-external bench checks (recorded in `docs/on-hardware-report.md`, not gated in CI):

6. **Cable-pull mid-transmission (issue #65 acceptance 3).** With a peered bus keyed from the joining node, physically drop the link (unplug/`ip link set down`) mid-QSO: the owner releases the token, both nodes mark the bus offline, and **neither daemon crash-loops** — the connection loss is an ordinary event, not a fault. On reconnect the bus resumes.
7. **Latency budget.** Re-run the `experiments/peerspike` measurement (or its productionised equivalent) on the bench pair and confirm one-way TCP+TLS latency stays within the §5 jitter-buffer budget; record the table with the stack pins, as done for the spike in this RFC.

## Alternatives considered

- **Media over the MQTT broker (QoS 0 or QoS 1).** Rejected. QoS 1 is disqualifying on measured latency (§1: ~16 ms one-way, the whole frame budget). QoS 0 is within margin on latency but couples voice liveness to a shared broker with no per-peer backpressure and forces retained-message/topic-fan-out contortions. The road not taken keeps the broker as the status/control plane only, matching RFC-0008.
- **Two-node-only protocol, bumped later for N.** Rejected. Baking a two-node assumption into the frame envelope or wire format would force a protocol version bump to add a third node — exactly the kind of avoidable migration RFC-0001 exists to prevent. The envelope is N-node; only the *validator* may cap v1 (§2).
- **Trust-on-first-use pairing (accept the first cert seen).** Rejected. TOFU on a media plane that carries live audio between nodes is too weak; a rogue node on the LAN could pair silently. Explicit mutual short-code pairing (RFC-0002 lineage) plus a shown fingerprint costs one screen and closes that hole.
- **Leader election / owner failover.** Rejected for v1. Failover means consensus, split-brain handling, and token hand-off — a large surface for a feature whose failure mode ("home the bus on your most reliable node") is easy to state and easy to live with. Predictability now; failover is a future RFC if the field asks for it.
- **Reuse the RFC-0012 HTTPS certificate for peering.** Rejected. It couples browser-facing cert rotation to the peer mesh and leaks media-plane trust to any web-UI client. Separate keypairs, separate lifecycles (§Security posture).
- **A fresh keypair per pairing (per-peer keys).** Rejected in favour of one node keypair (§3). Per-peer keys would let a node present a different identity to each peer and re-mint on every re-pair, but they buy nothing the pinned-cert model lacks — trust already lives in the per-peer *pinned cert*, so revocation is already per-peer without per-peer *keys* — while costing a keystore of N private keys to manage, rotate, and leak, and a murkier "what is this node's identity" story. One key = one identity; the pairing record is the per-peer trust. (This was a live ambiguity during implementation; ratified to one node keypair in §3.)
- **Queue late frames instead of dropping them.** Rejected. Buffering voice past its play-out slot produces growing latency and robotic audio; dropping is correct for real-time media (§5).

## Open questions

1. **Jitter-buffer defaults on weak nodes.** The spike ran Pi 3 ↔ x86 host; a Pi Zero W as a peer was not measured. The 40 ms/60 ms defaults are deliberately conservative — field data may justify tightening (lower latency) or a per-node auto-tune. Non-blocking; the constants are tunable.
2. **mDNS on segmented/guest networks.** Some home routers isolate wireless clients (AP isolation) so mDNS and even direct LAN traffic are blocked between nodes. The manual `host:port` fallback covers reachability, but discovery UX on such networks is an open question (a "can't see the other node?" help path).
3. **Home-node move.** Changing which node owns a bus is a delete-and-recreate today. A first-class "transfer ownership" flow (re-home without re-pairing) may be worth it once peering is in the field — but it edges toward the failover machinery §4 deliberately avoids, so it is deferred.
4. **Time source for play-out.** Play-out is scheduled on the receiver's clock with a jitter buffer; whether cross-node NTP skew ever matters at LAN scale (sub-millisecond transport, 40 ms buffer) is untested. Expected to be a non-issue, flagged for the bench check (§Test contract 7).
