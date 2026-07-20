package peer

import (
	"testing"
	"time"
)

var t0 = time.Unix(1_700_000_000, 0)

// --- owner server -----------------------------------------------------------

func TestServerSingleTokenFirstComeWins(t *testing.T) {
	s := NewServer(2 * time.Second)
	if s.RequestFromMember("garage", t0) != Granted {
		t.Fatal("first requester should be granted")
	}
	if s.RequestFromMember("spare", t0.Add(10*time.Millisecond)) != Denied {
		t.Fatal("a second member should be denied while held")
	}
	if s.LocalAcquire(t0.Add(20 * time.Millisecond)) {
		t.Fatal("a local source should not preempt a member holder")
	}
	if s.Dropped() != 2 {
		t.Fatalf("both losers should be counted, got %d", s.Dropped())
	}
}

func TestServerReleaseAndHang(t *testing.T) {
	s := NewServer(2 * time.Second)
	s.RequestFromMember("garage", t0)
	// explicit release frees it
	s.ReleaseFromMember("garage")
	if h, _ := s.Holder(); h != "" {
		t.Fatalf("explicit release should free the token, holder=%q", h)
	}
	// hang release: hold, go silent, Tick past hang
	s.RequestFromMember("garage", t0)
	if rel, _ := s.Tick(t0.Add(1 * time.Second)); rel {
		t.Fatal("should not release within hang")
	}
	rel, holder := s.Tick(t0.Add(2 * time.Second))
	if !rel || holder != "garage" {
		t.Fatalf("silence past hang should release garage, got rel=%v holder=%q", rel, holder)
	}
}

// TestServerCablePullReclaim is issue #65 acceptance 3 at the owner: a member
// holding the token drops its connection; the token is NOT freed instantly (a
// blip must not cut a QSO) but is reclaimed after the reclaim timeout, cleanly and
// with no crash. The former loser can then take the bus.
func TestServerCablePullReclaim(t *testing.T) {
	s := NewServer(30 * time.Second) // long hang so the reclaim path is what fires
	s.RequestFromMember("garage", t0)

	// cable pull mid-transmission
	s.MemberDisconnected("garage", t0.Add(100*time.Millisecond))

	// within the reclaim window the token is still held (brief blip tolerated)
	if rel, _ := s.Tick(t0.Add(1 * time.Second)); rel {
		t.Fatal("token should not be reclaimed within the reclaim window")
	}
	if s.RequestFromMember("spare", t0.Add(1*time.Second)) != Denied {
		t.Fatal("another member is still denied until reclaim")
	}

	// past the reclaim timeout it is reclaimed
	rel, holder := s.Tick(t0.Add(100*time.Millisecond + TokenReclaimTimeout))
	if !rel || holder != "garage" {
		t.Fatalf("token should be reclaimed from the dropped holder, rel=%v holder=%q", rel, holder)
	}
	// and now the former loser can take it
	if s.RequestFromMember("spare", t0.Add(10*time.Second)) != Granted {
		t.Fatal("after reclaim the bus should be free for another member")
	}
}

func TestServerReconnectBeforeReclaimKeepsToken(t *testing.T) {
	s := NewServer(30 * time.Second)
	s.RequestFromMember("garage", t0)
	s.MemberDisconnected("garage", t0.Add(1*time.Second))
	// the member reconnects and resumes activity before the reclaim timeout
	s.MemberActivity("garage", t0.Add(2*time.Second))
	if rel, _ := s.Tick(t0.Add(1*time.Second + TokenReclaimTimeout)); rel {
		t.Fatal("a reconnected holder that resumed activity must not be reclaimed")
	}
	if h, _ := s.Holder(); h != "garage" {
		t.Fatalf("garage should still hold after reconnect, got %q", h)
	}
}

// --- member client ----------------------------------------------------------

func TestClientRequestGrantStreamRelease(t *testing.T) {
	c := NewClient(2 * time.Second)
	if !c.LocalKeyup(0x11, t0) {
		t.Fatal("first key-up should send a request")
	}
	if c.CanStream() {
		t.Fatal("must not stream before a grant")
	}
	c.RxGrant(0x11, t0.Add(5*time.Millisecond))
	if !c.CanStream() {
		t.Fatal("after grant the member may stream")
	}
	// silence past hang (measured from the last activity) -> release
	rel, sid := c.Tick(t0.Add(2100 * time.Millisecond))
	if !rel || sid != 0x11 {
		t.Fatalf("hang should send a release for the stream, rel=%v sid=%x", rel, sid)
	}
	if c.CanStream() || c.State() != stIdle {
		t.Fatal("after release the client is idle")
	}
}

func TestClientDenyDropsUntilKeyDown(t *testing.T) {
	c := NewClient(2 * time.Second)
	c.LocalKeyup(0x22, t0)
	c.RxDeny(0x22)
	if c.CanStream() {
		t.Fatal("a denied member must not stream")
	}
	// frames during the denied transmission are dropped
	c.DropLocalVoice()
	c.DropLocalVoice()
	if c.Dropped() != 2 {
		t.Fatalf("denied frames should be counted, got %d", c.Dropped())
	}
	// key-down (hang) resets to idle, ready for the next transmission
	c.Tick(t0.Add(2 * time.Second))
	if c.State() != stIdle {
		t.Fatal("after key-down a denied client returns to idle")
	}
}

func TestClientStaleGrantIgnored(t *testing.T) {
	c := NewClient(2 * time.Second)
	c.LocalKeyup(0x33, t0)
	c.RxGrant(0x99, t0) // grant for a different (old) stream
	if c.CanStream() {
		t.Fatal("a grant for a different stream must be ignored")
	}
	c.RxGrant(0x33, t0)
	if !c.CanStream() {
		t.Fatal("the grant for the current stream is accepted")
	}
}

func TestClientRequestTimeout(t *testing.T) {
	c := NewClient(2 * time.Second)
	c.LocalKeyup(0x44, t0)
	c.Tick(t0.Add(TokenRequestTimeout)) // no grant/deny arrived
	if c.State() != stIdle {
		t.Fatal("an unanswered request should time out to idle")
	}
	if c.Dropped() != 1 {
		t.Fatalf("the lost transmission should be counted, got %d", c.Dropped())
	}
}

// TestClientOwnerDisconnect is issue #65 acceptance 3 at the member: the owner
// connection drops mid-transmission; the member releases locally (idle) so the
// caller can raise "bus down: owner offline" — no crash-loop, just a state reset.
func TestClientOwnerDisconnect(t *testing.T) {
	c := NewClient(2 * time.Second)
	c.LocalKeyup(0x55, t0)
	c.RxGrant(0x55, t0)
	if !c.OwnerDisconnected() {
		t.Fatal("a disconnect mid-transmission should report wasActive")
	}
	if c.CanStream() || c.State() != stIdle {
		t.Fatal("after owner loss the member is idle (local release)")
	}
	// a disconnect while idle is a clean no-op
	if c.OwnerDisconnected() {
		t.Fatal("an idle disconnect should report not-active")
	}
}
