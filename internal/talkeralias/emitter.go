package talkeralias

import (
	"strings"
	"sync"
	"time"
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
	dmrdStreamID = 16 // 4 bytes; see Call.StreamID for the byte order
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
	// StreamID is the transmission's id, and it is how a name announced by another
	// daemon is matched to the transmission it describes.
	//
	// It survives the hop through DMRGateway, which is the whole reason it is
	// usable as a key here: at the pinned 79edbc4, CDMRNetwork::read memcpys these
	// four bytes into a uint32 (DMRNetwork.cpp:155) and CMMDVMNetwork::write
	// memcpys that same uint32 straight back out at the same offset
	// (MMDVMNetwork.cpp:203). Neither end interprets it, so the bytes arrive
	// unaltered and the host byte order of the gateway never enters into it. The
	// only place in DMRGateway that ever sets a stream id is the XLX link it
	// synthesises itself (DMRGateway.cpp:1194), which is not this path.
	//
	// Read big-endian to agree with internal/bus/frames, which writes it that way.
	StreamID uint32
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
	stream := uint32(d[dmrdStreamID])<<24 | uint32(d[dmrdStreamID+1])<<16 |
		uint32(d[dmrdStreamID+2])<<8 | uint32(d[dmrdStreamID+3])
	return Call{SrcID: id, Slot: slot, StreamID: stream}, true
}

// Resolver is the emitter's view of the identity chain: a callsign and a name for
// a DMR id, and a bool for "nobody knows". Narrow on purpose — the emitter has no
// business reaching anything else the chain can do.
type Resolver interface {
	DisplayForID(id uint32) (callsign, fullName string, ok bool)
}

// announcedTTL bounds how long a name another daemon has announced is held
// waiting for the transmission it describes.
//
// Correctness does not rest on it: an announcement is keyed by stream id, which is
// unique per transmission, so a stale entry can only ever match the very
// transmission it was published for. The TTL is garbage collection — a name whose
// audio never arrived (the bus dropped the stream, the gateway refused the route)
// must not sit in the map for the life of the process.
const announcedTTL = 60 * time.Second

// maxAnnounced caps the store so a misbehaving publisher cannot grow it without
// bound. A bus holds one transmission at a time, so anything past a handful is
// already a fault; the ceiling is generous rather than tight because dropping a
// name loses an alias and dropping the wrong one is indistinguishable.
const maxAnnounced = 64

// announced is a name waiting for its transmission to appear on the seam.
type announced struct {
	name string
	at   time.Time
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
	//
	// It holds the whole call rather than the source id. That was a real bug and
	// not a tidy-up: the bus sources EVERY Zello transmission from one id, so
	// comparing source ids alone named the first caller of a session and then went
	// silent for the rest of it. Two calls from the same operator back to back are
	// two calls, and the stream id is what separates them. Call{} is a safe zero
	// because parseVoiceHeader never returns a call with source id 0.
	lastBySlot map[int]Call
	// announced holds names another daemon has said are talking, by stream id. See
	// Announce for why the phonebook cannot answer for these calls.
	announced map[uint32]announced
	// announceOnly are source ids this node transmits on somebody else's behalf,
	// where the phonebook is not merely unhelpful but WRONG. See Announce.
	announceOnly map[uint32]bool
}

// New returns an emitter. Every argument may be zero/nil/empty, and each case
// means "less to say" rather than an error: an unset template emits nothing at
// all, a node with no resolver has no phonebook to draw on, and no announce-only
// ids means every call is named from the phonebook as before.
func New(t Template, res Resolver, announceOnly []uint32) *Emitter {
	only := make(map[uint32]bool, len(announceOnly))
	for _, id := range announceOnly {
		if id != 0 {
			only[id] = true
		}
	}
	return &Emitter{
		template:     t,
		res:          res,
		lastBySlot:   map[int]Call{},
		announced:    map[uint32]announced{},
		announceOnly: only,
	}
}

// Announce records the name another daemon has said is talking on one
// transmission, to be used when that transmission appears on the seam.
//
// This exists because of a call the phonebook cannot answer. When the bus daemon
// puts Zello audio on the air it sources every transmission from ONE DMR id — the
// node's own, because a Zello user without a DMR registration has no id to borrow
// and one who has an id has not authorised this node to transmit as them. So the
// alias is the only thing that says who is actually speaking, and the phonebook
// would answer with the NODE's callsign: not merely unhelpful but a different
// operator's name on somebody's screen. Hence announceOnly, which suppresses the
// phonebook for those ids entirely — no announcement means no alias, never a
// fallback.
//
// The name is used verbatim, deliberately NOT through Template.Render. Render
// uppercases its argument, which is right for a callsign — a callsign has a
// canonical form — and wrong for a Zello account name, whose case is part of it:
// "Booting6228" would reach the radio as "BOOTING6228". Render's three shapes are
// about combining a callsign with a full name and there is no such pair here, only
// one string that came off the wire. The template therefore decides whether an
// alias is emitted at all, and an announced name passes through untouched.
func (e *Emitter) Announce(streamID uint32, name string, now time.Time) {
	name = strings.TrimSpace(name)
	if streamID == 0 || name == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	for id, a := range e.announced {
		if now.Sub(a.at) > announcedTTL {
			delete(e.announced, id)
		}
	}
	// Over the cap after pruning is a publisher fault, not a busy node. Drop the
	// oldest rather than refusing the newest: the newest is the one whose audio is
	// about to arrive.
	for len(e.announced) >= maxAnnounced {
		oldest, at := uint32(0), now
		for id, a := range e.announced {
			if !a.at.After(at) {
				oldest, at = id, a.at
			}
		}
		delete(e.announced, oldest)
	}
	e.announced[streamID] = announced{name: name, at: now}
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

	if e.template == TemplateOff || !e.template.Valid() {
		return nil
	}
	call, ok := parseVoiceHeader(datagram)
	if !ok {
		return nil
	}
	if prev := e.lastBySlot[call.Slot]; prev.SrcID == call.SrcID && prev.StreamID == call.StreamID {
		return nil // same call, repeated header
	}
	e.lastBySlot[call.Slot] = call

	text := e.textFor(call)
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

// textFor is what the alias for a call should say, or "" for "say nothing at all".
//
// The order is deliberate: an announcement beats the phonebook, because the
// daemon that announced it watched the call start and the phonebook only knows
// what an id is registered to.
//
// The caller holds the mutex.
func (e *Emitter) textFor(call Call) string {
	if a, ok := e.announced[call.StreamID]; ok {
		// Consumed on use. The announcement described one transmission, and keeping
		// it past that transmission's header could only ever attach the name to a
		// later one.
		delete(e.announced, call.StreamID)
		return a.name
	}
	if e.announceOnly[call.SrcID] {
		// A borrowed id with nothing announced for it — the announcement lost its
		// race with the audio, or the publisher is not running. The phonebook would
		// answer with THIS node's name rather than the caller's, so the honest
		// answer is no alias: a radio showing a bare id is a worse screen, and a
		// radio showing the wrong operator is a wrong one.
		return ""
	}
	if e.res == nil {
		return "" // no phonebook to draw on
	}
	callsign, fullName, ok := e.res.DisplayForID(call.SrcID)
	if !ok {
		return "" // nobody knows this station; never synthesise an alias
	}
	return e.template.Render(callsign, fullName)
}

// Reset forgets which call each slot was carrying. The relay calls it when the
// shim restarts, so a call in progress across the restart gets its alias again
// rather than being suppressed by state from a previous process.
// Announcements are deliberately KEPT. They are keyed by stream id, so one can
// only ever match the transmission it was published for, and a call that was in
// progress across the restart is exactly the case this method exists to re-alias.
func (e *Emitter) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	clear(e.lastBySlot)
}
