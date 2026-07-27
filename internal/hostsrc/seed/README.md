# Shipped hostlist copies

These are the floor under the reflector, master and talkgroup pickers: a node
with no working network still has something to show. They are **not** a source of
truth — `hostsrc.Restore` only writes one when there is no cache at all, and the
first successful download replaces it.

Refresh them when cutting a release:

```sh
go run ./cmd/hostseed
```

That rewrites every file below from its capture source and prints what changed.
Review the diff before committing: these are third-party operational lists, and a
bad capture ships a broken picker to every node.

## Provenance

| File | Captured from | Notes |
| --- | --- | --- |
| `DMR_Hosts.txt` | `https://www.pistar.uk/downloads/DMR_Hosts.txt` | Pi-Star's DMR master list, the same lineage as `/usr/local/etc/DMR_Hosts.txt`. |
| `TGList_BM.txt` | `https://www.pistar.uk/downloads/TGList_BM.txt` | BrandMeister talkgroup names. |
| `DStar_Hosts.json` | `g4klx/DStarGateway` @ `612f388` `Data/DStar_Hosts.json` | Pinned to the same commit the stack pins the gateway to, so the format matches the parser in the shipped binary. Bump both together. |
| `YSFHosts.json` | `https://www.pistar.uk/downloads/YSF_Hosts.txt` | **Converted** from the classic text list to the JSON YSFGateway parses (`internal/hostconv`). |
| `P25Hosts.json` | `P25_Hosts.txt` + `TGList_P25.txt` | **Converted.** The hostlist carries addresses only; the TGList supplies reflector names. |
| `NXDNHosts.json` | `NXDN_Hosts.txt` + `TGList_NXDN.txt` | **Converted**, same as P25. |
| `M17Hosts.txt` | `https://www.pistar.uk/downloads/M17_Hosts.txt` | No conversion — M17Gateway parses the classic text directly. |

`DMRIds.dat` is captured and **published** by the same tool, but is deliberately
not shipped: at 6.6 MB it is a third of the binary again, and it goes out of date
continuously rather than slowly, so an embedded copy would be both large and
wrong. A node with no network resolves numeric IDs instead of callsigns — the
behaviour it had before anything downloaded the table at all.

Captured 2026-07-27.

## A note on the converted lists

`YSFHosts.json`, `P25Hosts.json` and `NXDNHosts.json` are **not** captured
verbatim. Their old source served ready-made JSON and is gone; the only reachable
replacement serves the classic text formats, which the gateways cannot read — they
parse their hostlist with nlohmann::json.

`internal/hostconv` does the conversion at capture time. Its contract is stricter
than it looks: each gateway reads every field with `it["key"]` on a *const* JSON
object, where a missing key is undefined behaviour rather than a default. Every
record therefore carries every key the gateway touches, including the ones it will
only ever find null.

Verified against the real parsers on hardware, not just by shape:

```
Loaded 1405 YSF reflectors      (YSFGateway)
Loaded 292 P25 reflectors       (P25Gateway)
Loaded 276 NXDN reflectors      (NXDNGateway)
```

If you change the converter, re-run that check — a well-formed JSON file that the
gateway rejects still leaves an operator with no reflectors.
