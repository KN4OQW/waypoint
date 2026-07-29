# Bench run log — issue #33 closeout: DMR hang timers + DAPNETGateway

Hardware validation of the last two items on [#33], 2026-07-29: the DMR call-hold
and hang-timer overrides ([#193]) and the newly packaged DAPNETGateway
(KN4OQW/waypoint-stack#15).

Both had only ever been exercised against unit tests and a build container. The
hang timers are interesting on hardware because the *host* reduces oversized
values and Waypoint deliberately does not, so the only way to see the real
outcome is to read MMDVM-Host's own parameter block. DAPNETGateway had never run
here at all — the unit file has shipped in the image since July with no binary
behind it.

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

[#33]: https://github.com/KN4OQW/waypoint/issues/33
[#193]: https://github.com/KN4OQW/waypoint/pull/193
