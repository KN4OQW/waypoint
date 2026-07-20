# experiments/

Throwaway spikes whose measured numbers inform an RFC. **Not product code** — nothing here is imported by `cmd/` or `internal/`, none of it is a supported interface, and it may be deleted once the RFC it backs is merged. Kept in-tree only so the measurements are reproducible.

## peerspike (RFC-0016)

A latency spike for RFC-0016 (bus LAN peering): round-trip latency + jitter for
20 ms-cadence 55-byte (DMRD-sized) frames over a persistent TCP+TLS connection
vs. the local mosquitto at QoS 0 and QoS 1. The client stamps each frame with a
sequence number and its own monotonic send time; the far side echoes it verbatim;
the client computes RTT against the same clock (no cross-host time sync) and
reports one-way = RTT/2, with jitter as the standard deviation of one-way latency
plus p50/p95/p99.

Run (echo/broker on one node, client on the other):

```
# on the "server" node (e.g. the bench Pi):
peerspike tcp-echo  -addr 0.0.0.0:9100
peerspike mqtt-echo -broker 127.0.0.1:1884 -qos 0     # (or -qos 1)

# on the "client" node:
peerspike tcp-client  -addr  <server>:9100        -n 500 -cadence 20ms
peerspike mqtt-client -broker <server>:1884 -qos 0 -n 500 -cadence 20ms
peerspike mqtt-client -broker <server>:1884 -qos 1 -n 500 -cadence 20ms
```

The MQTT runs need a broker listener reachable from the client; on the bench a
temporary `listener 1884 0.0.0.0` drop-in was added and reverted afterward.

### Measured (2026-07-20, bench pair)

Pi 3 (`172.16.50.13`, full waypoint stack running) as echo/broker node; session
host (`172.16.50.24`) as client; same LAN switch. `paho.mqtt.golang v1.5.1`.
One-way = RTT/2. TCP and QoS 0 held across three runs (500–1000 frames), QoS 1
across two.

| Transport | one-way mean | median | jitter σ | p95 | p99 | max | loss |
|---|---|---|---|---|---|---|---|
| ICMP baseline | 0.26 ms | — | — | — | — | — | 0% |
| TCP + TLS | 0.64 ms | 0.59 ms | 0.25 ms | 1.1 ms | 1.7 ms | 2.6 ms | 0% |
| MQTT QoS 0 | 1.10 ms | 0.98 ms | 0.40 ms | 1.9 ms | 2.5 ms | 5.1 ms | 0% |
| MQTT QoS 1 | 16.6 ms | 20.1 ms | 6.4 ms | 30.3 ms | 30.6 ms | 44.6 ms | 0% |

Conclusion (see RFC-0016 §Design 1): QoS 1 is disqualifying on latency alone
(~one whole 20 ms frame budget, 6 ms jitter); QoS 0 is within margin, so the
transport decision rests on coupling/backpressure, not the QoS-0 numbers.
