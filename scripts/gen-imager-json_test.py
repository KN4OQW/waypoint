#!/usr/bin/env python3
"""Tests for gen-imager-json.py: structural correctness + validation against the
vendored Raspberry Pi Imager OS-list schema. Run: python3 -m pytest scripts/ (or
python3 scripts/gen-imager-json_test.py)."""
import importlib.util
import json
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCHEMA = os.path.join(HERE, "os-list-schema.json")

# Load the hyphenated module by path.
_spec = importlib.util.spec_from_file_location("gen_imager_json", os.path.join(HERE, "gen-imager-json.py"))
gen = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(gen)

SAMPLE_META = {
    "release_date": "2026-07-23",
    "images": [
        {
            "arch": "armhf",
            "file": "waypoint-v1.0.0-bookworm-armhf.img.xz",
            "image_download_size": 591194064,
            "image_download_sha256": "a" * 64,
            "extract_size": 3816816640,
            "extract_sha256": "b" * 64,
        },
        {
            "arch": "arm64",
            "file": "waypoint-v1.0.0-bookworm-arm64.img.xz",
            "image_download_size": 588659188,
            "image_download_sha256": "c" * 64,
            "extract_size": 4018143232,
            "extract_sha256": "d" * 64,
        },
    ],
}

# Fields Imager's schema requires on an OS entry, plus the download hash we add.
REQUIRED = [
    "name", "description", "icon", "url",
    "extract_size", "extract_sha256",
    "image_download_size", "image_download_sha256",
    "release_date", "devices",
]


class GenImagerJSONTest(unittest.TestCase):
    def setUp(self):
        self.doc = gen.generate(SAMPLE_META, "v1.0.0", "KN4OQW/waypoint")
        self.by_arch = {e["name"]: e for e in self.doc["os_list"]}

    def test_two_entries_64bit_first(self):
        names = [e["name"] for e in self.doc["os_list"]]
        self.assertEqual(names, ["Waypoint OS (64-bit)", "Waypoint OS (32-bit)"])

    def test_required_fields_present_and_typed(self):
        for e in self.doc["os_list"]:
            for k in REQUIRED:
                self.assertIn(k, e, f"{e['name']} missing {k}")
            self.assertIsInstance(e["extract_size"], int)
            self.assertIsInstance(e["image_download_size"], int)
            self.assertEqual(len(e["extract_sha256"]), 64)
            self.assertEqual(len(e["image_download_sha256"]), 64)
            self.assertTrue(e["url"].endswith(".img.xz"))
            self.assertTrue(e["description"])
            # release_date is YYYY-MM-DD
            y, m, d = e["release_date"].split("-")
            self.assertEqual((len(y), len(m), len(d)), (4, 2, 2))

    def test_device_tags_match_d1(self):
        self.assertEqual(self.by_arch["Waypoint OS (32-bit)"]["devices"], ["pi2-32bit", "pi3-32bit", "pi4-32bit"])
        self.assertEqual(self.by_arch["Waypoint OS (64-bit)"]["devices"], ["pi3-64bit", "pi4-64bit"])
        # Pi 1 / Zero (ARMv6) is never offered.
        for e in self.doc["os_list"]:
            self.assertNotIn("pi1-32bit", e["devices"])

    def test_url_construction(self):
        self.assertEqual(
            self.by_arch["Waypoint OS (64-bit)"]["url"],
            "https://github.com/KN4OQW/waypoint/releases/download/v1.0.0/waypoint-v1.0.0-bookworm-arm64.img.xz",
        )

    def test_sizes_and_hashes_from_meta(self):
        e = self.by_arch["Waypoint OS (64-bit)"]
        self.assertEqual(e["image_download_size"], 588659188)
        self.assertEqual(e["extract_size"], 4018143232)
        self.assertEqual(e["extract_sha256"], "d" * 64)

    def test_unknown_arch_rejected(self):
        with self.assertRaises(ValueError):
            gen.generate({"release_date": "2026-01-01", "images": [{"arch": "riscv", "file": "x", "image_download_size": 1, "image_download_sha256": "e" * 64, "extract_size": 1, "extract_sha256": "f" * 64}]}, "v1", "o/r")

    def test_validates_against_vendored_schema(self):
        try:
            import jsonschema  # noqa: F401
        except ImportError:
            self.skipTest("jsonschema not installed")
        gen.validate(self.doc, SCHEMA)  # raises on failure


if __name__ == "__main__":
    unittest.main()
