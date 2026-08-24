//go:build !(zello && linux && arm)

package vocoder

// Vocoder exists on every build so callers compile without tag gymnastics, but
// only the linux/arm zello build can do anything: the codec is an MD380's own
// firmware executed natively, and there is no portable substitute to fall back
// to. Everywhere else Open fails and says so.
type Vocoder struct{}

// Open reports ErrUnsupported.
func Open(Config) (*Vocoder, error) { return nil, ErrUnsupported }

// Close is a no-op; no vocoder can have been opened.
func (v *Vocoder) Close() error { return nil }

// Encode reports ErrUnsupported.
func (v *Vocoder) Encode([]int16) ([]byte, error) { return nil, ErrUnsupported }

// Decode reports ErrUnsupported.
func (v *Vocoder) Decode([]byte) ([]int16, error) { return nil, ErrUnsupported }
