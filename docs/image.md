# Flashing Waypoint and setting up your node

Waypoint ships a ready-to-flash SD-card image built on Raspberry Pi OS Lite
(Bookworm), containing the digital-voice stack, `waypointd` (dashboard,
supervisor and self-updater), and a hardened, appliance-grade OS posture.

The image has **no default password and no identity of its own**. A freshly
flashed node boots, raises its own temporary Wi-Fi network, asks you five
questions, and then hands you a dashboard to claim. Nothing is pre-seeded, so
there is nothing to change afterwards and nothing an attacker already knows.

You need a supported Pi, an SD card (8 GB or larger is comfortable), your modem
board, and a phone or laptop. Ethernet is convenient but not required.

| Board | Image to pick |
|---|---|
| Pi 4, Pi 3 / 3+ | **arm64** (best performance) or armhf |
| Pi Zero 2 W, Pi 2 | **armhf** |
| Pi Zero W, Pi 1 | *not supported* — see below |

**Pi Zero W and Pi 1 will not work.** They are ARMv6, and Debian's armhf port
targets ARMv7; the image will not boot. Use [Pi-Star](https://www.pistar.uk/) on
that hardware.

---

## 1. Download the image

Everything is on the **[latest release](https://github.com/KN4OQW/waypoint/releases/latest)**
page. Take the `.img.xz` for your architecture plus `SHA256SUMS` and
`SHA256SUMS.minisig`:

    waypoint-<version>-bookworm-arm64.img.xz
    waypoint-<version>-bookworm-armhf.img.xz

There is one release per version and it carries both images, so "the latest
image" is always whatever `releases/latest` points at. Do not unpack the `.xz` —
every flashing tool below reads it compressed.

## 2. Verify it before you flash

An SD-card image runs as root on a radio you leave switched on. Check it.

```console
$ sha256sum -c SHA256SUMS
waypoint-v0.2.0-shuri-bookworm-arm64.img.xz: OK

$ minisign -Vm SHA256SUMS -P RWRecbiMg67TbiFHluBimEaWz3fXBqGcDo4WZyfN4LHazgHxu2n2sfKd
Signature and comment signature verified
Trusted comment: waypoint v0.2.0-shuri image SHA256SUMS
```

`SHA256SUMS` covers both architectures, so if you downloaded only one you will
also see a `No such file or directory` line for the other. That is expected —
what matters is that the file you *do* have says `OK`. A checksum that fails, or
a signature that does not verify, means a corrupt or tampered download: delete it
and fetch it again. The public key is also in the repository as
[`docs/waypoint-release.pub`](https://github.com/KN4OQW/waypoint/blob/main/docs/waypoint-release.pub), and the signing arrangement
is described in [docs/signing.md](https://github.com/KN4OQW/waypoint/blob/main/docs/signing.md).

## 3. Write it to the card

Any of these is fine, because Waypoint sets itself up on first boot and needs
nothing configured at flash time:

- **[Raspberry Pi Imager](https://www.raspberrypi.com/software/)** — *Choose OS →
  Use custom* → the `.img.xz`.
- **[balenaEtcher](https://etcher.balena.io/)** — select the `.img.xz` and flash.
- **`dd`**, if you already know the incantation and which device your card is.

Imager will probably not offer you its advanced options (hostname, Wi-Fi, user)
for this image, and you do not need them. Imager 2.x only presents that panel for
images it has an OS manifest for, which a `.img.xz` you downloaded yourself does
not have. The wizard on the node asks for all of it instead — which is also why a
card written with plain `dd` works exactly as well.

## 4. First boot

Fit the card, connect the modem board, and power up. **The first boot takes one
to three minutes** while Raspberry Pi OS expands the filesystem and `waypointd`
mints the node's own TLS certificate.

Then the node is waiting for you, and there are two ways to reach it.

**Over the setup access point.** A node that has not been set up raises its own
Wi-Fi network — this happens whether or not it also has Ethernet:

    SSID     Waypoint-Setup-XXXX     (last four of the board serial)
    Address  http://10.42.0.1/

Join it from a phone. Your phone's own captive-portal sheet should open the
wizard by itself; if it does not, browse to `http://10.42.0.1/`. The four-digit
suffix is there for the case where several unconfigured nodes are powered up in
one room.

**Over Ethernet.** A freshly flashed node still answers to Raspberry Pi OS's
default name, so browse to **`https://raspberrypi.local/`** (or find its address
in your router's DHCP list). Your browser will warn about the self-signed
certificate — expected for a local appliance, and you will only be asked once per
node. Choosing a proper hostname is the wizard's first question.

The setup network is up for as long as it is needed and no longer: it goes away
the moment the node joins your Wi-Fi, immediately when setup completes, and after
thirty minutes if nobody ever associates. In that last case it stays down until
the next boot, so if you flashed a card and came back an hour later, power-cycle
the node.

### Locking down the setup network

It is open by default, which means anyone in radio range can reach the wizard
during those few minutes. The first device to load the page holds the session and
everyone else gets a refusal, but that is not authentication and does not pretend
to be.

If that bothers you — a hamfest, an apartment block — put a file called
**`waypoint-setup.txt`** on the small boot partition of the card before first
boot, containing one line:

    psk=my-setup-passphrase

Eight to sixty-three characters. A malformed file is ignored with a warning and
the network still comes up open, deliberately: a node that refuses to raise its
setup network over a typo is a node you cannot reach at all.

## 5. The setup wizard

Five screens, and the progress rail at the top shows where you are. Every step is
idempotent, so anything you retry converges rather than failing on "already
exists".

**Name this node.** The hostname you pick is how you will reach it —
`hs-shack` gives you `https://hs-shack.local/`. `raspberrypi`, `waypoint` and
`localhost` are refused, because two nodes keeping a shared default answer to the
same `.local` address and you get whichever replied first.

**Recovery account.** This is the Linux login you will SSH in with, and it always
gets sudo — it exists to administer a node whose root account is about to be
locked, so one that could not become root would be decoration. An **SSH public
key is the recommended credential**: you cannot forget a key you already hold, and
a public key is not a secret, so handing it over an open Wi-Fi network gives
nothing away. A password works as the fallback, but on the setup access point it
travels in the clear — change it once the node is on your own network.

If the card came out of somebody else's node, this screen also lists every
existing administrator account it can find, with what each one carries, and a
checkbox — unchecked — to remove it. Treat that as a way to clear the obvious and
not as an audit: it finds accounts in the sudo group and cannot find a modified
binary, a systemd unit or a key added to root. **If this board came from someone
else, reflashing the card is the only way to know what is on it.**

**SSH key**, if you set a password rather than a key on the previous screen. You
can skip it.

**Connect to Wi-Fi**, offered only when you are on the setup access point — a node
you reached over Ethernet already has a network and is not asked. It lists what
the node can see, and you can type a name for a network that does not broadcast
one. This is the last screen before the point of no return, which is the point:
finishing gives up the setup access point for good, so a Wi-Fi-only node that has
not joined a network by then has no way back onto one.

**Lock root and finish.** Root is locked behind the recovery account, the SSH
policy is settled, and the node records that it is set up.

Both the Wi-Fi join and the finish take the setup network away, so the page tells
you what to expect *before* it acts rather than leaving you on a reply that can
never arrive. After a join: if it worked, rejoin your own Wi-Fi and carry on at
the new address; if it failed, the setup network comes back within about a minute
and the wizard picks up where it left off.

Progress is written to the node before each step returns, so losing the
connection mid-setup costs you nothing — reconnect and you resume at the first
step you had not finished.

## 6. Claim the dashboard

Setup says what the node is; claiming says who administers it. They are separate,
and the claim always happens over HTTPS on your own network.

Browse to `https://<your-hostname>.local/` and you land on the **claim screen**.
Set the Waypoint admin username and password — this is the dashboard account, and
it is not the recovery account you just created. The first person to reach an
unclaimed node claims it, so do it now rather than leaving it sitting there.

**Waypoint passwords cannot be recovered.** They are stored as an argon2id hash
and nothing on the node can turn one back into a password. What you can do is
prove you own the hardware; the three routes are in
[docs/recovery.md](https://github.com/KN4OQW/waypoint/blob/main/docs/recovery.md).

## 7. After the claim

Configure the node from the dashboard: station identity and frequencies on the
General tab, then your modem, then the modes you want. Enabling a mode renders its
configuration and starts its gateway when you press Apply.

The OS then patches itself — `unattended-upgrades` applies security updates only,
never the kernel or firmware, and never reboots on its own. Waypoint's own
software updates on your terms from the **Updates** tab, health-gated and rolled
back automatically if the modem does not come back. See
[docs/updates.md](https://github.com/KN4OQW/waypoint/blob/main/docs/updates.md).

## Setting up without the wizard

If you are bringing up your fifth node, building a batch for a club, or
reflashing the same box while chasing a bug, you have the card in a reader
anyway. Drop a **`waypoint.toml`** on the boot partition naming the hostname,
the account and the Wi-Fi, and the node provisions itself on first boot with no
access point and no typing. It runs the same wizard steps and the same
validators, it does not claim the node, and it deletes the file once it has used
it. The schema and the full list of what it will and will not do are in
[docs/provisioning.md](https://github.com/KN4OQW/waypoint/blob/main/docs/provisioning.md#the-fast-path-provisioning-from-the-boot-partition).

## When you cannot find the node

- **No `Waypoint-Setup-` network.** Either setup has already been completed on
  this card, or the thirty-minute window expired — power-cycle the node and look
  again within half an hour.
- **`raspberrypi.local` does not resolve.** Some networks and some Windows
  installations do not do mDNS. Use the address from your router's DHCP list.
- **The page says the node has not been set up yet.** That is the wizard's gate
  answering the dashboard's API. Go to `/` and finish setup.
- **The browser refuses to load it.** Waypoint serves HTTPS with a certificate
  the node signed itself, which every browser objects to the first time. Proceed
  past the warning — the alternative would be a hotspot that took your claim
  password in the clear.
- **A node that used to work and has vanished** after a router change or an SSID
  rename raises its setup access point again by itself, about ten minutes after
  it loses its route out. It stays set up and stays claimed; join the network and
  hand it the new Wi-Fi.
