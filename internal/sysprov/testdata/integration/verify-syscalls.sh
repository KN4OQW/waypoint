#!/usr/bin/env bash
# Check the helper's real syscall surface against the SystemCallFilter its unit
# applies.
#
# The unit sets SystemCallFilter=@system-service, and systemd does not tell you
# when a filter is too tight — the process is killed with SIGSYS at the moment it
# makes the call, which for a provisioning helper means midway through creating an
# account, on a node in the field, months after the directive was written.
#
# So: ask systemd what @system-service actually contains on this systemd version,
# run the helper's real operations under strace, and compare. Anything the helper
# calls that the filter does not permit is named, and the exit status fails.
set -uo pipefail

trace=/tmp/helper.strace
allow=/tmp/allowed.txt

echo "==> resolving @system-service"
# systemd-analyze lists a set's members without expanding the groups inside it, so
# the closure has to be walked. Getting this wrong is worse than not checking at
# all: an unexpanded list makes almost every syscall look forbidden, and a wall of
# false positives is a check people learn to ignore.
resolve_set() {
  systemd-analyze syscall-filter "$1" 2>/dev/null \
    | tail -n +2 \
    | sed 's/^[[:space:]]*//' \
    | grep -v -e '^#' -e '^$'
}

: > "$allow"
pending=("@system-service")
seen=""
while [ "${#pending[@]}" -gt 0 ]; do
  set_name="${pending[0]}"
  pending=("${pending[@]:1}")
  case " $seen " in *" $set_name "*) continue ;; esac
  seen="$seen $set_name"

  while read -r entry; do
    case "$entry" in
      @*) pending+=("$entry") ;;
      *)  printf '%s\n' "$entry" >> "$allow" ;;
    esac
  done < <(resolve_set "$set_name")
done
sort -u -o "$allow" "$allow"
echo "    $(wc -l < "$allow") syscalls permitted"

if [ "$(wc -l < "$allow")" -lt 100 ]; then
  echo "the permitted set resolved to almost nothing; the parse is wrong, not the helper" >&2
  exit 1
fi

echo "==> tracing the helper's operations"
# -f to follow the useradd/passwd/nmcli children the helper execs: their syscalls
# run under the same filter, so they are part of the surface being checked.
strace -qq -f -e trace=all -o "$trace" /sysprov.test -test.run TestProvisionsARealSystem -test.count=1 >/dev/null 2>&1
rc=$?
if [ ! -s "$trace" ]; then
  echo "strace produced nothing (is CAP_SYS_PTRACE granted?)" >&2
  exit 1
fi

echo "==> comparing"
# strace lines look like "1234  openat(AT_FDCWD, ...". Take the call name only,
# dropping unfinished/resumed markers and signal lines.
grep -oP '^\d+\s+\K[a-z_0-9]+(?=\()' "$trace" | sort -u > /tmp/used.txt
echo "    $(wc -l < /tmp/used.txt) distinct syscalls observed"

# Documented exceptions: syscalls observed here that the filter does not permit
# and that the filter should NOT be widened for. Each needs a reason, and the
# reason has to be "this cannot happen on a node", not "it seems to work".
#
#   sethostname — only reached by the no-systemd fallback in sysprov.setHostname.
#     On a node hasSystemd() is true and the hostname is set through hostnamectl,
#     which is a D-Bus call to systemd-hostnamed; this process never makes the
#     syscall. The fallback exists for containers, which is exactly where
#     SystemCallFilter is not enforced either — there is no systemd to enforce it.
#     Widening the filter to permit it would grant the helper a capability it does
#     not use in the only environment where the filter applies.
cat > /tmp/expected.txt <<'EXPECTED'
sethostname
EXPECTED
sort -u -o /tmp/expected.txt /tmp/expected.txt

missing="$(comm -23 /tmp/used.txt "$allow" | comm -23 - /tmp/expected.txt)"
unused_exception="$(comm -12 /tmp/used.txt "$allow" | comm -12 - /tmp/expected.txt)"
if [ -n "$unused_exception" ]; then
  # An exception that is no longer needed is a comment that has stopped being
  # true, which is worse than no comment.
  echo "these documented exceptions are now permitted by the filter and should be removed:" >&2
  printf '  %s\n' $unused_exception >&2
  exit 1
fi
if [ -n "$missing" ]; then
  echo
  echo "SystemCallFilter=@system-service does NOT permit syscalls the helper makes:" >&2
  printf '  %s\n' $missing >&2
  echo >&2
  echo "Either widen the filter in waypoint-provision-helper.service and say why," >&2
  echo "or stop making these calls." >&2
  exit 1
fi

echo "OK: every syscall the helper makes is permitted by @system-service"
echo "    (test exit status was $rc)"
exit 0
