// Package frames is the frame layer of the RFC-0003 mode-bus daemon: pure
// functions over byte slices that parse the network-side (loopback) frames each
// gateway speaks into a normalized Frame, and construct outbound frames for each
// destination mode. It does NO network I/O and runs NO hub loop — that is the
// next layer (the hub) which drives these functions.
//
// The scope is the REFRAME tier only (RFC-0003 §2): DMR, YSF DN (V/D mode 2,
// half-rate), and NXDN all carry the same AMBE+2 2450x1150 codec, so converting
// between them is packet reframing — lifting the AMBE codewords out of one mode's
// framing and repacking them into another's. There is NO vocoder, NO DSP, NO
// firmware anywhere in this package: the AMBE bytes are copied verbatim, which is
// exactly why a DMR->YSF->DMR round trip preserves the audio bit-for-bit. If a
// change here ever appears to need an AMBE encode/decode, it is wrong (re-read
// RFC-0003 §2).
//
// Ground truth: the reframe logic is ported from juribeparada/MMDVM_CM's
// ModeConv classes (DMR2YSF, YSF2DMR, NXDN2DMR, DMR2NXDN) against the g4klx
// MMDVMHost frame definitions (DMRDefines/YSFDefines/YSFFICH/NXDNDefines). Each
// nontrivial port cites the upstream file/function at the ported site.
package frames

import "errors"

// Mode is one reframe-tier digital voice mode.
type Mode uint8

const (
	ModeDMR Mode = iota
	ModeYSF      // YSF DN (V/D mode 2)
	ModeNXDN
)

func (m Mode) String() string {
	switch m {
	case ModeDMR:
		return "dmr"
	case ModeYSF:
		return "ysf"
	case ModeNXDN:
		return "nxdn"
	default:
		return "unknown"
	}
}

// Kind classifies a frame within a transmission (a "stream"): a header (link
// setup) starts it, voice frames carry the AMBE, and a terminator ends it. The
// hub uses this to bracket the AMBE it fans out (arbitration/loop rules, §5).
type Kind uint8

const (
	KindVoice      Kind = iota // carries AMBE codewords (the default/common case)
	KindHeader                 // voice header / link control — starts a stream
	KindTerminator             // voice terminator — ends a stream
)

func (k Kind) String() string {
	switch k {
	case KindHeader:
		return "header"
	case KindVoice:
		return "voice"
	case KindTerminator:
		return "terminator"
	default:
		return "unknown"
	}
}

// CodewordsPerFrame is how many AMBE+2 codewords one voice frame of a mode
// carries on the wire: DMR 3, YSF DN (VD mode 2) 5, NXDN 4 (two 2-codeword
// blocks). The hub uses this to rate-match a stream when reframing between modes
// — a DMR frame's 3 codewords do not line up 1:1 with a YSF frame's 5, so the
// codewords are buffered and repacked at the destination's cadence. Returns 0 for
// an unknown mode. (These are the counts the cross-mode reframe test pivots on;
// see frames_test.go ambePerFrame.)
func CodewordsPerFrame(m Mode) int {
	switch m {
	case ModeDMR:
		return dmrAMBEPerFrm
	case ModeYSF:
		return ysfVCHPerFrame
	case ModeNXDN:
		return nxdnAMBEPerBlk * 2
	}
	return 0
}

// AMBEBytes is the packed size of one normalized AMBE+2 2450x1150 codeword: the
// 49 codec-significant bits — a(12) at bit 0, b(12) at bit 12, c(25) at bit 24 —
// packed MSB-first into 7 bytes (the last 7 bits pad zero). This is the
// codec-INVARIANT the whole layer pivots on and is byte-identical to NXDN's flat
// loopback VCH layout. Each mode wraps these 49 bits in its own FEC on the wire
// (DMR: Golay+PRNG; YSF: rate-1/3 repetition+whitening+interleave; NXDN: none),
// and strips it on parse, so the 49 bits — hence the audio — survive every
// reframe unchanged (RFC-0003 §2). No vocoder is ever involved.
const AMBEBytes = 7

// ambeABits/ambeBBits/ambeCBits are the three AMBE+2 sub-vector widths (12+12+25
// = 49), the split DMR's Golay FEC protects and NXDN/YSF carry.
const (
	ambeABits = 12
	ambeBBits = 12
	ambeCBits = 25
)

// Stream identifies the transmission an AMBE frame belongs to so the hub can keep
// per-source ordering and detect frame loss without decoding audio.
type Stream struct {
	ID  uint32 // per-transmission stream id (DMR StreamId / YSF has none -> synthesized)
	Seq uint8  // frame sequence number within the stream (wraps per mode)
}

// Frame is the normalized, mode-independent representation the whole layer pivots
// on: parse(bytes) -> Frame, and Frame -> construct(bytes). It carries the source
// and destination addressing (resolved per mode) and the AMBE codewords lifted
// from (or to be packed into) the mode's framing. The reframe unit is the AMBE
// codeword: a DMR/NXDN frame carries a different COUNT of codewords than a YSF DN
// frame, so a Frame holds however many its source frame carried, and the
// constructors repack them at the destination's cadence.
type Frame struct {
	Mode Mode
	Kind Kind

	// Addressing, normalized to numbers + callsign. DMR/NXDN are ID-addressed;
	// YSF is callsign-addressed. Parsers fill whichever the wire carries and leave
	// the constructors to resolve the rest via a Resolver.
	SrcID       uint32
	DstID       uint32
	SrcCallsign string
	DstCallsign string

	Stream Stream

	// AMBE holds the codewords carried by THIS frame, in transmission order, each
	// exactly AMBEBytes long. Reframing copies these verbatim (no vocoder).
	AMBE [][]byte
}

// Params are the per-attachment translation parameters (RFC-0003 §3) the
// constructors apply. They mirror config.Attachment's translation fields but are
// duplicated here so the frame library stays decoupled from the config package
// (the hub maps Attachment -> Params). Only the fields for the destination mode
// are consulted.
type Params struct {
	// DMR destination.
	Slot      uint8             // 1 or 2
	DefaultTG uint32            // fallback destination TG when the source carries none / is unmapped
	TGMap     map[uint32]uint32 // source-mode target -> DMR TG

	// YSF destination.
	Target            string // reflector / DG-ID target label (carried for the hub; not in the audio path)
	WiresXPassthrough bool

	// NXDN destination.
	NXDNID    uint16 // [NXDN Network] Id
	NXDNTG    uint16 // [NXDN Network] TG
	DefaultID uint16 // [NXDN Network] DefaultID
}

// Resolver resolves between DMR/NXDN numeric ids and callsigns via the shared
// DMRIds.dat table (internal/dmrids.Table satisfies it). The frame library never
// reads the file itself — the hub loads the table and passes it in — so this
// package stays pure. A miss returns "" / 0; callers fall back to the numeric id.
type Resolver interface {
	CallsignForID(id uint32) string
	IDForCallsign(callsign string) uint32
}

// nullResolver is the zero-knowledge resolver used when none is supplied: every
// lookup misses, so addressing falls back to numeric ids / blank callsigns.
type nullResolver struct{}

func (nullResolver) CallsignForID(uint32) string { return "" }
func (nullResolver) IDForCallsign(string) uint32 { return 0 }

func resolverOrNull(r Resolver) Resolver {
	if r == nil {
		return nullResolver{}
	}
	return r
}

// Errors returned by the parsers. They are sentinel-wrapped so the hub can tell a
// malformed frame (drop it) from a genuine failure. A parser NEVER panics on bad
// input — that is a release-blocking property (fuzzed).
var (
	ErrShort       = errors.New("frames: buffer too short")
	ErrBadMagic    = errors.New("frames: bad frame magic/tag")
	ErrBadFrame    = errors.New("frames: malformed frame")
	ErrUnsupported = errors.New("frames: unsupported frame type")
)
