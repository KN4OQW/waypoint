package supervisor

import (
	"context"
	"errors"
	"net"
	"strings"
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

// lastClaim is the most recent link verdict, ignoring the action events the
// supervisor also publishes.
func (h *harness) lastClaim(t *testing.T) hub.Event {
	t.Helper()
	var last hub.Event
	var found bool
	for _, e := range h.drain() {
		if e.Type == status.TypeLinkUp || e.Type == status.TypeLinkDown {
			last, found = e, true
		}
	}
	if !found {
		t.Fatal("no link event published")
	}
	return last
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

// twoMasters are two DMR networks on one DMRGateway — the ordinary case, and the
// one that makes remediation a unit-level decision rather than a per-link one.
func twoMasters() (Attachment, Attachment) {
	a := testAttachment()
	b := Attachment{Name: "TGIF", Kind: KindDMRMaster, Host: "tgif.example", Port: "62031", Unit: a.Unit}
	return a, b
}

// Two broken masters on one gateway restart it ONCE, not once each.
func TestRunnerRestartsASharedUnitOnce(t *testing.T) {
	a, b := twoMasters()
	h := newHarness(t, a, b)
	h.resolves = false // both endpoints gone

	for i := 0; i < 5; i++ {
		h.step(30 * time.Second)
	}
	if len(h.restarts) != 1 {
		t.Fatalf("expected one restart of the shared unit, got %d: %v", len(h.restarts), h.restarts)
	}
}

// A master that was fine gets restarted underneath it when the gateway is cycled
// for a different one. It must stand by rather than read that as its own failure,
// and it must not be charged for a restart it did not ask for.
func TestRunnerSettlesBystandersOnASharedUnit(t *testing.T) {
	a, b := twoMasters()
	h := newHarness(t, a, b)

	// BM is broken (no DNS), TGIF is logged in and happy.
	h.sup.Prober = &Prober{Resolve: func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == "master.example" {
			return nil, errors.New("no such host")
		}
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
	}}
	h.sup.ObserveEvent(hub.Event{Type: status.TypeGatewayStatus, Network: "TGIF", Detail: "Logged into DMR Network: TGIF"})

	for i := 0; i < 5; i++ {
		h.step(30 * time.Second)
	}
	if len(h.restarts) != 1 {
		t.Fatalf("expected one restart, got %v", h.restarts)
	}
	if got := h.sup.monitors["TGIF"].Attempt(); got != 0 {
		t.Errorf("a bystander was charged for somebody else's restart: attempt = %d", got)
	}
	if got := h.sup.monitors["BM_3102"].Attempt(); got != 1 {
		t.Errorf("the asking monitor should have been charged once, got %d", got)
	}
}

// The rate limit that matters: three monitors each backing off politely can still
// add up to a restart every few seconds if nothing counts at the unit. The unit
// keeps its own schedule, so the gateway is cycled on a widening interval no
// matter how many unhappy masters ride it.
func TestRunnerRateLimitsTheUnitNotJustTheLink(t *testing.T) {
	a, b := twoMasters()
	c := Attachment{Name: "DMRplus", Kind: KindDMRMaster, Host: "dmrplus.example", Port: "62031", Unit: a.Unit}
	h := newHarness(t, a, b, c)
	h.resolves = false
	h.sup.MaxRestarts = 1000 // isolate the unit gate from the global backstop

	var at []time.Time
	h.sup.Signals.Restart = func(string) error {
		at = append(at, h.now)
		return nil
	}
	for i := 0; i < 240; i++ { // two hours at 30s
		h.step(30 * time.Second)
	}
	if len(at) < 3 {
		t.Fatalf("expected several restarts over two hours, got %d", len(at))
	}
	var gaps []time.Duration
	for i := 1; i < len(at); i++ {
		gaps = append(gaps, at[i].Sub(at[i-1]))
	}
	for i := 1; i < len(gaps); i++ {
		if gaps[i] < gaps[i-1] {
			t.Errorf("unit restart gap %d (%v) shorter than the previous (%v) — three links defeated the backoff", i, gaps[i], gaps[i-1])
		}
	}
	// The ceiling is the policy's, not three times faster because three links asked.
	if last := gaps[len(gaps)-1]; last < testPolicy().Settle {
		t.Errorf("restarts converged to %v, faster than the settle window", last)
	}
}

// The global cap is a backstop: if the supervisor's own reasoning ever goes wrong,
// it stops restarting daemons and says so rather than running as a tidy loop.
func TestRunnerGlobalCap(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.resolves = false
	h.sup.MaxRestarts = 2
	h.sup.RestartWindow = time.Hour
	var logged int
	h.sup.Logf = func(format string, _ ...any) {
		if strings.Contains(format, "which is the cap") {
			logged++
		}
	}

	for i := 0; i < 100; i++ { // 50 minutes — inside the one-hour window
		h.step(30 * time.Second)
	}
	if len(h.restarts) != 2 {
		t.Fatalf("the cap should have held restarts to 2, got %d: %v", len(h.restarts), h.restarts)
	}
	if logged == 0 {
		t.Error("hitting the cap should say so — a silent stop is indistinguishable from a bug")
	}

	// Once the window rolls past, remediation resumes.
	h.now = h.now.Add(2 * time.Hour)
	for i := 0; i < 20; i++ {
		h.step(30 * time.Second)
	}
	if len(h.restarts) <= 2 {
		t.Error("the cap never released after its window elapsed")
	}
}

// Two units both needing attention are each restarted, in a stable order.
func TestRunnerRestartsEachUnit(t *testing.T) {
	dmr := testAttachment()
	dapnet := Attachment{Name: "dapnet", Kind: KindDAPNET, Host: "dapnet.example", Port: "43434", Unit: "waypoint-dapnetgateway.service"}
	h := newHarness(t, dmr, dapnet)
	h.resolves = false

	for i := 0; i < 5; i++ {
		h.step(30 * time.Second)
	}
	if len(h.restarts) != 2 {
		t.Fatalf("expected both units restarted, got %v", h.restarts)
	}
	if h.restarts[0] != "waypoint-dapnetgateway.service" || h.restarts[1] != "waypoint-dmrgateway.service" {
		t.Errorf("units should be restarted in a stable order, got %v", h.restarts)
	}
}

// The acceptance is "full unattended recovery, visible in the event log" — so a
// restart has to be an event, not just a line in the daemon's own log. Whoever was
// asleep when the ISP dropped reads this back afterwards.
func TestRunnerAnnouncesItsActions(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.resolves = false

	for i := 0; i < 5; i++ {
		h.step(30 * time.Second)
	}
	var actions []hub.Event
	for _, e := range h.drain() {
		if e.Type == status.TypeSupervisorAction {
			actions = append(actions, e)
		}
	}
	if len(actions) != 1 {
		t.Fatalf("expected one supervisor_action event, got %d", len(actions))
	}
	if !strings.Contains(actions[0].Detail, "restarted waypoint-dmrgateway.service") {
		t.Errorf("the action should say what it did: %q", actions[0].Detail)
	}
	if !strings.Contains(actions[0].Detail, "BM_3102") {
		t.Errorf("the action should say which link caused it: %q", actions[0].Detail)
	}
	if actions[0].Source != "waypoint-dmrgateway.service" {
		t.Errorf("source = %q, want the unit", actions[0].Source)
	}
}

// Declining is part of the story too: a supervisor that hits its cap and goes
// silent is indistinguishable from one that fixed the problem.
func TestRunnerAnnouncesWhenItDeclines(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.resolves = false
	h.sup.MaxRestarts = 1
	h.sup.RestartWindow = time.Hour

	for i := 0; i < 100; i++ {
		h.step(30 * time.Second)
	}
	var declined bool
	for _, e := range h.drain() {
		if e.Type == status.TypeSupervisorAction && strings.Contains(e.Detail, "declined") {
			declined = true
		}
	}
	if !declined {
		t.Error("hitting the cap was never announced to the event log")
	}
}

// Observe-only says what it would have done, so a bench run can be read without
// anything having been touched.
func TestRunnerAnnouncesObserveOnly(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.sup.Remediate = false
	h.resolves = false

	for i := 0; i < 5; i++ {
		h.step(30 * time.Second)
	}
	var found bool
	for _, e := range h.drain() {
		if e.Type == status.TypeSupervisorAction && strings.Contains(e.Detail, "would restart") {
			found = true
		}
	}
	if !found {
		t.Error("observe-only never announced what it would have done")
	}
}

// A daemon stuck in a reconnect loop alternates "Failed connection" and "Opening"
// forever. Each "Opening" is an attempt, not news of health — if it were allowed
// to clear the failure, the grace period would reset every few seconds and the
// supervisor would watch a broken link indefinitely without ever acting. Found by
// the tier-2 harness against the real DMRGateway.
func TestReconnectLoopStillAccumulatesGrace(t *testing.T) {
	h := newHarness(t, testAttachment())
	h.sup.Prober = &Prober{Resolve: func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil // resolves fine; the link is what is broken
	}}

	for i := 0; i < 12; i++ {
		// The daemon's real cadence: a failure, then a retry, over and over.
		h.sup.ObserveEvent(hub.Event{Type: status.TypeGatewayStatus, Network: "BM_3102", Detail: "Failed connection into DMR Network: BM_3102"})
		h.step(15 * time.Second)
		h.sup.ObserveEvent(hub.Event{Type: status.TypeGatewayStatus, Network: "BM_3102", Detail: "Opening DMR Network: BM_3102"})
		h.step(15 * time.Second)
	}
	if len(h.restarts) == 0 {
		t.Fatal("a daemon looping on failed reconnects was never remediated — the retry attempts kept resetting the grace period")
	}

	// A real success still clears it.
	h.sup.ObserveEvent(hub.Event{Type: status.TypeGatewayStatus, Network: "BM_3102", Detail: "Logged into DMR Network: BM_3102"})
	h.step(30 * time.Second)
	if e := h.lastClaim(t); e.Type != status.TypeLinkUp {
		t.Errorf("a logged-in link should be claimed up, got %s", e.Type)
	}
	before := len(h.restarts)
	for i := 0; i < 10; i++ {
		h.step(30 * time.Second)
	}
	if len(h.restarts) != before {
		t.Errorf("kept restarting after a successful login: %d → %d", before, len(h.restarts))
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

// hubGatewayStatus builds the hub event the MQTT layer produces for one daemon
// status message.
func hubGatewayStatus(network, detail string) hub.Event {
	return hub.Event{Type: status.TypeGatewayStatus, Network: network, Detail: detail}
}
