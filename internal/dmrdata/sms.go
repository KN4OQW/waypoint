package dmrdata

import (
	"errors"
	"time"
)

// Assembling a text message into bursts, and putting one back together again.

// DefaultPreambles is how many preamble CSBKs precede a message.
//
// BrandMeister sends one to three and its messages arrive; the reference
// implementation that first proved this dialect sends nine. Nine costs about half
// a second of channel time and gives a receiver that is not already listening the
// best chance of opening in time, which matters most on the simplex hotspot where
// there is no continuous downlink to lock onto. It is a knob, not a constant, so a
// duplex node can turn it down.
const DefaultPreambles = 9

// SendOptions describes one outbound message.
type SendOptions struct {
	Src   uint32
	Dst   uint32
	Text  string
	Group bool
	// Seq lands in the IP identification field and the TMS message number. It
	// distinguishes retransmissions in a capture; nothing acknowledges it.
	Seq uint16
	// Preambles defaults to DefaultPreambles when zero.
	Preambles int
	// ColorCode defaults to DefaultColorCode when zero. Duplex selects the
	// base-station sync. MMDVM-Host overwrites both for the node's own settings.
	ColorCode byte
	Duplex    bool
	// Dialect is the short-data format to build. Empty means DialectTMS, which is
	// the one proven on air; DialectETSI is byte-verified against a capture of a
	// radio emitting it but has never been sent TO a radio, so a caller choosing it
	// is choosing something untested on that path.
	Dialect Dialect
}

// ErrBadAddress is returned for a DMR ID that does not fit the 24 bits the
// protocol gives it.
var ErrBadAddress = errors.New("dmrdata: DMR ID must be a 24-bit value")

// BuildMessage renders a text message as the complete burst sequence to transmit:
// the preambles, the data header, then the rate-1/2 body blocks, in order.
//
// Every burst it returns carries all 264 bits. The caller wraps each one for
// whatever transport it uses, copying Burst.DataType into the frame header so the
// two agree.
func BuildMessage(o SendOptions) ([]Burst, error) {
	if o.Src > 0xFFFFFF || o.Dst > 0xFFFFFF {
		return nil, ErrBadAddress
	}
	body, blocks, pad, err := buildBody(o.Src, o.Dst, o.Text, o.Seq, o.Group, o.Dialect)
	if err != nil {
		return nil, err
	}
	preambles := o.Preambles
	if preambles <= 0 {
		preambles = DefaultPreambles
	}
	cc := o.ColorCode
	if cc == 0 {
		cc = DefaultColorCode
	}

	out := make([]Burst, 0, preambles+1+blocks)
	add := func(pdu []byte, dt DataType) error {
		b, err := BuildBurst(pdu, dt, cc, o.Duplex)
		if err != nil {
			return err
		}
		out = append(out, b)
		return nil
	}

	// Each preamble carries a countdown of PDUs still to come. It starts one above
	// the true remainder and the last preamble therefore says blocks+2 where only
	// blocks+1 PDUs follow. That is what the proven implementation emits and what
	// the radio accepts; the field is advisory and no receiver seen on the bench
	// acts on the exact value. Reproduced rather than corrected, because a
	// "correction" here is an untested change to the one construction that works.
	toFollow := preambles + blocks + 1
	for range preambles {
		if err := add(buildPreambleCSBK(o.Src, o.Dst, toFollow, o.Group), DataTypeCSBK); err != nil {
			return nil, err
		}
		toFollow--
	}
	hdr := buildDataHeader(DataHeader{
		Group: o.Group, Src: o.Src, Dst: o.Dst, SAP: SAPIPData, Blocks: blocks, Pad: pad,
	})
	if err := add(hdr, DataTypeDataHeader); err != nil {
		return nil, err
	}
	for i := range blocks {
		if err := add(body[i*blockOctets:(i+1)*blockOctets], DataTypeRate12); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// --- Reassembly ---------------------------------------------------------------

// DefaultReassemblyTimeout is how long a partial message is held before its
// source is assumed to have given up. A message is a few hundred milliseconds of
// air time; anything still incomplete after ten seconds is debris.
const DefaultReassemblyTimeout = 10 * time.Second

// Reassembler turns a stream of received bursts back into messages.
//
// It keys partial transfers by source ID, which is what the header gives it. That
// means a source cannot have two messages in flight at once — true on a single
// timeslot, where the bursts of one transfer are contiguous by construction.
//
// The zero value is ready to use. It is not safe for concurrent use; the caller
// owns the goroutine.
type Reassembler struct {
	// Timeout defaults to DefaultReassemblyTimeout when zero.
	Timeout time.Duration
	// Stats counts what was dropped and why. A silent drop is indistinguishable
	// from a message nobody sent, which is exactly the confusion that cost days.
	Stats ReassemblyStats

	partial map[uint32]*transfer
}

// ReassemblyStats counts outcomes. A burst the reassembler cannot use increments
// one of these; a drop with no counter behind it is indistinguishable from a
// message nobody sent, which is the confusion that cost days on the bench.
type ReassemblyStats struct {
	Messages    int // completed and CRC-verified
	BadCRC      int // body checksum failed
	BadHeader   int // header PDU failed its own checksum
	Unusable    int // FEC could not converge on a burst
	Unsupported int // a well-formed PDU in a format this package declines
	Orphaned    int // a body block with no header before it
	Abandoned   int // a partial transfer that timed out
	Malformed   int // reassembled but not a message this package understands
	// NoSync counts bursts carrying none of the four sync patterns. Nothing on
	// air looks like that. It is the signature of an encoder that wrote the 196
	// BPTC bits and left the other 68 as it found them — the 2026-08-06 bug — so a
	// non-zero count here means the transmitter is broken, not the receiver.
	NoSync int
}

type transfer struct {
	hdr     DataHeader
	blocks  [][]byte
	started time.Time
}

// Feed offers one received DATA burst to the reassembler. It returns a message
// only on the burst that completes one; every other outcome is recorded in Stats.
//
// The caller filters voice out first — the transport knows which frames are voice
// and this package does not. A voice burst reaching here has its embedded
// signalling read as a slot type, which yields whichever data type the bits
// happen to spell.
func (r *Reassembler) Feed(burst []byte, now time.Time) (*Message, bool) {
	payload, dt, _, _, unfixable, err := ParseBurst(burst)
	if err != nil {
		return nil, false
	}
	r.expire(now)
	if !hasAnySync(burst) {
		r.Stats.NoSync++
		return nil, false
	}

	switch dt {
	case DataTypeCSBK:
		// A preamble carries no payload worth keeping; it only means traffic is
		// coming. Any OTHER CSBK — a call alert, a radio check — is a control
		// message this package has no business acting on, and saying so is more
		// useful than dropping it into the same silence as the preambles.
		if !isPreambleCSBK(payload) {
			r.Stats.Unsupported++
		}
		return nil, false

	case DataTypeDataHeader:
		if unfixable {
			r.Stats.Unusable++
			return nil, false
		}
		hdr, err := parseDataHeader(payload)
		switch {
		case errors.Is(err, ErrBadCRC):
			r.Stats.BadHeader++
			return nil, false
		case errors.Is(err, ErrUnsupportedPDU):
			r.Stats.Unsupported++
			return nil, false
		case err != nil:
			r.Stats.BadHeader++
			return nil, false
		}
		if hdr.SAP != SAPIPData {
			r.Stats.Unsupported++
			return nil, false
		}
		if r.partial == nil {
			r.partial = map[uint32]*transfer{}
		}
		// A new header supersedes whatever was in flight from this source: the
		// sender has moved on, and holding the old fragments only delays the
		// timeout.
		r.partial[hdr.Src] = &transfer{hdr: hdr, started: now}
		return nil, false

	case DataTypeRate12:
		t := r.pending()
		if t == nil {
			r.Stats.Orphaned++
			return nil, false
		}
		if unfixable {
			// Keep the block: the FEC gave up but the CRC is the real gate, and a
			// dropped block guarantees failure where a damaged one might survive.
			r.Stats.Unusable++
		}
		t.blocks = append(t.blocks, payload)
		if len(t.blocks) < t.hdr.Blocks {
			return nil, false
		}
		delete(r.partial, t.hdr.Src)
		return r.complete(t)
	}
	return nil, false
}

// pending returns the single transfer awaiting body blocks. Body blocks carry no
// addressing of their own — they are just twelve octets — so they can only be
// attributed to a transfer by position in the stream.
func (r *Reassembler) pending() *transfer {
	for _, t := range r.partial {
		if len(t.blocks) < t.hdr.Blocks {
			return t
		}
	}
	return nil
}

func (r *Reassembler) complete(t *transfer) (*Message, bool) {
	body := make([]byte, 0, len(t.blocks)*blockOctets)
	for _, b := range t.blocks {
		body = append(body, b...)
	}
	msg, err := parseBody(body)
	switch {
	case errors.Is(err, ErrBadCRC):
		r.Stats.BadCRC++
		return nil, false
	case err != nil:
		r.Stats.Malformed++
		return nil, false
	}
	// The data header addresses the transfer; the tunnel addresses the datagram.
	// They agree on every message seen on the bench, and where they cannot both be
	// right the header is the one the radio routed on.
	msg.Src, msg.Dst, msg.Group = t.hdr.Src, t.hdr.Dst, t.hdr.Group
	r.Stats.Messages++
	return &msg, true
}

func (r *Reassembler) expire(now time.Time) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultReassemblyTimeout
	}
	for src, t := range r.partial {
		if now.Sub(t.started) > timeout {
			delete(r.partial, src)
			r.Stats.Abandoned++
		}
	}
}
