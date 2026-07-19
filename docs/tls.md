# HTTPS / TLS

Waypoint serves the dashboard over **HTTPS out of the box** (RFC-0012). On first
start it mints a **per-device, self-signed certificate**, serves TLS with it, and
turns the session cookie's `Secure` flag on automatically. A bare `http://…`
request is redirected to HTTPS. So the claim/login password and the session cookie
are never sent in cleartext.

Design & rationale: [RFC-0012](rfcs/0012-https-by-default.md).

## The one-time trust prompt (self-signed)

Because a hotspot on a private address (`192.168.x.x`) can't get a public CA cert,
the device cert is **self-signed**. The first time you open the dashboard your
browser shows a certificate warning — this is expected. Accept it **once** and the
browser remembers it:

- **Chrome / Edge (desktop):** "Your connection is not private" → **Advanced** →
  **Proceed to `<host>` (unsafe)**.
- **Firefox:** "Warning: Potential Security Risk" → **Advanced…** → **Accept the
  Risk and Continue**.
- **Safari (macOS):** **Show Details** → **visit this website** → **Visit Website**.
- **iOS Safari / Android Chrome:** tap **Advanced** / **Details** → **proceed**.

The certificate's Subject Alternative Names include the hostname, `<hostname>.local`,
`waypoint.local`, `localhost`, and **every local IP the device has** — so reaching
the box by IP (`https://192.168.1.50/`) validates against the same cert you
trusted, and you are prompted only once, not per-address. The cert is persisted and
reused across restarts, so a reboot never re-prompts.

## Certificate location

The self-signed cert and key live in `-tls-dir` (default
`/home/pi-star/waypoint/tls`): `cert.pem` (public, `0644`) and `key.pem` (private,
`0600`). Delete both and restart to mint a fresh cert (you will re-trust it once).

## Behind a reverse proxy

If you terminate TLS at a reverse proxy (nginx, Caddy, Traefik), run waypointd with
`-tls=false` so it serves plaintext to the proxy, and set `-secure-cookie` (the
proxy speaks HTTPS to the browser, so the cookie should still be `Secure`). The
proxy owns the certificate and the HTTP→HTTPS redirect in that setup.

## Let's Encrypt (public hostnames)

If the node has a **real, publicly-resolvable hostname** with ports 80 and 443
reachable from the internet, use Let's Encrypt for a browser-trusted cert with **no
prompt at all**:

```
waypointd -acme-domain hotspot.example.com -acme-email you@example.com
```

waypointd provisions and auto-renews the certificate via ACME (HTTP-01 on `:80`),
caching it in `-acme-dir`. This replaces the self-signed cert. Let's Encrypt cannot
issue certificates for private IPs, so this is only for genuinely reachable hosts.

## Flags

| Flag | Default | Purpose |
|---|---|---|
| `-tls` | `true` | Serve HTTPS with the self-signed device cert. |
| `-tls-dir` | `…/waypoint/tls` | Where the device cert/key live. |
| `-http-redirect-addr` | *(empty)* | HTTP listener that 301-redirects to HTTPS (e.g. `:80`). |
| `-acme-domain` | *(empty)* | Public hostname for a Let's Encrypt cert instead of self-signed. |
| `-acme-email` | *(empty)* | ACME account contact email. |
| `-acme-dir` | `…/waypoint/acme` | Let's Encrypt cert cache. |
| `-secure-cookie` | `false` | Force the `Secure` cookie flag (auto-on under `-tls`; set it manually behind a TLS proxy). |
