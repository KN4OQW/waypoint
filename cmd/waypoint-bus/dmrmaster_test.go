package main

import (
	"bytes"
	"net"
	"testing"
)

func fixedSalt(b [4]byte) func() [4]byte { return func() [4]byte { return b } }

func addr(port int) *net.UDPAddr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
}

// TestDMRMasterLoginFlow walks the exact Homebrew client sequence DMRGateway
// drives and asserts the master answers each step so the client reaches RUNNING.
func TestDMRMasterLoginFlow(t *testing.T) {
	salt := [4]byte{0xDE, 0xAD, 0xBE, 0xEF}
	d := &dmrMaster{saltFn: fixedSalt(salt)}
	client := addr(59654)
	id := []byte{0x00, 0x30, 0x85, 0x9A} // 4-byte repeater id

	// RPTL -> RPTACK + salt
	reply, forward := d.handle(append([]byte("RPTL"), id...), client)
	if forward {
		t.Fatal("RPTL must not be forwarded to the router")
	}
	if want := append([]byte("RPTACK"), salt[:]...); !bytes.Equal(reply, want) {
		t.Fatalf("RPTL reply = %x, want %x", reply, want)
	}

	// RPTK (id + 32-byte digest) -> RPTACK + id ; login now complete
	rptk := append(append([]byte("RPTK"), id...), make([]byte, 32)...)
	reply, forward = d.handle(rptk, client)
	if forward {
		t.Fatal("RPTK must not be forwarded")
	}
	if want := append([]byte("RPTACK"), id...); !bytes.Equal(reply, want) {
		t.Fatalf("RPTK reply = %x, want %x", reply, want)
	}
	if !d.authed {
		t.Fatal("expected authed after RPTK")
	}

	// RPTC config -> RPTACK + id ; client goes RUNNING
	reply, _ = d.handle(append(append([]byte("RPTC"), id...), []byte("config...")...), client)
	if want := append([]byte("RPTACK"), id...); !bytes.Equal(reply, want) {
		t.Fatalf("RPTC reply = %x, want %x", reply, want)
	}

	// The client address is learned for the reverse path.
	if got := d.clientAddr(); got == nil || got.Port != 59654 {
		t.Fatalf("clientAddr = %v, want 127.0.0.1:59654", got)
	}
}

func TestDMRMasterPingPong(t *testing.T) {
	d := newDMRMaster()
	id := []byte{1, 2, 3, 4}
	d.handle(append([]byte("RPTL"), id...), addr(40000)) // learn id
	reply, forward := d.handle(append([]byte("RPTPING"), id...), addr(40000))
	if forward {
		t.Fatal("RPTPING must not be forwarded")
	}
	if want := append([]byte("MSTPONG"), id...); !bytes.Equal(reply, want) {
		t.Fatalf("ping reply = %x, want %x", reply, want)
	}
}

// TestDMRMasterForwardsDMRD asserts only DMRD is passed to the router, and that a
// DMRD sender is (re)learned as the reverse-path client.
func TestDMRMasterForwardsDMRD(t *testing.T) {
	d := newDMRMaster()
	dmrd := append([]byte("DMRD"), make([]byte, 51)...) // 55-byte homebrew voice frame
	reply, forward := d.handle(dmrd, addr(59654))
	if reply != nil {
		t.Fatalf("DMRD must not be answered inline, got %x", reply)
	}
	if !forward {
		t.Fatal("DMRD must be forwarded to the router")
	}
	if got := d.clientAddr(); got == nil || got.Port != 59654 {
		t.Fatalf("DMRD should learn the client, got %v", got)
	}
}

// TestDMRMasterCloseForgetsClient asserts RPTCL tears down the reverse path so the
// bus stops emitting toward a gone client.
func TestDMRMasterCloseForgetsClient(t *testing.T) {
	d := newDMRMaster()
	id := []byte{9, 9, 9, 9}
	d.handle(append([]byte("RPTL"), id...), addr(59654))
	if d.clientAddr() == nil {
		t.Fatal("precondition: client should be set after RPTL")
	}
	reply, forward := d.handle(append([]byte("RPTCL"), id...), addr(59654))
	if reply != nil || forward {
		t.Fatalf("RPTCL yields no reply/forward, got reply=%x forward=%v", reply, forward)
	}
	if d.clientAddr() != nil {
		t.Fatal("RPTCL must forget the client")
	}
}

// TestDMRMasterTagPrecedence guards the longest-first tag matching: RPTCL is not
// mistaken for an RPTC config, and RPTPING is not mistaken for a bare ping tag.
func TestDMRMasterTagPrecedence(t *testing.T) {
	d := newDMRMaster()
	id := []byte{1, 1, 1, 1}
	d.handle(append([]byte("RPTL"), id...), addr(1))
	// RPTCL must reset (not ACK like RPTC would).
	if reply, _ := d.handle(append([]byte("RPTCL"), id...), addr(1)); reply != nil {
		t.Fatalf("RPTCL misrouted to RPTC (got ACK %x)", reply)
	}
	// A junk/unknown command is ignored, never forwarded.
	if reply, forward := d.handle([]byte("HELLO there"), addr(1)); reply != nil || forward {
		t.Fatalf("unknown command must be ignored, got reply=%x forward=%v", reply, forward)
	}
}
