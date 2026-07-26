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

## The wizard

**`internal/wizard`** is waypointd's side: the step state machine and the gate
that serves it.

    unprovisioned          -> the setup wizard        (mode "setup")
    provisioned, unclaimed -> the claim flow          (mode "claim", RFC-0002)
    provisioned, claimed   -> the dashboard           (mode "done")

The gates nest, setup outside claim. An unprovisioned node still answers to
`raspberrypi.local` with an unlocked root, so `/api/claim` is closed too — letting
someone claim a node before any of that is fixed would hand them a box the wizard
is about to change under them.

### Steps

| Step | Endpoint | What it does |
|---|---|---|
| `hostname` | `POST /api/setup/hostname` | names the node; refuses the shipped default |
| `user` | `POST /api/setup/user` | creates the recovery account (always with sudo) |
| `key` | `POST /api/setup/key` | installs an SSH key, or skips if the account has a password |
| `lock` | `POST /api/setup/lock` | settles the SSH policy, locks root, writes the marker |
| `claim` | `POST /api/claim` | the existing RFC-0002 claim; the wizard only reports it |

`GET /api/setup/state` returns the current step, the completed ones, and what the
UI needs to render the form. Once setup is finished it reports the mode and
nothing else — the endpoint stays reachable without authentication, and the
recovery account's name is useful to someone deciding how to attack the box.

Sudo is not offered as a choice. The recovery account exists to administer a node
whose root is locked; one that cannot become root would be decoration, and the
option would only let an operator build that by accident.

### Resuming

Every step writes progress to `/var/lib/waypoint/setup-progress.json` before it
returns, and the wizard resumes at the first incomplete step. This is not a
hypothetical: the operator is often setting the node up over the setup access
point and loses that connection the moment the node joins their real network.

Progress is a separate file from the provisioned marker on purpose. Progress is
transient and is deleted when setup completes; the marker is durable, terminal,
and its presence is what flips the gate. Folding them together would mean a
half-finished wizard left a marker that `provision.IsProvisioned` had to be taught
to disbelieve.

A corrupt or unreadable progress file starts over. The fail direction is to repeat
work — every step is idempotent — rather than to skip it.

### Errors

Helper codes map onto HTTP so an operator can tell the two cases apart:

    invalid_argument -> 400   what you typed is wrong
    conflict         -> 409   right input, wrong moment (includes out-of-order steps)
    unsupported      -> 503   the helper is not reachable; not your fault

An out-of-order step also returns `expected_step`, so a client that has lost its
place gets directions rather than only a refusal.

## Still to come

- The wizard's frontend. `GET /` currently serves a self-contained placeholder
  page naming the next step; the real UI replaces it.
- The AP/join steps. `APUp`, `APDown`, `NetJoin`, and the checkpoint calls exist
  in the helper but are not yet wired into the wizard flow.
- Installing the helper binary and its units from the image module
  (`image/src/modules/waypoint/start_chroot_script`).
- Adding waypointd's service user to the `waypoint` group.

## The setup access point

A node flashed with `dd` has no network at all, so there is nothing to serve the
wizard over. The setup AP closes that gap and exists for nothing else — every
rule below is about making sure it stops existing once it is no longer needed.

### What it looks like

    SSID     Waypoint-Setup-XXXX   (last four of the board serial, uppercased)
    Security open by default
    Address  10.42.0.1/24          (NetworkManager's shared-mode range)

The serial suffix is for the hamfest case: several unconfigured nodes powered up
in one room, all broadcasting an identically named network, and no way to tell
which one your phone just joined.

### Open by default, and why

An open network means anyone in radio range can reach the wizard. The mitigations
are that the AP is up for minutes rather than permanently, that the session lock
admits one device, and that the page says so inline. The alternative — a printed
per-device key — produces the failure where an operator with no key cannot set up
the node they own, which is worse.

To protect it, put `waypoint-setup.txt` on the boot partition:

    psk=my-setup-passphrase

Eight to sixty-three characters. A malformed file is **ignored with a logged
warning** and the AP still comes up open. That file is edited by hand on a card
in a reader, often on Windows; a node that refuses to raise its AP over a typo is
a node the operator cannot reach at all.

### The captive portal

`internal/captive` answers every A query with `10.42.0.1` and answers the probe
URLs each operating system fetches — Apple's `hotspot-detect.html`, Android and
Chrome's `generate_204`, Windows' `connecttest.txt` and `ncsi.txt`, Firefox's
`canonical.html` — with a 302 to the wizard, so the phone's own captive sheet
opens on it. `dhcp-option=114` (RFC 8910) hands the portal URL to clients that
read it, and `/captive-portal` serves the RFC 8908 JSON they then fetch.

AAAA queries get an empty NOERROR rather than a forged address: a client handed a
nonsense AAAA tries it first and stalls until it times out.

NetworkManager's own dnsmasq is set to `port=0` — DHCP only — so the responder in
`internal/captive` owns port 53 rather than the behaviour living in a config file
written blind.

### The session lock

The first device to load the wizard holds it. Every other device gets a 403 page
naming the holder and the timeout. The session follows the client's MAC as well
as its address, so a DHCP renewal mid-setup does not cost the operator their
progress, and it is released after ten minutes idle or when setup completes.

This is not authentication and does not pretend to be — whoever associates first
holds the session. It bounds the damage to "first device wins", which is the same
property a physical setup button has, and it is the strongest thing available
before any credential exists.

### When it goes away

- **On a network join** — immediately. An open AP broadcasting beside the real
  network the node just joined is the thing the window rules exist to prevent.
- **On setup completion** — immediately.
- **After thirty minutes with nobody associated** — and it stays down until the
  next boot. Re-raising it would undo the reason it went down; a reboot is a
  deliberate act by someone with physical access, which is the right bar for
  re-opening an unauthenticated surface.

A daemon shutdown takes it down without spending it, so the next boot raises it
again if setup is still unfinished.

### No routing off the AP segment

NetworkManager's shared mode enables forwarding and adds a masquerade rule,
because it assumes you want to share an internet connection. An unattended open
access point that routes traffic is a different object from one that serves a
setup page. `net.ipv4.conf.<iface>.forwarding=0` is the primary control, with a
best-effort nft/iptables FORWARD drop behind it.

### Credentials on an open network

The wizard presents an **SSH public key as the recommended credential**: a key
the operator already holds cannot be forgotten, and a public key is not a secret,
so sending it over an open network gives nothing away. A **password is the
fallback**, with the caveat stated inline that it travels in the clear and should
be changed once the node is on the operator's own Wi-Fi.
