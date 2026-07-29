package peer

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"github.com/KN4OQW/waypoint/internal/backoff"
)

// dial.go is the thin socket layer: dial a peer with exponential backoff and wrap
// the resulting *tls.Conn in a Session. It is the only part of the package that
// touches a real network; the backoff schedule itself is a pure function so it is
// unit-tested without dialing.

// Backoff is an exponential dial backoff with a cap. Zero value is unusable; use
// DefaultBackoff. The schedule itself lives in internal/backoff, shared with the
// resilience supervisor; this alias keeps the dialer's call sites reading in terms
// of peering.
type Backoff = backoff.Backoff

// DefaultBackoff dials quickly at first (1 s) and backs off to 30 s, so a peer
// that is briefly down reconnects fast without hammering one that is gone. No
// jitter: these are LAN peers on the operator's own bus, not a shared upstream a
// herd of nodes could stampede.
func DefaultBackoff() Backoff { return Backoff{Initial: time.Second, Max: 30 * time.Second} }

// DialFunc dials an address and returns a connection; it is injectable so the
// reconnect loop is testable with a fake dialer.
type DialFunc func(ctx context.Context, addr string) (net.Conn, error)

// TLSDialer returns a DialFunc that establishes a pinned mutual-TLS 1.3 connection
// (cfg from ClientConfig).
func TLSDialer(cfg *tls.Config) DialFunc {
	return func(ctx context.Context, addr string) (net.Conn, error) {
		d := &tls.Dialer{Config: cfg}
		return d.DialContext(ctx, "tcp", addr)
	}
}

// Listen opens a pinned mutual-TLS listener for the owner side (cfg from
// ServerConfig). AcceptSessions turns accepted connections into started Sessions
// on the returned channel until ctx is cancelled or the listener closes — the
// owner's daemon selects on it alongside its local loopbacks.
func Listen(addr string, cfg *tls.Config) (net.Listener, error) {
	return tls.Listen("tcp", addr, cfg)
}

// AcceptSessions accepts connections on ln and delivers a started Session per
// connection. The peer id is not yet known at accept time (it arrives in the
// Hello), so sessions start with an empty peer id the caller fills from Hello.
func AcceptSessions(ctx context.Context, ln net.Listener) <-chan *Session {
	out := make(chan *Session)
	go func() {
		defer close(out)
		go func() { <-ctx.Done(); ln.Close() }()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s := NewSession(conn, "", DefaultSendQueue)
			s.Start(KeepaliveInterval)
			select {
			case out <- s:
			case <-ctx.Done():
				s.Close()
				return
			}
		}
	}()
	return out
}

// SetPeer fills in a session's peer id once it is known (from the Hello). It is
// safe to call once, before the session is shared across goroutines by id.
func (s *Session) SetPeer(id string) { s.peer = id }

// DialWithBackoff repeatedly dials addr until it connects or ctx is cancelled,
// backing off between failures. On success it returns a started Session (the
// caller drives Recv()); on ctx cancellation it returns ctx.Err(). The sleep uses
// a timer so a cancelled context aborts the wait immediately — a dead peer never
// blocks anything but its own reconnect goroutine.
func DialWithBackoff(ctx context.Context, addr, peerID string, dial DialFunc, bo Backoff) (*Session, error) {
	for {
		conn, err := dial(ctx, addr)
		if err == nil {
			s := NewSession(conn, peerID, DefaultSendQueue)
			s.Start(KeepaliveInterval)
			return s, nil
		}
		wait := bo.Next()
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}
