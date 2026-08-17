package main

import (
	"encoding/binary"
	"testing"
)

// buildPcap wraps datagrams as a classic little-endian pcap over Ethernet, which
// is what "tcpdump -i lo -w" produces on the bench box.
func buildPcap(t *testing.T, dgrams []testDatagram) []byte {
	t.Helper()
	out := make([]byte, 24)
	binary.LittleEndian.PutUint32(out[0:4], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(out[4:6], 2)
	binary.LittleEndian.PutUint16(out[6:8], 4)
	binary.LittleEndian.PutUint32(out[16:20], 262144)
	binary.LittleEndian.PutUint32(out[20:24], linkEthernet)

	for _, d := range dgrams {
		pkt := make([]byte, 14+20+8+len(d.payload))
		binary.BigEndian.PutUint16(pkt[12:14], 0x0800) // IPv4
		pkt[14] = 0x45                                 // v4, IHL 5
		pkt[14+9] = 17                                 // UDP
		binary.BigEndian.PutUint16(pkt[34:36], uint16(d.srcPort))
		binary.BigEndian.PutUint16(pkt[36:38], uint16(d.dstPort))
		copy(pkt[42:], d.payload)

		hdr := make([]byte, 16)
		binary.LittleEndian.PutUint32(hdr[8:12], uint32(len(pkt)))
		binary.LittleEndian.PutUint32(hdr[12:16], uint32(len(pkt)))
		out = append(out, hdr...)
		out = append(out, pkt...)
	}
	return out
}

type testDatagram struct {
	srcPort, dstPort int
	payload          []byte
}

// dmrdFrame builds a 55-byte DMRD frame with a recognisable burst.
func dmrdFrame(src, dst uint32, flags byte, fill byte) []byte {
	f := make([]byte, 55)
	copy(f, "DMRD")
	f[5], f[6], f[7] = byte(src>>16), byte(src>>8), byte(src)
	f[8], f[9], f[10] = byte(dst>>16), byte(dst>>8), byte(dst)
	f[15] = flags
	for i := 20; i < 53; i++ {
		f[i] = fill
	}
	return f
}

func TestReadPcapExtractsUDPPorts(t *testing.T) {
	raw := buildPcap(t, []testDatagram{
		{62032, 62033, dmrdFrame(3180202, 9000001, 0xa6, 0x11)},
		{62034, 62031, dmrdFrame(3180202, 9000001, 0xa6, 0x11)},
	})
	frames, err := readPcap(raw)
	if err != nil {
		t.Fatalf("readPcap: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2", len(frames))
	}
	if frames[0].srcPort != 62032 || frames[0].dstPort != 62033 {
		t.Errorf("first frame ports = %d->%d, want 62032->62033", frames[0].srcPort, frames[0].dstPort)
	}
}

// A capture on a node running the relay sees each burst on both legs. The port
// filter is the only thing standing between that and a fixture full of
// duplicates that read as retransmissions.
func TestPortFilterSeparatesTheLegs(t *testing.T) {
	raw := buildPcap(t, []testDatagram{
		{62032, 62033, dmrdFrame(3180202, 9000001, 0xa6, 0x11)},
		{62034, 62031, dmrdFrame(3180202, 9000001, 0xa6, 0x11)},
	})
	frames, err := readPcap(raw)
	if err != nil {
		t.Fatalf("readPcap: %v", err)
	}
	kept := 0
	for _, f := range frames {
		if f.srcPort == 62032 {
			kept++
		}
	}
	if kept != 1 {
		t.Errorf("kept %d frames for the MMDVM-Host leg, want 1", kept)
	}
}

func TestParseDMRDFields(t *testing.T) {
	tests := []struct {
		name     string
		flags    byte
		wantSlot int
		wantPriv bool
		wantVox  bool
		wantType byte
	}{
		// 0x20 = data sync, low nibble = data type.
		{"group data header ts1", 0x26, 1, false, false, 0x06},
		// 0x40 set is USER_USER — private. The inverse of what the name suggests.
		{"private rate-1/2 ts2", 0xe7, 2, true, false, 0x07},
		{"group voice ts1", 0x00, 1, false, true, 0x00},
		{"private voice sync ts2", 0xd0, 2, true, true, 0x00},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := parseDMRD(dmrdFrame(3180202, 9990, tc.flags, 0x22))
			if !ok {
				t.Fatal("parseDMRD rejected a well-formed frame")
			}
			if d.slot != tc.wantSlot {
				t.Errorf("slot = %d, want %d", d.slot, tc.wantSlot)
			}
			if d.private != tc.wantPriv {
				t.Errorf("private = %v, want %v", d.private, tc.wantPriv)
			}
			if d.voice != tc.wantVox {
				t.Errorf("voice = %v, want %v", d.voice, tc.wantVox)
			}
			if !tc.wantVox && d.dataType != tc.wantType {
				t.Errorf("dataType = %#x, want %#x", d.dataType, tc.wantType)
			}
			if d.src != 3180202 || d.dst != 9990 {
				t.Errorf("addressing = %d->%d, want 3180202->9990", d.src, d.dst)
			}
			if len(d.burst) != 33 {
				t.Errorf("burst = %d bytes, want 33", len(d.burst))
			}
		})
	}
}

// The daemons exchange login and ping traffic on the same ports. A fixture run
// must skip it rather than emit garbage lines, so the conversion of a capture
// taken across a gateway restart still produces something committable.
func TestNonDMRDPayloadsAreSkipped(t *testing.T) {
	for _, p := range [][]byte{
		[]byte("RPTL\x00\x30\x84\xaa"),
		[]byte("DMRD"), // truncated
		{},
	} {
		if _, ok := parseDMRD(p); ok {
			t.Errorf("parseDMRD accepted %q, want it skipped", p)
		}
	}
}

func TestRejectsPcapng(t *testing.T) {
	// pcapng's Section Header Block magic, which tcpdump writes only if asked.
	raw := make([]byte, 32)
	binary.BigEndian.PutUint32(raw[0:4], 0x0a0d0d0a)
	if _, err := readPcap(raw); err == nil {
		t.Fatal("readPcap accepted a pcapng file; it must say so rather than return nothing")
	}
}
