package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/netconfig"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/internal/tlscert"
)

// The rename surface after first boot. `/api/network/host/apply` is the second
// place a Waypoint node can change its own name, and it has the same consequence
// the wizard's hostname step does: the operator is sent to a new address, and a
// certificate still naming the old host meets them with a mismatch warning on a
// node that just told them where to go.
//
// These tests drive the handler itself rather than the holder, because the gap
// they guard against was never a holder bug — `Ensure` always worked. It was that
// nothing on this path called it.

// hostEnv is a server with just enough wired for the host-apply handler: a store
// to read the model from, a timesyncd path under a temp dir, a certificate holder,
// and a fake command runner so the test does not rename the machine it runs on.
type hostEnv struct {
	s      *server
	holder *tlscert.Holder
	cmds   []string // every command the handler ran, for the idempotence checks
}

func newHostEnv(t *testing.T, bootHostname string) *hostEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	e := &hostEnv{holder: &tlscert.Holder{Dir: t.TempDir(), Logf: t.Logf}}
	e.s = &server{
		store:         st,
		timesyncdConf: filepath.Join(dir, "timesyncd.conf"),
		certs:         e.holder,
	}

	// The daemon mints for the name the node booted with, long before any of this.
	if _, err := e.holder.Ensure(bootHostname); err != nil {
		t.Fatal(err)
	}

	// A runner that answers the idempotence probes with the boot state and records
	// what the handler asked the host to do.
	current := bootHostname
	saved := netRun
	t.Cleanup(func() { netRun = saved })
	netRun = func(name string, args ...string) (string, error) {
		e.cmds = append(e.cmds, name+" "+strings.Join(args, " "))
		switch {
		case name == "hostnamectl" && len(args) == 1 && args[0] == "--static":
			return current, nil
		case name == "hostnamectl" && len(args) == 2 && args[0] == "set-hostname":
			current = args[1]
			return "", nil
		case name == "timedatectl" && len(args) > 0 && args[0] == "show":
			return "UTC", nil
		}
		return "", nil
	}
	return e
}

// apply stores host and runs the apply, returning the decoded response.
func (e *hostEnv) apply(t *testing.T, h netconfig.Host) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"host": h})
	if err != nil {
		t.Fatal(err)
	}
	if err := netconfig.Set(e.s.store, body, "test"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	e.s.networkHostApply(rec, httptest.NewRequest("POST", "/api/network/host/apply", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("host apply = %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Property: renaming through the network surface remints, and says so.
//
// The "says so" matters as much as the remint. A silently replaced certificate is
// a browser warning the operator has no reason to expect; the response carries the
// fact so the UI can tell them to re-trust it once.
func TestHostApplyRemintsTheCertificateForTheNewName(t *testing.T) {
	e := newHostEnv(t, "raspberrypi")
	before := serialOf(t, e.holder)

	out := e.apply(t, netconfig.Host{Hostname: "hs-shack", Timezone: "UTC"})

	if out["cert_reminted"] != true {
		t.Errorf("cert_reminted = %v, want true", out["cert_reminted"])
	}
	if got := e.holder.Hostname(); got != "hs-shack" {
		t.Errorf("the certificate is still held for %q", got)
	}
	if serialOf(t, e.holder) == before {
		t.Fatal("the certificate was not reminted after the rename")
	}

	// Both forms, because the dashboard is reached by both — and the old name is
	// genuinely gone rather than merely joined by the new one.
	leaf := leafOf(t, e.holder)
	for _, name := range []string{"hs-shack", "hs-shack.local"} {
		if err := leaf.VerifyHostname(name); err != nil {
			t.Errorf("the reminted certificate does not name %s: %v", name, err)
		}
	}
	if leaf.VerifyHostname("raspberrypi") == nil {
		t.Error("the certificate still validates for the host the node booted as")
	}
}

// Property: applying the same hostname again mints nothing.
//
// Regenerating on every apply would hand the operator a fresh trust prompt every
// time they touched an unrelated field on the Network tab, which trains them to
// click through certificate warnings — the opposite of what a pinned self-signed
// certificate is for.
func TestHostApplyDoesNotRemintWhenTheNameIsUnchanged(t *testing.T) {
	e := newHostEnv(t, "hs-shack")
	before := serialOf(t, e.holder)

	out := e.apply(t, netconfig.Host{Hostname: "hs-shack", Timezone: "UTC"})

	if out["cert_reminted"] != false {
		t.Errorf("cert_reminted = %v, want false", out["cert_reminted"])
	}
	// Note what is deliberately not asserted: `changed`. It is the aggregate over
	// hostname, timezone and NTP, and the first apply on a fresh node writes the
	// timesyncd drop-in — so it is true here while the hostname did not move. That
	// is exactly why the remint is not gated on it.
	if serialOf(t, e.holder) != before {
		t.Error("an unchanged hostname reminted the certificate")
	}
	for _, cmd := range e.cmds {
		if strings.HasPrefix(cmd, "hostnamectl set-hostname") {
			t.Errorf("an unchanged hostname issued %q", cmd)
		}
	}
}

// Property: a blank hostname leaves the certificate alone.
//
// Blank means "leave it" everywhere else in this domain. Passing it through would
// normalize to the `waypoint` fallback and mint for a name the node does not
// answer to — a self-inflicted mismatch on a node nobody renamed.
func TestHostApplyLeavesTheCertificateAloneWhenNoHostnameIsSet(t *testing.T) {
	e := newHostEnv(t, "hs-shack")
	before := serialOf(t, e.holder)

	out := e.apply(t, netconfig.Host{Timezone: "UTC"})

	if out["cert_reminted"] != false {
		t.Errorf("cert_reminted = %v, want false", out["cert_reminted"])
	}
	if got := e.holder.Hostname(); got != "hs-shack" {
		t.Errorf("a blank hostname moved the certificate to %q", got)
	}
	if serialOf(t, e.holder) != before {
		t.Error("a blank hostname reminted the certificate")
	}
}

// Property: a node without a certificate holder still applies host settings.
//
// The holder is optional on the server struct, and an apply is not the place to
// discover that with a nil dereference.
func TestHostApplyWithoutACertificateHolder(t *testing.T) {
	e := newHostEnv(t, "raspberrypi")
	e.s.certs = nil

	out := e.apply(t, netconfig.Host{Hostname: "hs-shack", Timezone: "UTC"})

	if out["applied"] != true || out["changed"] != true {
		t.Errorf("apply = %v, want applied+changed", out)
	}
	if out["cert_reminted"] != false {
		t.Errorf("cert_reminted = %v, want false", out["cert_reminted"])
	}
}
