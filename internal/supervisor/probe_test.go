package supervisor

import (
	"context"
	"errors"
	"net"
	"testing"
)

func dmrAttachment() Attachment {
	return Attachment{Name: "BM", Kind: KindDMRMaster, Host: "master.example", Port: "62031"}
}

func dapnetAttachment() Attachment {
	return Attachment{Name: "dapnet", Kind: KindDAPNET, Host: "dapnet.example", Port: "43434"}
}

// A name that does not resolve is the #682 signature: the daemon resolved once at
// startup and will never try again, so the node has to notice on its behalf.
func TestProbeUnresolvableIsDefinitelyDown(t *testing.T) {
	p := &Prober{Resolve: func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("no such host")
	}}
	got, detail := p.Probe(context.Background(), dmrAttachment(), false)
	if got != TriNo {
		t.Errorf("got %v, want TriNo", got)
	}
	if detail == "" {
		t.Error("a failure should say what could not be resolved")
	}
}

// A lookup that succeeds but returns nothing is the same failure wearing a
// different hat, and the daemon would be just as stuck.
func TestProbeEmptyAnswerIsDown(t *testing.T) {
	p := &Prober{Resolve: func(context.Context, string) ([]net.IPAddr, error) {
		return nil, nil
	}}
	if got, _ := p.Probe(context.Background(), dmrAttachment(), false); got != TriNo {
		t.Errorf("got %v, want TriNo", got)
	}
}

// Resolving is NOT reachability. Claiming otherwise would let a DNS server report
// a dead master as healthy — and being unable to tell "fine" from "don't know" is
// what makes a supervisor restart daemons that were working.
func TestProbeResolvingIsNotReachability(t *testing.T) {
	p := &Prober{Resolve: func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
	}}
	if got, _ := p.Probe(context.Background(), dmrAttachment(), false); got != TriUnknown {
		t.Errorf("a successful lookup should be unknown, not healthy: got %v", got)
	}
}

// A DMR master is UDP: there is no connection to attempt, so even a deep probe
// stays at what the lookup proved and never dials.
func TestProbeNeverDialsAUDPMaster(t *testing.T) {
	dialed := false
	p := &Prober{
		Resolve: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
		},
		Dial: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("should not happen")
		},
	}
	if got, _ := p.Probe(context.Background(), dmrAttachment(), true); got != TriUnknown {
		t.Errorf("got %v, want TriUnknown", got)
	}
	if dialed {
		t.Error("dialled a UDP master — an unauthenticated login attempt against somebody else's server")
	}
}

// DAPNET is TCP, so a deep probe is real evidence in both directions.
func TestProbeDeepTCP(t *testing.T) {
	resolve := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
	}

	client, server := net.Pipe()
	defer server.Close()
	up := &Prober{Resolve: resolve, Dial: func(context.Context, string, string) (net.Conn, error) {
		return client, nil
	}}
	if got, _ := up.Probe(context.Background(), dapnetAttachment(), true); got != TriYes {
		t.Errorf("a successful connect should be TriYes, got %v", got)
	}
	// The probe must hang up immediately; a probe that lingers is a session.
	if _, err := client.Write([]byte("x")); err == nil {
		t.Error("the probe left its connection open")
	}

	down := &Prober{Resolve: resolve, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}}
	if got, _ := down.Probe(context.Background(), dapnetAttachment(), true); got != TriNo {
		t.Errorf("a refused connect should be TriNo, got %v", got)
	}
}

// A shallow probe never dials, whatever the kind — the restraint that keeps a
// fleet of nodes off somebody's server on a timer.
func TestProbeShallowNeverDials(t *testing.T) {
	dialed := false
	p := &Prober{
		Resolve: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
		},
		Dial: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, nil
		},
	}
	p.Probe(context.Background(), dapnetAttachment(), false)
	if dialed {
		t.Error("a shallow probe opened a connection")
	}
}

// An address literal cannot fail to resolve, so the lookup is skipped rather than
// asked a question with only one answer.
func TestProbeSkipsLookupForALiteral(t *testing.T) {
	resolved := false
	p := &Prober{Resolve: func(context.Context, string) ([]net.IPAddr, error) {
		resolved = true
		return nil, errors.New("should not be called")
	}}
	a := dmrAttachment()
	a.Host = "192.0.2.10"
	if got, _ := p.Probe(context.Background(), a, false); got != TriUnknown {
		t.Errorf("got %v, want TriUnknown", got)
	}
	if resolved {
		t.Error("resolved an address literal")
	}
}
