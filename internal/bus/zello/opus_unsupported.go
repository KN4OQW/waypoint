//go:build !zello

package zello

import "errors"

// Without the `zello` tag there is no libopus and no codec. The protocol and
// client layers above still compile and are still tested — only the audio codec
// is missing — so a default build can parse and reason about Zello without
// carrying a cgo dependency.

// ErrNoOpus is returned by every codec entry point on an untagged build.
var ErrNoOpus = errors.New("zello: built without the zello tag, so libopus is unavailable")

const (
	DefaultSampleRate = 16000
	DefaultFrameMS    = 60
)

// Encoder is unavailable without the tag.
type Encoder struct{}

// NewEncoder reports ErrNoOpus.
func NewEncoder(int, int) (*Encoder, error) { return nil, ErrNoOpus }

// FrameSamples reports zero.
func (e *Encoder) FrameSamples() int { return 0 }

// Encode reports ErrNoOpus.
func (e *Encoder) Encode([]int16) ([]byte, error) { return nil, ErrNoOpus }

// CodecHeader reports the zero header.
func (e *Encoder) CodecHeader() CodecHeader { return CodecHeader{} }

// Decoder is unavailable without the tag.
type Decoder struct{}

// NewDecoder reports ErrNoOpus.
func NewDecoder(int) (*Decoder, error) { return nil, ErrNoOpus }

// Decode reports ErrNoOpus.
func (d *Decoder) Decode([]byte) ([]int16, error) { return nil, ErrNoOpus }
