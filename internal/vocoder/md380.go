//go:build zello && linux && arm

package vocoder

// The firmware is 32-bit ARM code from an MD380, executed natively. That is why
// this file is arm-only rather than merely cgo-only: there is no portable path,
// and a build tag that let it compile elsewhere would only produce a binary that
// segfaults the first time it encodes.
//
// Linking requires the operator's md380_vocoder archive built WITHOUT its
// firmware.o and ram.o members, plus md380tools' symbols file to resolve the
// firmware entry points, which is addresses and carries no code:
//
//	CGO_LDFLAGS="-L/path -Xlinker --just-symbols=/path/symbols_d02.032" \
//	  go build -tags zello ./cmd/waypoint-bus
//
// If the archive still contains firmware.o the build will succeed and the
// resulting binary will carry the licensed blob. That is the failure this whole
// arrangement exists to prevent, so the image build checks for it rather than
// trusting the flags.

/*
#cgo LDFLAGS: -lmd380_vocoder

#include <stdint.h>
#include <stdlib.h>

int  wp_md380_load(const char *fw, const char *ram);
int  md380_init(void);
void md380_encode(uint8_t *ambe49, const int16_t *pcm);
void md380_decode(uint8_t *ambe49, int16_t *pcm);
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"
)

// open guards the process-global firmware mapping. The images land at fixed
// addresses and the library computes through fixed buffers inside them, so a
// second Vocoder would not be a second vocoder — it would be the same one with
// two owners.
var openMu sync.Mutex
var isOpen bool

// Vocoder is the process's one AMBE+2 codec.
type Vocoder struct {
	// mu serialises every call into the library. It is not reentrant: the
	// scratch buffers are fixed addresses in the firmware image, so two
	// concurrent encodes would each see the other's intermediate state.
	mu     sync.Mutex
	closed bool
}

// Open maps the firmware images and initialises the vocoder. At most one may be
// open per process; the second call returns ErrAlreadyOpen.
func Open(cfg Config) (*Vocoder, error) {
	if cfg.FirmwarePath == "" || cfg.RAMPath == "" {
		return nil, fmt.Errorf("vocoder: both firmware and ram image paths are required")
	}

	openMu.Lock()
	defer openMu.Unlock()
	if isOpen {
		return nil, ErrAlreadyOpen
	}

	fw := C.CString(cfg.FirmwarePath)
	defer C.free(unsafe.Pointer(fw))
	ram := C.CString(cfg.RAMPath)
	defer C.free(unsafe.Pointer(ram))

	if rc := C.wp_md380_load(fw, ram); rc != 0 {
		return nil, fmt.Errorf("vocoder: mapping firmware from %q and %q failed (rc %d); "+
			"the images must be the unwrapped D002.032 firmware and its matching SRAM core",
			cfg.FirmwarePath, cfg.RAMPath, int(rc))
	}
	if rc := C.md380_init(); rc != 0 {
		return nil, fmt.Errorf("vocoder: md380_init failed (rc %d); "+
			"the firmware mapped but could not be made executable", int(rc))
	}

	isOpen = true
	return &Vocoder{}, nil
}

// Close releases the vocoder so another may be opened.
//
// The mappings are deliberately left in place. Unmapping and remapping the
// firmware at fixed addresses inside a live process is a good way to hand those
// addresses to something else in between, and the process-per-bus model means a
// bus that is finished with its vocoder is a process that is about to exit.
func (v *Vocoder) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	v.closed = true

	openMu.Lock()
	isOpen = false
	openMu.Unlock()
	return nil
}

// Encode turns one 20 ms frame of 8 kHz signed 16-bit audio into a canonical
// AMBE+2 codeword.
func (v *Vocoder) Encode(pcm []int16) ([]byte, error) {
	if len(pcm) != SamplesPerFrame {
		return nil, fmt.Errorf("vocoder: encode needs exactly %d samples, got %d", SamplesPerFrame, len(pcm))
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, fmt.Errorf("vocoder: encode on a closed vocoder")
	}

	// Zeroed, and it has to be. md380_encode ORs into bytes 0-5 rather than
	// assigning them, so a dirty buffer does not produce a slightly wrong
	// codeword — it produces a different one. Measured: the frame that encodes
	// to f8f011044ca880 from a zeroed buffer comes back as ffffffffffff00 from
	// one filled with 0xff.
	cw := make([]byte, CodewordBytes)

	C.md380_encode(
		(*C.uint8_t)(unsafe.Pointer(&cw[0])),
		(*C.int16_t)(unsafe.Pointer(&pcm[0])),
	)
	return cw, nil
}

// Decode turns a canonical AMBE+2 codeword back into one 20 ms frame of audio.
//
// The first frame after opening comes back near-silent while the codec's model
// converges; see the package comment. Callers that care about the start of a
// transmission should prime with a frame of silence rather than treat the first
// real frame as lost.
func (v *Vocoder) Decode(cw []byte) ([]int16, error) {
	if len(cw) != CodewordBytes {
		return nil, fmt.Errorf("vocoder: decode needs exactly %d bytes, got %d", CodewordBytes, len(cw))
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, fmt.Errorf("vocoder: decode on a closed vocoder")
	}

	// Copied because the library takes a non-const pointer and this must not
	// scribble on a caller's codeword — the same bytes are on their way to the
	// frame layer.
	in := make([]byte, CodewordBytes)
	copy(in, cw)

	pcm := make([]int16, SamplesPerFrame)
	C.md380_decode(
		(*C.uint8_t)(unsafe.Pointer(&in[0])),
		(*C.int16_t)(unsafe.Pointer(&pcm[0])),
	)
	return pcm, nil
}
