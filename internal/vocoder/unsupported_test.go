//go:build !(zello && linux && arm)

package vocoder

import (
	"errors"
	"testing"
)

// Everywhere but a tagged 32-bit ARM build there is no vocoder and no pretending
// otherwise: the codec is an MD380's firmware executed natively. A caller must
// get a clear refusal rather than silence or zeroed audio.
func TestOpenIsRefusedOffTarget(t *testing.T) {
	v, err := Open(Config{FirmwarePath: "/nonexistent/fw.img", RAMPath: "/nonexistent/ram.img"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Open err = %v, want ErrUnsupported", err)
	}
	if v != nil {
		t.Error("Open returned a vocoder on an unsupported build")
	}
}

func TestEncodeAndDecodeAreRefusedOffTarget(t *testing.T) {
	var v Vocoder
	if _, err := v.Encode(make([]int16, SamplesPerFrame)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Encode err = %v, want ErrUnsupported", err)
	}
	if _, err := v.Decode(make([]byte, CodewordBytes)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Decode err = %v, want ErrUnsupported", err)
	}
}
