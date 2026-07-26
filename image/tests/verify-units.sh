#!/usr/bin/env bash
# Parse every systemd unit the image ships, with systemd itself.
#
# systemd does not fail on a directive it does not recognise — it logs and carries
# on — so a misspelled hardening option or a stale unit name in an After= is
# invisible until the node it was meant to protect is in the field. This is the
# only check that catches that before the image is built.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
units_dir="$here/../src/modules/waypoint/filesystem/root/etc/systemd/system"

if ! command -v systemd-analyze >/dev/null; then
  echo "systemd-analyze not found; install the systemd package" >&2
  exit 1
fi

# The units reference binaries that exist on a node and not on a build machine.
# --man=false stops it fetching documentation; the ExecStart paths are checked for
# existence separately below, where the failure message can say why.
fail=0
for unit in "$units_dir"/*.service "$units_dir"/*.socket; do
  [ -e "$unit" ] || continue
  echo "==> $(basename "$unit")"
  # systemd-analyze verify resolves dependencies against the host's unit tree, so
  # a unit naming one the build machine does not have produces a warning we do not
  # care about. Everything else — unknown directives, malformed values, bad
  # section names — is a real fault and is what the grep keeps.
  out="$(systemd-analyze verify --man=false --recursive-errors=no "$unit" 2>&1 || true)"
  if [ -n "$out" ]; then
    # Two classes of message are expected on a build machine and are not faults:
    #   - a dependency unit the build host does not have (mosquitto, NetworkManager)
    #   - an ExecStart binary that only exists on a node (the gateways, waypointd)
    # Everything else — unknown directives, malformed values, bad section names —
    # is a real fault, and is exactly what systemd would otherwise ignore silently.
    filtered="$(printf '%s\n' "$out" \
      | grep -v -e "Unit .* not found" \
                -e "not found\.$" \
                -e "is not executable: No such file or directory" \
      || true)"
    if [ -n "$filtered" ]; then
      printf '%s\n' "$filtered" >&2
      fail=1
    fi
  fi
done

exit "$fail"
