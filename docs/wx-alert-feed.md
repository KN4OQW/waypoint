# The weather alert feed — what leaves the device

Waypoint's weather feature makes one outbound connection, and only when an
operator has switched it on and chosen at least one county.

## What it contacts

```
wss://mqtt.wxalerts.org/mqtt      MQTT over websockets, port 443
```

A public, read-only feed of National Weather Service alerts. The account is
`wxalerts` / `wxalerts`, published openly in the feed's own documentation: it is
read-only on public data, and the broker's ACL is what makes that safe rather
than the secrecy of the password. Both the address and the credentials are
editable, so a node can be pointed at a different feed without a new build.

## What it sends

**Nothing about the device.** The connection carries:

- the shared username and password above,
- an MQTT client id of the form `waypointd-wx-<random>`,
- subscriptions to `wxalerts/nws/v1/same/<county>/#` for the counties chosen.

The client id is random per connection and is deliberately not derived from the
callsign, the DMR id, the hostname or anything else about the node. MQTT wants
unique client ids — two connections sharing one make a broker evict the first —
and the obvious ways to get uniqueness are all identifiers. Random satisfies the
protocol and names nobody. `TestFeedClientIDCarriesNoDeviceIdentity` holds that.

The county list is the one thing that says anything at all, and it says roughly
where the node is — which is inherent to subscribing to alerts for a place. An
operator who does not want that does not enable the feature.

Nothing is ever published: the account is subscribe-only and the broker enforces
it.

## The off switch

Weather panel → **Broadcast weather alerts**. Off by default.

With it off, or with no counties chosen, the node does not contact the broker at
all — not at boot, not on a timer.
`TestFeedDoesNotConnectWithNothingToWatch` covers that, and it is also the state
every node ships in.

Turning it off breaks nothing else: the feed is independent of every other
subsystem, and a node that never enables it behaves exactly as it did before the
feature existed.
