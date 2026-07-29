package supervisor

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/status"
)

// harness is a supervisor wired entirely to fakes: no clock, no sockets, no
// systemd. Every knob the runner reads is a field here.
type harness struct {
	sup      *Supervisor
	h        *hub.Hub
	events   <-chan hub.Event
	now      time.Time
	wan      bool
	tx       bool
	unit     Tri
	resolves bool
	restarts []string
}

func newHarness(t *testing.T, attachments ...Attachment) *harness {
	t.Helper()
	hb := hub.New()
	ch, _, cancel := hb.Subscribe()
	t.Cleanup(cancel)

	hs := &harness{h: hb, events: ch, now: t0, wan: true, unit: TriYes, resolves: true}
	hs.sup = &Supervisor{
		Hub:       hb,
		Policy:    testPolicy(),
		Remediate: true,
		Now:       func() time.Time { return hs.now },
		Logf:      func(string, ...any) {},
		Prober: &Prober{Resolve: func(context.Context, string) ([]net.IPAddr, error) {
			if !hs.resolves {
				return nil, errors.New("no such host")
			}
			return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
		}},
		Signals: Signals{
			Attachments: func() []Attachment { return attachments },
			WANUp:       func() bool { return hs.wan },
			TXActive:    func() bool { return hs.tx },
			UnitActive:  func(string) Tri { return hs.unit },
			Restart: func(unit string) error {
				hs.restarts = append(hs.restarts, unit)
				return nil
			},
		},
	}
	return hs
}

// step advances the fake clock and runs one evaluation.
func (h *harness) step(d time.Duration) {
	h.now = h.now.Add(d)
	h.sup.Step(context.Background())
}

// drain collects the link events published so far.
func (h *harness) drain() []hub.Event {
	var out []hub.Event
	for {
		select {
		case e := <-h.events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func (h *harness) lastClaim(t *testing.T) hub.Event {
	t.Helper()
	evs := h.drain()
	if len(evs) == 0 {
		t.Fatal("no link event published")
	}
	return evs[len(evs)-1]
}

// A healthy attachment is announced up once and then stops being news. The
// supervisor evaluates on every tick; re-announcing an unchanged verdict would
// bury the event log and churn the retained topics.
func TestRunnerAnnouncesOnChangeOnly(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.sup.Signals.UnitActive = func(string) Tri { return TriYes }

	h.step(0)
	first := h.drain()
	if len(first) != 1 || first[0].Type != status.TypeLinkUp {
		t.Fatalf("expected one link_up, got %+v", first)
	}
	for i := 0; i < 5; i++ {
		h.step(time.Minute)
	}
	if extra := h.drain(); len(extra) != 0 {
		t.Errorf("an unchanged verdict was re-announced %d times: %+v", len(extra), extra)
	}
}

// The endpoint going away flips the claim to down with the reason attached — the
// honest status the requirement asks for, independent of any remediation.
func TestRunnerReportsAnUnresolvableEndpoint(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.step(0)
	h.drain()

	h.resolves = false
	h.step(time.Minute)
	e := h.lastClaim(t)
	if e.Type != status.TypeLinkDown {
		t.Fatalf("expected link_down, got %s", e.Type)
	}
	if e.Network != "BM_3102" || e.Detail == "" {
		t.Errorf("event should name the network and say why: %+v", e)
	}
}

// With no route out every attachment reports down and nothing is restarted, for
// as long as the outage lasts. This is the ten-minute WAN pull in miniature.
func TestRunnerHonestAndPassiveDuringAnOutage(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.step(0)
	h.drain()

	h.wan, h.resolves = false, false
	for i := 0; i < 20; i++ { // ten minutes at 30s
		h.step(30 * time.Second)
	}
	e := h.lastClaim(t)
	if e.Type != status.TypeLinkDown {
		t.Errorf("an offline node should report its links down, got %s", e.Type)
	}
	if len(h.restarts) != 0 {
		t.Errorf("restarted %v during a WAN outage", h.restarts)
	}
}

// The full recovery: WAN returns, the daemon stays stuck on its resolve-once
// address, and after the grace period the supervisor restarts it exactly once
// before the cooldown takes over.
func TestRunnerRecoversAfterTheOutage(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.step(0)

	h.wan, h.resolves = false, false
	for i := 0; i < 20; i++ {
		h.step(30 * time.Second)
	}

	// Route back; DNS works again, but the daemon resolved once at startup and is
	// still talking to nothing, so it never reports a login.
	h.wan, h.resolves = true, true
	h.sup.Signals.UnitActive = func(string) Tri { return TriYes }
	h.sup.ObserveEvent(hub.Event{Type: status.TypeGatewayStatus, Network: "BM_3102", Detail: "Failed login into DMR Network: BM_3102"})

	for i := 0; i < 4; i++ { // two minutes, past the 90s grace
		h.step(30 * time.Second)
	}
	if len(h.restarts) != 1 {
		t.Fatalf("expected exactly one restart after the outage, got %v", h.restarts)
	}
	if h.restarts[0] != "waypoint-dmrgateway.service" {
		t.Errorf("restarted the wrong unit: %q", h.restarts[0])
	}

	// It logs back in: the claim goes up and stays there.
	h.sup.ObserveEvent(hub.Event{Type: status.TypeGatewayStatus, Network: "BM_3102", Detail: "Logged into DMR Network: BM_3102"})
	h.drain()
	h.step(30 * time.Second)
	if e := h.lastClaim(t); e.Type != status.TypeLinkUp {
		t.Errorf("a recovered link should be announced up, got %s", e.Type)
	}
	if len(h.restarts) != 1 {
		t.Errorf("restarted again after recovery: %v", h.restarts)
	}
}

// With remediation off the whole detection path runs and nothing is acted on —
// the state this ships in until the bench run says otherwise.
func TestRunnerObserveOnly(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.sup.Remediate = false
	h.resolves = false

	for i := 0; i < 10; i++ {
		h.step(30 * time.Second)
	}
	if len(h.restarts) != 0 {
		t.Errorf("remediated with Remediate off: %v", h.restarts)
	}
	if e := h.lastClaim(t); e.Type != status.TypeLinkDown {
		t.Errorf("observe-only must still report the truth, got %s", e.Type)
	}
}

// The daemon's own report is folded in per network, so two masters on one gateway
// are judged separately even though they share a unit.
func TestRunnerTracksEachNetworkSeparately(t *testing.T) {
	a := testAttachment()
	b := Attachment{Name: "TGIF", Kind: KindDMRMaster, Host: "tgif.example", Port: "62031", Unit: a.Unit}
	h := newHarness(t, a, b)

	h.sup.ObserveEvent(hub.Event{Type: status.TypeGatewayStatus, Network: "BM_3102", Detail: "Logged into DMR Network: BM_3102"})
	h.sup.ObserveEvent(hub.Event{Type: status.TypeGatewayStatus, Network: "TGIF", Detail: "Failed login into DMR Network: TGIF"})
	h.step(0)

	claims := map[string]string{}
	for _, e := range h.drain() {
		claims[e.Network] = e.Type
	}
	if claims["BM_3102"] != status.TypeLinkUp {
		t.Errorf("BM_3102 = %q, want link_up", claims["BM_3102"])
	}
	if claims["TGIF"] != status.TypeLinkDown {
		t.Errorf("TGIF = %q, want link_down", claims["TGIF"])
	}
}

// A network the operator removes stops being reported on rather than lingering as
// a permanent row nothing will ever update.
func TestRunnerForgetsRemovedAttachments(t *testing.T) {
	attachments := []Attachment{testAttachment()}
	h := newHarness(t)
	h.sup.Signals.Attachments = func() []Attachment { return attachments }

	h.step(0)
	h.drain()
	attachments = nil
	h.step(time.Minute)
	if len(h.sup.monitors) != 0 {
		t.Errorf("kept %d monitors for attachments that no longer exist", len(h.sup.monitors))
	}

	// Re-adding it starts clean rather than resuming an old verdict.
	attachments = []Attachment{testAttachment()}
	h.step(time.Minute)
	if len(h.drain()) == 0 {
		t.Error("a re-added attachment should announce its state afresh")
	}
}

// A transmission in progress holds the restart until the release.
func TestRunnerDefersToTraffic(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.resolves = false
	h.tx = true
	for i := 0; i < 10; i++ {
		h.step(30 * time.Second)
	}
	if len(h.restarts) != 0 {
		t.Fatalf("restarted mid-transmission: %v", h.restarts)
	}
	h.tx = false
	h.step(30 * time.Second)
	if len(h.restarts) != 1 {
		t.Errorf("the deferred restart did not fire after the release: %v", h.restarts)
	}
}

// Events that are not gateway status must not disturb the supervisor's view.
func TestObserveEventIgnoresUnrelated(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.sup.ObserveEvent(hub.Event{Type: status.TypeRFStart, Network: "BM_3102", Detail: "Logged into DMR Network: BM_3102"})
	h.sup.ObserveEvent(hub.Event{Type: status.TypeGatewayStatus, Network: "", Detail: "DMRGateway is starting"})
	h.sup.ObserveEvent(hub.Event{Type: status.TypeGatewayStatus, Network: "BM_3102", Detail: "something unrecognised"})
	if len(h.sup.login) != 0 {
		t.Errorf("unrelated events changed the login view: %+v", h.sup.login)
	}
}
