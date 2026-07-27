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

Captured 2026-07-27.

## Lists with no shipped copy

`YSFHosts`, `P25Hosts`, `NXDNHosts` and `M17Hosts` ship no copy. Every source for
them — `hostfiles.refcheck.radio` and `hostfiles.w0chp.net` alike — resolves to a
single host that is refusing connections, and no other machine-fetchable mirror
was found (`pistar.uk` returns 502 for those four, `dvref.com` is behind an
anti-bot challenge). See #138.

They get their floor once `hostfiles.kn4oqw.com` serves aggregated lists; until
then their status reports no seed and the UI says the list is unavailable rather
than showing an empty picker. Nothing here is invented: shipping a made-up
reflector address is worse than shipping none.
