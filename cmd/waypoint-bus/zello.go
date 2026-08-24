//go:build zello

package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/backoff"
	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/bus/zello"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/vocoder"
)

// zello.go is the tagged half of the Zello endpoint: the parts that need libopus
// and the AMBE+2 firmware. The buffering and the router edge are in
// zellobridge.go, untagged, so they are tested in CI.

func init() { newZelloSink = openZelloSink }

// The vocoder is process-global because the firmware is mapped at fixed
// addresses; internal/vocoder refuses a second one. Every Zello endpoint on this
// bus therefore shares it, which is safe in the outbound direction because
// RFC-0003 §5 gives the bus one token and so one talker at a time.
//
// Inbound is not covered by that, and this is the sharp edge. Two bridged
// channels can have far-end talkers at the same moment, and the transcode happens
// BEFORE the frames reach the router's arbitration — so without a gate they would
// interleave through one stateful vocoder and corrupt each other's audio rather
// than one of them simply losing. inboundGate admits one inbound stream at a time
// and drops the rest, which is §5 rule 2's shape applied one layer earlier,
// where the constraint actually lives.
var (
	vocOnce sync.Once
	vocInst *vocoder.Vocoder
	vocErr  error

	inboundGate struct {
		sync.Mutex
		owner string    // endpoint id currently transcoding inbound audio
		last  time.Time // when it last produced a frame
	}
)

// inboundIdle is how long a silent inbound owner keeps the vocoder before another
// channel may claim it. It matches the bus hang time's intent: long enough to
// bridge gaps inside an over, short enough that a finished transmission frees the
// codec promptly.
const inboundIdle = 2 * time.Second

func sharedVocoder(v config.BusVocoder) (*vocoder.Vocoder, error) {
	vocOnce.Do(func() {
		vocInst, vocErr = vocoder.Open(vocoder.Config{FirmwarePath: v.FirmwarePath, RAMPath: v.RAMPath})
	})
	return vocInst, vocErr
}

// claimInbound admits one endpoint to the shared vocoder for inbound audio.
func claimInbound(id string, now time.Time) bool {
	inboundGate.Lock()
	defer inboundGate.Unlock()
	if inboundGate.owner != "" && inboundGate.owner != id && now.Sub(inboundGate.last) < inboundIdle {
		return false
	}
	inboundGate.owner, inboundGate.last = id, now
	return true
}

func releaseInbound(id string) {
	inboundGate.Lock()
	defer inboundGate.Unlock()
	if inboundGate.owner == id {
		inboundGate.owner = ""
	}
}

// zelloEndpoint is one bridged channel: a WebSocket, a bridge, and the stream
// bracketing that turns a bus transmission into a Zello voice message.
type zelloEndpoint struct {
	cfg    config.BusZello
	inject func(frames.Frame)

	mu       sync.Mutex
	cli      *zello.Client
	br       *zelloBridge
	outbound uint32 // Zello stream id we are transmitting on; 0 = idle
	lastOut  time.Time

	inStream  uint32 // synthesized bus stream id for the inbound transmission
	closeOnce sync.Once
	done      chan struct{}
}

func openZelloSink(z config.BusZello, v config.BusVocoder, inject func(frames.Frame)) (zelloSink, error) {
	voc, err := sharedVocoder(v)
	if err != nil {
		return nil, fmt.Errorf("vocoder: %w", err)
	}
	enc, err := zello.NewEncoder(zello.DefaultSampleRate, z.PacketMS)
	if err != nil {
		return nil, err
	}
	dec, err := zello.NewDecoder(zello.DefaultSampleRate)
	if err != nil {
		return nil, err
	}

	e := &zelloEndpoint{
		cfg:    z,
		inject: inject,
		br:     newZelloBridge(voc, enc, dec, z.PacketMS),
		done:   make(chan struct{}),
	}
	go e.run()
	return e, nil
}

// run keeps a connection up. The client is deliberately not self-healing, so the
// reconnect schedule lives here, next to the endpoint state that has to be
// discarded when a connection ends.
func (e *zelloEndpoint) run() {
	b := &backoff.Backoff{Initial: time.Second, Max: 2 * time.Minute, Rand: rand.Float64}
	for {
		select {
		case <-e.done:
			return
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cli, err := zello.Dial(ctx, zello.Config{
			Channel:    e.cfg.Channel,
			AuthToken:  e.cfg.AuthToken,
			Username:   e.cfg.Username,
			Password:   e.cfg.Password,
			ListenOnly: e.cfg.ListenOnly,
		})
		cancel()
		if err != nil {
			d := b.Next()
			log.Printf("zello %q: %v; retrying in %s", e.cfg.Channel, err, d.Round(time.Second))
			select {
			case <-time.After(d):
			case <-e.done:
				return
			}
			continue
		}

		b.Reset()
		log.Printf("zello %q: connected", e.cfg.Channel)
		e.mu.Lock()
		e.cli = cli
		e.mu.Unlock()

		e.pump(cli)

		e.mu.Lock()
		e.cli = nil
		e.outbound = 0
		e.br.Reset()
		e.mu.Unlock()
		releaseInbound(e.cfg.ID)

		if err := cli.Err(); err != nil {
			log.Printf("zello %q: disconnected: %v", e.cfg.Channel, err)
		}
	}
}

// pump reads one connection until it ends.
func (e *zelloEndpoint) pump(cli *zello.Client) {
	events, audio := cli.Events(), cli.Audio()
	for {
		select {
		case <-e.done:
			cli.Close()
			return

		case ev, ok := <-events:
			if !ok {
				return
			}
			switch ev.Command {
			case zello.EvtOnStreamStart:
				log.Printf("zello %q: %s is talking", e.cfg.Channel, ev.From)
				e.beginInbound()
			case zello.EvtOnStreamStop:
				e.endInbound()
			case zello.EvtOnError:
				log.Printf("zello %q: server error: %s", e.cfg.Channel, ev.Error)
			}

		case p, ok := <-audio:
			if !ok {
				return
			}
			e.onInboundAudio(p)
		}
	}
}

func (e *zelloEndpoint) beginInbound() {
	e.mu.Lock()
	defer e.mu.Unlock()
	// A fresh stream id per inbound transmission, so the router's echo
	// suppression and its per-stream bus_busy accounting treat each over
	// separately.
	e.inStream = uint32(time.Now().UnixNano())
	e.br.Reset()
}

func (e *zelloEndpoint) endInbound() {
	e.mu.Lock()
	stream := e.inStream
	e.inStream = 0
	e.br.Reset()
	e.mu.Unlock()
	releaseInbound(e.cfg.ID)
	if stream != 0 {
		e.inject(frames.Frame{Kind: frames.KindTerminator, Stream: frames.Stream{ID: stream}})
	}
}

func (e *zelloEndpoint) onInboundAudio(p zello.StreamPacket) {
	now := time.Now()
	if !claimInbound(e.cfg.ID, now) {
		return // another channel is mid-transcode; see inboundGate
	}

	e.mu.Lock()
	stream := e.inStream
	if stream == 0 {
		// Audio without an on_stream_start. Synthesize a stream rather than drop
		// it: the event and the first packets race on the wire, and losing the
		// start of every over to that race would be worse than a missing log line.
		stream = uint32(now.UnixNano())
		e.inStream = stream
	}
	groups, err := e.br.ToBus(p.Data)
	e.mu.Unlock()

	if err != nil {
		log.Printf("zello %q: %v", e.cfg.Channel, err)
		return
	}
	for _, g := range groups {
		e.inject(frames.Frame{
			Kind:   frames.KindVoice,
			Stream: frames.Stream{ID: stream},
			AMBE:   g,
		})
	}
}

// Emit sends one bus emission to the channel, opening and closing the Zello
// stream around the transmission.
func (e *zelloEndpoint) Emit(f frames.Frame) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	cli := e.cli
	if cli == nil {
		return nil // not connected; the bus keeps running and run() is retrying
	}
	if e.cfg.ListenOnly {
		return nil
	}

	switch f.Kind {
	case frames.KindTerminator:
		return e.stopLocked(cli)

	case frames.KindVoice:
		if e.outbound == 0 {
			enc := e.br.enc.(*zello.Encoder)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			id, err := cli.StartStream(ctx, enc.CodecHeader(), e.cfg.PacketMS)
			cancel()
			if err != nil {
				return fmt.Errorf("start_stream: %w", err)
			}
			e.outbound = id
			e.br.Reset()
		}
		pkts, err := e.br.FromBus(f.AMBE)
		if err != nil {
			return err
		}
		for _, pkt := range pkts {
			if err := cli.SendAudio(e.outbound, pkt); err != nil {
				return fmt.Errorf("sending audio: %w", err)
			}
		}
		e.lastOut = time.Now()
	}
	return nil
}

func (e *zelloEndpoint) stopLocked(cli *zello.Client) error {
	if e.outbound == 0 {
		return nil
	}
	id := e.outbound
	e.outbound = 0
	e.br.Reset()
	return cli.StopStream(id)
}

func (e *zelloEndpoint) Close() error {
	e.closeOnce.Do(func() { close(e.done) })
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cli != nil {
		e.stopLocked(e.cli)
		return e.cli.Close()
	}
	return nil
}
