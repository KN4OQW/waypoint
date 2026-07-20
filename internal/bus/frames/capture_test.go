package frames

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRealCaptureDMRParrot validates the frame layer against a REAL bench-Pi
// loopback capture (RFC-0003 §6, Task 4) rather than a synthetic fixture. The
// bytes in testdata/capture/dmr_parrot_9990.bin are a keyed-up DMR transmission
// pulled off the WPSD stack's MMDVM-Host -> DMRGateway loopback (127.0.0.1:62032
// -> :62031) with tcpdump: the modem decoded live RF into the "DMRD" wire frames
// this parser is written against. See testdata/capture/README.md for provenance.
//
// This is the ground-truth check the synthetic golden fixture cannot give: it
// proves ParseDMR accepts frames a real MMDVM-Host actually emits (header, voice,
// terminator, real 24-bit ids and stream id), and that the reframe is byte-exact
// on REAL AMBE+2 codec bits — not just the random codewords the other tests use.
func TestRealCaptureDMRParrot(t *testing.T) {
	path := filepath.Join("testdata", "capture", "dmr_parrot_9990.bin")
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) == 0 || len(blob)%dmrdLen != 0 {
		t.Fatalf("capture is not a whole number of %d-byte DMRD frames: %d bytes", dmrdLen, len(blob))
	}

	const (
		wantSrc    = 3180202 // KN4OQW's DMR id (RadioID), as it rode the real header
		wantDst    = 9990    // BrandMeister Parrot/Echo (private call)
		wantVoice  = 20      // 20 voice frames -> 60 codewords (LCM of 3,4,5: reframes with no padding)
		wantHeader = 1
		wantTerm   = 1
	)

	var voiceCWs [][]byte
	var nHeader, nVoice, nTerm int
	var streamID uint32
	haveStream := false

	for off := 0; off+dmrdLen <= len(blob); off += dmrdLen {
		f, err := ParseDMR(blob[off : off+dmrdLen])
		if err != nil {
			t.Fatalf("real frame at offset %d failed to parse: %v", off, err)
		}
		if f.Mode != ModeDMR {
			t.Fatalf("frame at %d: mode %v, want DMR", off, f.Mode)
		}
		if f.SrcID != wantSrc || f.DstID != wantDst {
			t.Fatalf("frame at %d: addressing src=%d dst=%d, want %d/%d",
				off, f.SrcID, f.DstID, wantSrc, wantDst)
		}
		if !haveStream {
			streamID, haveStream = f.Stream.ID, true
		} else if f.Stream.ID != streamID {
			t.Fatalf("frame at %d: stream id %08x, want a single stream %08x", off, f.Stream.ID, streamID)
		}

		switch f.Kind {
		case KindHeader:
			nHeader++
			if len(f.AMBE) != 0 {
				t.Fatalf("voice header at %d carried AMBE", off)
			}
		case KindTerminator:
			nTerm++
			if len(f.AMBE) != 0 {
				t.Fatalf("terminator at %d carried AMBE", off)
			}
		case KindVoice:
			nVoice++
			if len(f.AMBE) != dmrAMBEPerFrm {
				t.Fatalf("voice frame at %d carried %d codewords, want %d", off, len(f.AMBE), dmrAMBEPerFrm)
			}
			for i, cw := range f.AMBE {
				if len(cw) != AMBEBytes {
					t.Fatalf("voice frame at %d codeword %d is %d bytes, want %d", off, i, len(cw), AMBEBytes)
				}
			}
			voiceCWs = append(voiceCWs, f.AMBE...)
		default:
			t.Fatalf("frame at %d has unexpected kind %v", off, f.Kind)
		}
	}

	if nHeader != wantHeader || nVoice != wantVoice || nTerm != wantTerm {
		t.Fatalf("capture shape header/voice/term = %d/%d/%d, want %d/%d/%d",
			nHeader, nVoice, nTerm, wantHeader, wantVoice, wantTerm)
	}
	if streamID == 0 {
		t.Fatal("real capture carried a zero stream id")
	}
	if len(voiceCWs) != wantVoice*dmrAMBEPerFrm {
		t.Fatalf("extracted %d codewords, want %d", len(voiceCWs), wantVoice*dmrAMBEPerFrm)
	}

	// The reframe must be byte-exact on the REAL codec bits, not just random ones.
	// Each mode's own hop is lossless, every ordered pair round-trips, and the full
	// DMR->YSF->NXDN->DMR triangle preserves the real AMBE (RFC-0003 §2).
	modes := []Mode{ModeDMR, ModeYSF, ModeNXDN}
	names := map[Mode]string{ModeDMR: "DMR", ModeYSF: "YSF", ModeNXDN: "NXDN"}
	for _, m := range modes {
		if got := reframeStream(t, m, voiceCWs); !equalCodewords(got, voiceCWs) {
			t.Fatalf("%s reframe hop altered the real captured AMBE", names[m])
		}
	}
	for _, a := range modes {
		for _, b := range modes {
			if a == b {
				continue
			}
			viaB := reframeStream(t, b, reframeStream(t, a, voiceCWs))
			back := reframeStream(t, a, viaB)
			if !equalCodewords(back, voiceCWs) {
				t.Fatalf("%s->%s->%s did not preserve the real captured AMBE byte-exactly",
					names[a], names[b], names[a])
			}
		}
	}
	triangle := reframeStream(t, ModeDMR,
		reframeStream(t, ModeNXDN,
			reframeStream(t, ModeYSF,
				reframeStream(t, ModeDMR, voiceCWs))))
	if !equalCodewords(triangle, voiceCWs) {
		t.Fatal("DMR->YSF->NXDN->DMR did not preserve the real captured AMBE")
	}
}

// TestRealCaptureYSFFromDMRBench validates the frame layer against a REAL YSFD
// capture produced BY the waypoint-bus daemon on the bench Pi during Phase-1
// hardware validation (docs/on-hardware-report.md, 2026-07-20): the daemon was
// fed the real DMR Parrot transmission (dmr_parrot_9990.bin) on the DMR loopback
// and reframed it to YSF, and this is what emerged on the YSF peer port
// (127.0.0.1:4200 -> :3200), captured with tcpdump. So unlike the synthetic YSF
// golden fixture, these are YSFD bytes a real daemon actually emitted, carrying
// the source callsign KN4OQW resolved from DMR id 3180202 via the shared
// DMRIds.dat — proving on-hardware ID->callsign resolution and reframe. The
// capture is a prefix of a longer transmission (header + 9 voice frames; tcpdump
// bounded it), so it has no terminator.
func TestRealCaptureYSFFromDMRBench(t *testing.T) {
	path := filepath.Join("testdata", "capture", "ysf_bench_from_dmr.bin")
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) == 0 || len(blob)%ysfdLen != 0 {
		t.Fatalf("capture is not a whole number of %d-byte YSFD frames: %d bytes", ysfdLen, len(blob))
	}
	var nHeader, nVoice int
	for off := 0; off+ysfdLen <= len(blob); off += ysfdLen {
		f, err := ParseYSF(blob[off : off+ysfdLen])
		if err != nil {
			t.Fatalf("real YSFD frame at %d failed to parse: %v", off, err)
		}
		if f.Mode != ModeYSF {
			t.Fatalf("frame at %d: mode %v, want YSF", off, f.Mode)
		}
		if f.SrcCallsign != "KN4OQW" {
			t.Fatalf("frame at %d: src callsign %q, want KN4OQW (resolved from the DMR id on the bench)", off, f.SrcCallsign)
		}
		switch f.Kind {
		case KindHeader:
			nHeader++
		case KindVoice:
			nVoice++
			if len(f.AMBE) != ysfVCHPerFrame {
				t.Fatalf("voice frame at %d carried %d codewords, want %d", off, len(f.AMBE), ysfVCHPerFrame)
			}
		}
	}
	if nHeader != 1 || nVoice < 1 {
		t.Fatalf("expected 1 header and >=1 voice frame, got header=%d voice=%d", nHeader, nVoice)
	}
}
