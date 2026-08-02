# Bench run log — issue #33 closeout: DMR hang timers + DAPNETGateway

Hardware validation of the last two items on [#33]: the DMR call-hold and
hang-timer overrides ([#193]) and the newly packaged DAPNETGateway
(KN4OQW/waypoint-stack#15).

Both had only ever been exercised against unit tests and a build container. The
hang timers are interesting on hardware because the *host* reduces oversized
values and Waypoint deliberately does not, so the only way to see the real
outcome is to read MMDVM-Host's own parameter block. DAPNETGateway had never run
here at all — the unit file has shipped in the image since July with no binary
behind it.

Three runs, appended in order:

| Date | Covers | Outcome |
|---|---|---|
| 2026-07-29 | DMR hang timers; DAPNETGateway tier 1 (no credential) | Both pass; DAPNET stops at the AuthKey guard (finding 1) |
| 2026-08-02 | DAPNET tier 2, with a real operator AuthKey ([#196]) | Authenticated connect confirmed |
| 2026-08-02 | Delivery — does *apt* carry the daemon, not `dpkg -i` | Confirmed, after fixing two updater defects ([#220], [#221]) |

## The bench

| | |
|---|---|
| Board | Raspberry Pi 3, armv7l, Raspbian 12 bookworm, kernel 6.12.25+rpt-rpi-v7 |
| Host | `KN4OQW` @ 172.16.50.13, reached as `rescue` over `eth0` |
| Modem | MMDVM_HS_Dual_Hat on `/dev/ttyAMA0`, KN4OQW firmware fork |
| Store | `/home/pi-star/waypoint/config.db` (dev-box path, not the production one) |
| Backup | `sqlite3 … ".backup /root/config.db.issue33-20260729-175830"`, `pragma integrity_check` → `ok`. Never a bare `cp` — the store runs in WAL |
| `waypointd` | built from `feat/dmr-hang-timers` @ `43fc04a`, `GOOS=linux GOARCH=arm GOARM=6 CGO_ENABLED=0`; previous binary kept at `bin/waypointd.bak-issue33` |
| Stack | `waypoint-dapnetgateway 0~git5527546+wp1` (armhf), the **CI-built** .deb from waypoint-stack run 30497462651, not a local cross-build |

Run against the two branches rather than post-merge `main`: the code is identical
to what merges, and the bench was free.

`DisplayLevel=0` in the rendered host ini, so the journal carries only MQTT
chatter. Every parameter block below was read by copying the rendered ini,
raising `DisplayLevel` to 2 in the copy, and running the daemon in the foreground
with the unit stopped — the same technique that diagnosed the earlier
crash-loop.

## Results

| # | Check | Result | Notes |
|---|---|---|---|
| 1 | `waypoint-dapnetgateway.service` present, `/usr/bin/DAPNETGateway` absent | **CONFIRMED** | The gap #33 was tracking, reproduced before the fix |
| 2 | .deb installs, binary runs | **PASS** | `DAPNETGateway version 20260214 git #` |
| 3 | Four hang keys render into the right sections | **PASS** | See §1 |
| 4 | Partial `PUT` merges | **PASS** | A body naming only the timers left `ColorCode=2` intact |
| 5 | Host reports the set values | **PASS** | See §1 |
| 6 | Clamp chain, all three branches | **PASS** | See §2 — including one the runbook did not anticipate |
| 7 | Blanking removes the keys, `[General]` resumes | **PASS** | See §3 — the result that justifies the blank-omit design |
| 8 | POCSAG enable reaches the host | **PASS** | `POCSAG: enabled`, network connection opened |
| 9 | DAPNETGateway loads its ini and connects MQTT | **PASS** | See §4 |
| 10 | DAPNET connection attempted | **BLOCKED — tier 1** | Upstream refuses to try without an AuthKey; see §4 and finding 1 |
| 11 | Every other mode daemon still `NRestarts=0` | **PASS** | See §5 |

## 1. The keys render, and the host reads them

Baseline: no hang keys in either section, `[General] RFModeHang=300`,
`NetModeHang=300`.

`PUT /api/config/dmr {"call_hang":"8","tx_hang":"6","mode_hang":"30"}` and
`PUT /api/config/dmrnet {"mode_hang":"25"}`, then apply → both 204, apply 200,
and `waypoint-mmdvm` in the restart set. Rendered:

```ini
[DMR]                        [DMR Network]
Enable=1                     Enable=1
ColorCode=2                  LocalAddress=127.0.0.1
Id=3180202                   LocalPort=62032
SelfOnly=0                   GatewayAddress=127.0.0.1
EmbeddedLCOnly=0             GatewayPort=62031
DumpTAData=0                 Jitter=360
Beacons=0                    Slot1=0
CallHang=8                   Slot2=1
TXHang=6                     ModeHang=25
ModeHang=30
```

Exactly four keys, each in its own section, appended after the existing ones with
the prior order untouched. `ColorCode=2` survived a PUT that never mentioned it,
so the section merge holds.

## 2. The clamp chain, on real hardware

The host clamps `TXHang` down to the RF mode hang, then (network enabled) down to
the net mode hang, then `CallHang` down to `TXHang`. All three legs fired:

| `CallHang` / `TXHang` / RF `ModeHang` / net `ModeHang` | Host logged | Which clamp |
|---|---|---|
| 8 / 6 / 30 / 25 | Call Hang **6s**, TX Hang 6s, Mode Hang 30s, Net Mode Hang 25s | `CallHang` → `TXHang` |
| 4 / 6 / 30 / 25 | Call Hang **4s**, TX Hang 6s, Mode Hang 30s, Net Mode Hang 25s | none — passes through |
| 35 / 40 / 30 / 25 | Call Hang **25s**, TX Hang **25s**, Mode Hang 30s, Net Mode Hang 25s | `TXHang` → net mode hang (25, the smaller of 30/25), then `CallHang` → the clamped `TXHang` |

Worth recording: **the runbook's own prescribed values already exercise the
clamp.** `CallHang=8` with `TXHang=6` is an oversized call hold, so the "set the
four values and read them back" check and the "now exercise the clamp" check are
the same check. The second row above was added to see the unclamped path, and the
third to reach the mode-hang leg.

In every case the rendered ini carries what the operator typed
(`CallHang=35`, `TXHang=40`) while the host logs what it used (`25s`, `25s`).
That is the intent: pre-clamping in the renderer would hide the reduction from
the operator, whereas this way the host's own log is the record.

## 3. Blanking gives `[General]` back — and shows why blank-omit matters

Blanking all four (`{"call_hang":"","tx_hang":"","mode_hang":""}` +
`{"mode_hang":""}`) and applying removed every one of the four keys from the
rendered ini — no empty `CallHang=` left behind — and the host reverted to:

```
DMR Network Parameters   Mode Hang: 300s
DMR RF Parameters        Call Hang: 4s   TX Hang: 4s   Mode Hang: 300s
```

300s is the operator's `[General]` value, arriving through the fan-out.
`TX Hang: 4s` is MMDVM-Host's own default and `Call Hang` is its default 10
clamped down to it.

This is the whole argument for omit-when-blank, measured. Had the renderer
emitted `ModeHang` with a `def(…, "10")` fallback, this node's DMR mode hang
would have dropped from the configured **300s to 10s** the moment the feature
merged, on every existing config, with nothing in the UI changed to explain it.

## 4. DAPNETGateway: tier 1 reached, tier 2 blocked upstream

Before: `/etc/systemd/system/waypoint-dapnetgateway.service` dated 26 July,
`/usr/bin/DAPNETGateway` → *No such file or directory*. The gap, exactly as #33
described it.

After installing the CI armhf .deb, enabling POCSAG and applying,
`waypoint-dapnetgateway.service` appeared in the apply's restart set alongside the
seven others, and the host came up with `POCSAG: enabled`, a `POCSAG Network
Parameters` block, and `Opening POCSAG network connection`.

The gateway itself gets as far as its credentials:

```
DAPNETGateway (dapnet-gateway) connecting to MQTT as DAPNETGateway.15750
M: Opening POCSAG network connection
Opening UDP port on 4800
E: AuthKey not set or invalid
MQTT: on_connect: Connection Accepted.
```

So the packaged binary runs, parses the rendered `DAPNETGateway.ini`, opens the
POCSAG loopback on the port the renderer wrote (4800), and its MQTT connection is
accepted by the broker — every part of the path Waypoint owns. It then stops at
the AuthKey and exits 1 **without attempting DAPNET at all**:

> `DAPNETGateway.cpp:283` — `if (dapnetAuthKey.length() == 0 || dapnetAuthKey ==
> "TOPSECRET") { LogError("AuthKey not set or invalid"); return 1; }`

That guard sits before `new CDAPNETNetwork(...)`, so "attempts its DAPNET
connection" is not reachable without a real key — tier 1 as the runbook defined
it is upstream-bounded one step earlier than expected. **Tier reached: 1.** No
DAPNET account was available; tier 2 (authenticated connect, test page) is
untested.

### Finding 1 — enabling POCSAG without an AuthKey crash-loops the unit

`Restart=on-failure`, `RestartSec=3`, and an unconditional `return 1` combine
into a restart loop that does not converge: the counter passed 300 while POCSAG
was left enabled on this box. Nothing else is harmed — the other eight units are
untouched, and the loop is idle CPU — but it is a bad first experience for an
operator who turns POCSAG on before signing up for DAPNET, and the reason is
invisible from the UI (the journal only shows the exit, and the rendered ini has
`DisplayLevel=0`).

Waypoint's config surface permits it: `pocsag.auth_key` has no
required-when-enabled rule, so a POCSAG enable with `has_auth_key: false` renders
`AuthKey=` and starts a daemon guaranteed to fail. Worth a follow-up — either
gate the unit on a non-empty AuthKey (the same enable-gating pattern the render
targets already use), or surface "DAPNET AuthKey required" on the POCSAG tab.
Not a defect in the packaging, and not a reason to hold #33.

**Left enabled deliberately** at Clint's request, so the box is in this state now:
POCSAG on, AuthKey blank, `waypoint-dapnetgateway` restarting every 3s.

## 5. Nothing else moved

Pins-and-packaging changes are exactly the kind that perturb neighbours, so every
unit was checked after the run:

```
waypointd               active   NRestarts=0
waypoint-mmdvm          active   NRestarts=0
waypoint-dmrgateway     active   NRestarts=0
waypoint-ysfgateway     active   NRestarts=0
waypoint-p25gateway     active   NRestarts=0
waypoint-nxdngateway    active   NRestarts=0
waypoint-dstargateway   active   NRestarts=0
waypoint-m17gateway     active   NRestarts=0
waypoint-dapnetgateway  activating  NRestarts=300   (finding 1, deliberate)
```

Eight of nine clean. The new .deb installs alongside the others without touching
them; `waypoint-stack`'s CI install-test had already proved the same on all three
architectures.

## Bench state left behind

- `waypointd` = the `feat/dmr-hang-timers` build; previous binary at
  `/home/pi-star/waypoint/bin/waypointd.bak-issue33`.
- `waypoint-dapnetgateway 0~git5527546+wp1` installed.
- DMR hang timers **blank** (the `[General]`-governed default).
- POCSAG **enabled** with a blank AuthKey — see finding 1.
- Store backup at `/root/config.db.issue33-20260729-175830`.
- `sqlite3` was installed to take that backup (it was absent post-reimage).

---

# Tier 2 — DAPNET with a real AuthKey, 2026-08-02

[#196] tracked the one thing tier 1 could not reach: an authenticated DAPNET
session. The operator's credential arrived and the remaining checks were run.

**The bench is not the machine described above.** It was reimaged between the two
runs — new `/usr/local/lib/waypoint/bin` layout, a fresh `config.db` (claimed
2026-08-02 05:51Z), hostname now `kn4oqw`. Nothing hand-installed on 2026-07-29
survived, including the store backup and the DAPNETGateway .deb. Treat the "bench
state left behind" list above as history, not as the current box.

## Results

| # | Check | Result | Notes |
|---|---|---|---|
| 12 | `waypoint-dapnetgateway` present after reimage | **FAIL** | Not installed at all; see finding 2 |
| 13 | Authenticated connect to DAPNET | **PASS** | §6 — the tier 2 item #196 existed for |
| 14 | Test page delivered over the air | **NOT FEASIBLE** | No pager available to receive one |
| 15 | WhiteList / BlackList render and parse | **PASS (contract)** | §7 — not exercised with live traffic |
| 16 | Blank filters omit the keys | **PASS** | §7 |

## 6. Logged into DAPNET

`DisplayLevel=0` again, so the session was read by the §4 technique — a copy of
the rendered ini with `DisplayLevel=2`, run in the foreground with the unit
stopped:

```
M: Opening DAPNET connection
M: Logging into DAPNET
M: Logged into the DAPNET network
M: Schedule information received: 048C
M: Loaded new schedule: *---*---*---*---
```

A transmitter schedule is only issued to an authenticated, registered
transmitter, so that is a real session and not merely an open socket. Under the
unit the gateway then held `ESTAB` to `137.226.79.100:43434` with `NRestarts=0`
across every subsequent check. **Tier reached: 2.**

The rendered ini was correct as generated: `Callsign=KN4OQW`, the DAPNET address
and the fixed transmitter port 43434, and a non-empty `AuthKey`. The AuthKey is
the operator's secret and is deliberately absent from this log.

### DAPNET's server drops most connection attempts

Worth recording because it looks like a Waypoint defect and is not. Measured from
the bench with `nc`:

| Pattern | Result |
|---|---|
| 20 attempts, back to back | 2 succeeded, 18 timed out |
| 8 attempts, 25s apart | 3 succeeded, 5 timed out |

Failures are SYN with no reply — `ss` shows `SYN-SENT` with the retransmit
counter climbing to 10 — not a refusal. DAPNETGateway makes one attempt per
process lifetime and exits when it times out, so `Restart=on-failure` gives it
roughly one attempt every two minutes and it connects within a few restarts. An
operator watching the first minutes of a POCSAG enable will see restarts that are
nothing to do with their configuration.

## 7. RIC filters

`PUT /api/config/pocsag {"whitelist":"1234567,7654321","blacklist":"9999999"}`,
then apply → 204/200, `waypoint-dapnetgateway.service` in the restart set and
`stopped: null`. Rendered:

```ini
[General]
Callsign=KN4OQW
WhiteList=1234567,7654321
BlackList=9999999
RptAddress=127.0.0.1
```

Blanking both and re-applying removed the two keys and left the rest of
`[General]` intact — the blank-omit path, the same result §3 justified for the
hang timers.

Live paging traffic never arrived during the observation windows, and with no
pager on hand none could be originated, so the filters were confirmed against the
pinned daemon's source rather than over the air (`g4klx/DAPNETGateway` @
`5527546`):

- `Conf.cpp:130-145` parses `WhiteList` / `BlackList` with `strtok(value, ",\r\n")`
  and `atoi`, keeping RICs `> 0` — exactly the comma-separated form Waypoint
  renders, in the `[General]` section Waypoint writes it to.
- `DAPNETGateway.cpp:363-405` applies them per inbound message: `found` starts
  `true` and is narrowed only when the whitelist is non-empty; a blacklist hit
  drops the message.

So the config contract holds end to end. What remains unproven is only the last
hop — an actual page matching a filter — which needs a receiver.

**Correction to the renderer's comment.** `RenderDAPNETGateway` omits blank
filters "(an empty value would filter everything)". Per the source above that
rationale is inverted: `WhiteList=` with an empty value yields no tokens, leaves
the list empty, and therefore allows everything. Omitting blank keys is still the
right behaviour — it is simply equivalent to the empty value, not a guard against
it. The comment should be corrected; the code should not.

## 8. What tier 2 did not re-verify

The AuthKey gate from `981c56e` (`gateway_requirements.go`) was **not** exercised
on hardware. Doing so means clearing `pocsag.auth_key`, and the store treats a
blank AuthKey on write as "keep the stored one" — the secret is write-only, so
clearing it would destroy the operator's credential with no way to restore it
from this side. Unit tests cover the gate; hardware confirmation is deliberately
left undone rather than bought at that price.

Indirect evidence is still on record: the pre-fix box crash-looped 35,517 times
with a blank key, and the post-fix box renders the ini and starts the daemon
cleanly with the key set.

## Findings

### Finding 2 — the DAPNET package reaches no image

`waypoint-dapnetgateway` was absent after the reimage: the unit shipped, the
binary did not, and the service failed `203/EXEC` 524 times. The other eleven
stack packages were all present.

Cause: **[waypoint-stack#15] is still open.** The daemon is pinned, built and
packaged only on that branch, so it is in neither the published apt repository
nor the `waypoint-stack` metapackage's `Depends`. The 2026-07-29 bench only had
it because the CI .deb was installed by hand, which the reimage undid. Tier 2 was
unblocked the same way — artifact `waypoint-stack-debs-armhf` from run
`30497462651`, `dpkg -i`.

Until #15 merges, no image has ever contained this daemon and every reimage will
lose it again.

**Resolved 2026-08-02.** [waypoint-stack#15] merged at 06:07Z: the daemon is
pinned at `5527546`, published to the signed repo for armhf and arm64, and
`waypoint-stack 0.2.0` carries it in `Depends`, so an install of the metapackage
now pulls it. Verified on this bench the same day, twice — see the run below.

# Delivery — the apt path, 2026-08-02

Both runs above installed the .deb by hand, so the *supported* path had never
carried this daemon. Proving it did meant removing the hand-installed package to
restore the Finding 2 state, then letting apt deliver it.

It could not. `POST /api/update/stack/apply` failed:

```
reverted: apt install failed: exit status 100:
E: Held packages were changed and -y was used without --allow-change-held-packages.
```

Filed as **[#220]**. The image holds every `waypoint-*` package and the updater
never passed that flag, so no imaged node could take any stack update; with
`auto_apply` on, this box had already retried and reverted three times that day
(06:34Z, 09:06Z, 17:56Z) with nothing but a `last_result` string to show. Three
simulations isolated it: the metapackage alone and the metapackage with its new
dependency named explicitly failed identically, and adding the flag resolved the
transaction — so the hold was the whole cause, not the new dependency.

With the flag, apt installed `waypoint-stack 0.2.0`, pulled `waypoint-dapnetgateway`
in as a new dependency, and the daemon came up bound to the rendered loopback
`127.0.0.1:4800` with an ESTABLISHED connection to `137.226.79.100:43434` — DAPNET's
core server, reachable only past the AuthKey guard. **Delivery confirmed.**

Two further results came out of the fix for #220, which was then re-verified here
against a recreated fielded-node state (the genuine `0.1.0` metapackage from CI run
`30495252689`, twelve packages held, no DAPNET binary):

- The flag **clears** the hold on whatever it changed, and a newly pulled dependency
  arrives unheld — so the fix re-asserts the holds afterwards. After the verified
  run all thirteen packages were held, including the two that would otherwise have
  been left exposed.
- The revert path cannot run at all: the published pool keeps only the current
  version, so the health gate's failure here (`waypoint-mmdvm.service is not
  active` — that unit is disabled on this box) ended in `REVERT FAILED … E: Version
  '0.1.0' for 'waypoint-stack' was not found`. Filed as **[#221]**; the earlier
  "reverted" outcomes had only looked healthy because the install failed before
  changing anything, so nothing needed the pool.

### Finding 3 — YSFGateway has a startup guard the survey says it does not

Filed as [#215].

`gateway_requirements.go` records a survey of daemons that exit before opening
anything, and concludes that only DAPNETGateway has such a guard — YSFGateway
among those whose `return 1` paths "are all runtime conditions (an unresolvable
host, a port already bound), not missing configuration."

On this box `waypoint-ysfgateway` is crash-looping 2,127 times on:

```
YSFGateway: WiresX.cpp:103: void CWiresX::setInfo(const std::string&, unsigned int, unsigned int): Assertion `txFrequency > 0U' failed.
```

An assert on an unset TX frequency is missing configuration, not a runtime
condition, and it is the same non-converging `Restart=on-failure` loop finding 1
described. It is a candidate for the same registry, keyed on the RF frequency
rather than a credential.

### Finding 4 — MMDVM-Host will not start without a frequency

Filed as [#216].

Same root cause, different daemon. `waypoint-mmdvm` exits `status=1` in a loop;
raising `DisplayLevel` shows the modem itself rejecting the setup:

```
I:     TX Frequency: 0Hz (0Hz)
I:     RX Frequency: 0Hz (0Hz)
M: Opening the MMDVM
I: MMDVM protocol version: 1, description: MMDVM_HS_Dual_Hat-v1.6.1 ...
E: Received a NAK to the SET_FREQ command from the modem
```

This is why the over-the-air page was unreachable independently of the missing
pager: the node has no RF frequency configured, so nothing can transmit. Both
loops predate this run and were not caused by it.

## Bench state left behind (2026-08-02)

- `waypoint-dapnetgateway 0~git5527546+wp1` installed **by hand** from the CI
  artifact — it will not survive the next reimage until [waypoint-stack#15]
  merges.
- POCSAG **enabled**, AuthKey **set**, gateway `active` and logged into DAPNET
  with `NRestarts=0`.
- POCSAG whitelist and blacklist returned to **blank**.
- `waypoint-mmdvm` and `waypoint-ysfgateway` still crash-looping on the unset RF
  frequency (findings 3 and 4) — pre-existing, untouched.
- The dashboard was re-claimed by the operator after an unrelated process had
  taken the admin account; `waypointd reset-claim` was used to release it.

[#33]: https://github.com/KN4OQW/waypoint/issues/33
[#193]: https://github.com/KN4OQW/waypoint/pull/193
[#196]: https://github.com/KN4OQW/waypoint/issues/196
[#215]: https://github.com/KN4OQW/waypoint/issues/215
[#216]: https://github.com/KN4OQW/waypoint/issues/216
[#220]: https://github.com/KN4OQW/waypoint/issues/220
[#221]: https://github.com/KN4OQW/waypoint/issues/221
[waypoint-stack#15]: https://github.com/KN4OQW/waypoint-stack/pull/15
