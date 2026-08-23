package talkeralias

import (
	"testing"
)

// fakeResolver stands in for the identity chain.
type fakeResolver map[uint32][2]string

func (f fakeResolver) DisplayForID(id uint32) (string, string, bool) {
	v, ok := f[id]
	return v[0], v[1], ok
}

// voiceHeader builds a DMRD voice LC header the way MMDVM-Host writes one.
func voiceHeader(srcID uint32, slot int) []byte {
	d := make([]byte, dmrdLen)
	copy(d, "DMRD")
	d[dmrdSrcID] = byte(srcID >> 16)
	d[dmrdSrcID+1] = byte(srcID >> 8)
	d[dmrdSrcID+2] = byte(srcID)
	flags := byte(dmrdDataBit | dtVoiceLCHdr)
	if slot == 2 {
		flags |= dmrdSlot2Bit
	}
	d[dmrdFlags] = flags
	return d
}

// voiceFrame is a mid-stream voice burst — not a call start.
func voiceFrame(srcID uint32, slot int, n byte) []byte {
	d := voiceHeader(srcID, slot)
	flags := n & dmrdTypeMask
	if slot == 2 {
		flags |= dmrdSlot2Bit
	}
	d[dmrdFlags] = flags
	return d
}

func chain() fakeResolver {
	return fakeResolver{
		3180202: {"KN4OQW", "Clint Chance"},
		3110000: {"W1AW", ""}, // known, but no name recorded
	}
}

// TestTemplateRender covers the copy each setting produces, including the two
// fallbacks: a name template with no name, and a resolver miss.
func TestTemplateRender(t *testing.T) {
	for _, tc := range []struct {
		t          Template
		call, name string
		want       string
	}{
		{TemplateCallsign, "KN4OQW", "Clint Chance", "KN4OQW"},
		{TemplateCallsignName, "KN4OQW", "Clint Chance", "KN4OQW Clint Chance"},
		{TemplateName, "KN4OQW", "Clint Chance", "Clint Chance"},
		// No name recorded: both name templates fall back to the callsign rather
		// than producing an empty alias.
		{TemplateCallsignName, "W1AW", "", "W1AW"},
		{TemplateName, "W1AW", "", "W1AW"},
		// Lower case off the air comes back upper.
		{TemplateCallsign, "kn4oqw", "", "KN4OQW"},
		// Off produces nothing whatever the inputs.
		{TemplateOff, "KN4OQW", "Clint Chance", ""},
		// No callsign means the resolver found nothing; never synthesise.
		{TemplateCallsign, "", "Clint Chance", ""},
		{TemplateName, "", "Clint Chance", ""},
		// An unrecognised template behaves as off.
		{Template("shouty"), "KN4OQW", "Clint Chance", ""},
	} {
		if got := tc.t.Render(tc.call, tc.name); got != tc.want {
			t.Errorf("Template(%q).Render(%q, %q) = %q, want %q", tc.t, tc.call, tc.name, got, tc.want)
		}
	}
}

// TestFeatureOffEmitsNothing is the blank-omit contract: an unconfigured node puts
// nothing on the seam.
func TestFeatureOffEmitsNothing(t *testing.T) {
	for name, e := range map[string]*Emitter{
		"template off":   New(TemplateOff, chain()),
		"no resolver":    New(TemplateCallsignName, nil),
		"bad template":   New(Template("nonsense"), chain()),
		"both defaulted": New(TemplateOff, nil),
	} {
		if got := e.Observe(voiceHeader(3180202, 1)); got != nil {
			t.Errorf("%s: emitted %d frames, want none", name, len(got))
		}
	}
}

// TestNoMatchNoAlias: a station nobody has entered gets no alias. The receiving
// radio shows the bare id, exactly as it does today.
func TestNoMatchNoAlias(t *testing.T) {
	e := New(TemplateCallsignName, chain())
	if got := e.Observe(voiceHeader(4242424, 1)); got != nil {
		t.Errorf("emitted %d frames for an unknown station, want none", len(got))
	}
}

// TestEmitsOncePerCall: MMDVM-Host repeats the voice LC header for late entry,
// and an emitter that fired on each would put three copies of one alias on a live
// voice seam.
func TestEmitsOncePerCall(t *testing.T) {
	e := New(TemplateCallsignName, chain())

	first := e.Observe(voiceHeader(3180202, 1))
	if len(first) != 4 {
		t.Fatalf("first header emitted %d frames, want 4", len(first))
	}
	for i := range 3 {
		if got := e.Observe(voiceHeader(3180202, 1)); got != nil {
			t.Errorf("repeat header %d emitted again", i)
		}
	}
	// A different caller on the same slot is a new call and does emit.
	if got := e.Observe(voiceHeader(3110000, 1)); len(got) != 4 {
		t.Errorf("new caller emitted %d frames, want 4", len(got))
	}
	// The original caller returning is also a new call.
	if got := e.Observe(voiceHeader(3180202, 1)); len(got) != 4 {
		t.Errorf("returning caller emitted %d frames, want 4", len(got))
	}
}

// TestSlotsAreIndependent: two slots carry two calls, and one must not suppress
// the other's alias.
func TestSlotsAreIndependent(t *testing.T) {
	e := New(TemplateCallsign, chain())
	if got := e.Observe(voiceHeader(3180202, 1)); len(got) != 4 {
		t.Fatalf("slot 1 emitted %d frames", len(got))
	}
	if got := e.Observe(voiceHeader(3180202, 2)); len(got) != 4 {
		t.Errorf("slot 2 suppressed by slot 1's call: emitted %d frames", len(got))
	}
}

// TestOnlyTheVoiceHeaderStarts: nothing else on the seam is a call start.
func TestOnlyTheVoiceHeaderStarts(t *testing.T) {
	e := New(TemplateCallsign, chain())
	for name, d := range map[string][]byte{
		"mid-stream voice": voiceFrame(3180202, 1, 2),
		"voice sync":       func() []byte { d := voiceHeader(3180202, 1); d[dmrdFlags] = dmrdSyncBit; return d }(),
		"wrong data type":  func() []byte { d := voiceHeader(3180202, 1); d[dmrdFlags] = dmrdDataBit | 0x03; return d }(),
		"zero source":      voiceHeader(0, 1),
		"not DMRD":         func() []byte { d := voiceHeader(3180202, 1); copy(d, "DMRA"); return d }(),
		"short datagram":   voiceHeader(3180202, 1)[:20],
		"empty":            {},
	} {
		if got := e.Observe(d); got != nil {
			t.Errorf("%s was treated as a call start (%d frames)", name, len(got))
		}
	}
}

// TestEmittedFramesAreAddressedToTheCaller: the DMRA id must be the station being
// described, or the fork's slot-matching drops it.
func TestEmittedFramesAreAddressedToTheCaller(t *testing.T) {
	e := New(TemplateCallsignName, chain())
	frames := e.Observe(voiceHeader(3180202, 1))
	if len(frames) != 4 {
		t.Fatalf("emitted %d frames", len(frames))
	}
	id, alias, _, err := Decode(frames)
	if err != nil {
		t.Fatal(err)
	}
	if id != 3180202 {
		t.Errorf("frames addressed to %d, want 3180202", id)
	}
	if alias != "KN4OQW Clint Chance" {
		t.Errorf("alias = %q", alias)
	}
}

// TestResetForgetsCalls: after a shim restart a call in progress should get its
// alias again rather than be suppressed by state from a dead process.
func TestResetForgetsCalls(t *testing.T) {
	e := New(TemplateCallsign, chain())
	if got := e.Observe(voiceHeader(3180202, 1)); len(got) != 4 {
		t.Fatal("first emit failed")
	}
	if got := e.Observe(voiceHeader(3180202, 1)); got != nil {
		t.Fatal("second emit should have been suppressed")
	}
	e.Reset()
	if got := e.Observe(voiceHeader(3180202, 1)); len(got) != 4 {
		t.Errorf("after Reset the call was still suppressed")
	}
}
