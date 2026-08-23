package main

import (
	"log"
	"sync"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/dmrshim"
	"github.com/KN4OQW/waypoint/internal/idresolve"
	"github.com/KN4OQW/waypoint/internal/talkeralias"
)

// Talker Alias injection: naming the caller on the receiving radio's screen.
//
// A hotspot relaying a network call transmits whatever alias the originating
// radio sent, which is frequently nothing, leaving the receiver showing a bare
// numeric ID. This watches network→RF call starts on the loopback seam, resolves
// the source through the phonebook chain, and injects the DMRA frames the
// Waypoint MMDVM-Host fork accepts.
//
// Two properties are load-bearing and both are structural rather than careful.
//
// It only ever ADDS frames. The tap runs on the shim's fan-out goroutine, off the
// forwarding path entirely — a datagram has already been forwarded by the time an
// observer sees a copy of it — so nothing here can delay, reorder or block voice.
// That is the shim's package contract, and injection was the one extension it was
// built for.
//
// With the feature unset it emits nothing at all. The template's zero value is
// OFF, the emitter returns no frames for it, and the renderer omits the fork's
// InboundTalkerAlias key — so a node that has never been configured for this puts
// byte-identical traffic on the seam and hands MMDVM-Host a byte-identical INI.

// taInjector owns the emitter and its subscription to the shim.
//
// It is reconciled from config like the relay itself: the template can change
// under a running node, and the phonebook it draws on changes constantly.
type taInjector struct {
	mu       sync.Mutex
	emitter  *talkeralias.Emitter
	remove   func() // detaches the tap; nil when not attached
	template talkeralias.Template
	attached *dmrshim.Shim
}

// reconcile brings the injector in line with the template and the live shim.
//
// Called on the same tick as the relay's own reconcile, after it, so the shim it
// is handed is the one that reconcile just built or kept.
func (t *taInjector) reconcile(sh *dmrshim.Shim, want talkeralias.Template, chain *idresolve.Chain) {
	if t == nil {
		return // a server built without one, as several tests do
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	// An unrecognised template is treated as off rather than as an error: a value
	// written by a newer build must not make this one emit something it does not
	// understand.
	if !want.Valid() {
		want = talkeralias.TemplateOff
	}
	// No chain means no phonebook to draw on, which is the same as off.
	if chain == nil {
		want = talkeralias.TemplateOff
	}

	// Detach whenever the thing we are attached to, or what we would emit, has
	// changed. Re-attaching is cheap and happens at configuration time, not per
	// frame.
	if t.remove != nil && (sh != t.attached || want != t.template) {
		t.remove()
		t.remove, t.attached = nil, nil
	}
	t.template = want

	if want == talkeralias.TemplateOff || sh == nil {
		if t.emitter != nil {
			t.emitter.Reset()
		}
		return
	}
	if t.remove != nil {
		return // already attached with the right template
	}

	t.emitter = talkeralias.New(want, chainResolver{chain})
	shim := sh
	em := t.emitter
	t.remove = sh.AddTap(func(dir dmrshim.Direction, data []byte) {
		// Network→RF only. The other direction is what the radio sent, and the
		// alias on it is the originating operator's own business.
		if dir != dmrshim.ToHost {
			return
		}
		frames := em.Observe(data)
		if frames == nil {
			return // the common case: not a call start, or nobody knows the caller
		}
		for _, fr := range frames {
			// Injection failure is dropped deliberately. The alias is cosmetic; a
			// caller whose name does not appear is a worse screen, not a worse call,
			// and nothing here may take a voice path down over it. The shim counts
			// its own write errors for anyone diagnosing it.
			_ = shim.InjectToHost(fr)
		}
	})
	t.attached = sh
	log.Printf("talker alias: injecting %q on network->RF calls", want)
}

// stop detaches the tap. Called when the relay goes away.
func (t *taInjector) stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.remove != nil {
		t.remove()
		t.remove, t.attached = nil, nil
	}
	if t.emitter != nil {
		t.emitter.Reset()
	}
}

// talkerAliasTemplate reads the operator's choice out of the model.
func talkerAliasTemplate(m *config.Model) talkeralias.Template {
	if m == nil {
		return talkeralias.TemplateOff
	}
	return talkeralias.Template(m.DMR.TalkerAlias)
}

// chainResolver adapts the identity chain to the emitter's narrow view of it.
//
// The adapter lives here rather than in either package because neither should
// know about the other: internal/talkeralias is a self-contained format package
// that declares what it needs as primitives, and internal/idresolve is kept free
// of every import so it can drop into the frame layer's Resolver slot. Five lines
// at the wiring site is the price of both staying that way.
type chainResolver struct{ c *idresolve.Chain }

func (r chainResolver) DisplayForID(id uint32) (string, string, bool) {
	d := r.c.DisplayForID(id)
	return d.Callsign, d.FullName, d.Resolved()
}
