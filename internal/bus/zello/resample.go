package zello

// Resampling between the vocoder's 8 kHz and Zello's 16 kHz.
//
// Zello accepts 8 kHz, so this could in principle be skipped — but 16 kHz mono
// is what Zello's own documented example uses (gD4BPA== is 16000 Hz) and what
// every Zello client is tuned for, and a bridge that sends 8 kHz sounds worse to
// the far end than the vocoder alone accounts for.
//
// The filters are deliberately cheap. This runs per 20 ms frame on a Pi 3
// alongside the vocoder, and the source material has already been through
// AMBE+2 at 8 kHz — there is no detail above 4 kHz to protect, so a windowed
// sinc would cost cycles to preserve nothing. Upsampling inserts a linearly
// interpolated sample; downsampling averages each pair, which is a 2-tap box
// filter and enough anti-aliasing for a signal that is already band-limited.

// Upsample8to16 doubles the rate by linear interpolation, returning 2*len(in)
// samples.
//
// The final sample is duplicated rather than interpolated against the next
// frame's first sample: frames arrive one at a time and holding one back to
// interpolate across the boundary would add 20 ms of latency to every frame for
// an inaudible difference.
func Upsample8to16(in []int16) []int16 {
	if len(in) == 0 {
		return nil
	}
	out := make([]int16, len(in)*2)
	for i := 0; i < len(in)-1; i++ {
		out[i*2] = in[i]
		out[i*2+1] = int16((int32(in[i]) + int32(in[i+1])) / 2)
	}
	last := len(in) - 1
	out[last*2] = in[last]
	out[last*2+1] = in[last]
	return out
}

// Downsample16to8 halves the rate by averaging each pair, returning
// len(in)/2 samples. An odd trailing sample is dropped: it is 62.5 µs of audio
// and keeping it would misalign every frame after it.
func Downsample16to8(in []int16) []int16 {
	out := make([]int16, len(in)/2)
	for i := range out {
		// Averaged in int32. Two samples near full scale sum past int16 and
		// would wrap to the opposite sign — silence replaced by a loud click.
		out[i] = int16((int32(in[i*2]) + int32(in[i*2+1])) / 2)
	}
	return out
}

// ResampleTo8k converts PCM at any rate to the vocoder's 8 kHz.
//
// The two fixed-ratio helpers above are the hot path and stay; this is the
// general case, needed because the far end chooses its own rate and announces it
// in on_stream_start. Zello clients overwhelmingly send 16 kHz, but nothing in
// the protocol says they must, and assuming it produced audio at the wrong speed
// rather than an error — the failure mode with no symptom except that everyone
// sounds wrong.
//
// Linear interpolation, for the same reason the fixed helpers use it: the signal
// is about to go through AMBE+2 at 8 kHz, so there is no detail above 4 kHz for
// anything more careful to preserve.
func ResampleTo8k(in []int16, rate int) []int16 {
	switch {
	case len(in) == 0 || rate <= 0:
		return nil
	case rate == 8000:
		return in
	case rate == 16000:
		return Downsample16to8(in)
	}
	out := make([]int16, len(in)*8000/rate)
	for i := range out {
		// Position in the source, in fixed point to avoid a float per sample.
		pos := i * rate / 8000
		frac := (i * rate) % 8000 * 256 / 8000
		if pos+1 >= len(in) {
			out[i] = in[len(in)-1]
			continue
		}
		a, b := int32(in[pos]), int32(in[pos+1])
		out[i] = int16(a + (b-a)*int32(frac)/256)
	}
	return out
}
