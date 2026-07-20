package peer

import "testing"

func TestLoopNeverBackToOrigin(t *testing.T) {
	env := NewEnvelope("shack", "dmr", "A")
	// never emit toward the exact origin node+attachment
	if ShouldEmitTo(env, "shack", "dmr") {
		t.Fatal("must not emit back to the origin node+attachment")
	}
	// a different attachment on the origin node IS allowed (a distinct edge)
	if !ShouldEmitTo(env, "shack", "ysf") {
		t.Fatal("a different attachment on the origin node is a valid destination")
	}
	// any attachment on another node is allowed
	if !ShouldEmitTo(env, "garage", "ysf") {
		t.Fatal("another node is a valid destination")
	}
}

func TestLoopInboundRejectsOwnAndCeiling(t *testing.T) {
	env := NewEnvelope("garage", "ysf", "A")
	env = Forward(env) // crossed one link (hop 1), arriving at the owner
	if ok, _ := AcceptInbound(env, "shack", DefaultMaxHops); !ok {
		t.Fatal("a foreign-origin frame within the hop ceiling should be accepted")
	}
	// a frame that returns to its origin node is a loop-back -> reject
	if ok, reason := AcceptInbound(env, "garage", DefaultMaxHops); ok || reason == "" {
		t.Fatal("a frame returning to its origin node must be rejected")
	}
	// hop-count ceiling
	over := env
	over.HopCount = DefaultMaxHops
	if ok, _ := AcceptInbound(over, "shack", DefaultMaxHops); ok {
		t.Fatal("a frame at the hop ceiling must be rejected")
	}
}

func TestForwardIncrementsHopPreservesOrigin(t *testing.T) {
	env := NewEnvelope("shack", "dmr", "A")
	f := Forward(env)
	if f.HopCount != 1 || f.OriginNode != "shack" || f.OriginAttachment != "dmr" || f.BusID != "A" {
		t.Fatalf("Forward should bump hop only: %+v", f)
	}
}
