# Release signing & verified downloads

Waypoint makes two things it pulls from the network **tamper-evident** (RFC-0013):
the software it updates to, and the reference data it downloads (host lists,
talkgroup names). Both use **minisign** (Ed25519) — a tiny, offline-verifiable
signature format with a 56-byte public key you can pin.

## Verifying a release artifact

Every released binary ships with a `<file>.minisig` signature —
`waypointd-linux-<arch>`, and alongside it `waypoint-provision-helper-linux-<arch>`
and `waypoint-bus-linux-<arch>`, which the image module verifies against the same
key before baking them in. Verify one before running an update:

```
# with the bundled Waypoint verifier (no extra tools):
waypointd -verify waypointd-linux-arm6 -verify-pubkey docs/waypoint-release.pub
# -> "verify: OK — … is signed by the trusted key"   (exit 0)
# a tampered file -> "verify: REJECTED: …"            (exit 1)

# or with the standard minisign tool:
minisign -Vm waypointd-linux-arm6 -p docs/waypoint-release.pub
```

The atomic updater (#13) runs this verification automatically and **refuses to
switch** to a release whose signature does not verify — so a tampered artifact is
rejected with a clear error, never applied.

## Maintainer: generating the release key

The release keypair is generated once by the maintainer. The **public** key is
committed to the repo (`docs/waypoint-release.pub`) and bundled so anyone can
verify offline; the **secret** key never touches the repo — it lives only as a CI
secret.

```
minisign -G -p docs/waypoint-release.pub -s waypoint-release.key
# choose a strong password when prompted
```

Then configure the CI secrets (Settings → Secrets and variables → Actions):

- `MINISIGN_SECRET_KEY` — the full contents of `waypoint-release.key`.
- `MINISIGN_PASSWORD` — the password you chose.

Commit `docs/waypoint-release.pub` and delete `waypoint-release.key` from disk
(keep an offline backup). The [release workflow](../.github/workflows/release.yml)
signs each artifact on a `v*` tag with `minisign -S -H` (prehashed BLAKE2b, the
variant Waypoint's verifier expects) and a trusted comment binding the version +
filename into the signature. Without the secret configured the workflow still
builds and uploads **unsigned** artifacts (it warns, it does not fail), so a fork
or a pre-key repo is not blocked.

## Verified reference-data downloads

The DMR host list and talkgroup-name list refreshers verify a signature before a
download replaces the cache, when a trusted key is configured:

```
waypointd -hostfile-pubkey /path/to/trusted.pub          # verify when a <url>.minisig exists
waypointd -hostfile-pubkey /path/to/trusted.pub -require-signed-hostfiles  # reject anything unverified
```

With no key configured the lists are fetched as before (community mirrors do not
sign today). Verification lights up fully once a source publishes signatures —
the natural home is a Waypoint-hosted, signed mirror of the reference data. A
verification failure is logged clearly and the **previous cache is kept**, so a
tampered list is never adopted.

## Why minisign, not Sigstore

An appliance may be offline; Sigstore's keyless model needs OIDC and a
transparency log at verify time. minisign's pinned public key verifies offline
with a few hundred lines of Go (Ed25519 is in the standard library; BLAKE2b is
already a dependency), which is the right fit for a Pi. See RFC-0013 for the full
rationale.

## Hostfiles

The reflector, master and talkgroup lists published to `hostfiles.kn4oqw.com` are
signed the same way releases are, by `.github/workflows/hostfiles.yml`: prehashed
minisign (`-H`) with the same `MINISIGN_SECRET_KEY` / `MINISIGN_PASSWORD` secrets,
producing a `<file>.minisig` alongside each list.

A node verifies them only when it is configured with the matching public key:

```sh
waypointd -hostfile-pubkey /usr/share/waypoint/waypoint-release.pub
```

Without that flag the lists are fetched unverified, exactly as before — signing
adds an integrity guarantee for operators who want it rather than a hard
requirement. `-require-signed-hostfiles` makes it mandatory, so an unsigned or
tampered list is rejected and the previous cache kept.

This matters more for hostfiles than it looks: they are operational data fetched
over plain HTTP from a host the node does not authenticate, and they contain the
addresses the node will connect to. Signing is what makes republishing them from
the project's own bucket a security improvement rather than merely an
availability one.
