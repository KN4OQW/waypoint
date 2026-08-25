package talkeralias

import (
	"testing"
	"time"
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

// voiceHeaderStream is voiceHeader with an explicit stream id, for the cases that
// turn on which transmission a header belongs to. voiceHeader leaves it 0, which
// is a legal id the emitter must not confuse with "no previous call".
func voiceHeaderStream(srcID uint32, slot int, streamID uint32) []byte {
	d := voiceHeader(srcID, slot)
	d[dmrdStreamID] = byte(streamID >> 24)
	d[dmrdStreamID+1] = byte(streamID >> 16)
	d[dmrdStreamID+2] = byte(streamID >> 8)
	d[dmrdStreamID+3] = byte(streamID)
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
		"template off":   New(TemplateOff, chain(), nil),
		"no resolver":    New(TemplateCallsignName, nil, nil),
		"bad template":   New(Template("nonsense"), chain(), nil),
		"both defaulted": New(TemplateOff, nil, nil),
	} {
		if got := e.Observe(voiceHeader(3180202, 1)); got != nil {
			t.Errorf("%s: emitted %d frames, want none", name, len(got))
		}
	}
}

// TestNoMatchNoAlias: a station nobody has entered gets no alias. The receiving
// radio shows the bare id, exactly as it does today.
func TestNoMatchNoAlias(t *testing.T) {
	e := New(TemplateCallsignName, chain(), nil)
	if got := e.Observe(voiceHeader(4242424, 1)); got != nil {
		t.Errorf("emitted %d frames for an unknown station, want none", len(got))
	}
}

// TestEmitsOncePerCall: MMDVM-Host repeats the voice LC header for late entry,
// and an emitter that fired on each would put three copies of one alias on a live
// voice seam.
func TestEmitsOncePerCall(t *testing.T) {
	e := New(TemplateCallsignName, chain(), nil)

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
	e := New(TemplateCallsign, chain(), nil)
	if got := e.Observe(voiceHeader(3180202, 1)); len(got) != 4 {
		t.Fatalf("slot 1 emitted %d frames", len(got))
	}
	if got := e.Observe(voiceHeader(3180202, 2)); len(got) != 4 {
		t.Errorf("slot 2 suppressed by slot 1's call: emitted %d frames", len(got))
	}
}

// TestOnlyTheVoiceHeaderStarts: nothing else on the seam is a call start.
func TestOnlyTheVoiceHeaderStarts(t *testing.T) {
	e := New(TemplateCallsign, chain(), nil)
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
	e := New(TemplateCallsignName, chain(), nil)
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
	e := New(TemplateCallsign, chain(), nil)
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

// --- announced names ---------------------------------------------------------

// The bus daemon sources every Zello transmission from ONE DMR id, so the
// phonebook cannot say who is talking; a name is announced instead. These tests
// cover the three ways that can go wrong: the wrong name, no name, and a name
// attached to the wrong transmission.

// TestStreamIDIsReadFromTheHeader: the stream id is the key an announcement is
// matched on, so a header must parse it out of the right four bytes, big-endian.
func TestStreamIDIsReadFromTheHeader(t *testing.T) {
	call, ok := parseVoiceHeader(voiceHeaderStream(3180202, 2, 0xDEADBEEF))
	if !ok {
		t.Fatal("a voice LC header did not parse")
	}
	if call.StreamID != 0xDEADBEEF {
		t.Errorf("stream id = %#x, want 0xdeadbeef", call.StreamID)
	}
	if call.SrcID != 3180202 || call.Slot != 2 {
		t.Errorf("call = %+v", call)
	}
}

// TestBackToBackCallsFromOneSourceEachGetAnAlias is the regression this stream-id
// dedup exists for. Comparing source ids alone, every Zello transmission after the
// first looked like a repeated header — so a session named its first caller and
// then went silent.
func TestBackToBackCallsFromOneSourceEachGetAnAlias(t *testing.T) {
	e := New(TemplateCallsign, chain(), nil)
	for i, stream := range []uint32{0x11, 0x22, 0x33} {
		if got := e.Observe(voiceHeaderStream(3180202, 1, stream)); len(got) != 4 {
			t.Errorf("call %d (stream %#x) emitted %d frames, want 4", i, stream, len(got))
		}
	}
	// Repeated headers WITHIN one call are still suppressed.
	if got := e.Observe(voiceHeaderStream(3180202, 1, 0x33)); got != nil {
		t.Errorf("a repeated header for stream 0x33 emitted again")
	}
}

// TestAnnouncedNameBeatsThePhonebook: the announcing daemon watched the call
// start; the phonebook only knows what an id is registered to.
func TestAnnouncedNameBeatsThePhonebook(t *testing.T) {
	e := New(TemplateCallsignName, chain(), []uint32{3180202})
	e.Announce(0x5150, "waypoint dev", time.Now())

	frames := e.Observe(voiceHeaderStream(3180202, 1, 0x5150))
	if len(frames) != 4 {
		t.Fatalf("emitted %d frames, want 4", len(frames))
	}
	id, alias, _, err := Decode(frames)
	if err != nil {
		t.Fatal(err)
	}
	// Addressed to the transmitting id — the fork's slot matching drops anything else.
	if id != 3180202 {
		t.Errorf("frames addressed to %d, want 3180202", id)
	}
	if alias != "waypoint dev" {
		t.Errorf("alias = %q, want the announced name; the phonebook would have said KN4OQW Clint Chance", alias)
	}
}

// TestAnnouncedNameKeepsItsCase: Template.Render uppercases, which is right for a
// callsign and wrong for a display name. "Booting6228" must not reach the radio as
// "BOOTING6228".
func TestAnnouncedNameKeepsItsCase(t *testing.T) {
	e := New(TemplateCallsign, chain(), []uint32{3180202})
	e.Announce(1, "Booting6228", time.Now())
	_, alias, _, err := Decode(e.Observe(voiceHeaderStream(3180202, 1, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if alias != "Booting6228" {
		t.Errorf("alias = %q, want Booting6228", alias)
	}
}

// TestAnnounceOnlyWithNoAnnouncementSaysNothing is the trap that makes this a
// suppression list and not just a preference. The id is the NODE's own, so the
// phonebook answers with the node's callsign — a different operator's name on
// somebody's screen. A bare id is a worse screen; a wrong name is a wrong one.
func TestAnnounceOnlyWithNoAnnouncementSaysNothing(t *testing.T) {
	e := New(TemplateCallsignName, chain(), []uint32{3180202})
	if got := e.Observe(voiceHeaderStream(3180202, 1, 0x99)); got != nil {
		_, alias, _, _ := Decode(got)
		t.Errorf("emitted %q for an announce-only id with nothing announced", alias)
	}
	// The suppression is per id, not global: another station still resolves.
	if got := e.Observe(voiceHeaderStream(3110000, 1, 0x9A)); len(got) != 4 {
		t.Errorf("an ordinary station emitted %d frames, want 4", len(got))
	}
}

// TestWithoutAnnounceOnlyThePhonebookStillAnswers: a node with no Zello channel
// gets exactly the behaviour it had before announcements existed.
func TestWithoutAnnounceOnlyThePhonebookStillAnswers(t *testing.T) {
	e := New(TemplateCallsign, chain(), nil)
	_, alias, _, err := Decode(e.Observe(voiceHeaderStream(3180202, 1, 7)))
	if err != nil {
		t.Fatal(err)
	}
	if alias != "KN4OQW" {
		t.Errorf("alias = %q, want KN4OQW", alias)
	}
}

// TestAnnouncementBelongsToOneTransmission: it is consumed by the call it names,
// so a later transmission cannot inherit it.
func TestAnnouncementBelongsToOneTransmission(t *testing.T) {
	e := New(TemplateCallsign, chain(), []uint32{3180202})
	e.Announce(0xAA, "first caller", time.Now())

	if _, alias, _, err := Decode(e.Observe(voiceHeaderStream(3180202, 1, 0xAA))); err != nil || alias != "first caller" {
		t.Fatalf("alias = %q, err = %v", alias, err)
	}
	// A second transmission, nothing announced for it: silence, not the last name.
	if got := e.Observe(voiceHeaderStream(3180202, 1, 0xBB)); got != nil {
		_, alias, _, _ := Decode(got)
		t.Errorf("the next transmission inherited %q", alias)
	}
}

// TestAnnouncementsExpire: a name whose audio never arrived is collected rather
// than held for the life of the process.
func TestAnnouncementsExpire(t *testing.T) {
	e := New(TemplateCallsign, chain(), []uint32{3180202})
	t0 := time.Now()
	e.Announce(0xC1, "stale", t0)
	// Pruning happens on the next Announce, which is the only thing that grows the
	// map.
	e.Announce(0xC2, "fresh", t0.Add(announcedTTL+time.Second))
	if _, ok := e.announced[0xC1]; ok {
		t.Error("an expired announcement survived")
	}
	if _, ok := e.announced[0xC2]; !ok {
		t.Error("the fresh announcement was collected")
	}
}

// TestAnnouncementsAreCapped: a misbehaving publisher must not grow the store
// without bound, and the newest name is the one whose audio is about to arrive.
func TestAnnouncementsAreCapped(t *testing.T) {
	e := New(TemplateCallsign, chain(), []uint32{3180202})
	t0 := time.Now()
	for i := range maxAnnounced * 2 {
		e.Announce(uint32(i+1), "caller", t0.Add(time.Duration(i)*time.Millisecond))
	}
	if len(e.announced) > maxAnnounced {
		t.Errorf("store holds %d announcements, cap is %d", len(e.announced), maxAnnounced)
	}
	if _, ok := e.announced[uint32(maxAnnounced*2)]; !ok {
		t.Error("the newest announcement was dropped in favour of older ones")
	}
}

// TestAnnounceRejectsNothingToSay: neither a blank name nor a zero stream id is an
// announcement, and neither may leave a phantom entry the wrong call could match.
func TestAnnounceRejectsNothingToSay(t *testing.T) {
	e := New(TemplateCallsign, chain(), []uint32{3180202})
	e.Announce(0, "somebody", time.Now())
	e.Announce(5, "   ", time.Now())
	if len(e.announced) != 0 {
		t.Errorf("store holds %d entries, want none", len(e.announced))
	}
}

// TestResetKeepsAnnouncements: Reset exists so a call in progress across a shim
// restart is re-aliased. Dropping the name would defeat exactly that case, and an
// announcement is keyed by stream id so it cannot land anywhere else.
func TestResetKeepsAnnouncements(t *testing.T) {
	e := New(TemplateCallsign, chain(), []uint32{3180202})
	e.Announce(0xD1, "mid-call", time.Now())
	e.Reset()
	if _, alias, _, err := Decode(e.Observe(voiceHeaderStream(3180202, 1, 0xD1))); err != nil || alias != "mid-call" {
		t.Errorf("after Reset: alias = %q, err = %v", alias, err)
	}
}

// TestTemplateOffIgnoresAnnouncements: the template is still the on/off switch. An
// operator who has not asked for aliases gets none, whatever a bus announces.
func TestTemplateOffIgnoresAnnouncements(t *testing.T) {
	e := New(TemplateOff, chain(), []uint32{3180202})
	e.Announce(0xE1, "waypoint dev", time.Now())
	if got := e.Observe(voiceHeaderStream(3180202, 1, 0xE1)); got != nil {
		t.Errorf("emitted %d frames with the feature off", len(got))
	}
}

// TestAnnouncementNeedsNoResolver: an announced name comes off the wire, so it
// must work on a node whose phonebook the injector could not build.
func TestAnnouncementNeedsNoResolver(t *testing.T) {
	e := New(TemplateCallsign, nil, []uint32{3180202})
	e.Announce(0xF1, "waypoint dev", time.Now())
	if _, alias, _, err := Decode(e.Observe(voiceHeaderStream(3180202, 1, 0xF1))); err != nil || alias != "waypoint dev" {
		t.Errorf("alias = %q, err = %v", alias, err)
	}
}
