package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/wxfeed"
	"github.com/KN4OQW/waypoint/internal/wxvoice"
)

// The weather broadcast's lifecycle and delivery.
//
// The feed subscriber decides WHETHER an alert should go on the air; this file
// is what happens when it says yes. Keeping the two apart is what lets the
// decision be tested exhaustively without a transmitter attached, which matters
// more here than usual because the consequence of a wrong decision is a
// transmission.
//
// # Reconciled, not started once
//
// The subscription depends on the county list and the broker, and both change
// under an Apply. So the service is reconciled on a tick like the DMR relay
// rather than started at boot: a county added in the panel takes effect without
// a restart, and a feed that was unreachable at boot is retried.

// wxReconcileInterval is how often the running subscription is compared with the
// configured one. The feed changes only on an Apply, so this is a safety net
// rather than a poll.
const wxReconcileInterval = 30 * time.Second

// weatherService owns the subscription and turns announcements into
// transmissions.
type weatherService struct {
	srv *server

	mu      sync.Mutex
	cancel  context.CancelFunc
	client  *wxfeed.Client
	running config.WX
	// active is what is currently in effect, so the panel and the public
	// bulletin can show it and a tombstone can clear it.
	active map[string]wxfeed.Alert
}

func newWeatherService(s *server) *weatherService {
	return &weatherService{srv: s, active: map[string]wxfeed.Alert{}}
}

// wxPolicy adapts the stored configuration to the feed's Policy interface. It
// exists so wxfeed does not import config, keeping the decision layer free of
// the store.
type wxPolicy struct{ w config.WX }

func (p wxPolicy) ShouldAnnounce(action string) bool { return p.w.ShouldAnnounce(action) }
func (p wxPolicy) Announces(event, sig string) bool {
	r := p.w.RuleFor(event, sig)
	return r.SMS || r.Voice
}

// run reconciles until ctx is cancelled.
func (ws *weatherService) run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = wxReconcileInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	defer ws.stop()

	step := func() {
		m, err := config.Load(ws.srv.store)
		if err != nil {
			return // a store we cannot read is the store layer's problem to report
		}
		ws.reconcile(ctx, m.WX)
	}
	step()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			step()
		}
	}
}

func (ws *weatherService) reconcile(ctx context.Context, want config.WX) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	on := want.Enabled && len(want.Counties) > 0 && strings.TrimSpace(want.Broker) != ""
	if !on {
		if ws.cancel != nil {
			log.Printf("weather: switched off")
			ws.stopLocked()
		}
		return
	}
	// Restart on any change that affects what we subscribe to or how. Comparing
	// the resolved subscription list rather than the whole section means an
	// unrelated edit (a talkgroup, the speaking rate) does not drop the feed.
	same := ws.client != nil &&
		ws.running.Broker == want.Broker &&
		ws.running.Username == want.Username &&
		ws.running.Password == want.Password &&
		strings.Join(ws.running.WXSubscriptions(), ",") == strings.Join(want.WXSubscriptions(), ",")
	ws.running = want
	if same {
		return
	}
	ws.stopLocked()

	cl := wxfeed.New(wxfeed.Options{
		Broker:   want.Broker,
		Username: want.Username,
		Password: want.Password,
		Topics:   want.WXSubscriptions(),
		// A random suffix, deliberately not derived from anything about this
		// node. MQTT wants client ids to be unique -- two connections sharing one
		// make the broker evict the first -- but the obvious ways to get
		// uniqueness are a callsign, a DMR id or a hostname, and every one of
		// those would put a device identifier on the wire of a public broker for
		// no benefit to the operator. Random satisfies the protocol and
		// identifies nothing; wxFeedClientID and its test hold that line.
		ClientID: wxFeedClientID(),
	}, wxPolicy{w: want}, ws)

	runCtx, cancel := context.WithCancel(ctx)
	ws.client, ws.cancel = cl, cancel
	go func() { _ = cl.Run(runCtx) }()
	log.Printf("weather: watching %d county topic(s) on %s", len(want.WXSubscriptions()), want.Broker)
}

func (ws *weatherService) stop() {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.stopLocked()
}

func (ws *weatherService) stopLocked() {
	if ws.cancel != nil {
		ws.cancel()
		ws.cancel = nil
	}
	ws.client = nil
}

// AnnounceAlert is the wxfeed.Announcer half: an alert that passed every gate.
//
// It runs on the MQTT client's goroutine, so the actual transmitting is handed
// to the message service's own queue rather than done inline. A feed that
// delivers a burst must not be able to block on the air.
func (ws *weatherService) AnnounceAlert(a wxfeed.Alert) {
	ws.mu.Lock()
	w := ws.running
	ws.active[a.DedupKey()] = a
	ws.mu.Unlock()

	ws.deliver(w, a, false)
}

// ClearAlert drops a hazard that has ended.
func (ws *weatherService) ClearAlert(key string) {
	ws.mu.Lock()
	_, had := ws.active[key]
	delete(ws.active, key)
	ws.mu.Unlock()
	if had {
		ws.publish(hub.Event{Time: time.Now().UTC(), Type: "wx_alert_cleared", Detail: key})
	}
}

// deliver puts one alert on the air, on every configured talkgroup.
func (ws *weatherService) deliver(w config.WX, a wxfeed.Alert, test bool) {
	rule := w.RuleFor(a.Event, a.Significance)
	if test {
		// A test transmits regardless of the routing matrix — the operator
		// pressed the button, and making them first configure a class they may
		// not want would be a poor way to answer "does this work at all".
		rule.SMS, rule.Voice = true, w.Voice.Enabled
	}

	limit := w.MaxTextUnits
	if limit <= 0 {
		limit = config.DefaultWXMaxTextUnits
	}
	text := wxfeed.SMSText(a, time.Now(), time.Local, limit)

	if rule.SMS && ws.srv.msgs != nil {
		for _, tg := range w.Talkgroups {
			if _, err := ws.srv.msgs.Send(tg, text, true); err != nil {
				log.Printf("weather: sending to TG %d failed: %v", tg, err)
				ws.publish(hub.Event{Time: time.Now().UTC(), Type: "wx_delivery_failed",
					Detail: fmt.Sprintf("TG %d: %v", tg, err)})
				continue
			}
			ws.publish(hub.Event{Time: time.Now().UTC(), Type: "wx_delivery_sent",
				Dest: fmt.Sprintf("TG %d", tg), Detail: a.Event})
		}
	}

	if rule.Voice {
		ws.speak(w, text)
	}
}

// speak synthesises and, if a vocoder exists, would transmit.
//
// The skip is logged as an event rather than swallowed, because "the node has no
// vocoder" and "the node ignored my alert" look identical from a dashboard and
// only one of them is a configuration an operator can fix.
func (ws *weatherService) speak(w config.WX, text string) {
	cfg := wxvoice.Config{
		PiperPath:       w.Voice.PiperPath,
		ModelPath:       w.Voice.ModelPath,
		Speaker:         w.Voice.Speaker,
		LengthScale:     w.Voice.LengthScale,
		Vocoder:         w.Voice.Vocoder,
		DongleDevice:    w.Voice.DongleDevice,
		ExternalCommand: w.Voice.ExternalCommand,
	}
	voc := wxvoice.VocoderFor(cfg)
	if voc.Name() == "none" {
		log.Printf("weather: spoken alert skipped, no voice encoder configured")
		ws.publish(hub.Event{Time: time.Now().UTC(), Type: "wx_voice_skipped",
			Detail: "no voice encoder configured"})
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		pcm, err := wxvoice.Synthesize(ctx, cfg, text)
		if err != nil {
			log.Printf("weather: speech synthesis failed: %v", err)
			ws.publish(hub.Event{Time: time.Now().UTC(), Type: "wx_voice_failed", Detail: err.Error()})
			return
		}
		// The attention tone goes in front of the speech and through the same
		// vocoder, so it arrives as ordinary audio rather than as a separate
		// transmission a radio would have to open squelch for twice.
		if w.Voice.ToneEnabled {
			tone := wxvoice.GenerateTone(wxvoice.ToneOptions{
				HzA:    float64(w.Voice.ToneHzA),
				HzB:    float64(w.Voice.ToneHzB),
				Millis: w.Voice.ToneMillis,
			})
			pcm = wxvoice.PrependTone(tone, pcm)
		}
		codewords, err := voc.Encode(ctx, pcm)
		if err != nil {
			log.Printf("weather: voice encoding failed: %v", err)
			ws.publish(hub.Event{Time: time.Now().UTC(), Type: "wx_voice_failed", Detail: err.Error()})
			return
		}
		relay := ws.srv.relay.shimOrNil()
		if relay == nil {
			// The relay is what puts anything on the air, and it is opt-in. A
			// node with voice configured but the relay off should be told that
			// rather than left wondering why it never speaks.
			log.Printf("weather: %d codewords encoded but the DMR message relay is off", len(codewords))
			ws.publish(hub.Event{Time: time.Now().UTC(), Type: "wx_voice_failed",
				Detail: "the DMR message relay is switched off, so nothing can be transmitted"})
			return
		}
		m, err := config.Load(ws.srv.store)
		if err != nil {
			return
		}
		src := wxSrcID(m)
		if src == 0 {
			ws.publish(hub.Event{Time: time.Now().UTC(), Type: "wx_voice_failed",
				Detail: "no DMR ID is configured, so a transmission has no source"})
			return
		}
		for _, tg := range w.VoiceTalkgroups() {
			n, err := wxvoice.Transmit(relay, codewords, wxvoice.TransmitOptions{
				SrcID: src, DstID: tg, Slot: uint8(messageSlot(m)),
				// A stream id distinguishes this transmission from the next in a
				// capture and to a receiver. Any changing value does; the clock
				// is the cheapest one that never repeats within a session.
				StreamID: uint32(time.Now().UnixNano()),
			})
			if err != nil {
				log.Printf("weather: voice to TG %d failed after %d frames: %v", tg, n, err)
				ws.publish(hub.Event{Time: time.Now().UTC(), Type: "wx_voice_failed",
					Detail: fmt.Sprintf("TG %d: cut off after %d frames: %v", tg, n, err)})
				continue
			}
			log.Printf("weather: spoke %d frames to TG %d", n, tg)
			ws.publish(hub.Event{Time: time.Now().UTC(), Type: "wx_voice_sent",
				Dest: fmt.Sprintf("TG %d", tg), Detail: fmt.Sprintf("%d frames", n)})
		}
	}()
}

func (ws *weatherService) publish(e hub.Event) {
	if ws.srv.hub != nil {
		ws.srv.hub.Publish(e)
	}
}

// wxFeedClientID returns a per-connection MQTT client id that identifies the
// software and nothing else.
//
// GOVERNANCE.md principle 2: a Waypoint device is not something that reports on
// itself. The feed is a public read-only broker on a shared account, so nothing
// needs to tell one NODE from another -- only one CONNECTION from another,
// which randomness does without naming anybody.
func wxFeedClientID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Randomness failing is no reason to refuse the feed, and the fallback
		// still identifies nothing about this node.
		return "waypointd-wx"
	}
	return "waypointd-wx-" + hex.EncodeToString(b)
}

// wxSrcID is the DMR ID a spoken alert is transmitted from: the DMR section's
// own id when set, otherwise the station id. Same resolution the message path
// uses, because a receiver seeing two different sources for the same node is
// a confusing thing to explain.
func wxSrcID(m *config.Model) uint32 {
	for _, s := range []string{m.DMR.ID, m.General.ID} {
		if n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32); err == nil && n > 0 && n <= 0xFFFFFF {
			return uint32(n)
		}
	}
	return 0
}

// wxStatus is what the panel and the API are told.
type wxStatus struct {
	Enabled bool           `json:"enabled"`
	Stats   wxfeed.Stats   `json:"stats"`
	Active  []wxfeed.Alert `json:"active"`
}

func (ws *weatherService) status() wxStatus {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	out := wxStatus{Enabled: ws.running.Enabled, Active: []wxfeed.Alert{}}
	if ws.client != nil {
		out.Stats = ws.client.Stats()
	}
	for _, a := range ws.active {
		out.Active = append(out.Active, a)
	}
	return out
}

// weatherRoutes registers the weather surface.
func (s *server) weatherRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/wx/status", s.wxStatusHandler)
	mux.HandleFunc("/api/wx/test", s.wxTestHandler)
}

func (s *server) wxStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	if s.weather == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "weather is not available on this node"})
		return
	}
	writeJSONStatus(w, http.StatusOK, s.weather.status())
}

// wxTestHandler transmits a clearly-marked test alert.
//
// It is a POST and it is authenticated like every other write, because it keys a
// transmitter. The text says TEST in it and cannot be supplied by the caller:
// an endpoint that transmits arbitrary operator-supplied text to a talkgroup is
// a different feature with a different set of questions, and the message path
// already has one.
func (s *server) wxTestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	if s.weather == nil || s.msgs == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "weather is not available on this node"})
		return
	}
	m, err := config.Load(s.store)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(m.WX.Talkgroups) == 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "no talkgroups are configured, so a test alert has nowhere to go"})
		return
	}

	a := wxfeed.Alert{
		Event:    "TEST - Weather Alert",
		Headline: "This is a test of the weather alert broadcast. No action needed.",
		Action:   "NEW", Status: "active", Significance: "W",
		Ends: time.Now().Add(15 * time.Minute),
	}
	// Deliberately the freshly loaded configuration rather than the running
	// one. The service reconciles on a tick, so the copy it holds can be up to
	// half a minute out of date -- and the moment an operator is most likely to
	// press Test is straight after changing something. Serving them the previous
	// settings makes the feature look broken when it is merely stale, which cost
	// two puzzled bench runs before this comment existed.
	//
	// It also means a test transmits on a switched-off feature, which is correct:
	// the operator asked, and it is the only way to check the path before going
	// live.
	s.weather.deliver(m.WX, a, true)

	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"sent":       true,
		"talkgroups": m.WX.Talkgroups,
	})
}
