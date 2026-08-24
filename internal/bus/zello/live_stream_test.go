//go:build zello

package zello

import (
	"context"
	"math"
	"testing"
	"time"
)

// TestLiveStream transmits into a real Zello channel. It needs the same
// environment as the other live tests plus the zello build tag for libopus, and
// it is the first end-to-end proof of the outbound half: codec header, stream
// bracketing, binary framing and Opus, all judged by whether a human hears it.
//
// The audio is deliberately synthetic and obviously artificial — three tone
// bursts — so nobody listening mistakes it for a person, and so a partial failure
// is audible as a wrong number of beeps rather than as indistinct noise.
//
//	ZELLO_CHANNEL=... ZELLO_TOKEN=... ZELLO_USER=... ZELLO_PASS=... \
//	  go test -tags "zello nolibopusfile" ./internal/bus/zello -run TestLiveStream -v
func TestLiveStream(t *testing.T) {
	cfg := liveConfig(t)
	if cfg.AuthToken == "" {
		t.Skip("set ZELLO_TOKEN to run this")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatalf("logon: %v", err)
	}
	defer c.Close()

	// Transmitting before the channel reports online is refused with
	// "channel is not ready", so wait for it rather than racing.
	waitOnline(t, c)

	enc, err := NewEncoder(DefaultSampleRate, DefaultFrameMS)
	if err != nil {
		t.Fatalf("encoder: %v", err)
	}
	header := enc.CodecHeader()
	t.Logf("codec_header=%s packet_duration=%d", header.Encode(), DefaultFrameMS)

	id, err := c.StartStream(ctx, header, DefaultFrameMS)
	if err != nil {
		t.Fatalf("start_stream: %v", err)
	}
	t.Logf("stream %d open", id)

	// Three 300 ms bursts at 1 kHz, 200 ms apart, then silence to the end.
	const totalMS = 2400
	packets := totalMS / DefaultFrameMS
	frame := enc.FrameSamples()
	pcm := make([]int16, frame)

	// Paced at the packet duration rather than sent as a burst: the far end plays
	// this out in real time, and a burst arrives as one over-long packet run that
	// a jitter buffer has to discard most of.
	tick := time.NewTicker(DefaultFrameMS * time.Millisecond)
	defer tick.Stop()

	sent := 0
	for p := 0; p < packets; p++ {
		for i := range pcm {
			ms := float64(p*DefaultFrameMS) + float64(i)*1000.0/float64(DefaultSampleRate)
			pcm[i] = 0
			for _, start := range []float64{100, 600, 1100} {
				if ms >= start && ms < start+300 {
					pcm[i] = int16(9000 * math.Sin(2*math.Pi*1000*ms/1000.0))
				}
			}
		}
		opus, err := enc.Encode(pcm)
		if err != nil {
			t.Fatalf("encode packet %d: %v", p, err)
		}
		if err := c.SendAudio(id, opus); err != nil {
			t.Fatalf("send packet %d: %v", p, err)
		}
		sent++
		<-tick.C
	}

	if err := c.StopStream(id); err != nil {
		t.Fatalf("stop_stream: %v", err)
	}
	t.Logf("sent %d packets (%d ms) and stopped stream %d", sent, sent*DefaultFrameMS, id)

	// Give the server a moment to report anything wrong with what we just sent —
	// a malformed packet surfaces as on_error rather than as a failed write.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				t.Fatalf("connection dropped after the stream: %v", c.Err())
			}
			t.Logf("event %s error=%q", ev.Command, ev.Error)
			if ev.Command == EvtOnError {
				t.Errorf("the server rejected the stream: %s", ev.Error)
			}
		case <-deadline:
			return
		}
	}
}

func waitOnline(t *testing.T, c *Client) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				t.Fatalf("connection closed waiting for the channel: %v", c.Err())
			}
			if ev.Command == EvtOnChannelStatus && ev.Status == "online" {
				t.Logf("channel %q online, %d user(s)", ev.Channel, ev.UsersOnline)
				return
			}
			if ev.Command == EvtOnError {
				t.Fatalf("server error before transmitting: %s", ev.Error)
			}
		case <-deadline:
			t.Fatal("the channel never reported online")
		}
	}
}
