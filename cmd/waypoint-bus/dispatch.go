package main

import (
	"fmt"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/config"
)

// parseFrame turns a loopback datagram into a normalized frame, dispatching on
// the attachment's mode (the socket it arrived on tells us the wire format). A
// malformed or unsupported packet returns an error the caller drops silently —
// never a panic (the frames parsers are fuzzed for exactly this).
func parseFrame(m config.Mode, data []byte) (frames.Frame, error) {
	switch m {
	case config.ModeDMR:
		return frames.ParseDMR(data)
	case config.ModeYSF:
		return frames.ParseYSF(data)
	case config.ModeNXDN:
		return frames.ParseNXDN(data)
	}
	return frames.Frame{}, fmt.Errorf("no parser for mode %q", m)
}

// constructFrame renders an outbound normalized frame into the destination mode's
// wire bytes, applying that attachment's translation params and the shared
// DMRIds.dat resolver (callsign<->id).
func constructFrame(m config.Mode, f frames.Frame, p frames.Params, r frames.Resolver) ([]byte, error) {
	switch m {
	case config.ModeDMR:
		return frames.ConstructDMR(f, p, r)
	case config.ModeYSF:
		return frames.ConstructYSF(f, p, r)
	case config.ModeNXDN:
		return frames.ConstructNXDN(f, p, r)
	}
	return nil, fmt.Errorf("no constructor for mode %q", m)
}
