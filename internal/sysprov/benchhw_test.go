//go:build benchhw

// Hardware validation on a real Raspberry Pi with a Broadcom radio.
//
// Everything else in this workstream is tested against a fake system or a Debian
// container. Neither has a radio, and the setup access point is the one part of
// the flow whose behaviour is decided by firmware: brcmfmac's AP mode, its
// channel and band handling, and how it sequences a station association after an
// access point has been torn down. This file is the only place those are
// exercised.
//
// It is destructive to the node's wireless configuration and is gated behind a
// build tag and an environment variable. Run it with testdata/bench/run.sh, which
// refuses to touch a host whose SSH session is on the wireless interface it is
// about to reconfigure.
//
//	WAYPOINT_BENCH_HW=1 ./sysprov.benchhw -test.v -test.run TestBench

package sysprov_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/captive"
	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/setupap"
	"github.com/KN4OQW/waypoint/internal/sysprov"
	"github.com/miekg/dns"
)

func benchGuard(t *testing.T) {
	t.Helper()
	if os.Getenv("WAYPOINT_BENCH_HW") != "1" {
		t.Skip("set WAYPOINT_BENCH_HW=1 to run the destructive hardware bench")
	}
	if os.Geteuid() != 0 {
		t.Skip("the hardware bench needs root")
	}
}

func sh(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Logf("    (%s %s -> %v)", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// step logs a heading so the captured output reads as a run log rather than a
// wall of assertions.
func step(t *testing.T, n int, what string) {
	t.Helper()
	t.Logf("---------- STEP %d: %s ----------", n, what)
}

func newSystem(t *testing.T) *sysprov.System {
	t.Helper()
	s := sysprov.New()
	s.Logf = func(f string, a ...any) { t.Logf("    helper: "+f, a...) }
	return s
}

// wlanIface is the interface under test.
const wlanIface = "wlan0"

// TestBenchAccessPoint drives the access point through its whole life on the real
// radio, recording what the firmware actually did at each stage.
func TestBenchAccessPoint(t *testing.T) {
	benchGuard(t)
	ctx := context.Background()
	s := newSystem(t)

	step(t, 0, "hardware identity")
	serial := setupap.Serial(setupap.CPUInfoPath)
	ssid := setupap.SSID(setupap.CPUInfoPath)
	t.Logf("    model:    %s", strings.TrimRight(readOrEmpty("/proc/device-tree/model"), "\x00"))
	t.Logf("    serial:   %s", serial)
	t.Logf("    SSID:     %s", ssid)
	t.Logf("    chip/fw:  %s", firmwareLine())
	t.Logf("    NM:       %s", sh(t, "nmcli", "--version"))
	if ssid == setupap.SSIDPrefix+setupap.FallbackSuffix {
		t.Errorf("the SSID fell back to %s — the serial was not readable on real hardware", ssid)
	}

	step(t, 1, "APUp on the real radio")
	start := time.Now()
	up, err := s.APUp(ctx, privhelper.APUpRequest{
		SSID:      ssid,
		Interface: wlanIface,
		Country:   os.Getenv("WAYPOINT_BENCH_COUNTRY"),
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("APUp failed after %s: %v", elapsed, err)
	}
	t.Logf("    APUp took %s -> %+v", elapsed.Round(time.Millisecond), up)
	defer func() {
		if _, err := s.APDown(ctx, privhelper.APDownRequest{Interface: wlanIface}); err != nil {
			t.Logf("    cleanup APDown: %v", err)
		}
	}()

	step(t, 2, "what the firmware actually did")
	info := sh(t, "iw", "dev", wlanIface, "info")
	t.Logf("    iw dev %s info:\n%s", wlanIface, indent(info))
	if !strings.Contains(info, "type AP") {
		t.Errorf("the interface is not in AP mode:\n%s", info)
	}
	// The channel the firmware settled on, which is not necessarily the one asked
	// for: brcmfmac follows the regulatory domain and any active station link.
	for _, line := range strings.Split(info, "\n") {
		if strings.Contains(line, "channel") {
			t.Logf("    NEGOTIATED %s", strings.TrimSpace(line))
		}
	}
	t.Logf("    addr:  %s", sh(t, "ip", "-brief", "-4", "addr", "show", wlanIface))
	t.Logf("    regdom: %s", firstLine(sh(t, "iw", "reg", "get")))

	step(t, 3, "the AP profile and its DHCP drop-in")
	t.Logf("    active: %s", sh(t, "nmcli", "-t", "-f", "NAME,DEVICE,STATE", "connection", "show", "--active"))
	if body := readOrEmpty("/etc/NetworkManager/dnsmasq-shared.d/waypoint-setup-ap.conf"); body == "" {
		t.Error("the dnsmasq drop-in was not written")
	} else {
		t.Logf("    dnsmasq-shared.d drop-in:\n%s", indent(body))
		for _, want := range []string{"port=0", "dhcp-option=114"} {
			if !strings.Contains(body, want) {
				t.Errorf("the drop-in is missing %q", want)
			}
		}
	}
	// Whether NM's dnsmasq actually honoured port=0 is the question that decides
	// whether our DNS responder can bind 53 at all.
	t.Logf("    listeners on :53: %s", sh(t, "ss", "-lunp", "sport = :53"))

	step(t, 4, "the AP segment is not routable")
	fwd := sh(t, "sysctl", "-n", "net.ipv4.conf."+wlanIface+".forwarding")
	t.Logf("    net.ipv4.conf.%s.forwarding = %s", wlanIface, fwd)
	if fwd != "0" {
		t.Errorf("forwarding is %q on the AP interface; the setup AP would route traffic", fwd)
	}

	step(t, 5, "captive portal on the AP address")
	benchCaptive(t)

	step(t, 6, "APDown")
	down, err := s.APDown(ctx, privhelper.APDownRequest{Interface: wlanIface})
	if err != nil {
		t.Fatalf("APDown: %v", err)
	}
	t.Logf("    APDown -> %+v", down)
	after := sh(t, "iw", "dev", wlanIface, "info")
	if strings.Contains(after, "type AP") {
		t.Errorf("the interface is still in AP mode after APDown:\n%s", after)
	}
	t.Logf("    iw dev %s info after:\n%s", wlanIface, indent(after))
}

// benchCaptive checks the DNS responder and the portal can actually bind and
// answer on the AP address, which is where every port-conflict problem shows up.
func benchCaptive(t *testing.T) {
	t.Helper()
	addr := captive.DefaultAddress.String()

	resolver := &captive.DNSResponder{Logf: func(f string, a ...any) { t.Logf("    dns: "+f, a...) }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- resolver.ListenAndServe(ctx, net.JoinHostPort(addr, "53")) }()
	defer resolver.Shutdown()
	time.Sleep(300 * time.Millisecond)

	select {
	case err := <-errCh:
		t.Errorf("the DNS responder could not bind %s:53 — something else holds it: %v", addr, err)
	default:
		t.Logf("    DNS responder bound %s:53", addr)
		// Queried in-process rather than by shelling to dig: the image does not
		// ship dig, and a missing tool produced an empty answer that read exactly
		// like a broken responder. A check that cannot tell "no answer" from "no
		// dig" is worse than no check.
		m := new(dns.Msg)
		m.SetQuestion("captive.apple.com.", dns.TypeA)
		resp, _, qerr := (&dns.Client{Timeout: 3 * time.Second}).Exchange(m, net.JoinHostPort(addr, "53"))
		if qerr != nil {
			t.Errorf("the hijack did not answer: %v", qerr)
		} else if len(resp.Answer) == 0 {
			t.Errorf("the hijack returned no records: %s", resp)
		} else {
			got := resp.Answer[0].(*dns.A).A.String()
			t.Logf("    captive.apple.com -> %s", got)
			if got != addr {
				t.Errorf("the hijack answered %s, want %s", got, addr)
			}
		}
	}

	// The portal itself is served over plain HTTP; httptest gives it a port
	// without needing to bind 80 while the real daemon may hold it.
	portal := &captive.Portal{Wizard: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("WIZARD"))
	})}
	srv := httptest.NewServer(portal.Handler())
	defer srv.Close()
	resp, err := (&http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}).Get(srv.URL + "/generate_204")
	if err != nil {
		t.Errorf("probe request: %v", err)
		return
	}
	defer resp.Body.Close()
	t.Logf("    GET /generate_204 -> %d, Location: %s", resp.StatusCode, resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound {
		t.Errorf("the Android probe got %d, want 302", resp.StatusCode)
	}
}

// TestBenchJoinRevert is the confirm-or-revert path on real hardware: a real
// SSID, a deliberately wrong passphrase, and the access point expected back
// afterwards.
//
// The wrong passphrase is the point. It needs no credentials, and it exercises
// the failure path that actually matters — the operator who mistypes their Wi-Fi
// password on a phone connected to the setup AP has to end up where they started.
func TestBenchJoinRevert(t *testing.T) {
	benchGuard(t)
	ssid := os.Getenv("WAYPOINT_BENCH_SSID")
	if ssid == "" {
		t.Skip("set WAYPOINT_BENCH_SSID to an in-range network for the revert test")
	}
	ctx := context.Background()
	s := newSystem(t)
	apSSID := setupap.SSID(setupap.CPUInfoPath)

	step(t, 1, "raise the AP the operator is connected over")
	if _, err := s.APUp(ctx, privhelper.APUpRequest{SSID: apSSID, Interface: wlanIface}); err != nil {
		t.Fatalf("APUp: %v", err)
	}
	defer func() { _, _ = s.APDown(ctx, privhelper.APDownRequest{Interface: wlanIface}) }()

	step(t, 2, "checkpoint, hand the radio over, join with a wrong passphrase")
	cp, err := s.NetCheckpointCreate(ctx, privhelper.NetCheckpointCreateRequest{TimeoutSeconds: 120})
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if _, err := s.APDown(ctx, privhelper.APDownRequest{Interface: wlanIface}); err != nil {
		t.Fatalf("APDown for the join: %v", err)
	}

	start := time.Now()
	got, joinErr := s.NetJoin(ctx, privhelper.NetJoinRequest{
		SSID:           ssid,
		PSK:            "definitely-not-the-right-passphrase",
		Interface:      wlanIface,
		TimeoutSeconds: 30,
	})
	elapsed := time.Since(start)
	t.Logf("    NetJoin took %s -> connected=%v ipv4=%q err=%v",
		elapsed.Round(time.Second), got.Connected, got.IPv4, joinErr)
	if joinErr == nil && got.Connected && got.IPv4 != "" {
		t.Fatalf("a wrong passphrase produced a working join — check %q is really protected", ssid)
	}
	if elapsed > 90*time.Second {
		t.Errorf("the failed join took %s; the operator is staring at a dead AP for that long", elapsed)
	}

	step(t, 3, "roll back and bring the AP back")
	if _, err := s.NetCheckpointRollback(ctx, privhelper.NetCheckpointRollbackRequest{Handle: cp.Handle}); err != nil {
		t.Errorf("rollback: %v", err)
	}
	back := time.Now()
	if _, err := s.APUp(ctx, privhelper.APUpRequest{SSID: apSSID, Interface: wlanIface}); err != nil {
		t.Fatalf("the AP did not come back after a failed join: %v", err)
	}
	t.Logf("    AP back in %s", time.Since(back).Round(time.Millisecond))
	info := sh(t, "iw", "dev", wlanIface, "info")
	if !strings.Contains(info, "type AP") {
		t.Errorf("the interface is not in AP mode again:\n%s", info)
	}
	t.Logf("    total operator-visible outage: %s", time.Since(start).Round(time.Second))
}

func readOrEmpty(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func firmwareLine() string {
	out, err := exec.Command("sh", "-c", "dmesg 2>/dev/null | grep -m1 'Firmware: BCM'").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func indent(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		fmt.Fprintf(&b, "      %s\n", l)
	}
	return strings.TrimRight(b.String(), "\n")
}
