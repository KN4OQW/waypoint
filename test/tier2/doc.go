// Package tier2 holds loopback integration tests that drive the real, pinned
// gateway daemons against configs produced by this project's renderers.
//
// Tier 1 (the ordinary unit tests) proves the renderers emit the bytes they
// intended. These tests prove the daemons agree those bytes are a valid
// configuration, and that traffic then lands where the generated routing says
// it should — without RF and without any upstream credential.
//
// They are excluded from the default build by the "tier2" build tag because
// they need daemon binaries and a local MQTT broker. See README.md to run them.
package tier2
