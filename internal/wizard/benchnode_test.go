//go:build benchnode

// End-to-end join-failure test against a real, running node.
//
// The rollback is already tested twice: internal/wizard/join_test.go drives it
// against the fake, and internal/sysprov TestBenchJoinRevert drives it against a
// real radio. Both stop short of the thing that actually failed on the bench.
// The fake cannot be wrong about a radio it does not have, and the sysprov test
// calls System directly — so neither exercises the checkpoint, the AP handover,
// the verification, the rollback and the re-raise as one sequence behind the HTTP
// handler the operator's browser talks to. Every defect found on the bench so far
// has been in that seam rather than in either half of it.
//
// It runs from a workstation against a node in setup mode, and the reason that
// works is worth stating: /api/setup/* is served on every interface, so a node
// whose setup access point is on wlan0 is still reachable over Ethernet. The test
// watches the access point go down, the join fail, and the access point come back
// — across a link that is not the one being torn down. Driving this from a phone
// on the access point could not observe its own failure.
//
//	WAYPOINT_BENCH_NODE=https://172.16.50.13 \
//	WAYPOINT_BENCH_SSID='DeathStar' \
//	go test -tags benchnode ./internal/wizard/ -run TestBenchNode -v -timeout 10m
//
// It is destructive to the node's wireless state and expects an UNPROVISIONED
// node with its access point up. It refuses to run against a provisioned one.

package wizard_test

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// apReturnDeadline bounds the wait for the access point to come back after a
// failed join. NetworkManager's rollback plus a fresh AP activation is a few
// seconds on the bench; this is deliberately much longer, because the failure
// this test exists to catch is "it never comes back", and calling that at five
// seconds would turn a slow node into a false alarm.
const apReturnDeadline = 3 * time.Minute

// wrongPassphrase is long enough to be a legal WPA2 passphrase, so the node gets
// far enough to actually be refused by the access point. A too-short one would be
// rejected by argument validation and never reach the radio — which would test
// the validator and report it as a rollback.
const wrongPassphrase = "definitely-not-the-right-passphrase"

type benchNode struct {
	t    *testing.T
	base string
	c    *http.Client
}

func newBenchNode(t *testing.T) *benchNode {
	t.Helper()
	base := os.Getenv("WAYPOINT_BENCH_NODE")
	if base == "" {
		t.Skip("set WAYPOINT_BENCH_NODE to a node's base URL (e.g. https://172.16.50.13)")
	}
	return &benchNode{
		t:    t,
		base: strings.TrimSuffix(base, "/"),
		// The node serves its own self-signed certificate (RFC-0012) and this test
		// is about the join, not the trust chain.
		c: &http.Client{
			Timeout:   2 * time.Minute,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // bench node, self-signed by design
		},
	}
}

func (b *benchNode) do(method, path string, body any) (int, map[string]any) {
	b.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			b.t.Fatalf("encode %s: %v", path, err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, b.base+path, rdr)
	if err != nil {
		b.t.Fatalf("build %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// state fetches the wizard's view.
func (b *benchNode) state() map[string]any {
	b.t.Helper()
	code, body := b.do(http.MethodGet, "/api/setup/state", nil)
	if code != http.StatusOK {
		b.t.Fatalf("GET /api/setup/state returned %d: %v", code, body)
	}
	return body
}

// apUp reports whether the node says its setup access point is up.
func (b *benchNode) apUp() bool {
	ap, _ := b.state()["ap"].(map[string]any)
	up, _ := ap["up"].(bool)
	return up
}

// requireSetupModeWithAP refuses to run against a node this test would damage
// rather than measure.
func (b *benchNode) requireSetupModeWithAP() {
	b.t.Helper()
	st := b.state()
	if mode, _ := st["mode"].(string); mode != "setup" {
		b.t.Skipf("node is in %q mode, not setup — reflash or run `waypointd -reset-claim --full` first", mode)
	}
	if !b.apUp() {
		ap, _ := st["ap"].(map[string]any)
		b.t.Skipf("the setup access point is not up (%v); this test needs it up to prove it comes back", ap)
	}
}

// waitForAP polls until the node reports its access point up again.
func (b *benchNode) waitForAP(deadline time.Duration) time.Duration {
	b.t.Helper()
	start := time.Now()
	for time.Since(start) < deadline {
		if b.apUp() {
			return time.Since(start)
		}
		time.Sleep(2 * time.Second)
	}
	return -1
}

// TestBenchNodeRejectsAWrongPassphraseAndComesBack is the whole point.
//
// A join that fails is not an error case, it is the common case: an operator
// mistypes a passphrase on a phone keyboard. What must not happen is that the
// node takes its access point down to try, fails, and leaves the operator with
// no way back to the wizard — which is a reflash, not an inconvenience.
func TestBenchNodeRejectsAWrongPassphraseAndComesBack(t *testing.T) {
	b := newBenchNode(t)
	ssid := os.Getenv("WAYPOINT_BENCH_SSID")
	if ssid == "" {
		t.Skip("set WAYPOINT_BENCH_SSID to a real in-range network — a wrong passphrase for a network that is not there tests the wrong failure")
	}
	b.requireSetupModeWithAP()
	t.Logf("node %s is unprovisioned with its access point up; joining %q with a deliberately wrong passphrase", b.base, ssid)

	start := time.Now()
	code, body := b.do(http.MethodPost, "/api/setup/network", map[string]any{
		"ssid":            ssid,
		"psk":             wrongPassphrase,
		"country":         os.Getenv("WAYPOINT_BENCH_COUNTRY"),
		"timeout_seconds": 45,
	})
	elapsed := time.Since(start)
	t.Logf("POST /api/setup/network -> %d in %s: %v", code, elapsed.Round(time.Second), body["error"])

	// 1. It must fail. A node that reports success on a wrong passphrase would
	//    spend the access point and strand the operator believing they were done.
	if code >= 200 && code < 300 {
		if connected, _ := body["connected"].(bool); connected {
			t.Fatalf("the node reported a SUCCESSFUL join with a wrong passphrase: %v", body)
		}
		t.Fatalf("the node returned %d for a join that cannot have worked: %v", code, body)
	}

	// 2. The message must name the network and be about the passphrase. "Secrets
	//    were required, but not provided" is what NetworkManager says when an
	//    access point refuses a key, and it reads as "you forgot the password" to
	//    an operator who just typed one.
	msg, _ := body["error"].(string)
	if msg == "" {
		t.Error("the failure carried no error message")
	}
	if !strings.Contains(msg, ssid) {
		t.Errorf("the error does not name the network the operator tried: %q", msg)
	}
	for _, leak := range []string{"passwd-file", "no-secrets", "Secrets were required"} {
		if strings.Contains(msg, leak) {
			t.Errorf("the raw NetworkManager wording %q reached the operator: %q", leak, msg)
		}
	}

	// 3. The access point must come back. This is the acceptance criterion; the
	//    rest is diagnosis.
	back := b.waitForAP(apReturnDeadline)
	if back < 0 {
		t.Fatalf("THE ACCESS POINT DID NOT COME BACK within %s — a mistyped passphrase has stranded this node", apReturnDeadline)
	}
	t.Logf("the access point came back %s after the failure", back.Round(time.Second))

	// 4. Setup must be resumable, not merely reachable. An operator whose join
	//    failed has to be able to correct it.
	st := b.state()
	if mode, _ := st["mode"].(string); mode != "setup" {
		t.Errorf("the node left setup mode after a failed join: %q", mode)
	}
	if provisioned, _ := st["provisioned"].(bool); provisioned {
		t.Error("the node marked itself provisioned after a failed join")
	}
}

// TestBenchNodeHandlesANetworkThatIsNotThere covers the other way a join fails,
// which produces a different message and a different timing profile: nothing
// refuses the passphrase because nothing answers at all.
func TestBenchNodeHandlesANetworkThatIsNotThere(t *testing.T) {
	b := newBenchNode(t)
	b.requireSetupModeWithAP()

	const absent = "waypoint-bench-no-such-network"
	start := time.Now()
	code, body := b.do(http.MethodPost, "/api/setup/network", map[string]any{
		"ssid": absent, "psk": wrongPassphrase, "timeout_seconds": 45,
	})
	t.Logf("POST for an absent network -> %d in %s: %v", code, time.Since(start).Round(time.Second), body["error"])

	if code >= 200 && code < 300 {
		t.Fatalf("the node reported success joining a network that does not exist: %v", body)
	}
	if msg, _ := body["error"].(string); msg == "" {
		t.Error("the failure carried no error message")
	}

	back := b.waitForAP(apReturnDeadline)
	if back < 0 {
		t.Fatalf("THE ACCESS POINT DID NOT COME BACK within %s after a join to an absent network", apReturnDeadline)
	}
	t.Logf("the access point came back %s after the failure", back.Round(time.Second))
}

// TestBenchNodeSurvivesRepeatedFailures is the case an operator actually
// produces: they mistype, retry, mistype again. Each attempt tears the access
// point down and puts it back, and any state that leaks between attempts —
// a checkpoint left behind, a profile not cleaned up, an access point marked
// spent — shows up here rather than on the first try.
func TestBenchNodeSurvivesRepeatedFailures(t *testing.T) {
	b := newBenchNode(t)
	ssid := os.Getenv("WAYPOINT_BENCH_SSID")
	if ssid == "" {
		t.Skip("set WAYPOINT_BENCH_SSID to a real in-range network")
	}
	b.requireSetupModeWithAP()

	const attempts = 3
	for i := 1; i <= attempts; i++ {
		code, body := b.do(http.MethodPost, "/api/setup/network", map[string]any{
			"ssid":            ssid,
			"psk":             fmt.Sprintf("%s-%d", wrongPassphrase, i),
			"timeout_seconds": 45,
		})
		if code >= 200 && code < 300 {
			t.Fatalf("attempt %d reported success with a wrong passphrase: %v", i, body)
		}
		back := b.waitForAP(apReturnDeadline)
		if back < 0 {
			t.Fatalf("the access point did not come back after failure %d of %d — the node strands an operator who mistypes twice", i, attempts)
		}
		t.Logf("attempt %d: refused, access point back in %s", i, back.Round(time.Second))
	}

	if st := b.state(); st["mode"] != "setup" {
		t.Errorf("after %d failed joins the node is in %q mode", attempts, st["mode"])
	}
}
