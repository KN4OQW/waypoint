package peer

// loop.go is cross-peer loop prevention (RFC-0016 §5), pure functions over the
// frame Envelope. Combined with RFC-0003's rules (never emit to the source; a mode
// attaches to at most one bus per node), these make a peered fan-out acyclic.

// DefaultMaxHops is the hard hop-count ceiling backstop. RFC-0016 §5 sets the
// natural bound at the peer count (a valid frame never approaches it); this is a
// belt-and-suspenders cap against a mis-paired ring, well above any real topology.
const DefaultMaxHops = 8

// AcceptInbound decides whether a frame arriving over a peer link may enter this
// node's fan-out. It is rejected when it has returned to its ORIGIN node (a
// loop-back of our own transmission) or when its hop count has hit the ceiling
// (RFC-0016 §5). The reason is returned for counting/logging.
func AcceptInbound(env Envelope, localNode string, maxHops uint8) (accept bool, reason string) {
	if env.OriginNode == localNode {
		return false, "loop: frame returned to its origin node"
	}
	if env.HopCount >= maxHops {
		return false, "loop: hop-count ceiling reached"
	}
	return true, ""
}

// ShouldEmitTo reports whether a frame may be emitted toward a destination peer
// link identified by (dstNode, dstAttachment). RFC-0016 §5 rule 1 across peers: a
// frame is never re-emitted to the node+attachment it originated on.
func ShouldEmitTo(env Envelope, dstNode, dstAttachment string) bool {
	return !(dstNode == env.OriginNode && dstAttachment == env.OriginAttachment)
}

// Forward returns the envelope to put on a frame leaving this node toward a peer:
// the origin fields are preserved (they identify where the frame entered the
// cluster) and the hop count is incremented for the link it is about to cross.
func Forward(env Envelope) Envelope {
	env.HopCount++
	return env
}

// NewEnvelope stamps a frame entering the cluster at this node on a given
// attachment for a bus — hop count 0, origin = here.
func NewEnvelope(localNode, attachment, busID string) Envelope {
	return Envelope{OriginNode: localNode, OriginAttachment: attachment, BusID: busID, HopCount: 0}
}
