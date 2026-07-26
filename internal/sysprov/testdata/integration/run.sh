#!/usr/bin/env bash
# Run the sysprov integration test inside a throwaway Debian container.
#
# The test creates an account, rewrites /etc/hosts, writes sshd drop-ins, and
# locks root. None of that belongs on a development machine, so the container is
# the point: it is built fresh, the test runs as root inside it, and it is
# discarded. The test binary itself refuses to run without a container marker
# unless WAYPOINT_SYSPROV_ALLOW_HOST=1 is set deliberately.
#
# Usage:  internal/sysprov/testdata/integration/run.sh [-v]
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../../.." && pwd)"
image="waypoint-sysprov-integration"

# The test binary is cross-compiled for the container's architecture rather than
# built inside it, so the image needs no Go toolchain and the run is quick enough
# to be part of an ordinary change.
arch="$(uname -m)"
case "$arch" in
  x86_64)  goarch=amd64 ;;
  aarch64) goarch=arm64 ;;
  armv7l)  goarch=arm   ;;
  *) echo "unsupported architecture $arch" >&2; exit 1 ;;
esac

echo "==> building the test binary (linux/$goarch)"
bin="$(mktemp -d)/sysprov.test"
( cd "$repo" && CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" \
    go test -c -tags integration -o "$bin" ./internal/sysprov )

# The helper itself, so the first-boot simulation exercises the binary the image
# ships rather than a stand-in.
helper_bin="$(dirname "$bin")/waypoint-provision-helper"
( cd "$repo" && CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" \
    go build -o "$helper_bin" ./cmd/waypoint-provision-helper )

echo "==> building the container image"
docker build --quiet -t "$image" "$here" >/dev/null

echo "==> running"
run_container() {
  docker run --rm \
    --cap-add SYS_ADMIN \
    --cap-add SYS_PTRACE \
    -e WAYPOINT_SYSPROV_INTEGRATION=1 \
    -v "$bin:/sysprov.test:ro" \
    -v "$repo/image/src/modules/waypoint/filesystem/root/etc/systemd/system:/units:ro" \
    -v "$helper_bin:/usr/bin/waypoint-provision-helper:ro" \
    "$image" "$@"
}

# The first-boot oneshot: does it run when the marker is absent, and not when it
# is present. Checked against the real unit file and the real binary.
if ! run_container /verify-firstboot.sh; then
  echo "!! first-boot simulation did not pass; see above" >&2
  firstboot_rc=1
else
  firstboot_rc=0
fi

# The syscall surface check runs first, in its own container, because it traces a
# full test run and would otherwise be reading a filesystem the tests had already
# changed. It is skipped rather than failed when the kernel will not allow
# ptrace — some CI sandboxes refuse it — but a refusal is said out loud.
if ! run_container /verify-syscalls.sh; then
  echo "!! syscall surface check did not pass; see above" >&2
  syscall_rc=1
else
  syscall_rc=0
fi

# --cap-add SYS_ADMIN lets sethostname(2) succeed inside the container's UTS
# namespace, so the running hostname changes too and not only /etc/hostname.
# Nothing else here needs elevated privileges.
run_container /sysprov.test -test.count=1 "$@"
test_rc=$?

exit $(( syscall_rc || firstboot_rc || test_rc ))
