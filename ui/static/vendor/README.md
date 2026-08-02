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

## Leaflet 1.9.4

| | |
|---|---|
| Files | `leaflet/leaflet.js`, `leaflet/leaflet.css`, `leaflet/images/*.png` |
| Upstream | https://leafletjs.com — https://github.com/Leaflet/Leaflet |
| Source | `https://unpkg.com/leaflet@1.9.4/dist/…` |
| License | BSD-2-Clause — (c) 2010-2023 Vladimir Agafonkin, (c) 2010-2011 CloudMade (header retained in `leaflet.js`) |
| sha256 `leaflet.js` | `db49d009c841f5ca34a888c96511ae936fd9f5533e90d8b2c4d57596f4e5641a` |
| sha256 `leaflet.css` | `a7837102824184820dfa198d1ebcd109ff6d0ff9a2672a074b9a1b4d147d04c6` |
| sha256 `images/layers.png` | `1dbbe9d028e292f36fcba8f8b3a28d5e8932754fc2215b9ac69e4cdecf5107c6` |
| sha256 `images/layers-2x.png` | `066daca850d8ffbef007af00b06eac0015728dee279c51f3cb6c716df7c42edf` |
| sha256 `images/marker-icon.png` | `574c3a5cca85f4114085b6841596d62f00d7c892c7b03f28cbfa301deb1dc437` |
| sha256 `images/marker-icon-2x.png` | `00179c4c1ee830d3a108412ae0d294f55776cfeb085c60129a39aa6fc4ae2528` |
| sha256 `images/marker-shadow.png` | `264f5c640339f042dd729062cfc04c17f8ea0f29882b538e3848ed8f10edb4da` |

Unmodified. The `images/` directory is required: `leaflet.css` references
`images/layers.png`, `images/layers-2x.png` and `images/marker-icon.png` by
relative URL, so the CSS must be served from a path that has `images/` beside it
or the layer control renders blank.

Map **tiles** are a separate matter and are NOT vendored — they are fetched from
OpenStreetMap at run time, which is the only way a map can work. That is the one
deliberate exception to this directory's no-external-requests rule, and the public
page's CSP names `tile.openstreetmap.org` explicitly rather than opening `img-src`
to `https:`. See the OSM tile usage policy notes in cmd/waypointd/mapview.go.
