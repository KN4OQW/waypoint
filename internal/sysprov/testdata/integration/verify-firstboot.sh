#!/usr/bin/env bash
# Simulate the first-boot condition the oneshot is gated on.
#
# systemd evaluates ConditionPathExists=! itself, and there is no systemd here to
# evaluate it — so this checks the two things that actually matter: that the unit
# carries the condition, and that the binary it runs behaves correctly on both
# sides of it. A unit whose condition is right and whose ExecStart fails on a
# fresh node would pass a file-content check and strand a real one.
set -uo pipefail

unit=/units/waypoint-firstboot.service
helper=/usr/bin/waypoint-provision-helper
marker=/var/lib/waypoint/provisioned
fail=0

echo "==> the unit is gated on the marker's absence"
if ! grep -qx 'ConditionPathExists=!/var/lib/waypoint/provisioned' "$unit"; then
  echo "the oneshot is not conditional on the marker; it would run on every boot" >&2
  fail=1
fi
if ! grep -qx 'Before=waypointd.service' "$unit"; then
  echo "the oneshot is not ordered before waypointd; waypointd could read the marker first" >&2
  fail=1
fi

echo "==> first boot: no marker, the oneshot's work succeeds"
rm -rf /var/lib/waypoint
if ! "$helper" -prepare; then
  echo "-prepare failed on a fresh node" >&2
  fail=1
fi
if [ ! -d /var/lib/waypoint ]; then
  echo "-prepare did not create the state directory" >&2
  fail=1
fi
# 0750: the marker inside is world-readable, the directory is not somewhere an
# unprivileged process needs to list.
mode="$(stat -c %a /var/lib/waypoint)"
if [ "$mode" != "750" ]; then
  echo "state directory is mode $mode, want 750" >&2
  fail=1
fi

echo "==> -prepare is idempotent"
if ! "$helper" -prepare; then
  echo "-prepare failed on a second run" >&2
  fail=1
fi

echo "==> second boot: the marker exists, so systemd would skip the unit"
printf '{"schema_version":1,"provisioned_at":"2026-07-26T12:00:00Z","updated_at":"2026-07-26T12:00:00Z","root_locked":true}\n' > "$marker"
if [ ! -e "$marker" ]; then
  echo "could not write the marker" >&2
  fail=1
fi
# The condition is a negated path test, so the unit is skipped exactly when the
# marker is present. Asserting the file's presence is asserting the skip.
echo "    marker present; ConditionPathExists=! is false, unit skipped"

echo "==> without the waypoint group, -prepare fails loudly"
groupdel waypoint 2>/dev/null
rm -rf /var/lib/waypoint
if "$helper" -prepare 2>/dev/null; then
  echo "-prepare succeeded with no waypoint group; the socket would have no access control" >&2
  fail=1
fi

exit "$fail"
