package peer

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestBackoffSchedule(t *testing.T) {
	b := Backoff{Initial: time.Second, Max: 30 * time.Second}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for i, w := range want {
		if got := b.Next(); got != w*time.Second {
			t.Fatalf("attempt %d: got %v, want %vs", i, got, w)
		}
	}
	b.Reset()
	if got := b.Next(); got != time.Second {
		t.Fatalf("after reset: got %v, want 1s", got)
	}
}

// TestDialWithBackoffCancels: a dial that keeps failing is abandoned promptly when
// the context is cancelled — a dead peer's reconnect never wedges.
func TestDialWithBackoffCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	dial := func(context.Context, string) (net.Conn, error) {
		attempts++
		if attempts == 2 {
			cancel() // cancel during the second backoff wait
		}
		return nil, errors.New("refused")
	}
	bo := Backoff{Initial: 10 * time.Millisecond, Max: 20 * time.Millisecond}
	_, err := DialWithBackoff(ctx, "x:1", "peer", dial, bo)
	if err == nil {
		t.Fatal("cancelled dial should return an error")
	}
}

// TestDialWithBackoffConnects: once the dialer succeeds, a started Session is
// returned over the fake connection.
func TestDialWithBackoffConnects(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c2.Close()
	tries := 0
	dial := func(context.Context, string) (net.Conn, error) {
		tries++
		if tries < 2 {
			return nil, errors.New("not yet")
		}
		return c1, nil
	}
	s, err := DialWithBackoff(context.Background(), "x:1", "peer", dial, Backoff{Initial: time.Millisecond, Max: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Peer() != "peer" {
		t.Fatalf("peer id = %q", s.Peer())
	}
}
