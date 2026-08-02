# Vendored third-party assets

Everything the public page loads is served from this node. Nothing here is
fetched from a CDN at runtime, for two reasons that both matter on this device:

  - A hotspot frequently has no internet, or has it intermittently. A page whose
    QR code or map only renders when a third party is reachable is a page that
    fails at exactly the moment someone is standing in a car park trying to read
    it.
  - The public page ships a strict Content-Security-Policy with `default-src
    'self'`. A CDN would have to be allow-listed, which means trusting a third
    party with script execution on a page that anonymous visitors load.

Update procedure: fetch the release, verify the checksum below, copy the file in
unmodified, and update this file. Do not edit vendored sources in place — a local
patch that survives an update is how a dependency silently forks.

## qrcode-generator 1.4.4

| | |
|---|---|
| File | `qrcode.js` |
| Upstream | https://github.com/kazuhikoarase/qrcode-generator |
| Source | `https://registry.npmjs.org/qrcode-generator/-/qrcode-generator-1.4.4.tgz` (`package/qrcode.js`) |
| License | MIT — Copyright (c) 2009 Kazuhiko Arase (header retained in the file) |
| sha256 (tarball) | `ab6ed47d378877441deae95972e07b2716c26545a735a23aa6b9d442b33026ed` |
| sha256 (`qrcode.js`) | `18ae399f81182bc9de916e9c77b195df20cc58d6f2d55a62b085a299f1bf1780` |

Unmodified. Chosen because it has no dependencies, no build step, and generates
SVG directly — which is what lets the QR module work under a CSP that forbids
`data:` URIs and inline script.

"QR Code" is a registered trademark of DENSO WAVE INCORPORATED.
