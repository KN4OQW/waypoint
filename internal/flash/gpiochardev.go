package flash

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The GPIO character-device ABI (v2), transcribed from linux/gpio.h.
//
// The structs are laid out to the byte because the kernel checks: each ioctl
// number encodes the size of its argument, so a field in the wrong place is not
// a subtle bug but an immediate EINVAL. gpio_test.go asserts every size against
// the sizes those ioctl numbers encode, which is the only check that catches a
// transcription slip on a machine with no GPIO chip to try it against.
//
// x/sys/unix carries the ioctl numbers but not these types, and it is already a
// dependency; nothing here justifies pulling in a GPIO library.

const (
	gpioMaxNameSize       = 32
	gpioV2LinesMax        = 64
	gpioV2LineNumAttrsMax = 10

	gpioV2LineFlagOutput uint64 = 1 << 3

	gpioV2LineAttrIDOutputValues uint32 = 2
)

type gpiochipInfo struct {
	Name  [gpioMaxNameSize]byte
	Label [gpioMaxNameSize]byte
	Lines uint32
}

type gpioV2LineAttribute struct {
	ID      uint32
	Padding uint32
	Value   uint64 // a union in C: flags, values, or a debounce period
}

type gpioV2LineConfigAttribute struct {
	Attr gpioV2LineAttribute
	Mask uint64
}

type gpioV2LineConfig struct {
	Flags    uint64
	NumAttrs uint32
	Padding  [5]uint32
	Attrs    [gpioV2LineNumAttrsMax]gpioV2LineConfigAttribute
}

type gpioV2LineRequest struct {
	Offsets         [gpioV2LinesMax]uint32
	Consumer        [gpioMaxNameSize]byte
	Config          gpioV2LineConfig
	NumLines        uint32
	EventBufferSize uint32
	Padding         [5]uint32
	FD              int32
}

type gpioV2LineValues struct {
	Bits uint64
	Mask uint64
}

// Sizes pinned at COMPILE time, in both directions, so every architecture the
// release pipeline builds is checked rather than only the one the tests run on.
// This is not belt-and-braces: the kernel's __aligned_u64 forces 8-byte
// alignment, while Go on 32-bit ARM — the GOARM=6 build a Pi Zero runs — aligns
// uint64 to 4. The layouts below happen to agree, and these constants are what
// keeps a later edit from quietly ending that.
const (
	_ = uint(unsafe.Sizeof(gpiochipInfo{}) - 68)
	_ = uint(68 - unsafe.Sizeof(gpiochipInfo{}))
	_ = uint(unsafe.Sizeof(gpioV2LineConfig{}) - 272)
	_ = uint(272 - unsafe.Sizeof(gpioV2LineConfig{}))
	_ = uint(unsafe.Sizeof(gpioV2LineRequest{}) - 592)
	_ = uint(592 - unsafe.Sizeof(gpioV2LineRequest{}))
	_ = uint(unsafe.Sizeof(gpioV2LineValues{}) - 16)
	_ = uint(16 - unsafe.Sizeof(gpioV2LineValues{}))
)

func ioctlPtr(fd int, req uintptr, arg unsafe.Pointer) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

// chardevChip is one enumerated /dev/gpiochipN.
type chardevChip struct {
	path  string
	label string
	lines int
}

// readChipInfo asks a chip what it is. It is a variable so the enumeration can
// be tested without a GPIO controller.
var readChipInfo = func(path string) (chardevChip, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return chardevChip{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)

	var info gpiochipInfo
	if err := ioctlPtr(fd, unix.GPIO_GET_CHIPINFO_IOCTL, unsafe.Pointer(&info)); err != nil {
		return chardevChip{}, fmt.Errorf("%s: chip info: %w", path, err)
	}
	return chardevChip{
		path:  path,
		label: cstring(info.Label[:]),
		lines: int(info.Lines),
	}, nil
}

// enumerateChips lists the GPIO controllers, numerically rather than
// lexically — gpiochip10 must not sort before gpiochip2 when a label is being
// matched in preference order.
func enumerateChips(devDir string) ([]chardevChip, error) {
	entries, err := os.ReadDir(devDir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "gpiochip") {
			paths = append(paths, filepath.Join(devDir, e.Name()))
		}
	}
	sort.Slice(paths, func(i, j int) bool { return chipIndex(paths[i]) < chipIndex(paths[j]) })

	var chips []chardevChip
	for _, p := range paths {
		c, err := readChipInfo(p)
		if err != nil {
			// A chip that cannot be interrogated is not a reason to give up on the
			// others: on a Pi 5 the first chip may be one this daemon has no
			// business opening, and the header is on the next one.
			continue
		}
		chips = append(chips, c)
	}
	return chips, nil
}

func chipIndex(path string) int {
	n := 0
	for _, c := range filepath.Base(path) {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

// chardevLines holds a two-line request open. Closing its descriptor is what
// releases the lines back to the kernel.
type chardevLines struct {
	fd    int
	chip  string
	label string
}

func openChardevLines(cfg LineConfig) (driver, error) {
	chips, err := enumerateChips(cfg.DevDir)
	if err != nil {
		return nil, err
	}
	need := cfg.BOOT0
	if cfg.Reset > need {
		need = cfg.Reset
	}
	chip, err := pickChip(chips,
		func(c chardevChip) string { return c.label },
		func(c chardevChip) int { return c.lines },
		cfg.ChipLabels, need)
	if err != nil {
		return nil, err
	}

	fd, err := unix.Open(chip.path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", chip.path, err)
	}
	defer unix.Close(fd) // the line request gets its own descriptor

	req := gpioV2LineRequest{NumLines: 2}
	req.Offsets[0] = uint32(cfg.BOOT0)
	req.Offsets[1] = uint32(cfg.Reset)
	copy(req.Consumer[:], "waypoint-flash")
	req.Config.Flags = gpioV2LineFlagOutput

	// Initial values, set in the same request that claims the lines: BOOT0 low,
	// reset RELEASED. Without this the kernel would drive both low on claim,
	// which asserts reset — so merely opening the lines would knock a running
	// modem off the air before anything had decided to flash it.
	req.Config.NumAttrs = 1
	req.Config.Attrs[0] = gpioV2LineConfigAttribute{
		Attr: gpioV2LineAttribute{ID: gpioV2LineAttrIDOutputValues, Value: 0b10},
		Mask: 0b11,
	}

	if err := ioctlPtr(fd, unix.GPIO_V2_GET_LINE_IOCTL, unsafe.Pointer(&req)); err != nil {
		return nil, fmt.Errorf("claim lines %d,%d on %s (%s): %w",
			cfg.BOOT0, cfg.Reset, chip.path, chip.label, err)
	}
	return &chardevLines{fd: int(req.FD), chip: chip.path, label: chip.label}, nil
}

func (l *chardevLines) set(boot0, reset bool) error {
	v := gpioV2LineValues{Mask: 0b11}
	if boot0 {
		v.Bits |= 0b01
	}
	if reset {
		v.Bits |= 0b10
	}
	return ioctlPtr(l.fd, unix.GPIO_V2_LINE_SET_VALUES_IOCTL, unsafe.Pointer(&v))
}

func (l *chardevLines) describe() string {
	return fmt.Sprintf("%s (%s), character device", l.chip, l.label)
}

func (l *chardevLines) Close() error {
	if l.fd < 0 {
		return nil
	}
	err := unix.Close(l.fd)
	l.fd = -1
	return err
}

// cstring trims a fixed-width kernel string at its NUL.
func cstring(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}
