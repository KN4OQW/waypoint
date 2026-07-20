package peer

import (
	"io"
	"sync"
	"time"
)

// session.go is the thin transport shell: it moves wire Messages over an
// io.ReadWriteCloser (a *tls.Conn in the field, net.Pipe in tests) with a bounded
// per-peer send queue and a keepalive heartbeat. It is deliberately socket-free —
// all the protocol logic (token, loop, jitter) lives in the pure files and is
// driven by the caller reading Recv().
//
// Backpressure (RFC-0016 §Design 1, made real): Send NEVER blocks the caller (the
// local fan-out loop). If the peer is slow or dead the send queue fills and the
// OLDEST voice frame is dropped (counted) — control messages (token, keepalive)
// are never dropped — so a wedged peer degrades that peer only.

// DefaultSendQueue bounds the per-peer outbound queue. At 20 ms cadence this is ~1 s
// of voice; a peer that cannot keep up loses its oldest frames, never the token
// control plane.
const DefaultSendQueue = 50

// Session is one peer connection.
type Session struct {
	rw    io.ReadWriteCloser
	peer  string // the peer node id (from Hello / config)
	queue *sendQueue

	recv    chan Message
	closed  chan struct{}
	closeMu sync.Once
	err     error
}

// NewSession wraps a connection. peer is the remote node id; queueMax bounds the
// send queue (0 => DefaultSendQueue).
func NewSession(rw io.ReadWriteCloser, peer string, queueMax int) *Session {
	if queueMax <= 0 {
		queueMax = DefaultSendQueue
	}
	return &Session{
		rw:     rw,
		peer:   peer,
		queue:  newSendQueue(queueMax),
		recv:   make(chan Message, 64),
		closed: make(chan struct{}),
	}
}

// Peer is the remote node id. Recv delivers received messages; it is closed when
// the connection ends (read Err() for the cause). Dropped counts voice frames
// shed to backpressure.
func (s *Session) Peer() string            { return s.peer }
func (s *Session) Recv() <-chan Message    { return s.recv }
func (s *Session) Closed() <-chan struct{} { return s.closed }
func (s *Session) Err() error              { return s.err }
func (s *Session) Dropped() int64          { return s.queue.droppedCount() }

// Start launches the read and write goroutines. keepalive is the idle heartbeat
// cadence (0 => KeepaliveInterval).
func (s *Session) Start(keepalive time.Duration) {
	if keepalive <= 0 {
		keepalive = KeepaliveInterval
	}
	go s.readLoop()
	go s.writeLoop(keepalive)
}

// Send enqueues a message for the peer without ever blocking (RFC-0016 backpressure
// rule). On a full queue it drops the oldest voice frame.
func (s *Session) Send(m Message) {
	s.queue.push(m)
}

// Close ends the session once; the read/write goroutines unwind and Recv/Closed
// close.
func (s *Session) Close() { s.closeWith(nil) }

func (s *Session) closeWith(err error) {
	s.closeMu.Do(func() {
		s.err = err
		close(s.closed)
		s.queue.close()
		_ = s.rw.Close()
	})
}

func (s *Session) readLoop() {
	for {
		m, err := ReadMessage(s.rw)
		if err != nil {
			s.closeWith(err)
			close(s.recv)
			return
		}
		select {
		case s.recv <- m:
		case <-s.closed:
			return
		}
	}
}

func (s *Session) writeLoop(keepalive time.Duration) {
	t := time.NewTimer(keepalive)
	defer t.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-s.queue.notify:
			for {
				m, ok := s.queue.pop()
				if !ok {
					break
				}
				if err := WriteMessage(s.rw, m); err != nil {
					s.closeWith(err)
					return
				}
			}
			resetTimer(t, keepalive)
		case <-t.C:
			if err := WriteMessage(s.rw, Message{Type: MsgKeepalive}); err != nil {
				s.closeWith(err)
				return
			}
			t.Reset(keepalive)
		}
	}
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// --- bounded send queue, drop-oldest-voice ----------------------------------

type sendQueue struct {
	mu      sync.Mutex
	items   []Message
	max     int
	dropped int64
	notify  chan struct{}
	done    bool
}

func newSendQueue(max int) *sendQueue {
	return &sendQueue{max: max, notify: make(chan struct{}, 1)}
}

func (q *sendQueue) push(m Message) {
	q.mu.Lock()
	if !q.done && len(q.items) >= q.max {
		// Drop the oldest VOICE frame; never a control message.
		if i := firstVoice(q.items); i >= 0 {
			q.items = append(q.items[:i], q.items[i+1:]...)
			q.dropped++
		} else if m.Type == MsgVoice {
			// Queue is full of control messages and the newcomer is voice: shed it.
			q.dropped++
			q.mu.Unlock()
			return
		}
	}
	if !q.done {
		q.items = append(q.items, m)
	}
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *sendQueue) pop() (Message, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return Message{}, false
	}
	m := q.items[0]
	q.items = q.items[1:]
	return m, true
}

func (q *sendQueue) droppedCount() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}

func (q *sendQueue) close() {
	q.mu.Lock()
	q.done = true
	q.items = nil
	q.mu.Unlock()
}

func firstVoice(items []Message) int {
	for i, m := range items {
		if m.Type == MsgVoice {
			return i
		}
	}
	return -1
}
