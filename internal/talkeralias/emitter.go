package talkeralias

import (
	"strings"
	"sync"
)

// The emitter watches network→RF DMR traffic on the loopback seam and, once per
// call, hands back the DMRA frames that name who is talking.
//
// It is a pure decision function over observed datagrams: it holds the per-slot
// state needed to fire once per call and nothing else, does no I/O, and knows
// nothing about the shim it will be wired into. That is deliberate — the seam it
// runs on carries live voice, and the package contract there is that an observer
// can never delay or block a frame. Something that only inspects bytes and
// returns a decision cannot violate that.

// Template names what an alias says. It is an operator setting, and OFF is the
// zero value: a node that has never been configured for this emits nothing.
type Template string

const (
	// TemplateOff is the default. No alias is built and no frame is emitted.
	TemplateOff Template = ""
	// TemplateCallsign is "KN4OQW".
	TemplateCallsign Template = "callsign"
	// TemplateCallsignName is "KN4OQW Clint Chance".
	TemplateCallsignName Template = "callsign + name"
	// TemplateName is "Clint Chance", falling back to the callsign when the
	// phonebook row carries no name — an alias reading as nothing at all is worse
	// than one reading as a callsign.
	TemplateName Template = "name"
)

// Valid reports whether t is a template this node knows. An unrecognised value in
// the store is treated as OFF rather than as an error: a setting written by a
// newer build must not make an older one emit something it does not understand.
func (t Template) Valid() bool {
	switch t {
	case TemplateOff, TemplateCallsign, TemplateCallsignName, TemplateName:
		return true
	}
	return false
}

// Render builds the alias text for a resolved station. It returns "" whenever
// there is nothing to say, which the caller must treat as "emit no frames"
// rather than as an empty alias.
//
// The callsign is upper-cased. Everything else in this project settles on upper
// for callsigns, and a receiving radio showing "kn4oqw" beside another node's
// "KN4OQW" would look like two stations.
func (t Template) Render(callsign, fullName string) string {
	callsign = strings.ToUpper(strings.TrimSpace(callsign))
	fullName = strings.TrimSpace(fullName)
	if callsign == "" {
		// No callsign means the resolver found nothing. An alias is never
		// synthesised from a bare id: the id is what the radio already shows, and
		// dressing it up as an alias would claim knowledge this node does not have.
		return ""
	}
	switch t {
	case TemplateCallsign:
		return callsign
	case TemplateCallsignName:
		if fullName == "" {
			return callsign
		}
		return callsign + " " + fullName
	case TemplateName:
		if fullName == "" {
			return callsign
		}
		return fullName
	default: // TemplateOff and anything unrecognised
		return ""
	}
}

// DMRD wire offsets, from MMDVM-Host's CDMRNetwork::write. Named rather than
// inlined because a bare 15 in a bit test is unreadable and unverifiable.
const (
	dmrdLen      = 55
	dmrdSrcID    = 5  // 3 bytes, big-endian
	dmrdFlags    = 15 // slot, call type, frame type, and N
	dmrdSlot2Bit = 0x80
	dmrdDataBit  = 0x20 // set for a data frame; the low nibble is then the data type
	dmrdSyncBit  = 0x10 // set for a voice sync frame (N is not meaningful)
	dmrdTypeMask = 0x0F
	dtVoiceLCHdr = 0x01 // DT_VOICE_LC_HEADER
)

// Call is what the emitter recognised on the wire.
type Call struct {
	SrcID uint32
	Slot  int
}

// parseVoiceHeader reports the call a datagram opens, if it opens one.
//
// Only the voice LC header counts as a start. A voice frame mid-stream would also
// name the source, but firing on one would re-send the alias on every burst; the
// header arrives once per transmission, which is exactly the cadence wanted.
func parseVoiceHeader(d []byte) (Call, bool) {
	if len(d) != dmrdLen || string(d[0:4]) != "DMRD" {
		return Call{}, false
	}
	flags := d[dmrdFlags]
	if flags&dmrdDataBit == 0 || flags&dmrdSyncBit != 0 {
		return Call{}, false // a voice frame, not a data one: no header here
	}
	if flags&dmrdTypeMask != dtVoiceLCHdr {
		return Call{}, false
	}
	id := uint32(d[dmrdSrcID])<<16 | uint32(d[dmrdSrcID+1])<<8 | uint32(d[dmrdSrcID+2])
	if id == 0 {
		return Call{}, false
	}
	slot := 1
	if flags&dmrdSlot2Bit != 0 {
		slot = 2
	}
	return Call{SrcID: id, Slot: slot}, true
}

// Resolver is the emitter's view of the identity chain: a callsign and a name for
// a DMR id, and a bool for "nobody knows". Narrow on purpose — the emitter has no
// business reaching anything else the chain can do.
type Resolver interface {
	DisplayForID(id uint32) (callsign, fullName string, ok bool)
}

// Emitter decides when to send an alias and what it should say.
//
// The zero value is not usable; see New. An Emitter with TemplateOff, or with no
// resolver, emits nothing at all — not an empty frame, no frame — which is what
// makes the feature's default state byte-identical to today's traffic.
type Emitter struct {
	mu       sync.Mutex
	template Template
	res      Resolver
	// lastByslot remembers the call each slot is carrying, so the alias is emitted
	// once per transmission rather than once per header. MMDVM-Host repeats the
	// voice LC header a few times at the start of a call for late entry, and
	// re-emitting on each would put three copies of the same alias on the seam.
	lastBySlot map[int]uint32
}

// New returns an emitter. Either argument may be zero/nil, and both cases mean
// "emit nothing": an unset template is the default, and a node with no resolver
// has no phonebook to draw on.
func New(t Template, res Resolver) *Emitter {
	return &Emitter{template: t, res: res, lastBySlot: map[int]uint32{}}
}

// Observe is handed a datagram travelling network→RF and returns the DMRA frames
// to inject, or nil.
//
// nil is the overwhelmingly common answer — every voice frame, every data frame,
// every call whose source nobody has entered in the phonebook — and the caller
// must treat it as "nothing to do" rather than as a failure.
func (e *Emitter) Observe(datagram []byte) [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.template == TemplateOff || !e.template.Valid() || e.res == nil {
		return nil
	}
	call, ok := parseVoiceHeader(datagram)
	if !ok {
		return nil
	}
	if e.lastBySlot[call.Slot] == call.SrcID {
		return nil // same call, repeated header
	}
	e.lastBySlot[call.Slot] = call.SrcID

	callsign, fullName, ok := e.res.DisplayForID(call.SrcID)
	if !ok {
		return nil // nobody knows this station; never synthesise an alias
	}
	text := e.template.Render(callsign, fullName)
	if text == "" {
		return nil
	}
	frames, err := Encode(call.SrcID, text, ChooseFormat(text))
	if err != nil {
		// The only errors Encode returns are "nothing to encode" and an id outside
		// 24 bits, both of which were ruled out above. Returning nil rather than
		// surfacing it keeps the voice path free of an error it cannot act on.
		return nil
	}
	return frames
}

// Reset forgets which call each slot was carrying. The relay calls it when the
// shim restarts, so a call in progress across the restart gets its alias again
// rather than being suppressed by state from a previous process.
func (e *Emitter) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	clear(e.lastBySlot)
}
