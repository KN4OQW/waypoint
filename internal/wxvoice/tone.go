package wxvoice

import "math"

// The attention tone that precedes a spoken alert.
//
// # What this does and does not do
//
// It generates audio. It does not trigger anything: a DMR radio has no alert
// decoder, so unlike a weather radio there is no receiver-side feature to
// activate. 1050 Hz is the NOAA Weather Radio Warning Alarm Tone and is the
// familiar one to a human ear, which is the whole of its value here.
//
// # What is deliberately not offered
//
// There is no SAME header generator and no EAS Attention Signal preset. Those
// are regulated signalling rather than attention tones -- 47 CFR 11.45 restricts
// transmitting the EAS codes or Attention Signal, or simulations of them,
// outside real alerts and authorised tests. A generic tone generator is the
// right tool: it does what an operator needs, and what they set it to is their
// call and their licence.

// ToneOptions describes the tone. Everything is a parameter because the useful
// values differ by audience: 1050 Hz is what a weather radio uses, but an
// operator whose users are listening on handhelds may want something shorter and
// less piercing over a vocoder.
type ToneOptions struct {
	// HzA is the primary frequency. Zero means no tone.
	HzA float64
	// HzB, when non-zero, is mixed with HzA to make a two-tone signal.
	HzB float64
	// Millis is the duration.
	Millis int
	// Amplitude is 0..1 of full scale. Well below 1 by default: a vocoder is
	// modelling a human voice, and a full-scale pure tone is the least
	// voice-like thing it can be handed.
	Amplitude float64
}

// GenerateTone renders the tone as 8 kHz mono PCM ready to sit in front of
// speech.
//
// It returns nil when no tone is configured, so a caller can prepend
// unconditionally.
func GenerateTone(o ToneOptions) []int16 {
	if o.HzA <= 0 || o.Millis <= 0 {
		return nil
	}
	amp := o.Amplitude
	if amp <= 0 || amp > 1 {
		amp = 0.5
	}
	// Two tones share the budget so mixing them cannot clip.
	scale := amp * math.MaxInt16
	if o.HzB > 0 {
		scale /= 2
	}

	n := DMRSampleRate * o.Millis / 1000
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		t := float64(i) / float64(DMRSampleRate)
		v := math.Sin(2 * math.Pi * o.HzA * t)
		if o.HzB > 0 {
			v += math.Sin(2 * math.Pi * o.HzB * t)
		}
		// A few milliseconds of fade at each end. A tone that starts and stops
		// at full amplitude produces a click, and a click through a vocoder is a
		// wideband splatter that sounds worse than the tone it brackets.
		out[i] = int16(v * scale * toneEnvelope(i, n))
	}
	return out
}

// toneEnvelope is a 5 ms linear ramp in and out.
func toneEnvelope(i, n int) float64 {
	ramp := DMRSampleRate * 5 / 1000
	if ramp*2 >= n {
		ramp = n / 4
	}
	if ramp <= 0 {
		return 1
	}
	switch {
	case i < ramp:
		return float64(i) / float64(ramp)
	case i >= n-ramp:
		// n-1-i so the LAST sample is exactly zero. Using n-i leaves the tone
		// ending one step up the ramp, which is a small step to silence and
		// therefore still a click -- the thing the envelope exists to prevent.
		return float64(n-1-i) / float64(ramp)
	default:
		return 1
	}
}

// PrependTone puts the tone in front of speech, with a short gap between them.
//
// The gap matters: a vocoder carries state across frames, and running a pure
// tone straight into the first syllable smears the two together. A moment of
// silence lets it settle.
func PrependTone(tone, speech []int16) []int16 {
	if len(tone) == 0 {
		return speech
	}
	gap := make([]int16, DMRSampleRate*150/1000) // 150 ms
	out := make([]int16, 0, len(tone)+len(gap)+len(speech))
	out = append(out, tone...)
	out = append(out, gap...)
	return append(out, speech...)
}
