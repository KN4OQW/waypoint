package captive

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/netip"
	"strings"
)

// Portal serves the endpoints each operating system probes to decide whether it
// is behind a captive portal, and enforces the one-device session lock.
//
// Every probe below is a real URL some vendor's connectivity check fetches. They
// are answered here rather than left to fall through because the answer decides
// whether the operator's phone silently pops the wizard up or shows "no internet"
// and waits for them to work it out.
type Portal struct {
	// Address is the node's address on the AP. Zero means DefaultAddress.
	Address netip.Addr
	// Lock admits one device. Nil disables the lock, which is for tests.
	Lock *Lock
	// Wizard serves the setup UI for an admitted device.
	Wizard http.Handler
	// OnRequest is called for every request from an AP client. It is how the AP
	// controller learns that somebody actually associated, which is what its
	// thirty-minute window is measured against.
	OnRequest func()
	// Logf receives one line per rejection.
	Logf func(format string, args ...any)
}

func (p *Portal) address() netip.Addr {
	if p.Address.IsValid() {
		return p.Address
	}
	return DefaultAddress
}

// PortalURL is the address a client is sent to.
func (p *Portal) PortalURL() string {
	return "http://" + p.address().String() + "/"
}

func (p *Portal) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

// Handler returns the portal's routes.
//
// The catch-all matters as much as the named probes: with DNS hijacked, a client
// asking for any host at all arrives here, and a 404 would tell the operator's
// browser the network is broken rather than that there is a page to see.
func (p *Portal) Handler() http.Handler {
	mux := http.NewServeMux()

	// Apple (iOS, macOS). CNA fetches captive.apple.com/hotspot-detect.html and
	// looks for exactly "Success"; anything else opens the captive sheet. A
	// redirect is followed and the destination is what the sheet displays.
	mux.HandleFunc("/hotspot-detect.html", p.redirectToPortal)
	mux.HandleFunc("/library/test/success.html", p.redirectToPortal)

	// Android and Chrome. A 204 with no body means "the internet is here"; a
	// redirect is the documented way to say otherwise.
	mux.HandleFunc("/generate_204", p.redirectToPortal)
	mux.HandleFunc("/gen_204", p.redirectToPortal)

	// Windows NCSI expects the literal "Microsoft Connect Test" / "Microsoft NCSI".
	mux.HandleFunc("/connecttest.txt", p.redirectToPortal)
	mux.HandleFunc("/ncsi.txt", p.redirectToPortal)

	// Firefox expects "success" from detectportal.firefox.com.
	mux.HandleFunc("/canonical.html", p.redirectToPortal)
	mux.HandleFunc("/success.txt", p.redirectToPortal)

	// RFC 8908, advertised by the DHCP option 114 the AP hands out. A client that
	// speaks it gets a structured answer instead of having to infer a portal from a
	// probe that came back wrong.
	mux.HandleFunc("/captive-portal", p.captiveJSON)

	mux.HandleFunc("/", p.serveWizard)
	return mux
}

// redirectToPortal answers a probe in the way that says "you are behind a
// portal".
func (p *Portal) redirectToPortal(w http.ResponseWriter, r *http.Request) {
	p.noteRequest()
	// 302 rather than 511 Network Authentication Required: every one of these
	// clients follows a redirect and shows the destination, and several of them do
	// nothing useful with a 511.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, p.PortalURL(), http.StatusFound)
}

// captiveJSON implements RFC 8908.
func (p *Portal) captiveJSON(w http.ResponseWriter, r *http.Request) {
	p.noteRequest()
	w.Header().Set("Content-Type", "application/captive+json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"captive":         true,
		"user-portal-url": p.PortalURL(),
	})
}

// serveWizard is the catch-all: the session holder gets the wizard, anyone else
// gets told who has it.
func (p *Portal) serveWizard(w http.ResponseWriter, r *http.Request) {
	p.noteRequest()
	if p.Lock == nil {
		p.Wizard.ServeHTTP(w, r)
		return
	}
	ok, holder := p.Lock.Admit(r.RemoteAddr)
	if !ok {
		p.logf("captive: refused %s; the setup session belongs to %s", r.RemoteAddr, holder.IP)
		writeBusyPage(w, holder, p.Lock.idle())
		return
	}
	p.Wizard.ServeHTTP(w, r)
}

func (p *Portal) noteRequest() {
	if p.OnRequest != nil {
		p.OnRequest()
	}
}

// writeBusyPage tells the second device what happened.
//
// It is a page rather than a bare 403 because the person holding that second
// device is usually the same operator, on their laptop, wondering why the wizard
// went blank — so it says who has the session and when it frees up, which is the
// difference between a diagnosable state and a broken one.
func writeBusyPage(w http.ResponseWriter, holder Holder, idle interface{ String() string }) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)

	from := "another device"
	if holder.IP != "" {
		from = html.EscapeString(holder.IP)
	}
	_, _ = fmt.Fprintf(w, `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Waypoint — setup in progress</title>
<style>
  body{font:16px/1.6 system-ui,sans-serif;max-width:32rem;margin:3rem auto;padding:0 1rem}
  h1{font-size:1.3rem}
  .note{background:#8881;border-radius:.4rem;padding:.75rem 1rem}
</style>
<h1>Setup is already in progress from another device</h1>
<p>This Waypoint is being set up from <strong>%s</strong>. Only one device can run
setup at a time, so that two people cannot half-configure the same node.</p>
<div class="note">
  <p>If that was you, go back to the device you started on.</p>
  <p>If that device is gone, this page will start working again after %s of
  inactivity — or power-cycle the node to start over.</p>
</div>
`, from, html.EscapeString(idle.String()))
}

// IsAPClient reports whether a remote address is on the AP segment.
//
// The portal's handlers are mounted only on the AP listener, so this is a
// belt-and-braces check for anything that mounts them more widely: a client from
// somewhere else must not be able to claim the setup session.
func IsAPClient(remoteAddr string, apPrefix netip.Prefix) bool {
	addr, err := netip.ParseAddr(hostOf(remoteAddr))
	if err != nil {
		return false
	}
	return apPrefix.Contains(addr)
}

// ProbePaths lists the endpoints answered on behalf of an operating system's
// connectivity check. It exists so a test can assert none of them is left to the
// catch-all by accident, and so the set is documented in one place.
func ProbePaths() []string {
	return []string{
		"/hotspot-detect.html",
		"/library/test/success.html",
		"/generate_204",
		"/gen_204",
		"/connecttest.txt",
		"/ncsi.txt",
		"/canonical.html",
		"/success.txt",
	}
}

// ProbeHostnames lists the hostnames those probes are fetched from. The DNS
// responder answers everything, so this is documentation and a test fixture
// rather than a filter.
func ProbeHostnames() []string {
	return []string{
		"captive.apple.com",
		"connectivitycheck.gstatic.com",
		"clients3.google.com",
		"www.msftconnecttest.com",
		"www.msftncsi.com",
		"detectportal.firefox.com",
	}
}

// HostMatchesProbe reports whether a Host header belongs to a known connectivity
// check, ignoring any port.
func HostMatchesProbe(host string) bool {
	h := strings.ToLower(hostOf(host))
	for _, known := range ProbeHostnames() {
		if h == known {
			return true
		}
	}
	return false
}
