# RFC-0012: HTTPS by Default

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #11 (HTTPS by default — self-signed at claim; ACME optional)
- Depends on: RFC-0002 (the auth posture — the session cookie's `Secure` flag and the claim flow this makes safe over TLS)

## Summary

Serve the dashboard over **HTTPS out of the box**. On first start the daemon mints
a **self-signed device certificate** (load-or-create, long-lived) and serves TLS
with it; the session cookie's `Secure` flag turns on automatically whenever TLS is
active; and a small HTTP listener **301-redirects to HTTPS** so a bare `http://…`
lands on the encrypted site. The phone flow is *one* trust prompt (accept the
device's self-signed cert once), documented. For a node with a real public
hostname, **optional Let's Encrypt** (ACME via autocert, already a transitive
dependency) provisions a browser-trusted cert with no prompt at all.

Today the daemon serves plaintext HTTP and the cookie's `Secure` flag defaults
off, so the claim/login password and the session cookie cross the network in the
clear. This RFC closes that — the single most-requested Pi-Star security gap
(forum t=3790, never shipped) and the concrete exposure a security review of a
LAN-deployed node surfaces first.

## Motivation

The auth core is already strong (RFC-0002: argon2id, hashed CSPRNG session tokens,
default-deny gate, no default credentials). None of it matters on the wire if the
transport is plaintext: an attacker on the same LAN segment sniffs the login POST
or replays the `Secure`-less session cookie. "Passwords cross home networks in
cleartext today" is the requirement's own framing, and it is exactly what a review
of `http://<node>/` flags as HIGH.

HTTPS on an appliance has one wrinkle — there is no public CA for a box on
`192.168.x.x` — which is why the incumbents never shipped it: a self-signed cert
triggers a browser warning, and operators (reasonably) hate warnings. The answer
is not to avoid TLS; it is to make the self-signed path a **single, documented,
one-time** trust decision, and to offer real ACME certs for the minority of nodes
that are publicly reachable. Encryption-with-one-prompt beats cleartext-with-none.

## Design

### The device certificate (self-signed, load-or-create)

A new package `internal/tlscert` owns the device cert:

- `LoadOrCreate(dir)` returns a `tls.Certificate`. If `dir/cert.pem` + `dir/key.pem`
  exist and parse, they are loaded; otherwise a fresh cert is **generated and
  persisted** (key `0600`). So the cert is minted once (first boot / first TLS
  start) and stable across restarts — a returning phone re-trusts nothing.
- The key is **ECDSA P-256** (small, fast on a Pi, universally supported), the cert
  is **self-signed** (its own CA), valid ~**10 years** (an appliance cert; rotation
  is a reflash, not a renewal cadence), with `KeyUsage` for digital-signature +
  key-encipherment and `ExtKeyUsage: ServerAuth`.
- **SANs** cover how the box is actually reached: the OS hostname, `<hostname>.local`
  (mDNS), `waypoint.local`, `localhost`, and every non-loopback IP the host
  currently has (v4 and v6), plus `127.0.0.1`/`::1`. IP SANs are what let
  `https://192.168.1.50/` validate against the cert the phone trusted, so the trust
  prompt is genuinely one-time even when the operator reaches the box by IP.

The generated cert is not a secret to protect beyond its key — it *is* the trust
anchor the operator pins, so it lives next to the config store (a `-tls-dir` flag,
default the store's sibling).

### Serving HTTPS

The main listener serves TLS: `http.Server.TLSConfig` holds the device cert (or the
ACME manager's `GetCertificate`), and `ListenAndServeTLS("", "")` runs it. TLS is on
by default (`-tls`, default `true`); `-tls=false` restores plaintext for a node
that sits behind a TLS-terminating reverse proxy (which then owns the cert and the
`Secure` cookie). `TLSConfig.MinVersion` is **TLS 1.2**.

### Secure cookie, automatically

The session cookie's `Secure` flag (RFC-0002 `setCookie`) turns **on automatically
whenever the daemon is serving TLS** — the operator never has to remember the
`-secure-cookie` flag, and it can't drift out of sync with the transport. When
`-tls=false` (proxy terminates TLS) the operator sets `-secure-cookie` explicitly,
because only they know the proxy speaks HTTPS. So: TLS on ⇒ secure cookie on; TLS
off ⇒ secure cookie follows the flag (default off, for a plain-HTTP dev run).

### HTTP → HTTPS redirect

A second, tiny listener on `-http-redirect-addr` (e.g. `:80`) answers every request
with a **301 to the `https://` form of the same host+path** — so an operator who
types `http://waypoint.local/` or `http://192.168.1.50/` is bounced to the secure
site rather than hitting a dead port. It serves nothing else (no assets, no API),
so it is not an unencrypted surface. Empty `-http-redirect-addr` disables it (for a
node where something else owns :80, or a proxy handles the redirect).

The redirect preserves the host and swaps only the scheme (and the port, when the
HTTPS port differs from 443 — the redirect targets the configured HTTPS host:port),
so it works on the non-standard ports an appliance often uses.

### Optional ACME (Let's Encrypt)

For the minority of nodes with a real, publicly-resolvable hostname, `-acme-domain`
(+ `-acme-email` for the ACME account, `-acme-dir` for the cert cache) switches the
main listener to `golang.org/x/crypto/acme/autocert` — already a transitive
dependency, so no new module. autocert provisions and auto-renews a
**browser-trusted** cert (zero trust prompt), serving the ACME HTTP-01 challenge on
the redirect listener's :80. ACME is **off by default** (a hotspot is usually not
publicly reachable, and Let's Encrypt can't issue for a private IP); when a domain
is set, autocert replaces the self-signed cert as the main cert source. The
self-signed path remains the fallback the moment ACME is not configured.

### Pre-claim vs post-claim

The requirement's acceptance is *post-claim HTTPS-only*, but the safer reading —
which this RFC takes — is **HTTPS from first start**, so even the claim (which
carries the new password) happens over TLS. The cert is generated on first start
regardless of claim state; "minted at claim time" is satisfied and strengthened
(the claim itself is encrypted). The operator sees the one self-signed-trust prompt
when they first open the box to claim it, then never again.

## The contract (test harness)

Automated (Go):

1. **Cert generation.** `LoadOrCreate` on an empty dir produces a valid, parseable,
   self-signed ECDSA cert whose SANs include `localhost`, `waypoint.local`, and at
   least one IP, with `ExtKeyUsage: ServerAuth` and a multi-year validity.
2. **Load-or-create idempotence.** A second `LoadOrCreate` on the same dir returns
   the **same** cert (same serial/key), not a new one — a restart never re-mints and
   never invalidates the operator's trust.
3. **Key permissions.** The persisted key file is mode `0600`.
4. **Redirect.** The HTTP redirect handler answers `http://host/path?q` with `301`
   to `https://host/path?q` (scheme swapped, host+path+query preserved, HTTPS port
   applied when non-default), and serves no other content.
5. **Secure cookie under TLS.** With TLS active the auth cookie is written with
   `Secure: true` (asserted through the existing `setCookie` seam), and with `-tls`
   off it follows the flag.

Manual (the #11 acceptance): claim and use the dashboard over `https://`; a bare
`http://` redirects; a phone shows exactly **one** trust prompt (documented in
`docs/tls.md`); with `-acme-domain` on a resolvable host, no prompt at all.

## Alternatives considered

- **Stay HTTP; tell operators to add a reverse proxy.** Rejected — it is the
  incumbent non-answer and the requirement is *out of the box*. The `-tls=false`
  escape hatch keeps the proxy option for those who want it, but the default is
  encrypted.
- **A bundled static self-signed cert shipped in the image.** Rejected — a shared
  private key across every device is a catastrophe (one extraction compromises all).
  Per-device generation is mandatory; it costs milliseconds on first boot.
- **A Waypoint-run private CA the operator installs.** Rejected as the default —
  installing a CA cert on a phone is *more* friction than accepting one leaf cert
  once, and a CA the box holds the key to is a bigger blast radius. (An operator who
  wants a CA can front with their own; out of scope.)
- **Short-lived certs with rotation.** Rejected for the self-signed appliance case —
  rotation re-triggers the trust prompt. A long-lived leaf the operator pins once is
  the right ergonomics; ACME (which auto-renews *trusted* certs) covers the
  rotate-often case without prompts.
- **RSA keys.** ECDSA P-256 is smaller and faster on a Pi with universal client
  support; RSA buys nothing here.

## Open questions

1. **mDNS/`waypoint.local` advertisement.** The cert carries `waypoint.local` in its
   SANs, but the daemon does not (yet) advertise that name over mDNS. Bundling an
   mDNS responder so `https://waypoint.local/` resolves without the operator knowing
   the IP is a strong follow-up; the SAN is ready for it.
2. **Cert regeneration on hostname/IP change.** The SANs are fixed at generation. A
   node whose IP changes still validates (the operator trusted the leaf, and IP
   SANs are a convenience), but adding the new IP would need a regenerate — tie it to
   an explicit `reset-tls` action rather than auto-regenerating (which would
   re-prompt). Deferred.
3. **HSTS.** Once HTTPS is the default, sending `Strict-Transport-Security` would
   pin browsers to HTTPS — good, but it can strand an operator who later runs
   `-tls=false`. Leaning: add HSTS with a short max-age when TLS is on, revisited
   with the updates lifecycle.
4. **Trust-prompt UX copy.** The one prompt is a browser-native dialog we cannot
   restyle; `docs/tls.md` documents what the operator will see per platform. A
   future first-run hint could pre-warn them.
