# Locked out? Getting back into your node

You cannot recover a Waypoint password. There is no reset link, no recovery
email, and no back door — the admin credential is stored as an argon2id hash and
nothing on the node can turn it back into a password. Support cannot help you,
and neither can we.

What you can do is **prove you own the hardware** and take the node back. There
are three ways, and which one you want depends on what you can still reach.

| You still have… | Use | What it costs you |
|---|---|---|
| A browser tab that is still logged in | [Change the password](#1-you-are-still-logged-in-somewhere) | nothing |
| An SSH shell on the node | [`waypointd reset-claim`](#2-you-have-a-shell-on-the-node) | you pick a new admin username and password |
| Only the SD card and a card reader | [The boot-partition reset file](#3-you-only-have-the-sd-card) | the node forgets its whole setup and runs first-boot again |

None of these is a security hole. Each one requires something an attacker on the
internet does not have: a live session, a shell on the box, or the card in your
hand. That is the same reasoning [RFC-0002](https://github.com/KN4OQW/waypoint/discussions/156) uses.

---

## 1. You are still logged in somewhere

Easiest by far, and worth checking before anything else — a phone or a second
browser often still has a valid session.

**Settings → Station Settings → Administrator → Change password.**

Changing the password signs out every other session, which is also what you want
if the reason you are here is that someone else knows it.

---

## 2. You have a shell on the node

Log in over SSH and run:

```sh
sudo waypointd reset-claim
```

**`sudo` is not optional.** The configuration store belongs to root, and without
it you get:

```
reset-claim: attempt to write a readonly database (8)
```

which looks like a broken database and is really a missing word at the front of
the command.

You should see something like:

```
reset-claim: wiped admin credential (admin "bench"), revoked 1 session(s),
cleared claimed_at — device returned to claim mode (store /var/lib/waypoint/config.db)
```

Now open the dashboard in a browser. Within a few seconds it shows the **claim
screen** instead of the login form, and the first person to reach it sets the new
username and password — so do it now rather than leaving the node sitting
unclaimed on your network.

If the login form is still there after a few seconds, restart the daemon:

```sh
sudo systemctl restart waypointd
```

<details>
<summary>Why a restart is ever needed</summary>

The daemon keeps the claim state in memory and re-reads it every few seconds.
`reset-claim` is a separate program writing the same database, so there is a
short window where the running daemon has not noticed yet. Waypoint builds
before this behaviour existed cached it *forever*, and on those the restart is
required rather than optional.
</details>

### What it does and does not touch

Only the dashboard login. The node stays set up: same hostname, same recovery
account, same Wi-Fi, same radio configuration, same reflectors. It does not go
off the air.

---

## 3. You only have the SD card

This is the path for a node you cannot log into at all — a forgotten password on
a box with no shell, or a second-hand hotspot arriving with somebody else's setup
on it.

1. Power the node down and put its SD card in a reader.
2. On the small **boot** partition (the one Windows and macOS will mount — it
   contains `config.txt`), create an empty file named:

   ```
   waypoint-reset
   ```

   No extension, no contents. On Windows, watch for Notepad silently saving it as
   `waypoint-reset.txt`, which will not work.
3. Put the card back and power the node on.

On boot the daemon finds the file, **fully resets the node**, deletes the file,
and logs it loudly. The node comes up believing it has never been set up: it
serves the first-boot wizard and raises its own setup Wi-Fi access point, exactly
like a new install.

### What this one costs

More than the shell route, deliberately. It clears the admin credential *and*
the node's provisioning state, so first-boot setup runs again — you will be asked
for the hostname and the rest.

What it does **not** do is undo changes already made to the system: the hostname
stays as it is until you set a new one, the recovery account stays, and root
stays locked. A reset that reverted half of those and not the others would leave
a node in a worse state than either end. Re-running setup converges all of it.

Your radio configuration, networks and event history are **not** erased.

---

## Still stuck?

If none of the three applies — no session, no shell, no physical access — then
you do not currently have a way in, and that is the system working as intended.
Get physical access to the card.

If you have physical access and the boot-partition file did nothing, check:

- the file is on the **boot** partition, not the large Linux one;
- it is named exactly `waypoint-reset` with no extension;
- the node actually rebooted with that card in it.

Waypoint checks both `/boot/waypoint-reset` and `/boot/firmware/waypoint-reset`,
because Raspberry Pi OS moved the boot mount in Bookworm — you do not need to
work out which one your image uses.
