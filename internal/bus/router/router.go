// Package router is the fan-out core of the RFC-0003 mode-bus daemon: the pure,
// socket-free hub that decides, for each inbound frame, which other attachments
// it is emitted to — enforcing §5's four loop-prevention rules. cmd/waypoint-bus
// wraps it with UDP loopback endpoints and a run loop; every rule here is a pure
// function of frame origin + bus state, so the whole contract (§6.4) is tested by
// driving synthetic frames, no sockets involved.
//
//	inbound bytes --ParseX--> Frame --Ingest(origin)--> []Emission --ConstructY--> outbound bytes
//	              (cmd, per socket)      (this package)                (cmd, per socket)
//
// The router never touches a codec: it regroups AMBE codewords for the
// destination's frame cadence (reframe.go) and copies them verbatim, exactly as
// the frame layer guarantees byte-exact (RFC-0003 §2). No vocoder, no DSP.
package router

import (
	"fmt"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
)

// Event types this daemon publishes onto the ordinary hub (RFC-0004 persistence
// and RFC-0008 status pick them up with no extra work — they are just events).
const (
	// EventBusBusy is emitted once per losing stream when arbitration drops a
	// second source while the token is held (RFC-0003 §5 rule 2). It maps onto
	// hub.Event as: Network = bus name, Source = winner mode, Mode = loser mode,
	// Detail = "busy: via <winner>" — enough for the UI to show "busy: via DMR".
	EventBusBusy = "bus_busy"
	// EventBusVoiceStart/End bracket a bus transmission so bridged traffic appears
	// in last-heard like any RF/net voice (RFC-0004: "a bus emits ordinary hub
	// events").
	EventBusVoiceStart = "bus_voice_start"
	EventBusVoiceEnd   = "bus_voice_end"
)

// Publisher receives the hub events the bus emits. cmd wires the real
// *hub.Hub; tests pass a capturing stub. It is the router's only side effect
// besides the emissions it returns.
type Publisher interface {
	Publish(hub.Event)
}

// Attachment is one runtime edge of the bus: a mode, its resolved frame-layer
// enum, and the translation params the destination constructor applies.
type Attachment struct {
	Mode   config.Mode
	FMode  frames.Mode
	Params frames.Params
}

// Config is everything the router needs for one bus, already resolved from the
// on-disk config.BusConfig (see FromBusConfig).
type Config struct {
	ID          string
	Name        string
	HangTime    time.Duration
	Attachments []Attachment
}

// FromBusConfig resolves a parsed on-disk config into a router Config, mapping
// each attachment's mode and translating its params. It rejects a non-reframe
// mode (the store validator already does, so this is defence in depth) or a bad
// numeric param.
func FromBusConfig(bc config.BusConfig) (Config, error) {
	c := Config{ID: bc.Bus.ID, Name: displayName(bc.Bus), HangTime: bc.HangTime()}
	for _, a := range bc.Attachments {
		fm, ok := frameMode(a.Mode)
		if !ok {
			return Config{}, fmt.Errorf("bus %q: mode %q has no frame implementation (reframe tier is DMR/YSF/NXDN)", bc.Bus.ID, a.Mode)
		}
		p, err := paramsFor(a)
		if err != nil {
			return Config{}, fmt.Errorf("bus %q %s attachment: %w", bc.Bus.ID, a.Mode, err)
		}
		c.Attachments = append(c.Attachments, Attachment{Mode: a.Mode, FMode: fm, Params: p})
	}
	return c, nil
}

func displayName(b config.Bus) string {
	if b.Name != "" {
		return b.Name
	}
	return b.ID
}

// AttachmentFor resolves one config.Attachment into a router Attachment through
// the same mode/param path FromBusConfig uses. It exists so the daemon can add a
// PEER-backed member's mode to a bus's router (RFC-0016): a remote attachment
// reframes exactly like a local one — only its I/O is a peer link, not a loopback
// — so its edge is built here rather than duplicating frameMode/paramsFor.
func AttachmentFor(a config.Attachment) (Attachment, error) {
	fm, ok := frameMode(a.Mode)
	if !ok {
		return Attachment{}, fmt.Errorf("mode %q has no frame implementation (reframe tier is DMR/YSF/NXDN)", a.Mode)
	}
	p, err := paramsFor(a)
	if err != nil {
		return Attachment{}, fmt.Errorf("%s attachment: %w", a.Mode, err)
	}
	return Attachment{Mode: a.Mode, FMode: fm, Params: p}, nil
}

// Emission is one outbound frame: a normalized destination Frame the caller turns
// into wire bytes via frames.Construct<Dst> with that attachment's params. The
// AMBE is already regrouped to the destination's cadence and the addressing is
// copied from the source (the constructor resolves callsign<->id per mode).
type Emission struct {
	Dst   config.Mode
	FMode frames.Mode
	Frame frames.Frame
}

// Bus is the running fan-out state for one bus. It is NOT safe for concurrent
// use: cmd drives it from a single goroutine (all sockets funnel into one select
// loop), which keeps the arbitration state a plain, testable state machine.
type Bus struct {
	cfg   Config
	pub   Publisher
	modes map[config.Mode]Attachment

	// Arbitration (§5 rule 2). One token; held by holder for holder's transmission.
	holding      bool
	holder       config.Mode
	lastActivity time.Time

	// Voice-event bracketing for the held transmission.
	voiceOpen  bool
	voiceSrc   string
	voiceDst   string
	voiceMode  config.Mode
	voiceStart time.Time

	// Per-transmission working state, reset on token acquire / release.
	reframers     map[config.Mode]*reframer
	emitted       map[emitKey]bool // (dst mode, stream id) the bus itself put out — echo suppression (§5 rule 4)
	busyAnnounced map[busyKey]bool // (loser mode, stream id) already surfaced as bus_busy — once per losing stream
	droppedFrames int64
}

type emitKey struct {
	mode   config.Mode
	stream uint32
}
type busyKey struct {
	mode   config.Mode
	stream uint32
}

// New builds a Bus from a resolved Config. pub may be nil (events are dropped),
// which is convenient in tests that only assert emissions.
func New(cfg Config, pub Publisher) *Bus {
	b := &Bus{cfg: cfg, pub: pub, modes: make(map[config.Mode]Attachment, len(cfg.Attachments))}
	for _, a := range cfg.Attachments {
		b.modes[a.Mode] = a
	}
	b.resetTransmission()
	return b
}

func (b *Bus) resetTransmission() {
	b.reframers = make(map[config.Mode]*reframer, len(b.cfg.Attachments))
	for _, a := range b.cfg.Attachments {
		b.reframers[a.Mode] = newReframer(a.FMode)
	}
	b.emitted = make(map[emitKey]bool)
	b.busyAnnounced = make(map[busyKey]bool)
}

// Ingest applies §5 to one inbound frame that entered on attachment `origin` at
// time `now`, returning the frames to emit to the other attachments (never to
// `origin` — §5 rule 1). It publishes bus_busy / voice events as a side effect.
// It never blocks and never panics on an unexpected frame.
func (b *Bus) Ingest(origin config.Mode, f frames.Frame, now time.Time) []Emission {
	b.MaybeRelease(now)

	if _, ok := b.modes[origin]; !ok {
		return nil // a frame from a mode not on this bus: ignore defensively
	}

	// §5 rule 4 — echo suppression. A frame whose (mode, stream) the bus itself
	// emitted is the loopback echo of its own transmission (the DMR case: the bus
	// shares the local DMRGateway loopback). Drop it before arbitration so it is
	// neither re-fanned nor mistaken for a competing source.
	if b.holding && b.emitted[emitKey{origin, f.Stream.ID}] {
		return nil
	}

	// §5 rule 2 — single-token arbitration.
	switch {
	case !b.holding:
		b.acquire(origin, f, now)
	case origin != b.holder:
		// A second source while the token is held: drop, count, and surface a
		// bus_busy exactly once for this losing stream (not once per frame).
		b.droppedFrames++
		k := busyKey{origin, f.Stream.ID}
		if !b.busyAnnounced[k] {
			b.busyAnnounced[k] = true
			b.publishBusy(origin)
		}
		return nil
	}

	// Accepted frame from the token holder.
	b.lastActivity = now
	if f.Kind == frames.KindVoice && !b.voiceOpen {
		b.openVoice(origin, f, now) // first audio after a header-less key-up
	}

	out := b.fanout(origin, f)

	if f.Kind == frames.KindTerminator {
		b.closeVoice(now) // stream ended; keep the token until hang so late echoes stay suppressed
	}
	return out
}

// acquire hands the token to origin at the start of its transmission and opens
// the voice bracket if the first frame carries/announces audio.
func (b *Bus) acquire(origin config.Mode, f frames.Frame, now time.Time) {
	b.holding = true
	b.holder = origin
	b.lastActivity = now
	b.resetTransmission()
	if f.Kind == frames.KindHeader || f.Kind == frames.KindVoice {
		b.openVoice(origin, f, now)
	}
}

// fanout emits f to every attachment except its origin (§5 rule 1). Header and
// terminator pass straight through (they carry no AMBE); voice frames are
// rate-matched to each destination's cadence and only whole destination frames
// are emitted.
func (b *Bus) fanout(origin config.Mode, f frames.Frame) []Emission {
	var out []Emission
	for _, dst := range b.cfg.Attachments {
		if dst.Mode == origin {
			continue // §5 rule 1: never emit to the source
		}
		switch f.Kind {
		case frames.KindHeader, frames.KindTerminator:
			out = append(out, b.emit(dst, framePassthrough(dst, f, f.Kind)))
		case frames.KindVoice:
			for _, grp := range b.reframers[dst.Mode].push(f.AMBE) {
				vf := framePassthrough(dst, f, frames.KindVoice)
				vf.AMBE = grp
				out = append(out, b.emit(dst, vf))
			}
		}
	}
	return out
}

// emit records the (dst, stream) so its loopback echo is later suppressed
// (§5 rule 4) and returns the Emission.
func (b *Bus) emit(dst Attachment, fr frames.Frame) Emission {
	b.emitted[emitKey{dst.Mode, fr.Stream.ID}] = true
	return Emission{Dst: dst.Mode, FMode: dst.FMode, Frame: fr}
}

// framePassthrough builds a destination frame carrying the source's addressing
// and stream identity (the constructor resolves callsign<->id and applies the
// destination params); AMBE is filled in by the caller for voice.
func framePassthrough(dst Attachment, f frames.Frame, kind frames.Kind) frames.Frame {
	return frames.Frame{
		Mode:        dst.FMode,
		Kind:        kind,
		SrcID:       f.SrcID,
		DstID:       f.DstID,
		SrcCallsign: f.SrcCallsign,
		DstCallsign: f.DstCallsign,
		Stream:      f.Stream,
	}
}

// MaybeRelease frees the token when the holder has been silent past the hang time
// (§5 rule 2). cmd calls this on a timer as well as on every frame, so a bus that
// goes quiet is released even with no further traffic. Releasing closes the voice
// bracket and clears the per-transmission echo/arbitration state.
func (b *Bus) MaybeRelease(now time.Time) bool {
	if !b.holding {
		return false
	}
	if now.Sub(b.lastActivity) < b.cfg.HangTime {
		return false
	}
	b.closeVoice(now)
	b.holding = false
	b.holder = ""
	b.resetTransmission()
	return true
}

// ForceRelease unconditionally releases the token and closes any open voice
// bracket — the clean-shutdown path (SIGTERM), where the daemon must not leave a
// dangling transmission on the bus regardless of hang time.
func (b *Bus) ForceRelease(now time.Time) {
	if !b.holding {
		return
	}
	b.closeVoice(now)
	b.holding = false
	b.holder = ""
	b.resetTransmission()
}

// openVoice publishes bus_voice_start once per held transmission.
func (b *Bus) openVoice(origin config.Mode, f frames.Frame, now time.Time) {
	b.voiceOpen = true
	b.voiceMode = origin
	b.voiceSrc = srcLabel(f)
	b.voiceDst = dstLabel(f)
	b.voiceStart = now
	if b.pub != nil {
		b.pub.Publish(hub.Event{
			Time:    now,
			Type:    EventBusVoiceStart,
			Mode:    modeLabel(origin),
			Source:  b.voiceSrc,
			Dest:    b.voiceDst,
			Network: b.cfg.Name,
			Detail:  "bus " + b.cfg.Name,
		})
	}
}

// closeVoice publishes bus_voice_end once, with the transmission duration.
func (b *Bus) closeVoice(now time.Time) {
	if !b.voiceOpen {
		return
	}
	b.voiceOpen = false
	if b.pub != nil {
		b.pub.Publish(hub.Event{
			Time:    now,
			Type:    EventBusVoiceEnd,
			Mode:    modeLabel(b.voiceMode),
			Source:  b.voiceSrc,
			Dest:    b.voiceDst,
			Network: b.cfg.Name,
			Seconds: now.Sub(b.voiceStart).Seconds(),
			Detail:  "bus " + b.cfg.Name,
		})
	}
}

// publishBusy surfaces a losing source as "busy: via <winner>" (RFC-0003 §5 rule
// 2). Winner is the current token holder, loser is `loser`.
func (b *Bus) publishBusy(loser config.Mode) {
	if b.pub == nil {
		return
	}
	winner := modeLabel(b.holder)
	b.pub.Publish(hub.Event{
		Time:    b.lastActivity,
		Type:    EventBusBusy,
		Mode:    modeLabel(loser),
		Source:  winner,
		Network: b.cfg.Name,
		Detail:  "busy: via " + winner,
	})
}

// Holder reports which mode holds the token (and whether one is held) — for the
// daemon's logging and the tests.
func (b *Bus) Holder() (config.Mode, bool) { return b.holder, b.holding }

// Dropped is the running count of voice frames dropped by arbitration (losing
// sources). Surfaced so the daemon can log it.
func (b *Bus) Dropped() int64 { return b.droppedFrames }

// Modes returns the attached modes (for the daemon's endpoint setup / logging).
func (b *Bus) Modes() []config.Mode {
	out := make([]config.Mode, 0, len(b.cfg.Attachments))
	for _, a := range b.cfg.Attachments {
		out = append(out, a.Mode)
	}
	return out
}

func srcLabel(f frames.Frame) string {
	if f.SrcCallsign != "" {
		return f.SrcCallsign
	}
	if f.SrcID != 0 {
		return fmt.Sprintf("%d", f.SrcID)
	}
	return ""
}

func dstLabel(f frames.Frame) string {
	if f.DstCallsign != "" {
		return f.DstCallsign
	}
	if f.DstID != 0 {
		return fmt.Sprintf("%d", f.DstID)
	}
	return ""
}
