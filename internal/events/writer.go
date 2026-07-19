package events

import (
	"context"
	"log"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
)

// Writer defaults. A flush every few seconds, or sooner once a batch fills, keeps
// the write cadence low (SD wear) without letting a burst sit unpersisted for long.
const (
	DefaultFlushInterval = 3 * time.Second
	DefaultBatchSize     = 64
)

// Run subscribes to the hub and persists every event in batches until ctx is
// canceled, at which point it flushes what it has buffered and returns. It is the
// persistence subscriber of RFC-0004.
//
// It never does a synchronous DB write on the hub's fan-out path: Hub.Publish
// holds the hub mutex while delivering to subscribers, so persistence runs here,
// on its own goroutine, decoupling disk latency from the bus. If a flush fails
// (e.g. a wedged disk) the buffered events are dropped rather than allowed to grow
// without bound — memory safety wins over durability for a status log — and the
// dropped count is logged, never silently swallowed.
func Run(ctx context.Context, s *Store, h *hub.Hub, flush time.Duration, batch int) {
	if flush <= 0 {
		flush = DefaultFlushInterval
	}
	if batch <= 0 {
		batch = DefaultBatchSize
	}

	ch, _, cancel := h.Subscribe()
	defer cancel()

	buf := make([]hub.Event, 0, batch)
	var dropped int64

	flushBuf := func() {
		if len(buf) == 0 {
			return
		}
		if err := s.Insert(buf); err != nil {
			dropped += int64(len(buf))
			log.Printf("events: persist failed, dropped %d event(s) (%d total dropped): %v", len(buf), dropped, err)
		}
		buf = buf[:0] // clear regardless: a failed batch is dropped, not retained, to bound memory
	}

	ticker := time.NewTicker(flush)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			flushBuf() // persist the tail on a clean shutdown
			return
		case e := <-ch:
			buf = append(buf, e)
			if len(buf) >= batch {
				flushBuf()
			}
		case <-ticker.C:
			flushBuf()
		}
	}
}

// RunPrune runs the nightly retention prune until ctx is canceled. Each tick it
// reads the current retention window via retentionDays (which the caller wires to
// the config store, so an operator's edit takes effect on the next prune without a
// restart) and deletes events older than that window. A window of 0 means keep
// forever — the prune is skipped that tick. The first prune runs one interval in,
// not at startup, so a boot storm doesn't race the store open.
func RunPrune(ctx context.Context, s *Store, interval time.Duration, retentionDays func() int) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			days := retentionDays()
			if days <= 0 {
				continue // keep forever
			}
			cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
			n, err := s.Prune(cutoff)
			if err != nil {
				log.Printf("events: prune failed: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("events: pruned %d event(s) older than %d day(s)", n, days)
			}
		}
	}
}
