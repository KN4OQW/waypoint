#!/usr/bin/env bash
# Run the join-failure tests against a real node in setup mode.
#
#   ./join-rollback.sh <node-url> <ssid> [country]
#   ./join-rollback.sh https://172.16.50.13 DeathStar US
#
# The node must be UNPROVISIONED with its setup access point up. Reach it over
# Ethernet, not over the access point: these tests take the access point down and
# wait for it to come back, so driving them over the link being torn down would
# lose the connection at exactly the moment the result matters.
#
# Nothing here is destructive to a node's *setup state* — every attempt is
# expected to fail and roll back, and the tests assert the node is still
# resumable afterwards. It is destructive to the wireless configuration while it
# runs.
set -euo pipefail

NODE="${1:-}"
SSID="${2:-}"
COUNTRY="${3:-}"

if [ -z "$NODE" ] || [ -z "$SSID" ]; then
  echo "usage: $0 <node-url> <ssid> [country]" >&2
  echo "  e.g. $0 https://172.16.50.13 DeathStar US" >&2
  exit 2
fi

# A real, in-range SSID is required on purpose. A wrong passphrase for a network
# that is not there fails for the wrong reason — nothing refuses the key because
# nothing answers — and would report a pass for a path that was never taken.
echo "==> node:    $NODE"
echo "==> network: $SSID (a deliberately wrong passphrase will be used)"
echo

# Pre-flight, so a node in the wrong state is reported here rather than as three
# skipped tests somebody has to interpret.
state="$(curl -sk --max-time 10 "$NODE/api/setup/state" || true)"
if [ -z "$state" ]; then
  echo "!! $NODE did not answer /api/setup/state" >&2
  exit 1
fi
case "$state" in
  *'"mode":"setup"'*) ;;
  *) echo "!! the node is not in setup mode:"; echo "   $state"
     echo "   reflash it, or run: waypointd -reset-claim --full" >&2; exit 1 ;;
esac
echo "==> node is in setup mode"

cd "$(dirname "$0")/../../../.."
WAYPOINT_BENCH_NODE="$NODE" \
WAYPOINT_BENCH_SSID="$SSID" \
WAYPOINT_BENCH_COUNTRY="$COUNTRY" \
  go test -tags benchnode ./internal/wizard/ -run TestBenchNode -v -count=1 -timeout 20m
