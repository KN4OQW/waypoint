package captive

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/privhelper/privtest"
	"github.com/miekg/dns"
)

// --- session lock ---------------------------------------------------------

// clock is a hand-wound time source. It is mutex-guarded because the controller
// reads it from its own goroutine while a test advances it, and an unsynchronised
// one both races and hides real ordering bugs behind a hung test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	return c.t
}

func newLock(t *testing.T, macs map[string]string) (*Lock, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
	return &Lock{
		Idle:      DefaultIdleTimeout,
		Now:       c.now,
		LookupMAC: func(ip string) string { return macs[ip] },
		Logf:      t.Logf,
	}, c
}

// Property 1: the first device in holds the session and every other one is turned
// away — the whole point of the lock on an open network.
func TestLockFirstDeviceWins(t *testing.T) {
	l, _ := newLock(t, map[string]string{
		"10.42.0.51": "aa:bb:cc:dd:ee:01",
		"10.42.0.52": "aa:bb:cc:dd:ee:02",
	})

	ok, holder := l.Admit("10.42.0.51:44321")
	if !ok {
		t.Fatal("the first device was refused")
	}
	if holder.IP != "10.42.0.51" || holder.MAC != "aa:bb:cc:dd:ee:01" {
		t.Errorf("holder = %+v", holder)
	}

	ok, holder = l.Admit("10.42.0.52:51000")
	if ok {
		t.Fatal("a second device was admitted")
	}
	if holder.IP != "10.42.0.51" {
		t.Errorf("the rejection names %q as the holder, want the first device", holder.IP)
	}

	// The holder keeps working, repeatedly.
	for i := 0; i < 3; i++ {
		if ok, _ := l.Admit("10.42.0.51:44321"); !ok {
			t.Fatalf("the holder was refused on request %d", i+2)
		}
	}
}

// Property 2: the session follows the device, not the address. A phone whose DHCP
// lease is renewed into a different address mid-setup must not have to start over.
func TestLockFollowsTheMACAcrossALeaseChange(t *testing.T) {
	macs := map[string]string{"10.42.0.51": "aa:bb:cc:dd:ee:01"}
	l, _ := newLock(t, macs)

	if ok, _ := l.Admit("10.42.0.51:1"); !ok {
		t.Fatal("the first device was refused")
	}

	// Same phone, new lease.
	macs["10.42.0.77"] = "aa:bb:cc:dd:ee:01"
	ok, holder := l.Admit("10.42.0.77:2")
	if !ok {
		t.Fatal("the holder was refused after its lease moved")
	}
	if holder.IP != "10.42.0.77" {
		t.Errorf("the holder's address was not updated: %+v", holder)
	}
	// And the old address still belongs to it, so a request in flight is not lost.
	if ok, _ := l.Admit("10.42.0.51:3"); !ok {
		t.Error("the previous address was refused immediately after the move")
	}
}

// Property 3: the session expires after the idle timeout, and the next device in
// gets it. An operator whose phone died must not be locked out for the AP's life.
func TestLockIdleExpiry(t *testing.T) {
	l, c := newLock(t, map[string]string{
		"10.42.0.51": "aa:bb:cc:dd:ee:01",
		"10.42.0.52": "aa:bb:cc:dd:ee:02",
	})

	if ok, _ := l.Admit("10.42.0.51:1"); !ok {
		t.Fatal("the first device was refused")
	}
	// Just short of the timeout: still held.
	c.add(DefaultIdleTimeout - time.Second)
	if ok, _ := l.Admit("10.42.0.52:1"); ok {
		t.Fatal("a second device was admitted before the session expired")
	}
	// The holder's own request slides the window forward.
	if ok, _ := l.Admit("10.42.0.51:1"); !ok {
		t.Fatal("the holder was refused")
	}
	c.add(DefaultIdleTimeout - time.Second)
	if ok, _ := l.Admit("10.42.0.52:1"); ok {
		t.Fatal("the idle window did not slide on the holder's request")
	}

	// Past the timeout with no activity: the session is free.
	c.add(2 * time.Second)
	ok, holder := l.Admit("10.42.0.52:1")
	if !ok {
		t.Fatal("the session did not expire")
	}
	if holder.IP != "10.42.0.52" {
		t.Errorf("holder = %+v after expiry", holder)
	}
}

// Property 4: Release frees the session at once, for an operator switching from a
// phone to a laptop rather than waiting out the timer.
func TestLockRelease(t *testing.T) {
	l, _ := newLock(t, map[string]string{
		"10.42.0.51": "aa:bb:cc:dd:ee:01",
		"10.42.0.52": "aa:bb:cc:dd:ee:02",
	})
	if ok, _ := l.Admit("10.42.0.51:1"); !ok {
		t.Fatal("the first device was refused")
	}
	l.Release()
	if _, held := l.Holder(); held {
		t.Error("a released lock still reports a holder")
	}
	if ok, _ := l.Admit("10.42.0.52:1"); !ok {
		t.Error("a second device was refused after a release")
	}
}

// Property 5: once setup is complete the lock admits nobody. There is no wizard
// left to hold, and the node has moved on to a flow with real authentication.
func TestLockCompleteAdmitsNobody(t *testing.T) {
	l, _ := newLock(t, map[string]string{"10.42.0.51": "aa:bb:cc:dd:ee:01"})
	if ok, _ := l.Admit("10.42.0.51:1"); !ok {
		t.Fatal("the first device was refused")
	}
	l.Complete()
	if ok, _ := l.Admit("10.42.0.51:1"); ok {
		t.Error("the previous holder was admitted after completion")
	}
	if ok, _ := l.Admit("10.42.0.99:1"); ok {
		t.Error("a new device was admitted after completion")
	}
}

// Property 6: a client with no ARP entry yet — normal, just after associating —
// is admitted and identified by address alone.
func TestLockWithoutAMAC(t *testing.T) {
	l, _ := newLock(t, map[string]string{})
	ok, holder := l.Admit("10.42.0.51:1")
	if !ok {
		t.Fatal("a client with no ARP entry was refused")
	}
	if holder.MAC != "" {
		t.Errorf("MAC = %q, want empty", holder.MAC)
	}
	if ok, _ := l.Admit("10.42.0.52:1"); ok {
		t.Error("a different address was admitted while the session was held")
	}
	if ok, _ := l.Admit("10.42.0.51:2"); !ok {
		t.Error("the holder was refused on a later request")
	}
}

// Property 7: an unparseable remote address is refused rather than admitted.
func TestLockRefusesAnUnknownAddress(t *testing.T) {
	l, _ := newLock(t, map[string]string{})
	if ok, _ := l.Admit(""); ok {
		t.Error("an empty remote address was admitted")
	}
}

// Property 8: the ARP table is parsed in its real format, and an incomplete entry
// is not treated as an identity.
func TestLookupMACFromARP(t *testing.T) {
	p := filepath.Join(t.TempDir(), "arp")
	if err := os.WriteFile(p, []byte(
		"IP address       HW type     Flags       HW address            Mask     Device\n"+
			"10.42.0.51       0x1         0x2         aa:bb:cc:dd:ee:01     *        wlan0\n"+
			"10.42.0.52       0x1         0x0         00:00:00:00:00:00     *        wlan0\n"+
			"10.42.0.53       0x1         0x2         not-a-mac             *        wlan0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LookupMACFromARP(p, "10.42.0.51"); got != "aa:bb:cc:dd:ee:01" {
		t.Errorf("MAC = %q", got)
	}
	if got := LookupMACFromARP(p, "10.42.0.52"); got != "" {
		t.Errorf("an incomplete entry returned %q, want empty", got)
	}
	if got := LookupMACFromARP(p, "10.42.0.53"); got != "" {
		t.Errorf("a malformed entry returned %q, want empty", got)
	}
	if got := LookupMACFromARP(p, "10.42.0.99"); got != "" {
		t.Errorf("an absent address returned %q", got)
	}
	if got := LookupMACFromARP(filepath.Join(t.TempDir(), "nope"), "10.42.0.51"); got != "" {
		t.Errorf("a missing ARP table returned %q", got)
	}
}

// --- portal ---------------------------------------------------------------

func newPortal(t *testing.T, l *Lock) (*Portal, *int) {
	t.Helper()
	hits := 0
	return &Portal{
		Lock: l,
		Wizard: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("THE WIZARD"))
		}),
		OnRequest: func() { hits++ },
		Logf:      t.Logf,
	}, &hits
}

// Property 9: every operating system's connectivity probe is answered in a way
// that says "you are behind a portal", and none of them is left to fall through.
func TestProbesSignalACaptivePortal(t *testing.T) {
	p, _ := newPortal(t, nil)
	h := p.Handler()

	for _, path := range ProbePaths() {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			if rec.Code != http.StatusFound {
				t.Fatalf("%s = %d, want 302", path, rec.Code)
			}
			if loc := rec.Header().Get("Location"); loc != p.PortalURL() {
				t.Errorf("Location = %q, want %q", loc, p.PortalURL())
			}
			// The words each vendor's check looks for must not appear: a body
			// containing "Success" tells iOS the network is fine and no sheet opens.
			body := rec.Body.String()
			for _, forbidden := range []string{"Success", "Microsoft Connect Test", "Microsoft NCSI"} {
				if strings.Contains(body, forbidden) {
					t.Errorf("the response contains %q, which tells the client it is online", forbidden)
				}
			}
		})
	}
}

// Property 10: the RFC 8908 endpoint answers the clients that ask directly,
// having learned the URL from DHCP option 114.
func TestCaptiveJSON(t *testing.T) {
	p, _ := newPortal(t, nil)
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/captive-portal", nil))

	if ct := rec.Header().Get("Content-Type"); ct != "application/captive+json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["captive"] != true {
		t.Errorf("captive = %v, want true", body["captive"])
	}
	if body["user-portal-url"] != p.PortalURL() {
		t.Errorf("user-portal-url = %v, want %q", body["user-portal-url"], p.PortalURL())
	}
}

// Property 11: the catch-all serves the wizard, so a client that asks for any
// host at all lands on the page rather than a 404 that reads as a broken network.
func TestCatchAllServesTheWizard(t *testing.T) {
	p, hits := newPortal(t, nil)
	for _, path := range []string{"/", "/index.html", "/some/deep/path", "/favicon.ico"} {
		rec := httptest.NewRecorder()
		p.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "THE WIZARD") {
			t.Errorf("%s = %d %q", path, rec.Code, rec.Body.String())
		}
	}
	if *hits == 0 {
		t.Error("the AP controller was never told a client is here")
	}
}

// Property 12: the second device gets the 403 page, and it names the holder and
// the timeout rather than being a bare refusal.
func TestSecondDeviceGetsTheBusyPage(t *testing.T) {
	l, _ := newLock(t, map[string]string{
		"10.42.0.51": "aa:bb:cc:dd:ee:01",
		"10.42.0.52": "aa:bb:cc:dd:ee:02",
	})
	p, _ := newPortal(t, l)
	h := p.Handler()

	first := httptest.NewRequest("GET", "/", nil)
	first.RemoteAddr = "10.42.0.51:1000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, first)
	if rec.Code != http.StatusOK {
		t.Fatalf("the first device got %d", rec.Code)
	}

	second := httptest.NewRequest("GET", "/", nil)
	second.RemoteAddr = "10.42.0.52:1000"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, second)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("the second device got %d, want 403", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "THE WIZARD") {
		t.Error("the second device was served the wizard")
	}
	for _, want := range []string{"another device", "10.42.0.51", "10m0s"} {
		if !strings.Contains(body, want) {
			t.Errorf("the busy page does not mention %q:\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q; the second device is a person, not a script", ct)
	}
}

// Property 13: the AP-segment check refuses an address from anywhere else, so
// mounting the portal more widely than intended cannot hand the session to an
// off-segment client.
func TestIsAPClient(t *testing.T) {
	prefix := netip.MustParsePrefix("10.42.0.0/24")
	for addr, want := range map[string]bool{
		"10.42.0.51:1000":   true,
		"10.42.0.1:1000":    true,
		"192.168.1.50:1000": false,
		"127.0.0.1:1000":    false,
		"not-an-address":    false,
		"[fd00::1]:1000":    false,
	} {
		if got := IsAPClient(addr, prefix); got != want {
			t.Errorf("IsAPClient(%q) = %v, want %v", addr, got, want)
		}
	}
}

// --- DNS ------------------------------------------------------------------

type recordingWriter struct {
	dns.ResponseWriter
	msg *dns.Msg
}

func (r *recordingWriter) WriteMsg(m *dns.Msg) error { r.msg = m; return nil }
func (r *recordingWriter) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(10, 42, 0, 51), Port: 5353}
}
func (r *recordingWriter) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(10, 42, 0, 1), Port: 53}
}

func ask(t *testing.T, d *DNSResponder, name string, qtype uint16) *dns.Msg {
	t.Helper()
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn(name), qtype)
	w := &recordingWriter{}
	d.ServeDNS(w, req)
	if w.msg == nil {
		t.Fatal("no response was written")
	}
	return w.msg
}

// Property 14: every A query is answered with the node, whatever was asked for.
// This is what makes a client's probe arrive here in the first place.
func TestDNSHijacksEveryAQuery(t *testing.T) {
	d := &DNSResponder{Logf: t.Logf}
	for _, name := range append(ProbeHostnames(), "example.com", "a.very.deep.name.invalid") {
		m := ask(t, d, name, dns.TypeA)
		if m.Rcode != dns.RcodeSuccess {
			t.Errorf("%s: rcode = %s", name, dns.RcodeToString[m.Rcode])
		}
		if len(m.Answer) != 1 {
			t.Fatalf("%s: %d answers, want 1", name, len(m.Answer))
		}
		a, ok := m.Answer[0].(*dns.A)
		if !ok {
			t.Fatalf("%s: answer is %T", name, m.Answer[0])
		}
		if got := a.A.String(); got != DefaultAddress.String() {
			t.Errorf("%s -> %s, want %s", name, got, DefaultAddress)
		}
		if !m.Authoritative {
			t.Errorf("%s: the answer is not authoritative", name)
		}
	}
}

// Property 15: AAAA gets an empty NOERROR rather than a forged address.
//
// A client handed a nonsense AAAA tries it first and waits for it to time out,
// which is the difference between the captive sheet appearing at once and
// appearing half a minute later.
func TestDNSDoesNotForgeAAAA(t *testing.T) {
	d := &DNSResponder{Logf: t.Logf}
	m := ask(t, d, "captive.apple.com", dns.TypeAAAA)
	if m.Rcode != dns.RcodeSuccess {
		t.Errorf("rcode = %s, want NOERROR", dns.RcodeToString[m.Rcode])
	}
	if len(m.Answer) != 0 {
		t.Errorf("AAAA returned %d answers, want none", len(m.Answer))
	}
}

// Property 16: a custom answer address is honoured, and anything that is not a
// query is refused rather than answered with a hijacked address.
func TestDNSAddressAndOpcode(t *testing.T) {
	d := &DNSResponder{Answer: netip.MustParseAddr("192.168.66.1"), Logf: t.Logf}
	m := ask(t, d, "example.com", dns.TypeA)
	if got := m.Answer[0].(*dns.A).A.String(); got != "192.168.66.1" {
		t.Errorf("answer = %s", got)
	}

	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("example.com"), dns.TypeA)
	req.Opcode = dns.OpcodeUpdate
	w := &recordingWriter{}
	d.ServeDNS(w, req)
	if w.msg.Rcode != dns.RcodeRefused {
		t.Errorf("an update was answered with %s, want REFUSED", dns.RcodeToString[w.msg.Rcode])
	}
}

// Property 17: the responder really serves on a socket, not just in unit-test
// calls.
func TestDNSListenAndServe(t *testing.T) {
	d := &DNSResponder{Logf: t.Logf}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Port 0 to avoid needing privileges or a fixed port; the listener reports the
	// address it actually got.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no UDP loopback available: %v", err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()

	done := make(chan error, 1)
	go func() { done <- d.ListenAndServe(ctx, addr) }()
	defer d.Shutdown()

	c := new(dns.Client)
	req := new(dns.Msg)
	req.SetQuestion("captive.apple.com.", dns.TypeA)

	var resp *dns.Msg
	for i := 0; i < 50; i++ { // the listener may not be bound on the first try
		resp, _, err = c.Exchange(req, addr)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("no answer from the responder at %s: %v", addr, err)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("%d answers", len(resp.Answer))
	}
	if got := resp.Answer[0].(*dns.A).A.String(); got != DefaultAddress.String() {
		t.Errorf("answer = %s", got)
	}
}

// --- controller -----------------------------------------------------------

func newController(t *testing.T, psk string) (*Controller, *privtest.Fake, *clock) {
	t.Helper()
	fake := privtest.NewFake()
	c := &clock{t: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
	return &Controller{
		Prov: fake, SSID: "Waypoint-Setup-1A2B", PSK: psk,
		Window: DefaultAssociateWindow, Now: c.now, Logf: t.Logf,
	}, fake, c
}

// Property 18: the AP comes up open by default and as WPA2 when a key file
// supplied one — the two variants the operator can actually get.
func TestControllerUpOpenAndProtected(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		ctrl, fake, _ := newController(t, "")
		if err := ctrl.Up(context.Background()); err != nil {
			t.Fatal(err)
		}
		if fake.AP == nil || fake.AP.HasPassword {
			t.Errorf("AP = %+v, want an open network", fake.AP)
		}
		if !ctrl.Status().Open {
			t.Error("Status does not report the network as open")
		}
	})

	t.Run("wpa2", func(t *testing.T) {
		ctrl, fake, _ := newController(t, "correct horse")
		if err := ctrl.Up(context.Background()); err != nil {
			t.Fatal(err)
		}
		if fake.AP == nil || !fake.AP.HasPassword {
			t.Errorf("AP = %+v, want a protected network", fake.AP)
		}
		if ctrl.Status().Open {
			t.Error("Status reports a protected network as open")
		}
	})
}

// Property 19: a network join takes the AP down at once, and it does not come
// back. Leaving it up would mean an open network broadcasting beside the real one
// the node just joined.
func TestControllerDownOnJoin(t *testing.T) {
	ctrl, fake, _ := newController(t, "")
	ctx := context.Background()
	if err := ctrl.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Down(ctx, ReasonJoined); err != nil {
		t.Fatal(err)
	}
	if fake.AP != nil {
		t.Error("the AP is still up after a join")
	}
	st := ctrl.Status()
	if st.Up || !st.Spent || st.Reason != ReasonJoined {
		t.Errorf("status = %+v", st)
	}

	// It refuses to come back, and says why.
	err := ctrl.Up(ctx)
	if privhelper.CodeOf(err) != privhelper.CodeConflict {
		t.Fatalf("Up after a join: code = %q (err %v), want %q", privhelper.CodeOf(err), err, privhelper.CodeConflict)
	}
	if !strings.Contains(err.Error(), "reboot") {
		t.Errorf("the refusal does not say how to get it back: %v", err)
	}
	if fake.AP != nil {
		t.Error("a refused Up still raised the AP")
	}
}

// Property 20: with nobody associated, the AP goes down when the window expires.
func TestControllerWindowExpires(t *testing.T) {
	ctrl, fake, c := newController(t, "")
	ctx := context.Background()
	if err := ctrl.Up(ctx); err != nil {
		t.Fatal(err)
	}

	tick := make(chan time.Time, 1)
	done := make(chan struct{})
	go func() { defer close(done); ctrl.Run(ctx, tick) }()

	// A tick inside the window changes nothing.
	tick <- c.add(DefaultAssociateWindow - time.Minute)
	if st := ctrl.Status(); !st.Up {
		t.Fatal("the AP came down inside the window")
	}

	tick <- c.add(2 * time.Minute)
	<-done

	if fake.AP != nil {
		t.Error("the AP is still up after the window expired")
	}
	st := ctrl.Status()
	if st.Up || !st.Spent || st.Reason != ReasonNobodyCame {
		t.Errorf("status = %+v", st)
	}
}

// Property 21: a client that turns up cancels the window. The AP exists for
// whoever is using it, and taking it down mid-setup would strand them.
func TestControllerClientCancelsTheWindow(t *testing.T) {
	ctrl, fake, c := newController(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	if err := ctrl.Up(ctx); err != nil {
		t.Fatal(err)
	}
	ctrl.NoteClient()

	tick := make(chan time.Time, 1)
	done := make(chan struct{})
	go func() { defer close(done); ctrl.Run(ctx, tick) }()
	// Run logs through t.Logf, so the test must not return until it has stopped —
	// a goroutine logging into a finished test is a race, and one that only shows
	// up under an unlucky schedule.
	t.Cleanup(func() { cancel(); <-done })

	tick <- c.add(2 * DefaultAssociateWindow)
	// A second tick guarantees the first has been processed, without a sleep.
	tick <- c.now()

	if fake.AP == nil {
		t.Error("the AP came down despite a client using it")
	}
	if st := ctrl.Status(); !st.Up || !st.Client {
		t.Errorf("status = %+v", st)
	}
}

// Property 22: a shutdown takes the AP down without spending it — the node is
// going away, and the next boot should raise it again if setup is unfinished.
func TestControllerShutdownDoesNotSpendTheAP(t *testing.T) {
	ctrl, fake, _ := newController(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	if err := ctrl.Up(ctx); err != nil {
		t.Fatal(err)
	}

	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() { defer close(done); ctrl.Run(ctx, tick) }()
	cancel()
	<-done

	if fake.AP != nil {
		t.Error("the AP is still up after a shutdown")
	}
	if st := ctrl.Status(); st.Spent {
		t.Error("a shutdown spent the AP; the next boot would not raise it")
	}
	// And it can be raised again.
	if err := ctrl.Up(context.Background()); err != nil {
		t.Errorf("Up after a shutdown: %v", err)
	}
}

// Property 23: Up is idempotent, so a restarted supervisor loop does not raise a
// second AP.
func TestControllerUpIsIdempotent(t *testing.T) {
	ctrl, fake, _ := newController(t, "")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := ctrl.Up(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(fake.CallsTo(privhelper.MethodAPUp)); n != 1 {
		t.Errorf("APUp was called %d times, want 1", n)
	}
}

// The controller must remember which interface the helper actually raised the AP
// on. It is the only place that learns it — the operator names none on a node with
// one radio, and the helper picks — and both route checks exclude it. Before this,
// the exclusion read a flag that is empty on a normal node, so it silently did
// nothing exactly where it mattered: a node that raised its AP during an outage
// would count the AP's own route as a way out.
func TestControllerRecordsTheRaisedInterface(t *testing.T) {
	fake := privtest.NewFake()
	c := &Controller{Prov: fake, SSID: "Waypoint-Setup-TEST", Logf: func(string, ...any) {}}

	if got := c.APInterface(); got != "" {
		t.Errorf("before the AP is raised there is nothing to exclude, got %q", got)
	}
	if err := c.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	// The request named no interface; the helper chose, and the controller kept it.
	if got := c.APInterface(); got == "" {
		t.Fatal("the interface the helper chose was discarded")
	} else if got != "wlan0" {
		t.Errorf("APInterface = %q, want the helper's choice", got)
	}
}

// What was actually raised wins over what was configured, since only the former is
// the interface the route checks must exclude.
func TestControllerPrefersTheRaisedInterface(t *testing.T) {
	c := &Controller{SSID: "s", Interface: "wlan0"}
	if got := c.APInterface(); got != "wlan0" {
		t.Errorf("with no AP up, the configured interface is the best guess: got %q", got)
	}
	c.mu.Lock()
	c.iface = "wlan1" // as recorded from an APUp response
	c.mu.Unlock()
	if got := c.APInterface(); got != "wlan1" {
		t.Errorf("APInterface = %q, want the interface actually raised", got)
	}
}
