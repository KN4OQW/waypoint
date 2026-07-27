//go:build tier2

package tier2

import (
	"testing"
	"time"
)

// TestTier2_BackToBackCrossNetwork characterises a drop seen while building the
// Tier 2 harness: within ONE DMRGateway session, a transmission routed to TGIF
// followed by a transmission routed to the BrandMeister primary — same slot,
// distinct stream ids — did not reach the primary at all. Isolating each
// transmission in its own gateway session made both pass.
//
// This is a diagnostic, not an acceptance test: it varies only the inter-
// transmission gap, to separate "state is keyed per stream and clears on a
// timer" from "the second transmission is lost regardless".
func TestTier2_BackToBackCrossNetwork(t *testing.T) {
	for _, gap := range []time.Duration{700 * time.Millisecond, 3 * time.Second, 8 * time.Second} {
		t.Run(gap.String(), func(t *testing.T) {
			r := startRig(t, false)
			defer r.stop()

			r.inject(t, retarget(loadCapture(t), 31665, 0xAABBCCDD, true))
			tgifFirst := len(r.tgif.received())

			time.Sleep(gap)

			r.inject(t, retarget(loadCapture(t), 9990, 0x11223344, true))

			var bmGot int
			for _, f := range r.bm.received() {
				if dmrdDst(f) == 9990 {
					bmGot++
				}
			}
			t.Logf("gap=%v: TGIF got %d frames for 31665; primary got %d frames for 9990",
				gap, tgifFirst, bmGot)
			if bmGot == 0 {
				t.Logf("REPRODUCED: second transmission never reached the primary after a %v gap", gap)
			}
		})
	}
}

// TestTier2_BackToBackSameNetwork is the discriminator for the above: two
// transmissions in one session to the SAME network. If this passes while the
// cross-network case fails, the effect is specific to switching networks and
// not simply "the harness cannot send a second transmission".
func TestTier2_BackToBackSameNetwork(t *testing.T) {
	r := startRig(t, false)
	defer r.stop()

	r.inject(t, retarget(loadCapture(t), 31665, 0xAAAA0001, true))
	first := len(r.tgif.received())

	time.Sleep(700 * time.Millisecond)

	r.inject(t, retarget(loadCapture(t), 31665, 0xAAAA0002, true))
	second := len(r.tgif.received()) - first

	t.Logf("same-network back-to-back: first=%d frames, second=%d frames", first, second)
	if second == 0 {
		t.Errorf("a second transmission to the SAME network was also dropped — the effect is not cross-network specific")
	}
}
