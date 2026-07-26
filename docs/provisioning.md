# Provisioning: the first boot, without Raspberry Pi Imager

A Waypoint node has to be set up before it can be claimed. Setting up means
things a dashboard process has no business doing: renaming the host, creating an
account, locking root, raising a Wi-Fi access point. This document describes how
that happens and why it is split the way it is.

## Why this exists

The image was designed around Raspberry Pi Imager's advanced options: the
operator would pre-seed a user and Wi-Fi credentials at flash time, and the image
would ship with no credentials of its own. Raspberry Pi Imager 2.x removed
customisation for locally-flashed images — it now refuses to offer the options at
all unless it has `init_format` metadata from an OS manifest — so that assumption
no longer holds for anyone who downloads a `.img.xz` from a release page.

The fix is not to work around Imager. It is to stop depending on it: a node
flashed with nothing but `dd` must be able to configure itself on first boot.

## The three pieces

**`internal/provision`** — the provisioned-state contract. A JSON marker at
`/var/lib/waypoint/provisioned` records that setup completed: hostname chosen,
recovery user created, root locked. It is a file rather than a store row because
the question has to be answerable before the config store is open and before the
network is up. A boolean is mirrored into `config.db` for the API; the file wins.

**`internal/privhelper`** — the protocol. `Provisioner` names eleven operations
and nothing else — no "run this command", no escape hatch. Requests are validated
on both ends. Frames are a 4-byte big-endian length followed by JSON.

**`internal/sysprov` + `cmd/waypoint-provision-helper`** — the root side. The
helper runs as root, listens on `/run/waypoint/provision.sock`, and performs those
eleven operations by shelling out to `hostnamectl`, `useradd`, `passwd`, `chsh`,
`nmcli`, and `systemctl`.

## Why a separate process

waypointd runs as root today. Every provisioning operation is a reason it should
not have to: they are rare, they are dangerous, and they are the only
root-requiring things a dashboard process does. Behind a socket, a flaw in the web
tier reaches eleven validated calls instead of reaching root — and dropping
waypointd's own privileges later becomes a mechanical change rather than a
rewrite.

## The security boundary

Three independent checks, because any one of them can be got wrong by a bad
`chmod` or a botched package upgrade:

1. `/run/waypoint` is `0750 root:waypoint`, so a process outside the group cannot
   traverse to the socket.
2. The socket is `0660 root:waypoint`, created by systemd with that mode already
   applied — there is no instant in which a bound socket is more permissive than
   it should be.
3. The helper checks `SO_PEERCRED` on every connection and refuses peers that are
   neither uid 0 nor in the `waypoint` group. This is the check the filesystem
   cannot make: the kernel reports who is on the other end, and no amount of
   tampering with the socket's permissions changes that answer.

The helper refuses to start if the `waypoint` group does not exist. A helper whose
access control silently resolved to "anyone" is worse than one that does not come
up.

## Secrets never reach argv

`/proc/<pid>/cmdline` is readable by other local users. So:

- A password reaches `chpasswd` on **stdin**, never as an argument.
- A Wi-Fi PSK reaches NetworkManager through a **0600 keyfile** written directly
  to `/etc/NetworkManager/system-connections`, never via
  `nmcli device wifi connect … password …`.

Request types that carry secrets redact themselves in `String()`, and the server
logs through that method rather than formatting the struct.

## Ordering contracts

Two orderings are enforced by the helper, not merely documented:

- **`CreateRecoveryUser` before `LockRoot`.** Locking root on a node with nothing
  else that can log in is how a hotspot becomes a coaster. The check is not "is
  there a member of the sudo group" but "can some non-root member of the sudo
  group actually log in" — it needs a usable password or an authorized key. A
  locked, keyless account in the sudo group is a name in a file.
- **A checkpoint before a network change**, if the change is to be revertible.
  `NetJoin` does not checkpoint on its own, because only the caller knows whether
  it is on the link it is about to change.

Root's shell is deliberately left alone. Setting it to `nologin` is a common
hardening step and it breaks `sudo -i` — which is exactly the path the recovery
account uses to fix a broken node.

## Idempotency

Every method reads the current state first and reports `Changed` honestly. A
wizard step that times out on the operator's phone and gets retried must converge:
re-running `CreateRecoveryUser` repairs and reports `Created: false` rather than
failing on "user already exists", and re-installing the same SSH key is a no-op
rather than a second line. Keys are compared by fingerprint, so the same key
pasted twice with different comments is one key.

## Testing

    go test ./internal/privhelper/... ./internal/sysprov/...

The unit suite runs against a scripted fake system and a temporary filesystem
root. The protocol's conformance suite runs three times: against the in-memory
fake, and against a real client/server pair over a Unix socket — the second proves
the transport preserves the contract, error codes included.

    ./internal/sysprov/testdata/integration/run.sh -test.v

The integration test drives the real `useradd`, `passwd`, `chsh`, and
`hostnamectl` against a real Debian filesystem inside a throwaway container, and
verifies the outcomes with `getent`, `id`, `stat`, and `sshd -T`. It is behind a
build tag and refuses to run outside a container without an explicit override,
because it creates an account and locks root on whatever machine it runs on.

## Still to come

- waypointd's client wiring and the setup wizard itself.
- Installing the helper binary and its units from the image module
  (`image/src/modules/waypoint/start_chroot_script`).
- Adding waypointd's service user to the `waypoint` group.
