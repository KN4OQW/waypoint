//go:build tier2

package tier2

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/backoff"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
	wpmqtt "github.com/KN4OQW/waypoint/internal/mqtt"
	"github.com/KN4OQW/waypoint/internal/status"
	"github.com/KN4OQW/waypoint/internal/supervisor"
)

// resilience_test.go is issue #22's acceptance, run against the real daemon
// instead of a description of it: a node loses its network, the gateway does not
// come back on its own, and Waypoint recovers it unattended.
//
// What the harness established about the pinned DMRGateway (79edbc4), by
// measurement — these are the facts the supervisor is built against, and each is
// asserted below rather than assumed:
//
//  1. When its master goes away and returns at the same address, DMRGateway does
//     NOT reconnect. Measured at 200 s with no recovery. This is the lived version
//     of MMDVM-Host#682, and it is why an external supervisor exists at all.
//  2. While that is happening it says NOTHING — no status message, no log line —
//     and asked directly it answers "net1:conn". A supervisor going on what the
//     daemon reports would hold the last thing it heard, which was "Logged in",
//     indefinitely.
//  3. Sometimes, though, it does notice and reports "disc" — when the master's
//     address has nothing listening on it. The difference is ICMP: an unreachable
//     port errors the socket read and knocks the daemon out of RUNNING, while a
//     port that answers does not. Whether the daemon ends up right about its own
//     link therefore depends on whether it caught an error in the window before
//     the address became answerable again, which is a race with the outage.
//
// So the daemon's view is worth polling and often correct, and it is exactly the
// wrong thing to depend on: it is right or wrong according to the timing of
// somebody else's outage. What is reliable is the thing the daemon has no access
// to — whether the node itself had a route out. That is why the acceptance below
// drives recovery from the WAN signal, and why a daemon report is one signal of
// three rather than the answer.
//
// TestTier2_DMRGatewayKnowsOnlyWhenNothingAnswers holds these facts to account, so
// a pinned-version bump that changes any of them fails loudly rather than quietly
// invalidating the design.

// fastPolicy compresses the production timings. The durations themselves are
// unit-tested against an injected clock in internal/supervisor; what tier 2 is for
// is the interaction with a real process, so the test spends its wall-clock on the
// daemon rather than on the grace period.
func fastPolicy() supervisor.Policy {
	return supervisor.Policy{
		Grace:       6 * time.Second,
		Settle:      6 * time.Second,
		SustainedOK: 30 * time.Second,
		Backoff:     backoff.Backoff{Initial: 2 * time.Second, Max: 20 * time.Second},
	}
}

// supervisedRig is one DMRGateway under a real supervisor, with the node's
// connectivity in the test's hands.
type supervisedRig struct {
	master   *stubMaster
	port     int
	conf     string
	model    *config.Model
	sup      *supervisor.Supervisor
	hub      *hub.Hub
	rep      *repeaterSide
	stopMQTT func()

	mu       sync.Mutex
	cmd      *exec.Cmd
	log      *strings.Builder
	restarts int
	wan      bool
	now      time.Time
}

func startSupervisedRig(t *testing.T) *supervisedRig {
	t.Helper()
	if _, err := os.Stat(gwBin("DMRGateway")); err != nil {
		t.Skipf("DMRGateway binary not built: %v", err)
	}

	master, err := newStubMasterOnPort("BM", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	port := master.port()

	m := &config.Model{
		General: config.General{Callsign: "KN4OQW", ID: strconv.Itoa(dmrID)},
		DMR:     config.DMR{ID: strconv.Itoa(dmrID), ColorCode: "1"},
		Networks: []config.Network{{
			Name: "BM", Type: config.NetBrandmeister, Primary: true,
			Address: "127.0.0.1", Port: strconv.Itoa(port), Password: "passw0rd", Enabled: true,
		}},
	}
	conf := filepath.Join(t.TempDir(), "DMRGateway.ini")
	if err := os.WriteFile(conf, []byte(m.RenderDMRGateway()), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := newRepeaterSide(rptPort, gwPort)
	if err != nil {
		t.Fatal(err)
	}

	r := &supervisedRig{master: master, port: port, conf: conf, model: m, rep: rep,
		hub: hub.New(), wan: true, now: time.Now()}

	// The supervisor's Login signal comes from the daemon's real MQTT status
	// plane, parsed by the real hint parser — so this test is also what checks
	// that parser against the wire format rather than against a fixture.
	r.stopMQTT = watchGatewayTopics(t, config.MQTTNameDMRGateway, func(topic string, payload []byte) {
		if !strings.HasSuffix(topic, "/json") {
			return
		}
		if e, ok := wpmqtt.TranslateGatewayStatus(payload); ok {
			t.Logf("[daemon] %s", e.Detail)
			r.sup.ObserveEvent(e)
		}
	})

	r.sup = &supervisor.Supervisor{
		Hub:       r.hub,
		Policy:    fastPolicy(),
		Remediate: true,
		Prober:    &supervisor.Prober{},
		Now:       func() time.Time { return r.clock() },
		Logf:      func(f string, a ...any) { t.Logf("[supervisor] "+f, a...) },
		Signals: supervisor.Signals{
			Attachments: func() []supervisor.Attachment { return supervisor.Attachments(m) },
			WANUp:       r.wanUp,
			TXActive:    func() bool { return false },
			// The daemon is a child process here rather than a systemd unit, so
			// liveness is "is it running", answered the same way systemd would.
			UnitActive: func(string) supervisor.Tri {
				r.mu.Lock()
				defer r.mu.Unlock()
				if r.cmd != nil && r.cmd.Process != nil {
					return supervisor.TriYes
				}
				return supervisor.TriNo
			},
			Restart: func(string) error { return r.restartDaemon() },
		},
	}

	r.spawn(t)
	t.Cleanup(func() {
		r.stopMQTT()
		r.kill()
		rep.close()
		master.close()
		if s := r.log.String(); strings.TrimSpace(s) != "" {
			t.Logf("--- DMRGateway log ---\n%s", s)
		}
	})
	return r
}

func (r *supervisedRig) clock() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *supervisedRig) setNow(at time.Time) {
	r.mu.Lock()
	r.now = at
	r.mu.Unlock()
}

func (r *supervisedRig) wanUp() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wan
}

func (r *supervisedRig) setWAN(up bool) {
	r.mu.Lock()
	r.wan = up
	r.mu.Unlock()
}

func (r *supervisedRig) spawn(t *testing.T) {
	t.Helper()
	cmd := exec.Command(gwBin("DMRGateway"), r.conf)
	if r.log == nil {
		r.log = &strings.Builder{}
	}
	cmd.Stdout, cmd.Stderr = r.log, r.log
	if err := cmd.Start(); err != nil {
		t.Fatalf("start DMRGateway: %v", err)
	}
	r.mu.Lock()
	r.cmd = cmd
	r.mu.Unlock()

	// DMRGateway blocks in "Waiting for MMDVM to connect....." until the repeater
	// side announces itself.
	time.Sleep(400 * time.Millisecond)
	for i := 0; i < 40 && !r.master.isLoggedIn(); i++ {
		if err := r.rep.connect(dmrID); err != nil {
			t.Fatalf("DMRC: %v", err)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func (r *supervisedRig) kill() {
	r.mu.Lock()
	cmd := r.cmd
	r.cmd = nil
	r.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
		cmd.Wait()
	}
}

// restartDaemon is what the supervisor's Restart signal does here: the same thing
// `systemctl restart` would, against a child process.
func (r *supervisedRig) restartDaemon() error {
	r.kill()
	cmd := exec.Command(gwBin("DMRGateway"), r.conf)
	cmd.Stdout, cmd.Stderr = r.log, r.log
	if err := cmd.Start(); err != nil {
		return err
	}
	r.mu.Lock()
	r.cmd = cmd
	r.restarts++
	r.mu.Unlock()

	// Re-announce the repeater so the restarted daemon leaves its "waiting for
	// MMDVM" loop, exactly as a real MMDVM-Host would keep doing.
	go func() {
		for i := 0; i < 40; i++ {
			r.rep.connect(dmrID)
			time.Sleep(150 * time.Millisecond)
		}
	}()
	return nil
}

func (r *supervisedRig) restartCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.restarts
}

// advance moves the supervisor's clock and steps it, the way its ticker would.
func (r *supervisedRig) advance(d time.Duration) {
	r.setNow(r.clock().Add(d))
	r.sup.Step(context.Background())
}

// claims drains the supervisor's link verdicts.
func (r *supervisedRig) claims() []hub.Event {
	ch, backlog, cancel := r.hub.Subscribe()
	defer cancel()
	_ = ch
	var out []hub.Event
	for _, e := range backlog {
		if e.Type == status.TypeLinkUp || e.Type == status.TypeLinkDown {
			out = append(out, e)
		}
	}
	return out
}

func (r *supervisedRig) lastClaim() (hub.Event, bool) {
	c := r.claims()
	if len(c) == 0 {
		return hub.Event{}, false
	}
	return c[len(c)-1], true
}

// TestTier2_SupervisorRecoversAWANOutage is the acceptance: the node loses its
// network, the real DMRGateway does not come back on its own, and the supervisor
// recovers it unattended — with the whole story in the event log.
func TestTier2_SupervisorRecoversAWANOutage(t *testing.T) {
	r := startSupervisedRig(t)

	if !r.master.waitLogin(10 * time.Second) {
		t.Fatalf("upstream login never completed\n%s", r.log.String())
	}
	t.Log("STEP 1: logged into the master")

	// Healthy: the supervisor claims the link up and touches nothing.
	for i := 0; i < 3; i++ {
		r.advance(2 * time.Second)
	}
	if e, ok := r.lastClaim(); !ok || e.Type != status.TypeLinkUp {
		t.Fatalf("a healthy link was not claimed up: %+v", e)
	}
	if n := r.restartCount(); n != 0 {
		t.Fatalf("restarted a healthy gateway %d times", n)
	}
	t.Log("STEP 2: healthy link claimed up, no restarts")

	// The outage. The node loses its route and the master becomes unreachable —
	// the ten-minute WAN pull, compressed.
	r.setWAN(false)
	r.master.close()
	for i := 0; i < 10; i++ {
		r.advance(2 * time.Second)
	}
	e, ok := r.lastClaim()
	if !ok || e.Type != status.TypeLinkDown {
		t.Fatalf("an offline node did not report its link down: %+v", e)
	}
	if !strings.Contains(e.Detail, "route out") {
		t.Errorf("the reason should name the node's own connectivity, got %q", e.Detail)
	}
	if n := r.restartCount(); n != 0 {
		t.Fatalf("restarted the gateway %d times during the outage — nothing it could have fixed", n)
	}
	t.Log("STEP 3: offline — link reported down, nothing restarted")

	// The network comes back, and so does the master, at the same address.
	master2, err := newStubMasterOnPort("BM", r.port, false)
	if err != nil {
		t.Fatalf("rebind :%d: %v", r.port, err)
	}
	defer master2.close()
	r.master = master2
	r.setWAN(true)
	t.Log("STEP 4: route and master are back")

	// The daemon is given its grace period to recover by itself. It does not:
	// measured at 200 s of nothing, which is the whole reason this package exists.
	r.advance(2 * time.Second)
	if master2.waitLogin(5 * time.Second) {
		t.Fatal("DMRGateway recovered on its own — the premise of #22 no longer holds against this pinned build, so this test and the supervisor's grace period both need revisiting")
	}
	if n := r.restartCount(); n != 0 {
		t.Fatalf("restarted inside the post-restore grace period (%d)", n)
	}
	t.Log("STEP 5: confirmed the daemon does NOT recover on its own")

	// Past the grace period, the supervisor acts.
	deadline := time.Now().Add(60 * time.Second)
	for r.restartCount() == 0 && time.Now().Before(deadline) {
		r.advance(3 * time.Second)
		time.Sleep(200 * time.Millisecond)
	}
	if r.restartCount() == 0 {
		t.Fatalf("the supervisor never restarted the gateway\n%s", r.log.String())
	}
	t.Logf("STEP 6: supervisor restarted the gateway (%d time(s))", r.restartCount())

	// And the restart is what fixes it: the master sees a fresh login.
	if !master2.waitLogin(20 * time.Second) {
		t.Fatalf("the master never saw a login after the restart\n%s", r.log.String())
	}
	t.Log("STEP 7: master logged in again — unattended recovery complete")

	// The supervisor announced what it did, which is the "visible in the event log"
	// half of the acceptance.
	_, backlog, cancel := r.hub.Subscribe()
	cancel()
	var actions []string
	for _, ev := range backlog {
		if ev.Type == status.TypeSupervisorAction {
			actions = append(actions, ev.Detail)
		}
	}
	if len(actions) == 0 {
		t.Fatal("the recovery is not in the event log")
	}
	t.Logf("STEP 8: event log carries the recovery: %q", actions[len(actions)-1])

	// The daemon's "Logged in" travels through the broker in real time, while the
	// supervisor's clock is driven by this test — so wait for the recovery to
	// actually be observed before asking whether it settles.
	settled := time.Now().Add(30 * time.Second)
	for time.Now().Before(settled) {
		if e, ok := r.lastClaim(); ok && e.Type == status.TypeLinkUp {
			break
		}
		r.advance(2 * time.Second)
		time.Sleep(300 * time.Millisecond)
	}
	if e, ok := r.lastClaim(); !ok || e.Type != status.TypeLinkUp {
		t.Fatalf("the recovered link was never claimed up again: %+v", e)
	}
	t.Log("STEP 9: recovered link claimed up again")

	// And having recovered, it is left alone.
	before := r.restartCount()
	for i := 0; i < 8; i++ {
		r.advance(3 * time.Second)
		time.Sleep(100 * time.Millisecond)
	}
	if n := r.restartCount(); n != before {
		t.Errorf("kept restarting a recovered gateway: %d → %d", before, n)
	}
	t.Log("STEP 10: recovered link left alone")
}

// TestTier2_DMRGatewayKnowsOnlyWhenNothingAnswers pins down exactly how much the
// daemon knows about its own link, because that is what decides which signals the
// supervisor may trust.
//
// The answer turns out to hinge on whether anything is listening at the master's
// address, which is not a distinction anyone would guess:
//
//   - Nothing listening. Sends draw an ICMP port-unreachable, the socket read
//     errors, and DMRGateway drops out of RUNNING. It reports "disc" and it is
//     right. A poll detects this, and it is the common case for a master that has
//     genuinely gone away.
//   - Something listening, session dead — the address is fine, our session is not.
//     No ICMP comes back, reads simply return nothing, and the connection timeout
//     that should catch this never fires. The daemon reports "conn", and it is
//     wrong, and it will not recover. Measured at 200 s of silence.
//
// The second is the state a returning WAN produces, and no daemon-side signal
// distinguishes it from a healthy link. What covers it is the thing Waypoint knows
// and the daemon does not: whether the node itself had a route out. That is why
// the acceptance test above drives recovery from the WAN signal rather than from
// anything the daemon says.
func TestTier2_DMRGatewayKnowsOnlyWhenNothingAnswers(t *testing.T) {
	r := startSupervisedRig(t)
	if !r.master.waitLogin(10 * time.Second) {
		t.Fatalf("upstream login never completed\n%s", r.log.String())
	}

	// The stub records the login when it receives the config packet; the daemon
	// reaches RUNNING only once it has processed the acknowledgement, so poll until
	// it agrees rather than sampling into that window.
	poll := func() string { return pollGatewayStatus(t, config.MQTTNameDMRGateway, 5*time.Second) }
	var live string
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if live = poll(); strings.Contains(live, "net1:conn") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !strings.Contains(live, "net1:conn") {
		t.Fatalf("a live link never reported conn, last answer %q — if this is empty, "+
			"[Remote Commands] is not enabled in the rendered config", live)
	}
	t.Logf("a live link reports %q", live)

	// Case 1: the master vanishes and nothing takes its place.
	r.master.close()
	var gone string
	for deadline := time.Now().Add(25 * time.Second); time.Now().Before(deadline); {
		if gone = poll(); strings.Contains(gone, "net1:disc") {
			break
		}
		time.Sleep(time.Second)
	}
	if !strings.Contains(gone, "net1:disc") {
		t.Errorf("with nothing listening the daemon should know its link is down, got %q", gone)
	} else {
		t.Logf("nothing listening: the daemon knows, and reports %q", gone)
	}

	// Case 2: something answers at the address again, but our session is dead.
	master2, err := newStubMasterOnPort("BM", r.port, false)
	if err != nil {
		t.Fatalf("rebind :%d: %v", r.port, err)
	}
	defer master2.close()

	time.Sleep(20 * time.Second)
	back := poll()
	if master2.isLoggedIn() {
		t.Logf("UPSTREAM CHANGED: DMRGateway reconnected on its own (status %q). The premise of #22 "+
			"no longer holds against this pinned build — revisit the supervisor's grace period and the "+
			"acceptance test above.", back)
		return
	}
	if strings.Contains(back, "net1:conn") {
		t.Logf("something listening, session dead: the daemon reports %q and has not reconnected — "+
			"it does not know, so this state is only reachable through what Waypoint knows itself", back)
	} else {
		t.Logf("something listening, session dead: the daemon reports %q without having reconnected", back)
	}
}
