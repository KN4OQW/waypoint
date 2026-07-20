# Synthetic frame fixtures

These `*_voice.bin` files are **synthetic**, not bench captures. They are the
exact wire bytes `ConstructDMR` / `ConstructYSF` / `ConstructNXDN` emit for one
deterministic voice frame per mode (see `fixtures_test.go`, `fixtureVoice`).

They exist to pin the wire format byte-for-byte so a refactor cannot silently
change it (`TestGoldenFixtures` is the golden check; it regenerates a file if it
is missing, otherwise compares).

Because they are constructed from the upstream frame definitions
(juribeparada/MMDVM_CM + g4klx/MMDVMHost) rather than captured from a keyed-up
transmission on the loopback, the AMBE payload is random bits, not real speech —
which is fine for the reframe/round-trip contract (the layer copies the codec
bits opaquely). **Prompt 6 replaces these with real sanitized loopback captures**
from the bench Pi, which will additionally validate that a real daemon accepts
the constructed frames on the wire.

- `dmr_voice.bin` — a 55-byte "DMRD" voice frame (3 AMBE codewords).
- `ysf_voice.bin` — a 155-byte "YSFD" VD-mode-2 voice frame (5 AMBE codewords).
- `nxdn_voice.bin` — a 43-byte "NXDND" voice frame (4 AMBE codewords).
