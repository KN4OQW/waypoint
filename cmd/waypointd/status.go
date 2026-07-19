package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/status"
)

// statusView serves GET /api/status: the authoritative live status the aggregator
// derives from the structured event stream (RFC-0008). The dashboard and any other
// client consume this one computed truth instead of each re-deriving it.
func (s *server) statusView(w http.ResponseWriter, _ *http.Request) {
	if s.agg == nil {
		http.Error(w, "status unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.agg.Snapshot())
}

// wsUpgrader accepts same-origin WebSocket upgrades. The endpoint is already behind
// the session wall (the auth gate runs before the mux), so this only guards against
// cross-site socket hijacking by checking the Origin host matches the request host.
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // non-browser client (no Origin) — already authenticated
		}
		return sameHost(origin, r.Host)
	},
}

func sameHost(origin, host string) bool {
	i := strings.Index(origin, "://")
	if i < 0 {
		return false
	}
	return origin[i+3:] == host
}

// wsStream serves GET /api/ws: on connect it sends the current status, then streams
// every subsequent hub event and status snapshot as JSON frames over one socket
// (RFC-0008). Frames are {"kind":"status"|"event","data":…}. A single write
// goroutine owns the socket (gorilla requires it); a reader goroutine detects close.
func (s *server) wsStream(w http.ResponseWriter, r *http.Request) {
	c, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error response
	}
	defer c.Close()

	ch, _, cancel := s.hub.Subscribe()
	defer cancel()

	statusCh := make(chan status.Status, 8)
	var offChange func()
	if s.agg != nil {
		offChange = s.agg.OnChange(func(st status.Status) {
			select {
			case statusCh <- st:
			default: // a slow client never back-pressures the aggregator
			}
		})
		defer offChange()
	}

	// Reader goroutine: gorilla needs reads pumped to see control frames and close.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Initial snapshot so a fresh client paints immediately.
	if s.agg != nil {
		if err := writeWSFrame(c, "status", s.agg.Snapshot()); err != nil {
			return
		}
	}

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-done:
			return
		case e := <-ch:
			if err := writeWSFrame(c, "event", e); err != nil {
				return
			}
		case st := <-statusCh:
			if err := writeWSFrame(c, "status", st); err != nil {
				return
			}
		case <-keepalive.C:
			_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func writeWSFrame(c *websocket.Conn, kind string, data any) error {
	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.WriteJSON(map[string]any{"kind": kind, "data": data})
}

// runLivenessProbe polls the gateway units the current config expects to be running
// and emits gateway_up/gateway_down when one transitions (RFC-0008). This is the
// log-free, structured source for "kill/restart any gateway → truth within 2 s":
// the supervisor already owns the units, so their systemd state is authoritative —
// no dying-daemon message to miss. Runs in live mode only (a demo node runs no
// gateways).
func (s *server) runLivenessProbe(ctx context.Context, interval time.Duration) {
	last := map[string]bool{}
	check := func() {
		m, err := config.Load(s.store)
		if err != nil {
			return
		}
		for _, unit := range restartSet(m.RenderTargets(s.paths)) {
			if unit == "" {
				continue
			}
			name := friendlyUnit(unit)
			_, aerr := systemctlRun("is-active", "--quiet", unit)
			up := aerr == nil
			if prev, seen := last[name]; !seen || prev != up {
				last[name] = up
				t, detail := status.TypeGWUp, "running"
				if !up {
					t, detail = status.TypeGWDown, "not running"
				}
				s.hub.Publish(hub.Event{Time: time.Now().UTC(), Type: t, Network: name, Detail: detail})
			}
		}
	}
	check()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}

// friendlyUnit turns "waypoint-dmrgateway.service" into "dmrgateway" for the
// status model's gateway key and the waypoint/status/gateway/<name> topic.
func friendlyUnit(unit string) string {
	u := strings.TrimSuffix(unit, ".service")
	u = strings.TrimPrefix(u, "waypoint-")
	return u
}
