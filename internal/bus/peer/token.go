package peer

import "time"

// token.go is the owner-held token protocol (RFC-0016 decision 4) as two pure
// state machines: Server runs on the bus OWNER and arbitrates the single
// cluster-wide token; Client runs on a MEMBER and manages its request/hold/release
// lifecycle. Both are driven entirely by method calls that take an explicit `now`,
// so they test against a fake clock with no sockets and no goroutines.

// Transport timing. RFC-0016 fixes the deadline/jitter (jitter.go) from the spike;
// these liveness/token timers are left to the implementation, chosen here to be
// comfortably longer than the LAN's sub-millisecond transport (RFC-0016 §Design 1)
// while still reclaiming promptly after a cable pull.
const (
	KeepaliveInterval   = 2 * time.Second // heartbeat cadence on an idle connection
	PeerDeadTimeout     = 6 * time.Second // 3 missed keepalives => the peer is gone
	TokenReclaimTimeout = 5 * time.Second // a member that drops while holding is reclaimed after this
	TokenRequestTimeout = 2 * time.Second // a member's unanswered request gives up (voice dropped)
)

// localHolder is the sentinel holder id for the owner's own local sources, so the
// server arbitrates local and remote sources with one token.
const localHolder = "\x00local"

// Grant is the outcome of a member token request.
type Grant int

const (
	Granted Grant = iota
	Denied
)

// Server is the owner-side token arbiter for ONE bus. It is not safe for
// concurrent use; the daemon drives it from its single select loop.
type Server struct {
	hang    time.Duration
	reclaim time.Duration

	holder       string    // "" = free, localHolder = a local source, else a member node id
	lastActivity time.Time // last accepted frame time from the holder
	lostAt       time.Time // when the current holder's connection dropped (zero = connected)
	dropped      int64
}

// NewServer builds an owner token server. hang is the bus arbitration hang
// (RFC-0003 §5 rule 2, from config); reclaim is how long a disconnected holder's
// token is kept before it is reclaimed.
func NewServer(hang time.Duration) *Server {
	return &Server{hang: hang, reclaim: TokenReclaimTimeout}
}

// Holder reports the current holder ("" if free); local reports whether it is the
// owner's own local source.
func (s *Server) Holder() (string, bool) { return s.holder, s.holder == localHolder }
func (s *Server) Dropped() int64         { return s.dropped }

// LocalAcquire is the owner's local source keying up. It wins iff the token is
// free (first-come, matching the single-token rule); a member already holding is
// NOT preempted. Returns true if the local source now holds the token.
func (s *Server) LocalAcquire(now time.Time) bool {
	if s.holder == "" {
		s.holder, s.lastActivity, s.lostAt = localHolder, now, time.Time{}
		return true
	}
	if s.holder == localHolder {
		s.lastActivity = now
		return true
	}
	s.dropped++
	return false
}

// LocalRelease frees the token if the local source holds it.
func (s *Server) LocalRelease() {
	if s.holder == localHolder {
		s.holder, s.lostAt = "", time.Time{}
	}
}

// RequestFromMember arbitrates a member key-up. Granted iff the token is free or
// already held by the same member; otherwise Denied and counted.
func (s *Server) RequestFromMember(node string, now time.Time) Grant {
	switch s.holder {
	case "":
		s.holder, s.lastActivity, s.lostAt = node, now, time.Time{}
		return Granted
	case node:
		s.lastActivity, s.lostAt = now, time.Time{}
		return Granted
	default:
		s.dropped++
		return Denied
	}
}

// MemberActivity refreshes the hang timer on each accepted voice frame from the
// token-holding member.
func (s *Server) MemberActivity(node string, now time.Time) {
	if s.holder == node {
		s.lastActivity, s.lostAt = now, time.Time{}
	}
}

// ReleaseFromMember frees the token if that member holds it (hang expired on the
// member, or an explicit release).
func (s *Server) ReleaseFromMember(node string) {
	if s.holder == node {
		s.holder, s.lostAt = "", time.Time{}
	}
}

// MemberDisconnected marks that a member's connection dropped. If it was holding
// the token, the token is NOT freed immediately — it is reclaimed after the
// reclaim timeout (Tick), so a brief blip does not cut a transmission, but a real
// cable pull frees the bus cleanly. A disconnect of a non-holder is a no-op.
func (s *Server) MemberDisconnected(node string, now time.Time) {
	if s.holder == node {
		s.lostAt = now
	}
}

// Tick advances time. It frees the token when the holder has been silent past the
// hang time, or when a disconnected holder's reclaim timeout elapses. Returns true
// (with the freed holder) when it released, so the caller can surface the release.
func (s *Server) Tick(now time.Time) (released bool, holder string) {
	if s.holder == "" {
		return false, ""
	}
	// A disconnected holder is reclaimed after the reclaim timeout (cable pull).
	if !s.lostAt.IsZero() && now.Sub(s.lostAt) >= s.reclaim {
		h := s.holder
		s.holder, s.lostAt = "", time.Time{}
		return true, h
	}
	// Any holder that goes silent past the hang time releases (RFC-0003 §5 rule 2).
	if now.Sub(s.lastActivity) >= s.hang {
		h := s.holder
		s.holder, s.lostAt = "", time.Time{}
		return true, h
	}
	return false, ""
}

// --- member side -------------------------------------------------------------

type clientState int

const (
	stIdle       clientState = iota // no local transmission
	stRequesting                    // sent a token request, awaiting grant/deny
	stHolding                       // holds the token, streaming
	stDenied                        // request was denied; local voice dropped until key-down
)

// Client is the member-side token lifecycle for ONE remote bus membership.
type Client struct {
	hang       time.Duration
	reqTimeout time.Duration

	state     clientState
	streamID  uint32
	sentAt    time.Time // when the current request was sent
	lastVoice time.Time // last local voice frame
	dropped   int64
}

// NewClient builds a member token client. hang is the local transmission hang.
func NewClient(hang time.Duration) *Client {
	return &Client{hang: hang, reqTimeout: TokenRequestTimeout, state: stIdle}
}

func (c *Client) Dropped() int64     { return c.dropped }
func (c *Client) CanStream() bool    { return c.state == stHolding }
func (c *Client) StreamID() uint32   { return c.streamID }
func (c *Client) State() clientState { return c.state }

// LocalKeyup begins a local transmission (stream). If idle, it moves to Requesting
// and the caller must send a TokenRequest for streamID; a repeated key-up on the
// same stream while requesting/holding just refreshes activity. Returns true when a
// TokenRequest should be sent.
func (c *Client) LocalKeyup(streamID uint32, now time.Time) (sendRequest bool) {
	c.lastVoice = now
	switch c.state {
	case stIdle:
		c.state, c.streamID, c.sentAt = stRequesting, streamID, now
		return true
	case stRequesting, stHolding:
		return false
	case stDenied:
		// still the same losing transmission; stay denied (dropped) until key-down
		return false
	}
	return false
}

// LocalVoice refreshes the local activity timer for a frame in the current stream.
func (c *Client) LocalVoice(now time.Time) { c.lastVoice = now }

// RxGrant handles a token grant. Accepted only for the outstanding request's
// stream (a stale grant for an old stream is ignored).
func (c *Client) RxGrant(streamID uint32, now time.Time) {
	if c.state == stRequesting && streamID == c.streamID {
		c.state, c.lastVoice = stHolding, now
	}
}

// RxDeny handles a token deny: the local transmission loses arbitration; its
// frames are dropped (counted) until key-down.
func (c *Client) RxDeny(streamID uint32) {
	if c.state == stRequesting && streamID == c.streamID {
		c.state = stDenied
	}
}

// DropLocalVoice counts a local voice frame dropped because the member does not
// hold the token (requesting or denied). The caller invokes it instead of
// forwarding the frame.
func (c *Client) DropLocalVoice() { c.dropped++ }

// Tick advances time. It ends a transmission after the hang time (sending a
// Release if the token was held) and abandons an unanswered request after the
// request timeout. Returns whether a Release should be sent for streamID.
func (c *Client) Tick(now time.Time) (sendRelease bool, streamID uint32) {
	switch c.state {
	case stHolding:
		if now.Sub(c.lastVoice) >= c.hang {
			sid := c.streamID
			c.reset()
			return true, sid
		}
	case stRequesting:
		if now.Sub(c.sentAt) >= c.reqTimeout {
			c.dropped++ // the whole transmission was lost to an unanswered request
			c.reset()
		}
	case stDenied:
		if now.Sub(c.lastVoice) >= c.hang {
			c.reset() // key-down: ready for the next transmission
		}
	}
	return false, 0
}

// OwnerDisconnected is the owner connection dropping at the member (RFC-0016 §4):
// the member releases locally and the bus is marked down. Returns whether the
// member was mid-transmission (so the caller can log the interruption); the
// "bus down: owner offline" hub event is the caller's to publish.
func (c *Client) OwnerDisconnected() (wasActive bool) {
	wasActive = c.state == stHolding || c.state == stRequesting
	c.reset()
	return wasActive
}

func (c *Client) reset() { c.state, c.streamID = stIdle, 0 }
