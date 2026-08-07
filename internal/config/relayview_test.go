package config

import "testing"

// The relay flag has to survive the whole round trip an operator's toggle takes:
// store -> view -> (the UI's working copy) -> PUT dmrnet -> store -> render.
//
// It did not before. The field existed on the model and in the renderer, and was
// missing from the view, so there was no way to switch the relay on except by
// editing the database by hand — and the messages page, which read it from the
// view, reported the relay off on a node that was transmitting.
func TestRelayFlagReachesTheView(t *testing.T) {
	for _, on := range []bool{false, true} {
		m := &Model{}
		m.Modes.DMR = true
		m.DMRNet.ShimEnabled = on

		if got := m.View(Sources{}).DMR.ShimEnabled; got != on {
			t.Errorf("ShimEnabled = %v in the view, want %v", got, on)
		}
	}
}

// And the view's copy agrees with what the renderer actually does, which is the
// thing an operator is really asking about. A view that said "on" while the
// rendered INI was wired direct would be worse than no control at all.
func TestViewAgreesWithTheRenderedWiring(t *testing.T) {
	m := &Model{}
	m.Modes.DMR = true
	m.General.Callsign = "KN4OQW"
	m.DMR.ID = "3180202"
	m.DMRNet.Slot2 = true

	if m.View(Sources{}).DMR.ShimEnabled {
		t.Fatal("the relay reads as on before anybody asked for it")
	}

	m.DMRNet.ShimEnabled = true
	v := m.View(Sources{})
	if !v.shimEnabledSaysRelayIsWired(m) {
		t.Error("the view says the relay is on but the render wired the loopback direct")
	}
}

// ShimEnabledSaysRelayIsWired is the agreement the test above asserts, kept as a
// helper rather than duplicated: the view's flag is what the operator asked for,
// DMRShimEnabled is what the renderer will actually do with it.
func (v View) shimEnabledSaysRelayIsWired(m *Model) bool {
	return v.DMR.ShimEnabled == m.DMRShimEnabled()
}

// A configuration the relay cannot serve still shows the operator's choice as
// they set it, while the renderer declines it and the readiness finding explains
// why. The view reports the SETTING; DMRShimEnabled reports the OUTCOME, and
// conflating them would hide the explanation.
func TestViewShowsTheChoiceEvenWhenItCannotBeServed(t *testing.T) {
	m := &Model{}
	m.Modes.DMR = true
	m.General.Callsign = "KN4OQW"
	m.DMR.ID = "3180202"
	m.DMRNet.Slot2 = true
	m.DMRNet.ShimEnabled = true
	m.DMRNet.GatewayAddress = "192.168.1.50" // DMRGateway is on another host

	if !m.View(Sources{}).DMR.ShimEnabled {
		t.Error("the operator's choice vanished from the view")
	}
	if m.DMRShimEnabled() {
		t.Error("the renderer accepted a relay it cannot wire up")
	}
	var found bool
	for _, p := range m.ModeProblems() {
		if p.Field == "dmrnet.shim_enabled" {
			found = true
		}
	}
	if !found {
		t.Error("nothing explained why the relay is not running")
	}
}
