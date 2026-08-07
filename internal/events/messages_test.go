package events

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func msgStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func outbound(text string) Message {
	return Message{Peer: 3180299, Local: 3180202, Text: text}
}

// The whole state machine, every state against every state. A table of the legal
// pairs plus an exhaustive sweep is the only way to be sure the map and the
// intended machine agree — a switch statement with a missing case reads exactly
// like one with all its cases.
func TestMessageTransitions(t *testing.T) {
	all := []MessageState{StateQueued, StateTransmitting, StateSent, StateReceived, StateFailed}
	legal := map[MessageState]map[MessageState]bool{
		StateQueued:       {StateTransmitting: true, StateFailed: true},
		StateTransmitting: {StateSent: true, StateFailed: true},
	}
	for _, from := range all {
		for _, to := range all {
			want := legal[from][to]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}

	// Terminal has to agree with the map, or a caller checking one and the store
	// enforcing the other would disagree about a finished message.
	for _, tc := range []struct {
		state MessageState
		want  bool
	}{
		{StateQueued, false},
		{StateTransmitting, false},
		{StateSent, true},
		{StateReceived, true},
		{StateFailed, true},
	} {
		if got := Terminal(tc.state); got != tc.want {
			t.Errorf("Terminal(%s) = %v, want %v", tc.state, got, tc.want)
		}
	}

	// Every state named by the type has to appear in the machine. A state added to
	// the constants and forgotten here would silently permit no transitions at all,
	// which for a new outbound state means messages that queue and never move.
	for _, s := range all {
		if _, ok := messageTransitions[s]; !ok {
			t.Errorf("state %s is not in the transition map", s)
		}
	}
}

// The outbound life story, in order, with the store enforcing each step.
func TestOutboundLifecycle(t *testing.T) {
	s := msgStore(t)

	m, err := s.Enqueue(outbound("hello there"))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if m.ID == 0 {
		t.Fatal("no id assigned")
	}
	if m.Direction != Outbound || m.State != StateQueued {
		t.Errorf("= %s/%s, want out/queued", m.Direction, m.State)
	}
	if m.CreatedAt.IsZero() || !m.UpdatedAt.Equal(m.CreatedAt) {
		t.Errorf("timestamps = %v / %v", m.CreatedAt, m.UpdatedAt)
	}

	if m, err = s.Advance(m.ID, StateTransmitting, ""); err != nil {
		t.Fatalf("Advance to transmitting: %v", err)
	}
	if m.State != StateTransmitting {
		t.Errorf("state = %s", m.State)
	}
	if m.UpdatedAt.Before(m.CreatedAt) {
		t.Error("updated_at went backwards")
	}

	if m, err = s.Advance(m.ID, StateSent, ""); err != nil {
		t.Fatalf("Advance to sent: %v", err)
	}
	if m.State != StateSent {
		t.Errorf("state = %s", m.State)
	}

	// And sent is the end. Unconfirmed DMR data carries no acknowledgement, so
	// there is no later fact to record and nothing may move it again.
	if _, err := s.Advance(m.ID, StateFailed, "too late"); !errors.Is(err, ErrBadTransition) {
		t.Errorf("advancing a sent message: err = %v, want ErrBadTransition", err)
	}

	got, err := s.Message(m.ID)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if got.Text != "hello there" || got.Peer != 3180299 || got.Local != 3180202 {
		t.Errorf("round trip = %+v", got)
	}
}

// A failure keeps its reason; a success is not allowed one. A "reason" beside a
// success is either noise or a lie, and both cost an operator time.
func TestFailureCarriesAReasonAndSuccessDoesNot(t *testing.T) {
	s := msgStore(t)

	m, _ := s.Enqueue(outbound("this one fails"))
	m, err := s.Advance(m.ID, StateFailed, "the relay is not running")
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if m.State != StateFailed || m.Reason != "the relay is not running" {
		t.Errorf("= %s/%q", m.State, m.Reason)
	}

	m2, _ := s.Enqueue(outbound("this one works"))
	m2, _ = s.Advance(m2.ID, StateTransmitting, "ignored")
	if m2.Reason != "" {
		t.Errorf("a transmitting message carries reason %q", m2.Reason)
	}
	m2, _ = s.Advance(m2.ID, StateSent, "also ignored")
	if m2.Reason != "" {
		t.Errorf("a sent message carries reason %q", m2.Reason)
	}
}

// Inbound messages are born finished. There is nothing to record about a message
// that has already been reassembled and checksum-verified.
func TestInboundIsBornTerminal(t *testing.T) {
	s := msgStore(t)

	m, err := s.RecordInbound(Message{Peer: 3180299, Local: 3180202, Text: "from the radio"})
	if err != nil {
		t.Fatalf("RecordInbound: %v", err)
	}
	if m.Direction != Inbound || m.State != StateReceived {
		t.Errorf("= %s/%s, want in/received", m.Direction, m.State)
	}
	for _, to := range []MessageState{StateQueued, StateTransmitting, StateSent, StateFailed} {
		if _, err := s.Advance(m.ID, to, ""); !errors.Is(err, ErrBadTransition) {
			t.Errorf("advancing a received message to %s: err = %v, want ErrBadTransition", to, err)
		}
	}
}

// A caller cannot smuggle a state in through the struct. Enqueue means queued and
// RecordInbound means received, whatever the caller filled in.
func TestTheCallerDoesNotChooseTheState(t *testing.T) {
	s := msgStore(t)

	m, err := s.Enqueue(Message{Peer: 1, Local: 2, Text: "x", Direction: Inbound, State: StateSent, Reason: "no"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Direction != Outbound || m.State != StateQueued || m.Reason != "" {
		t.Errorf("Enqueue honoured the caller's fields: %+v", m)
	}

	in, err := s.RecordInbound(Message{Peer: 1, Local: 2, Text: "y", Direction: Outbound, State: StateQueued})
	if err != nil {
		t.Fatal(err)
	}
	if in.Direction != Inbound || in.State != StateReceived {
		t.Errorf("RecordInbound honoured the caller's fields: %+v", in)
	}
}

func TestMessageRejections(t *testing.T) {
	s := msgStore(t)

	for _, tc := range []struct {
		name string
		m    Message
		want string
	}{
		{"a peer wider than 24 bits", Message{Peer: 0x1000000, Local: 1, Text: "x"}, "24-bit"},
		{"a local id wider than 24 bits", Message{Peer: 1, Local: 0x1000000, Text: "x"}, "24-bit"},
		{"text past the store's limit", Message{Peer: 1, Local: 2, Text: strings.Repeat("a", MaxMessageBytes+1)}, "limit"},
		{"text that is not UTF-8", Message{Peer: 1, Local: 2, Text: "bad \xc3"}, "UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Enqueue(tc.m); !errors.Is(err, ErrBadMessage) {
				t.Errorf("err = %v, want ErrBadMessage", err)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
			if _, err := s.RecordInbound(tc.m); !errors.Is(err, ErrBadMessage) {
				t.Errorf("RecordInbound: err = %v, want ErrBadMessage", err)
			}
		})
	}

	// Exactly at the limit is fine; the bound is inclusive.
	if _, err := s.Enqueue(Message{Peer: 1, Local: 2, Text: strings.Repeat("a", MaxMessageBytes)}); err != nil {
		t.Errorf("a message exactly at the limit was refused: %v", err)
	}
}

func TestMessageNotFound(t *testing.T) {
	s := msgStore(t)
	if _, err := s.Message(999); !errors.Is(err, ErrMessageNotFound) {
		t.Errorf("Message: err = %v, want ErrMessageNotFound", err)
	}
	if _, err := s.Advance(999, StateSent, ""); !errors.Is(err, ErrMessageNotFound) {
		t.Errorf("Advance: err = %v, want ErrMessageNotFound", err)
	}
}

// Two writers racing the same message must not both win. One of a (sent, failed)
// pair has to lose, or an operator is told the wrong thing about a message that is
// already on the air.
func TestConcurrentAdvanceHasOneWinner(t *testing.T) {
	s := msgStore(t)
	m, _ := s.Enqueue(outbound("contended"))
	m, _ = s.Advance(m.ID, StateTransmitting, "")

	var wg sync.WaitGroup
	results := make([]error, 2)
	targets := []MessageState{StateSent, StateFailed}
	start := make(chan struct{})
	for i := range targets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = s.Advance(m.ID, targets[i], "lost the race")
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrBadTransition) {
			t.Errorf("the loser failed with %v, want ErrBadTransition", err)
		}
	}
	if winners != 1 {
		t.Errorf("%d writers succeeded, want exactly 1", winners)
	}

	final, _ := s.Message(m.ID)
	if !Terminal(final.State) {
		t.Errorf("final state = %s, want a terminal one", final.State)
	}
}

func TestMessageQuery(t *testing.T) {
	s := msgStore(t)

	sent, _ := s.Enqueue(Message{Peer: 100, Local: 1, Text: "to 100"})
	sent, _ = s.Advance(sent.ID, StateTransmitting, "")
	_, _ = s.Advance(sent.ID, StateSent, "")
	queued, _ := s.Enqueue(Message{Peer: 200, Local: 1, Text: "to 200"})
	in, _ := s.RecordInbound(Message{Peer: 100, Local: 1, Text: "from 100"})

	for _, tc := range []struct {
		name string
		q    MessageQuery
		want []int64
	}{
		{"everything, newest first", MessageQuery{}, []int64{in.ID, queued.ID, sent.ID}},
		{"by direction", MessageQuery{Direction: Inbound}, []int64{in.ID}},
		{"by peer", MessageQuery{Peer: 200}, []int64{queued.ID}},
		{"by state", MessageQuery{State: StateSent}, []int64{sent.ID}},
		{"by peer and direction", MessageQuery{Peer: 100, Direction: Outbound}, []int64{sent.ID}},
		{"a peer nobody wrote to", MessageQuery{Peer: 999}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Messages(tc.q)
			if err != nil {
				t.Fatalf("Messages: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d messages, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i].ID != tc.want[i] {
					t.Errorf("position %d = id %d, want %d", i, got[i].ID, tc.want[i])
				}
			}
		})
	}

	t.Run("the limit is clamped, never unbounded", func(t *testing.T) {
		got, err := s.Messages(MessageQuery{Limit: MaxMessageLimit + 1000})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Errorf("got %d, want all 3", len(got))
		}
		got, _ = s.Messages(MessageQuery{Limit: 1})
		if len(got) != 1 {
			t.Errorf("Limit 1 returned %d", len(got))
		}
	})
}

// Messages written in the same millisecond must come back in a stable order, or a
// paging client sees one twice and misses another.
func TestQueryOrderIsStableWithinAMillisecond(t *testing.T) {
	s := msgStore(t)
	var ids []int64
	for i := 0; i < 20; i++ {
		m, err := s.Enqueue(Message{Peer: 1, Local: 2, Text: "burst"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	first, err := s.Messages(MessageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, _ := s.Messages(MessageQuery{})
		for j := range first {
			if again[j].ID != first[j].ID {
				t.Fatalf("read %d position %d = %d, first read had %d", i, j, again[j].ID, first[j].ID)
			}
		}
	}
	// Newest first, so the last one written leads.
	if first[0].ID != ids[len(ids)-1] {
		t.Errorf("first result is id %d, want the newest %d", first[0].ID, ids[len(ids)-1])
	}
}

// Messages age out on the event log's window, and pruning one does not touch the
// other's rows.
func TestPruneMessages(t *testing.T) {
	s := msgStore(t)
	for i := 0; i < 5; i++ {
		if _, err := s.Enqueue(Message{Peer: 1, Local: 2, Text: "old"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneMessages(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("PruneMessages: %v", err)
	}
	if n != 0 {
		t.Errorf("pruned %d fresh messages", n)
	}

	n, err = s.PruneMessages(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("pruned %d, want 5", n)
	}
	if c, _ := s.CountMessages(); c != 0 {
		t.Errorf("%d messages left", c)
	}
}

// The upgrade path: a database written by the build before messages existed must
// open, keep its events, and gain the table.
func TestSchemaUpgradeFromV1(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/events.db"

	// Stand up a v1 database by hand: the event table and a meta row claiming v1,
	// which is exactly what the previous build left behind.
	v1, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := v1.db.Exec(`DROP TABLE messages`); err != nil {
		t.Fatalf("undo the messages table: %v", err)
	}
	if _, err := v1.db.Exec(`UPDATE meta SET schema_version = 1 WHERE id = 1`); err != nil {
		t.Fatalf("set v1: %v", err)
	}
	if err := v1.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("reopen a v1 database: %v", err)
	}
	defer s.Close()

	if _, err := s.Enqueue(outbound("after the upgrade")); err != nil {
		t.Errorf("the messages table was not created on upgrade: %v", err)
	}
	var ver int
	if err := s.db.QueryRow(`SELECT schema_version FROM meta WHERE id = 1`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != SchemaVersion {
		t.Errorf("schema_version = %d after upgrade, want %d", ver, SchemaVersion)
	}
}

// And the rollback guard still bites, which it could not do before the version
// was ever written back.
func TestRefusesANewerSchema(t *testing.T) {
	path := t.TempDir() + "/events.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE meta SET schema_version = ? WHERE id = 1`, SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	if _, err := Open(path); err == nil {
		t.Fatal("opened a database from a newer build")
	} else if !strings.Contains(err.Error(), "refusing to run") {
		t.Errorf("err = %v", err)
	}
}
