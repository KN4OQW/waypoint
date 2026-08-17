// Command dmrdcapture turns a loopback packet capture into the burst fixture
// format the DMR testdata directories use.
//
// The bench box can run tcpdump, but a .pcap is not what the tests read. The
// committed fixtures are one burst per line —
//
//	# comment lines describing what this is and how it was taken
//	<data type from the DMRD flags byte> <33 bytes of burst, hex>
//
// — because that is diffable, reviewable, and says on its face which branch of
// MMDVM-Host each burst would take. See internal/dmrdata/testdata/ for the
// worked examples.
//
// Converting on a workstation rather than on the node is deliberate: the bench
// box captures and nothing else, so the only thing that has to work there is
// tcpdump. Everything that needs judgement happens where the result can be read
// before it is committed.
//
// Usage:
//
//	dmrdcapture -in sms.pcap -summary                 # what is in here?
//	dmrdcapture -in sms.pcap -from 62032 -out out.txt # one direction, as a fixture
//
// # Why the direction filter matters
//
// A loopback capture sees every frame twice on a node running the relay: once
// on the leg from the daemon, once on the leg to the other daemon. Without a
// port filter every burst appears in the fixture two or four times, which looks
// like a retransmission and is not. -from selects the UDP source port and is
// the normal way to pick a single leg.
package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/dmrdata"
)

func main() {
	in := flag.String("in", "", "pcap file to read (required)")
	out := flag.String("out", "", "write the fixture here (default stdout)")
	from := flag.Int("from", 0, "keep only datagrams with this UDP source port")
	to := flag.Int("to", 0, "keep only datagrams with this UDP destination port")
	summary := flag.Bool("summary", false, "describe each frame instead of emitting a fixture")
	decode := flag.Bool("decode", false, "reassemble the bursts into messages and report why any failed")
	header := flag.String("header", "", "comment line to write above the bursts")
	flag.Parse()

	if *in == "" {
		fmt.Fprintln(os.Stderr, "dmrdcapture: -in is required")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		fail(err)
	}
	frames, err := readPcap(raw)
	if err != nil {
		fail(err)
	}

	if *decode {
		decodeFrames(frames, *from, *to)
		return
	}

	var b strings.Builder
	if *header != "" {
		fmt.Fprintf(&b, "# %s\n", *header)
	}
	kept := 0
	for _, f := range frames {
		if *from != 0 && f.srcPort != *from {
			continue
		}
		if *to != 0 && f.dstPort != *to {
			continue
		}
		d, ok := parseDMRD(f.payload)
		if !ok {
			continue
		}
		kept++
		if *summary {
			fmt.Fprintf(&b, "%s %s\n", f, d)
			continue
		}
		fmt.Fprintf(&b, "%02x %s\n", d.dataType, hex.EncodeToString(d.burst))
	}
	if kept == 0 {
		fmt.Fprintln(os.Stderr, "dmrdcapture: no DMRD frames matched; check -from/-to against the capture")
	}

	if *out == "" {
		fmt.Print(b.String())
		return
	}
	if err := os.WriteFile(*out, []byte(b.String()), 0o644); err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "dmrdcapture: wrote %d frames to %s\n", kept, *out)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "dmrdcapture: %v\n", err)
	os.Exit(1)
}

// decodeFrames runs the captured bursts through the real reassembler and reports
// both what came out and what did not.
//
// The counters are the point, not the decoded text. A capture that yields no
// message has a reason, and the reason is the finding: Unsupported means the
// radio sent a format this codec declines — a confirmed-data header is exactly
// that (header.go, DPFConfirmedData) — while NoSync means the transmitter wrote
// only the BPTC bits, and BadCRC means the capture is lossy. Those lead to three
// completely different next steps, and guessing between them costs a trip to
// the bench.
func decodeFrames(frames []frame, from, to int) {
	r := &dmrdata.Reassembler{}
	seen := 0
	for _, f := range frames {
		if from != 0 && f.srcPort != from {
			continue
		}
		if to != 0 && f.dstPort != to {
			continue
		}
		d, ok := parseDMRD(f.payload)
		if !ok || d.voice {
			continue
		}
		seen++
		// The timestamp only drives reassembly expiry. Successive calls must
		// advance for the timeout to mean anything, but a capture is replayed far
		// faster than real time, so a fixed base plus the frame index is both
		// monotonic and far inside the window.
		msg, done := r.Feed(d.burst, replayClock.Add(time.Duration(seen)*60*time.Millisecond))
		if !done {
			continue
		}
		fmt.Printf("message %d -> %d group=%v dialect=%s\n  %q\n",
			msg.Src, msg.Dst, msg.Group, msg.Dialect, msg.Text)
	}
	fmt.Printf("\n%d data bursts fed\n", seen)
	fmt.Printf("stats: %+v\n", r.Stats)
	if r.Stats.Unsupported > 0 {
		fmt.Println("\nNOTE: 'Unsupported' means a well-formed PDU this codec declines.")
		fmt.Println("A confirmed-data header (DPF 0x03) lands here. If the capture was a")
		fmt.Println("text message and this is non-zero, the radio sent CONFIRMED data —")
		fmt.Println("which settles the D-F9 question and means intercept needs an ACK.")
	}
}

// replayClock is an arbitrary fixed base for replaying a capture. Any instant
// works; naming it says the value is not meaningful rather than leaving a
// reader to wonder what date it was chosen from.
var replayClock = time.Unix(1_700_000_000, 0).UTC()

// frame is one captured UDP datagram, reduced to the parts that identify it.
type frame struct {
	srcPort, dstPort int
	payload          []byte
}

func (f frame) String() string { return fmt.Sprintf("%5d->%-5d", f.srcPort, f.dstPort) }

// dmrd is the parts of a DMRD frame worth naming. The field layout is
// DMRNetwork.cpp:141-181 in the pinned MMDVM-Host: src at bytes 5-7, dst at
// 8-10, and byte 15 carrying slot, call type, sync and data type at once.
type dmrd struct {
	src, dst uint32
	slot     int
	private  bool
	voice    bool
	dataType byte
	burst    []byte
}

func (d dmrd) String() string {
	call := "group"
	if d.private {
		call = "private"
	}
	kind := fmt.Sprintf("data type %#x", d.dataType)
	if d.voice {
		kind = "voice"
	}
	return fmt.Sprintf("ts%d %-7s %7d -> %-7d %s", d.slot, call, d.src, d.dst, kind)
}

// parseDMRD reads a homebrew DMRD frame. It reports false for anything that is
// not one, so a capture containing the daemons' login chatter converts cleanly.
func parseDMRD(p []byte) (dmrd, bool) {
	// 53 rather than 55: the burst ends at byte 53 and the two trailing bytes
	// (BER, RSSI on some builds) are not needed to reproduce the frame.
	if len(p) < 53 || string(p[0:4]) != "DMRD" {
		return dmrd{}, false
	}
	d := dmrd{
		src:   uint32(p[5])<<16 | uint32(p[6])<<8 | uint32(p[7]),
		dst:   uint32(p[8])<<16 | uint32(p[9])<<8 | uint32(p[10]),
		slot:  1,
		burst: p[20:53],
	}
	if p[15]&0x80 != 0 {
		d.slot = 2
	}
	// Bit 6 SET means USER_USER, i.e. a private call. The polarity is the
	// inverse of the name and is an easy bug to write; DMRNetwork.cpp:157.
	d.private = p[15]&0x40 != 0
	if p[15]&0x20 == 0 {
		// No data sync: this is voice, and its low nibble is a frame counter
		// rather than a data type.
		d.voice = true
		return d, true
	}
	d.dataType = p[15] & 0x0F
	return d, true
}

// pcap link types this understands. EN10MB is what "tcpdump -i lo" produces;
// LINUX_SLL turns up when someone captures with "-i any" instead, and getting a
// silent empty result from that is a bad half hour.
const (
	linkEthernet = 1
	linkLinuxSLL = 113
)

// readPcap walks a classic libpcap file and returns the UDP datagrams in it.
//
// Only classic pcap is supported, not pcapng: tcpdump -w writes classic, and
// supporting a second container format nobody produces here would be code with
// no reader.
func readPcap(raw []byte) ([]frame, error) {
	if len(raw) < 24 {
		return nil, errors.New("not a pcap file: shorter than its own header")
	}
	var bo binary.ByteOrder
	switch binary.BigEndian.Uint32(raw[0:4]) {
	case 0xa1b2c3d4, 0xa1b23c4d: // microsecond and nanosecond, big endian
		bo = binary.BigEndian
	case 0xd4c3b2a1, 0x4d3cb2a1: // the same two, little endian
		bo = binary.LittleEndian
	default:
		return nil, fmt.Errorf("not a classic pcap file (magic %#x); pcapng is not supported", binary.BigEndian.Uint32(raw[0:4]))
	}
	link := bo.Uint32(raw[20:24])

	var out []frame
	for off := 24; off+16 <= len(raw); {
		inclLen := int(bo.Uint32(raw[off+8 : off+12]))
		off += 16
		if inclLen < 0 || off+inclLen > len(raw) {
			return out, io.ErrUnexpectedEOF
		}
		if f, ok := parseLink(raw[off:off+inclLen], link); ok {
			out = append(out, f)
		}
		off += inclLen
	}
	return out, nil
}

// parseLink peels the link, network and transport headers off one captured
// packet. Anything that is not IPv4 UDP is skipped rather than reported: a
// capture filter is a request, not a guarantee, and ARP in the file is not an
// error the operator can act on.
func parseLink(pkt []byte, link uint32) (frame, bool) {
	var off int
	switch link {
	case linkEthernet:
		if len(pkt) < 14 || binary.BigEndian.Uint16(pkt[12:14]) != 0x0800 {
			return frame{}, false
		}
		off = 14
	case linkLinuxSLL:
		if len(pkt) < 16 || binary.BigEndian.Uint16(pkt[14:16]) != 0x0800 {
			return frame{}, false
		}
		off = 16
	default:
		return frame{}, false
	}

	ip := pkt[off:]
	if len(ip) < 20 || ip[0]>>4 != 4 {
		return frame{}, false
	}
	ihl := int(ip[0]&0x0F) * 4
	if ihl < 20 || len(ip) < ihl || ip[9] != 17 { // 17 = UDP
		return frame{}, false
	}
	udp := ip[ihl:]
	if len(udp) < 8 {
		return frame{}, false
	}
	return frame{
		srcPort: int(binary.BigEndian.Uint16(udp[0:2])),
		dstPort: int(binary.BigEndian.Uint16(udp[2:4])),
		payload: udp[8:],
	}, true
}
