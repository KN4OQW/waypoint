package messages

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/events"
)

// The bench case, as a test.
//
// A message posted during a long transmission used to die on the single 60-second
// ceiling, because the transmission ran 37 seconds and BrandMeister then released
// a backlog of queued replies for another 43. The channel was busy for entirely
// ordinary reasons and the operator's text was thrown away.
//
// Now it waits, goes back on the queue, and sends when the channel finally clears.
func TestABusyChannelDelaysAMessageRatherThanLosingIt(t *testing.T) {
	h := newHarness(t, defaultWiring())

	// Busy for longer than ONE attempt's ceiling, so the message must be requeued
	// at least once before it gets through. Counted in sleeps because the clock is
	// virtual: waitForIdle polls every IdleWindow/4, so one ceiling is
	// ChannelWaitPerAttempt/(IdleWindow/4) polls.
	perAttempt := int(ChannelWaitPerAttempt / (IdleWindow / 4))
	busyUntil := perAttempt + perAttempt/2 // a ceiling and a half

	var mu sync.Mutex
	sleeps, maxAttempts := 0, 0
	// The attempt count is recorded from inside the clock hook, which already runs
	// under this lock and often. Watching it from a separate goroutine needs the
	// count sampled before success clears it, and racing the sender for that is a
	// worse test than piggybacking on a tick that is already happening.
	h.clock.on = func(time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		sleeps++
		if sleeps <= busyUntil {
			h.relay.traffic(2)
		}
		if n := h.svc.attemptsFor(msgID(h)); n > maxAttempts {
			maxAttempts = n
		}
	}
	h.run(t)

	m, err := h.svc.Send(3180299, "delayed, not lost", false)
	if err != nil {
		t.Fatal(err)
	}
	sent := h.waitState(t, m.ID, events.StateSent)

	if sent.Reason != "" {
		t.Errorf("reason = %q on a message that got through", sent.Reason)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxAttempts < 1 {
		t.Errorf("the message was never requeued (attempts = %d); the retry path was not exercised", maxAttempts)
	}
	if sleeps <= perAttempt {
		t.Errorf("only slept %d times, less than one ceiling (%d); the channel never blocked it", sleeps, perAttempt)
	}
}

// msgID is the id of the only message in flight in these tests. The clock hook
// runs before Send returns, so it cannot close over the id Send hands back.
func msgID(h *harness) int64 { return 1 }

// A channel that never clears still ends, and says how hard it tried. Waiting
// forever would be its own bug.
func TestAChannelThatNeverClearsGivesUpAndSaysHowLong(t *testing.T) {
	h := newHarness(t, defaultWiring())
	h.clock.on = func(time.Duration) { h.relay.traffic(2) }
	h.run(t)

	m, err := h.svc.Send(3180299, "never gets out", false)
	if err != nil {
		t.Fatal(err)
	}
	failed := h.waitState(t, m.ID, events.StateFailed)
	for _, want := range []string{"busy", "static talkgroup", "gave up", "attempts"} {
		if !strings.Contains(failed.Reason, want) {
			t.Errorf("reason does not mention %q: %s", want, failed.Reason)
		}
	}
	if n := len(h.relay.frames()); n != 0 {
		t.Errorf("%d frames were transmitted at a channel that never cleared", n)
	}
}

// The reason a busy message is requeued instead of blocking: with one sender
// goroutine, a message waiting out its ceiling would hold everything behind it.
// It goes to the BACK of the queue, so the next message gets its turn.
func TestAWaitingMessageDoesNotStallTheOnesBehindIt(t *testing.T) {
	h := newHarness(t, defaultWiring())

	// Busy only while the FIRST message is trying: once anything has been
	// transmitted the channel is clear. The first message therefore has to yield
	// for the second to get through at all.
	var mu sync.Mutex
	h.clock.on = func(time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		if len(h.relay.injected) == 0 && h.svc.attemptsFor(1) == 0 {
			h.relay.traffic(2)
		}
	}
	h.run(t)

	first, err := h.svc.Send(3180291, "first, blocked", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.svc.Send(3180292, "second, behind it", false)
	if err != nil {
		t.Fatal(err)
	}
	// Both must arrive. The order between them is not the contract — that a
	// blocked message does not strand the queue is.
	h.waitState(t, second.ID, events.StateSent)
	h.waitState(t, first.ID, events.StateSent)
}

// The attempt count is per message and is cleared when it stops needing one, so a
// message that waits once and then sends does not carry a strike into next time.
func TestAttemptCountsAreClearedOnSuccess(t *testing.T) {
	h := newHarness(t, defaultWiring())
	var mu sync.Mutex
	sleeps := 0
	h.clock.on = func(time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		sleeps++
		if sleeps <= 400 {
			h.relay.traffic(2)
		}
	}
	h.run(t)

	m, err := h.svc.Send(3180299, "waits once", false)
	if err != nil {
		t.Fatal(err)
	}
	h.waitState(t, m.ID, events.StateSent)
	if n := h.svc.attemptsFor(m.ID); n != 0 {
		t.Errorf("attempts for a sent message = %d, want 0", n)
	}
}

// attemptsFor exposes the in-memory attempt count for the tests. It is not part
// of the service's API: the count is this process's patience, not a property of
// the message, and nothing outside should be making decisions on it.
func (s *Service) attemptsFor(id int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts[id]
}
