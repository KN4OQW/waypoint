package router

import (
	"fmt"
	"strconv"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/config"
)

// This file bridges the two "Mode" universes and the two param shapes the bus
// straddles: config.Mode (the store's string token) <-> frames.Mode (the frame
// layer's enum), and config.Attachment's stringly-typed translation fields ->
// frames.Params. Keeping the conversion here leaves the router logic (router.go)
// working in one vocabulary and the frame layer working in its own.

// frameMode maps a store mode token to the frame layer's mode enum. Only the
// reframe tier (DMR/YSF/NXDN) has a frame implementation; anything else is a
// caller error the store's attach-time validator already rejects, so a false
// second return here means the config was hand-built wrong.
func frameMode(m config.Mode) (frames.Mode, bool) {
	switch m {
	case config.ModeDMR:
		return frames.ModeDMR, true
	case config.ModeYSF:
		return frames.ModeYSF, true
	case config.ModeNXDN:
		return frames.ModeNXDN, true
	}
	return 0, false
}

// modeLabel is the uppercase mode name the hub events use (matching hub.Event's
// existing Mode values like "DMR"), so bus events read the same as RF/net events.
func modeLabel(m config.Mode) string {
	switch m {
	case config.ModeDMR:
		return "DMR"
	case config.ModeYSF:
		return "YSF"
	case config.ModeNXDN:
		return "NXDN"
	default:
		return string(m)
	}
}

// paramsFor translates one attachment's stringly-typed store fields into the
// frame layer's numeric frames.Params, consulted by the constructor for the
// destination mode (RFC-0003 §3). A malformed number is a config error surfaced
// at daemon startup, never a silent zero.
func paramsFor(a config.Attachment) (frames.Params, error) {
	var p frames.Params
	if err := parseUint(a.Slot, 8, func(v uint64) { p.Slot = uint8(v) }, "slot"); err != nil {
		return p, err
	}
	if err := parseUint(a.DefaultTG, 32, func(v uint64) { p.DefaultTG = uint32(v) }, "default_tg"); err != nil {
		return p, err
	}
	if len(a.TGMap) > 0 {
		p.TGMap = make(map[uint32]uint32, len(a.TGMap))
		for k, v := range a.TGMap {
			kk, err := strconv.ParseUint(k, 10, 32)
			if err != nil {
				return p, fmt.Errorf("tg_map key %q: %w", k, err)
			}
			vv, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return p, fmt.Errorf("tg_map value %q: %w", v, err)
			}
			p.TGMap[uint32(kk)] = uint32(vv)
		}
	}
	p.Target = a.Target
	p.WiresXPassthrough = a.WiresXPassthrough
	if err := parseUint(a.ID, 16, func(v uint64) { p.NXDNID = uint16(v) }, "id"); err != nil {
		return p, err
	}
	if err := parseUint(a.TG, 16, func(v uint64) { p.NXDNTG = uint16(v) }, "tg"); err != nil {
		return p, err
	}
	if err := parseUint(a.DefaultID, 16, func(v uint64) { p.DefaultID = uint16(v) }, "default_id"); err != nil {
		return p, err
	}
	return p, nil
}

// parseUint parses an optional decimal field: empty leaves the target untouched
// (zero value), a bad number errors with the field name.
func parseUint(s string, bits int, set func(uint64), field string) error {
	if s == "" {
		return nil
	}
	v, err := strconv.ParseUint(s, 10, bits)
	if err != nil {
		return fmt.Errorf("%s %q: %w", field, s, err)
	}
	set(v)
	return nil
}

// reframer rate-matches one destination in a transmission: it accepts the source
// stream's AMBE codewords (however many per source frame) and hands them back in
// destination-sized groups (frames.CodewordsPerFrame(dst)). A DMR source's
// 3-codeword frames feeding a YSF destination (5 per frame) buffer until 5 are
// available, so the reframe never invents or drops a codeword mid-stream — the
// leftover (< one destination frame) is carried until the next voice frame, and
// only whatever remains at the terminator is dropped (logged by the caller).
//
// This is the hub half of the cross-mode preservation the frame layer proves
// byte-exact (frames TestCrossModeAMBEPreservation); the reframer only regroups.
type reframer struct {
	per int
	buf [][]byte
}

func newReframer(dst frames.Mode) *reframer {
	return &reframer{per: frames.CodewordsPerFrame(dst)}
}

// push appends the source frame's codewords and returns every full
// destination-sized group now available (possibly none, possibly several).
func (r *reframer) push(cws [][]byte) [][][]byte {
	r.buf = append(r.buf, cws...)
	var out [][][]byte
	for len(r.buf) >= r.per {
		grp := make([][]byte, r.per)
		copy(grp, r.buf[:r.per])
		out = append(out, grp)
		r.buf = r.buf[r.per:]
	}
	return out
}

// pending is how many codewords are buffered but not yet emitted (dropped at
// stream end); exposed so the caller can log the remainder.
func (r *reframer) pending() int { return len(r.buf) }
