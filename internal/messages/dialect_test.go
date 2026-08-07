package messages

import (
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/dmrdata"
	"github.com/KN4OQW/waypoint/internal/dmrshim"
	"github.com/KN4OQW/waypoint/internal/events"
)

// airDialect is air() with the dialect chosen, so a test can put a radio of
// either kind on the channel.
func airDialect(t *testing.T, src, dst uint32, text string, d dmrdata.Dialect) [][]byte {
	t.Helper()
	bursts, err := dmrdata.BuildMessage(dmrdata.SendOptions{
		Src: src, Dst: dst, Text: text, Preambles: 2, ColorCode: 1, Duplex: true, Dialect: d,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	out := make([][]byte, 0, len(bursts))
	for i, b := range bursts {
		f := make([]byte, 55)
		copy(f, "DMRD")
		f[4] = byte(i)
		putU24(f[5:], src)
		putU24(f[8:], dst)
		f[15] = 0x80 | 0x20 | 0x40 | byte(b.DataType)&0x0F
		putU32(f[16:], 0x11223344)
		copy(f[20:53], b.Payload[:])
		out = append(out, f)
	}
	return out
}

// A radio's dialect is recorded when it transmits, both kinds.
func TestCaptureRecordsTheDialect(t *testing.T) {
	for _, d := range []dmrdata.Dialect{dmrdata.DialectTMS, dmrdata.DialectETSI} {
		t.Run(string(d), func(t *testing.T) {
			h := captureHarness(t)
			h.feed(airDialect(t, 3180299, 3180202, "hello", d), dmrshim.ToGateway)
			got := h.waitMessages(t, 1)
			if got[0].Dialect != string(d) {
				t.Errorf("dialect = %q, want %q", got[0].Dialect, d)
			}
		})
	}
}

// And a reply goes out in whatever that radio last spoke. This is the whole point:
// a fleet with both kinds of radio on one frequency works with no configuration,
// because each radio announces its format every time it transmits.
func TestAReplyUsesTheDialectTheRadioSpoke(t *testing.T) {
	for _, d := range []dmrdata.Dialect{dmrdata.DialectTMS, dmrdata.DialectETSI} {
		t.Run(string(d), func(t *testing.T) {
			h := captureHarness(t)
			h.run(t) // the capture harness watches; this test also needs to send
			h.feed(airDialect(t, 3180299, 3180202, "ping", d), dmrshim.ToGateway)
			h.waitMessages(t, 1)

			m, err := h.svc.Send(3180299, "pong", false)
			if err != nil {
				t.Fatal(err)
			}
			if m.Dialect != string(d) {
				t.Fatalf("reply dialect = %q, want %q", m.Dialect, d)
			}
			// And the bytes really are that dialect, not just the label: the
			// destination port is what the radio keys off.
			sent := h.waitState(t, m.ID, events.StateSent)
			_ = sent
			body := bodyFromFrames(t, h.relay.frames())
			wantPort := map[dmrdata.Dialect]uint16{dmrdata.DialectTMS: 4007, dmrdata.DialectETSI: 5016}[d]
			if got := uint16(body[22])<<8 | uint16(body[23]); got != wantPort {
				t.Errorf("destination port on the wire = %d, want %d", got, wantPort)
			}
		})
	}
}

// A peer nobody has heard from gets the default, which must be the dialect proven
// on air rather than the one only reproduced from a capture.
func TestAnUnknownPeerGetsTheProvenDialect(t *testing.T) {
	h := newHarness(t, defaultWiring())
	m, err := h.svc.Send(3180299, "first contact", false)
	if err != nil {
		t.Fatal(err)
	}
	if m.Dialect != "" {
		t.Errorf("dialect = %q for a peer never heard from, want empty (the default)", m.Dialect)
	}
	// Empty means BuildMessage's default, and that default is TMS.
	body, _, _, err := dmrdataBuildBodyDefault()
	if err != nil {
		t.Fatal(err)
	}
	if port := uint16(body[22])<<8 | uint16(body[23]); port != 4007 {
		t.Errorf("the default dialect sends to port %d, want 4007 (TMS, the proven one)", port)
	}
}

// The newest inbound wins, so a radio whose channel setting changes is followed
// rather than remembered wrongly forever.
func TestTheMostRecentDialectWins(t *testing.T) {
	h := captureHarness(t)
	h.feed(airDialect(t, 3180299, 3180202, "as etsi", dmrdata.DialectETSI), dmrshim.ToGateway)
	h.waitMessages(t, 1)
	time.Sleep(2 * time.Millisecond) // distinct created_ms
	h.feed(airDialect(t, 3180299, 3180202, "now as tms", dmrdata.DialectTMS), dmrshim.ToGateway)
	h.waitMessages(t, 2)

	got, err := h.store.PeerDialect(3180299)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(dmrdata.DialectTMS) {
		t.Errorf("dialect = %q, want the most recent (%q)", got, dmrdata.DialectTMS)
	}
}

// One peer's dialect must not be read for another: that is the mixed-fleet case.
func TestDialectIsPerPeer(t *testing.T) {
	h := captureHarness(t)
	h.feed(airDialect(t, 3180291, 3180202, "etsi radio", dmrdata.DialectETSI), dmrshim.ToGateway)
	h.feed(airDialect(t, 3180292, 3180202, "tms radio", dmrdata.DialectTMS), dmrshim.ToGateway)
	h.waitMessages(t, 2)

	for peer, want := range map[uint32]dmrdata.Dialect{
		3180291: dmrdata.DialectETSI,
		3180292: dmrdata.DialectTMS,
		3180299: "", // never heard from
	} {
		got, err := h.store.PeerDialect(peer)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("peer %d: dialect = %q, want %q", peer, got, want)
		}
	}
}

// bodyFromFrames reassembles the rate-1/2 payload out of injected DMRD frames.
func bodyFromFrames(t *testing.T, frames [][]byte) []byte {
	t.Helper()
	var body []byte
	for _, f := range frames {
		payload, dt, _, _, _, err := dmrdata.ParseBurst(f[20:53])
		if err != nil {
			t.Fatal(err)
		}
		if dt == dmrdata.DataTypeRate12 {
			body = append(body, payload...)
		}
	}
	if len(body) < 24 {
		t.Fatalf("only %d octets of body", len(body))
	}
	return body
}

// dmrdataBuildBodyDefault builds with no dialect set, which is what an unknown
// peer gets.
func dmrdataBuildBodyDefault() ([]byte, int, int, error) {
	bursts, err := dmrdata.BuildMessage(dmrdata.SendOptions{Src: 1, Dst: 2, Text: "x", Preambles: 1})
	if err != nil {
		return nil, 0, 0, err
	}
	var body []byte
	for _, b := range bursts {
		if b.DataType == dmrdata.DataTypeRate12 {
			payload, _, _, _, _, err := dmrdata.ParseBurst(b.Payload[:])
			if err != nil {
				return nil, 0, 0, err
			}
			body = append(body, payload...)
		}
	}
	return body, 0, 0, nil
}
