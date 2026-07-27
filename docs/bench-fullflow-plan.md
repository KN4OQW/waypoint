# Full first-boot flow test — bench Pi 3

The run this plan is written for is the first one where every step has a fix in
it that has never met hardware. Previous bench runs exercised the access point on
a board that had been up for hours; this is the first end-to-end pass on a node
that has just been plugged in, and the first that goes Wi-Fi-only.

**Do not attach Ethernet.** Ethernet is what rescued the last two runs, and it
hides the failure that matters most: a Wi-Fi-only node that finishes setup
without joining a network has no way back onto one. If a step fails and you need
a way in, plug it in *then*, and note in the results that the step was rescued.

## Before flashing

Have to hand:

- The Wi-Fi SSID and passphrase for the network the node should end on.
- A phone or laptop that can join an open network.
- The board's LCD wired to I2C, if you want the panel checked (optional; it is
  probed for, so its absence is not a failure).

## What is being tested that never has been

| | Why it is new |
|---|---|
| Cold-boot access point | Every prior pass ran on a long-running board |
| `userconfig.service` masked | The last image never finished booting without it |
| Wi-Fi station join from the wizard | The UI step did not exist until now |
| Finish without stranding the node | Finish used to spend the AP mid-response |
| LCD setup + completion screens | Never run on a panel |
| Boot-partition log | Never written on real hardware |

## Steps

Record pass/fail and anything surprising against each.

### 1. Boot

Flash, insert, power on, **no Ethernet**.

- [ ] Console reaches a login prompt, **not** a "create a user" dialog.
      A dialog here means the `userconfig.service` mask did not take, and
      everything below is blocked.
- [ ] Within ~90s, `Waypoint-Setup-9E10` is visible from a phone.
- [ ] LCD (if wired) shows the setup network and the address.

If no SSID appears: pull the card and read `/boot/firmware/waypoint-setup.log`
from any laptop. That file is the point of this run — if it is empty or absent
when the AP failed, that is itself a finding worth reporting.

### 2. Portal

- [ ] Joining the network opens the captive sheet by itself. **Never verified on
      a handset.** If it does not, browse to `http://10.42.0.1/` — that
      distinction says whether the problem is the hijack or the OS probe.
- [ ] The wizard renders with real fields.
- [ ] The open-network caveat appears on the account step.

### 3. Setup

- [ ] Hostname accepted; the default name is refused with a clear reason.
- [ ] Account created; SSH key accepted (paste a real public key).
- [ ] Prior-admin enumeration lists nothing on a fresh image.

### 4. Wi-Fi join — the step that has never run

- [ ] A "Connect to Wi-Fi" screen appears before the finish step.
- [ ] Entering the SSID and passphrase and pressing Connect shows the handover
      explanation immediately, rather than a spinner.
- [ ] **The setup network disappears.** Expected: one radio cannot do both.
- [ ] Rejoining your own Wi-Fi, `https://<hostname>.local/` reaches the node.

Then the failure path, which is the half more likely to be wrong:

- [ ] Repeat with a **deliberately wrong passphrase**. The setup network should
      come back within about a minute, and the page should say the join failed
      rather than reporting success or hanging.

### 5. Finish

- [ ] Pressing "Lock root and finish" returns a completion screen — **not** a
      stuck "Finishing…". This is the bug from the last run.
- [ ] The completion screen names where the node moved to.
- [ ] LCD switches to "Setup complete" and stops advertising the setup SSID.

### 6. After setup

- [ ] `https://<hostname>.local/` serves the claim page.
- [ ] `ssh <account>@<hostname>.local` works with the key.
- [ ] `sudo -i` works from that account.
- [ ] `passwd -S root` reports `L`, and `sshd -T | grep permitrootlogin` reports
      `no`.
- [ ] Reboot. The node comes back on Wi-Fi, still provisioned, and does **not**
      raise the setup access point again.

### 7. Recovery paths

- [ ] `reset-claim` returns to the claim gate without undoing provisioning.
- [ ] `reset-claim --full` returns the node to first-boot setup, and the setup
      access point comes back after a reboot.

## Known gaps going in

Stated so a failure here is recognised rather than investigated from scratch:

- **`netwatch` cannot recover a node whose AP was spent by setup completion.**
  `Reraise` refuses while `spent` is set, so a node that finishes with no network
  needs a reboot — after which `spent` is clear and netwatch re-raises. Whether
  netwatch should override `spent` is an open decision, not an oversight.
- The image ships neither `nft` nor `iptables`, so the belt-and-braces FORWARD
  drop cannot install. The per-interface forwarding sysctl — the primary control
  — does apply.
- The image is signed with a throwaway key and will refuse a real release. It is
  a bench image.
