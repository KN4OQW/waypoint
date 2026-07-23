#!/usr/bin/env python3
"""Generate os_list_waypoint.json — a Raspberry Pi Imager custom-repository OS
list — from a release tag and the imager-meta.json emitted by the image CI.

The image CI (build-image.yml) already computes the four sizes/hashes Imager
needs (compressed .xz + uncompressed .img) and stores them in imager-meta.json,
so this generator never downloads or decompresses anything.

Schema: Raspberry Pi Imager "OS list" JSON Schema (Draft-07). The upstream schema
lives on the rpi-imager `main` branch and is NOT versioned, so we vendor a pinned
copy (scripts/os-list-schema.json) and validate against it; see docs/image.md for
the maintenance note.

Usage:
  gen-imager-json.py --meta imager-meta.json --tag v1.0.0 --repo KN4OQW/waypoint \
      --out os_list_waypoint.json [--validate] [--schema scripts/os-list-schema.json]
"""
import argparse
import json
import sys

# D1 hardware -> Imager device tags. Imager has no "pizero2w" tag; it maps the
# Zero 2 W to pi3 (same BCM2710 SoC family), so pi3-32bit covers it. pi1-32bit
# (Pi 1 / Zero, ARMv6) is deliberately excluded — Waypoint does not support it.
DEVICES = {
    "armhf": ["pi2-32bit", "pi3-32bit", "pi4-32bit"],   # ARMv7: Zero 2 W, 2, 3/3+, 4 (32-bit)
    "arm64": ["pi3-64bit", "pi4-64bit"],                 # ARMv8: Pi 3/4 (64-bit)
}

# Display metadata per arch.
BITS = {"armhf": "32-bit", "arm64": "64-bit"}
BOARDS = {"armhf": "Pi Zero 2 W / 2 / 3 / 4", "arm64": "Pi 3 / 4"}

DEFAULT_ICON = "https://raw.githubusercontent.com/KN4OQW/waypoint/main/site/waypoint-imager-icon.png"
DEFAULT_WEBSITE = "https://github.com/KN4OQW/waypoint"


def entry(img, tag, repo, icon, website, release_date):
    """Build one os_list entry from an imager-meta image record."""
    arch = img["arch"]
    if arch not in DEVICES:
        raise ValueError(f"unknown arch {arch!r} (expected one of {sorted(DEVICES)})")
    bits = BITS[arch]
    return {
        "name": f"Waypoint OS ({bits})",
        "description": (
            f"MMDVM digital-voice hotspot host — DMR, YSF, D-Star, P25, NXDN, M17. "
            f"{bits}, {BOARDS[arch]}."
        ),
        "icon": icon,
        # GitHub Release asset URL for this image.
        "url": f"https://github.com/{repo}/releases/download/{tag}/{img['file']}",
        "extract_size": img["extract_size"],
        "extract_sha256": img["extract_sha256"],
        "image_download_size": img["image_download_size"],
        "image_download_sha256": img["image_download_sha256"],
        "release_date": img.get("release_date") or release_date,
        "devices": DEVICES[arch],
        # Bookworm-based image: Imager applies Wi-Fi/user/SSH via the systemd
        # first-boot (userconf/firstrun) mechanism, same as Raspberry Pi OS
        # (Legacy) Lite. (Trixie uses cloudinit-rpi; we are Bookworm.)
        "init_format": "systemd",
        "website": website,
    }


def generate(meta, tag, repo, icon=DEFAULT_ICON, website=DEFAULT_WEBSITE):
    """Return the os_list JSON dict for the given imager-meta + release tag."""
    release_date = meta.get("release_date")
    # 64-bit first (preferred on a Pi 3/4), then 32-bit.
    order = {"arm64": 0, "armhf": 1}
    images = sorted(meta["images"], key=lambda i: order.get(i["arch"], 99))
    return {"os_list": [entry(img, tag, repo, icon, website, release_date) for img in images]}


def validate(doc, schema_path):
    """Validate doc against the vendored Imager schema. Returns None or raises."""
    import jsonschema  # optional dependency; only needed with --validate
    with open(schema_path) as f:
        schema = json.load(f)
    jsonschema.validate(instance=doc, schema=schema)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--meta", required=True, help="imager-meta.json from the image CI")
    ap.add_argument("--tag", required=True, help="release tag, e.g. v1.0.0")
    ap.add_argument("--repo", default="KN4OQW/waypoint", help="owner/repo for the release asset URLs")
    ap.add_argument("--out", default="-", help="output path ('-' for stdout)")
    ap.add_argument("--icon", default=DEFAULT_ICON)
    ap.add_argument("--website", default=DEFAULT_WEBSITE)
    ap.add_argument("--validate", action="store_true", help="validate against the vendored schema")
    ap.add_argument("--schema", default="scripts/os-list-schema.json")
    args = ap.parse_args(argv)

    with open(args.meta) as f:
        meta = json.load(f)
    doc = generate(meta, args.tag, args.repo, args.icon, args.website)

    if args.validate:
        validate(doc, args.schema)

    text = json.dumps(doc, indent=2, ensure_ascii=False) + "\n"
    if args.out == "-":
        sys.stdout.write(text)
    else:
        with open(args.out, "w") as f:
            f.write(text)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
