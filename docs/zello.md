# Bridging a bus to Zello

Waypoint can carry a mode bus to a [Zello](https://zello.com) channel, so people
on a phone hear what is on the air and can answer it.

This page is the setup. It takes about ten minutes and needs three things: a
Zello account **that belongs to the node and nothing else**, a key pair from
Zello's developer portal, and the AMBE+2 firmware images the vocoder needs.

## Before you start: the node needs its own Zello account

**Do not use your own Zello account.** Zello allows one session per account, so a
node signed in as you signs you out of your phone, and signing back in on your
phone signs the node out. The two will fight for as long as both are running.

Create a second account for the node. It is a normal Zello account made in the
app like any other — the only thing that makes it a bridge account is that only
the node ever signs in to it.

There is no anonymous option. Even a listen-only bridge needs its own account
with a username and a password; Zello refuses a connection that does not have
one.

## 1. Create the account and give it access

In the Zello app, signed out of your own account or on a second device:

1. Create the account. Give it a name that reads as a machine — `kn4oqw-bridge`
   or `n0call-waypoint` — so people in a channel can tell it is a gateway and not a
   person.
2. Confirm the account by whatever means Zello asks for.
3. Add it to each channel you want the node to bridge, and note the channel name
   exactly as the app shows it. Both of these fail silently: if the account
   cannot join a channel, or the name is even slightly wrong, the node connects
   normally and the channel simply stays offline forever with no error. If the
   node reports a channel that never came online, check the spelling first.

Then sign out of it on the phone and leave it alone. Anything signed in to it
will disconnect the node.

## 2. Mint an API key

Zello's Channel API authenticates with a signed token rather than the account
password alone.

1. Go to [developers.zello.com](https://developers.zello.com) and sign in **as
   the node's account**, not yours.
2. Complete the developer profile if it asks.
3. **Keys → Add Key.**
4. Copy the **Issuer** and the **Private Key**. Use Select All on each — the
   private key runs to many lines and a partial copy fails in a way that looks
   like a bad password.

You will also see a **Sample Development Token**. You do not need it, and you
should not use it — see below.

## 3. Enter it on the node

Everything is on **Settings → Gateways**, below the buses.

Under **Zello accounts**, press *Add Zello account* and fill in:

| Field | Value |
|---|---|
| Name | anything; it is how channels refer to this account |
| Zello username | the node's Zello account name |
| Zello password | that account's password |
| Issuer | from the developer portal |
| Private key | from the developer portal |
| Token | leave empty — see below |

Enable it. If the issuer and private key are both set, the card says the account
signs its own tokens and nothing expires. If you used the token field instead, it
warns you that the token stops working after 30 days.

The three secrets are write-only. Once saved they are never shown again — the
fields read *stored — leave blank to keep it*, and leaving them blank on a later
edit keeps what is there. To change one, type the new value over it.

Then, on the bus you want to bridge, press **Add Zello channel** and set the
channel name exactly as the Zello app shows it, pick the account, and enable it.
*Packet size* can stay at 60 ms, which is Zello's own default; 20 ms trades
bandwidth for a little less delay. *Listen only* joins without transmitting.

Press **Apply** when you are done. The node refuses a configuration it could not
run — a channel with no account, an account with no username, a packet size Opus
does not have — and says which, so nothing invalid reaches the daemon.

### Why not the Sample Development Token

Because it expires after 30 days, silently. The node keeps running, the channel
goes quiet, and nothing says why until somebody mentions it.

With the Issuer and Private Key, the node signs its own token every time it
connects — valid for sixty seconds, minted fresh, never stored. Nothing expires
and there is nothing to renew. That is how Zello's own reference implementation
does it.

The token field exists for anyone who only has the sample token. If you have the
key material, leave it empty.

## 4. Vocoder firmware

Zello carries Opus; the digital modes carry AMBE+2. Bridging them means decoding
and re-encoding voice, and the AMBE+2 vocoder is patented — Waypoint cannot ship
it, and does not.

**You supply the two firmware images.** Put them here, readable by the node:

	/var/lib/waypoint/vocoder/md380fw.img
	/var/lib/waypoint/vocoder/md380ram.img

They are not downloaded for you and they are in no Waypoint release, image or
package. Obtaining them is your decision and your act; nothing about it is
automated, and the node makes no request to get them.

Until both files are there, a bus with a Zello channel refuses to start and says
which file is missing and where it expected it.

The first image is the unwrapped MD380 firmware; the second is its matching SRAM
core. The tooling in the md380tools project produces both.

## What it sounds like, and what it cannot do

**Everyone on RF arrives as one Zello user.** A Zello connection has one
identity, so every operator the node relays reaches the channel as the node's
account. Individual callsigns cannot show up as separate Zello senders — that is
Zello's model, not a limitation Waypoint can code around. The node's account name
is what the channel sees, which is why it should read as a machine.

**Zello talkers show up by name on RF — if the DMR relay is on.** Each inbound
transmission carries the Zello username as a Talker Alias, so a receiving radio
shows who is talking rather than the node's bare DMR ID. Two settings under **DMR**
have to be there for it: a **Talker Alias** template, and the **DMR message
relay** switched on. The relay is the only place on the node an alias can be added
to the DMR path, so with it off the name is silently lost — the DMR panel warns
when that is the case. Voice works in both directions either way.

**One at a time, in both directions.** A bus carries a single talker. If somebody
keys up on RF while a Zello user is talking, the second one is dropped and the
node reports the bus busy, exactly as it does between two radio modes.

**Audio has been through two codecs.** AMBE+2 at 8 kHz and then Opus, or the
reverse. It is intelligible, not hi-fi, and it will not sound as good as either
side does natively.

## Things that will go wrong

| What you see | What it means |
|---|---|
| You keep getting signed out of Zello | The node is using your account. Give it its own. |
| `invalid username` | No Zello account by that name. Check the spelling against the app. |
| `no permission` | The account exists but the password is missing or wrong. |
| `not enough params` | No token and no key material — the node has nothing to authenticate with. |
| `not authorized` | The token is not valid. If it is a sample token, it has probably expired. |
| `channel is not ready` | The channel has not come online yet. The node waits and retries. |
| The channel stays offline and never comes online | The node cannot join that channel — either the name is not exactly right, or the account is not a member. Check the name character for character against the app first; it is the more common of the two and neither produces an error. |
| `on_error` right after connecting | No channel by that name at all. |
| The channel is silent after a month | A sample development token expired. Switch to the Issuer and Private Key. |
| The bus will not start and names a missing image | The vocoder firmware is not in `/var/lib/waypoint/vocoder/`. See section 4 — you supply those files. |

## Privacy

The node connects to Zello's servers only, and only when you have configured a
channel. Nothing about Zello bridging reports to Waypoint's own infrastructure.

The account password and the private key are stored on the node and never
displayed again — the settings page shows only whether they are set. They are
excluded from exported profiles, so a profile shared with another operator does
not carry them.
