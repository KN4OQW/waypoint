package modem

import (
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/sys/unix"
)

// The serial layer is raw termios on a raw file descriptor rather than an
// os.File, and that is deliberate.
//
// Read timeouts here come from VTIME — the terminal driver returns short after
// a fixed interval of silence — which is exactly the behaviour a probe wants:
// "answer within half a second or you are not a modem". Handing the descriptor
// to os.NewFile would put it under Go's poller, which manages non-blocking mode
// itself and would quietly take VTIME out of the picture. hd44780 talks to its
// bus the same way, for the same reason.
//
// No dependency is added for this. golang.org/x/sys is already in go.mod.

// ErrTimeout is a read that returned nothing within the port's VTIME window.
// It is a distinct error because "silence" is the single most common outcome of
// probing a port that is not a modem, and it must not be reported as a fault.
var ErrTimeout = errors.New("modem: port did not answer")

// Bauds are the line speeds a probe tries, in order. 115200 is the MMDVM_HS
// family's default and covers the entire launch tier; the others exist because
// full-size boards and a few reflashed clones run faster, and trying them costs
// one extra second on a port that has already proven it is not answering.
var Bauds = []int{115200, 460800, 230400}

var baudBits = map[int]uint32{
	115200: unix.B115200,
	230400: unix.B230400,
	460800: unix.B460800,
	9600:   unix.B9600,
	38400:  unix.B38400,
}

// Parity is the framing a port uses.
//
// It is a parameter rather than a constant because the same physical port
// carries two protocols with different framing: MMDVM's own protocol is 8N1,
// and the STM32 ROM bootloader a firmware flash talks to is 8E1 — it uses the
// parity bit as part of its autobaud detection. One port implementation serves
// both (internal/flash), which is better than a second copy of this file
// differing in one termios flag.
type Parity uint8

const (
	ParityNone Parity = iota
	ParityEven
)

// serialPort is an open, raw-mode serial device.
type serialPort struct {
	fd   int
	path string
}

// OpenPort opens a serial port in raw mode, with reads that give up after
// readTimeout of silence. It is the exported form for callers outside this
// package — the firmware flasher, which needs even parity.
func OpenPort(path string, baud int, parity Parity, readTimeout time.Duration) (io.ReadWriteCloser, error) {
	return openSerialParity(path, baud, parity, readTimeout)
}

// openSerial opens a port for the modem protocol: 8N1.
func openSerial(path string, baud int, readTimeout time.Duration) (*serialPort, error) {
	return openSerialParity(path, baud, ParityNone, readTimeout)
}

func openSerialParity(path string, baud int, parity Parity, readTimeout time.Duration) (*serialPort, error) {
	bits, ok := baudBits[baud]
	if !ok {
		return nil, fmt.Errorf("modem: unsupported baud %d", baud)
	}
	// O_NONBLOCK on open so a port whose carrier lines are unasserted cannot
	// block us forever; it is cleared below so VTIME governs reads.
	// O_NOCTTY so opening the console UART does not make it this process's
	// controlling terminal — on a node where the serial console was freed for
	// the modem, that would be a real hazard.
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("modem: open %s: %w", path, err)
	}
	p := &serialPort{fd: fd, path: path}

	// Advisory exclusivity: further opens fail with EBUSY while we hold it.
	// It does not evict a holder that got there first — nothing can, and
	// nothing should; that is what the service arbitration in detect.go is for.
	_ = unix.IoctlSetInt(fd, unix.TIOCEXCL, 0)

	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		p.Close()
		return nil, fmt.Errorf("modem: %s is not a terminal: %w", path, err)
	}

	// Raw mode: no line editing, no echo, no signal characters, no newline
	// translation. The modem protocol is binary and 0x0A and 0x0D are ordinary
	// payload bytes in it.
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | unix.CRTSCTS | unix.CBAUD
	// 8N1, receiver on, and CLOCAL so the driver ignores modem control lines —
	// a three-wire hat asserts none of them.
	//
	// HUPCL is cleared, and that clearing is load-bearing: with it set, closing
	// the port drops DTR, and dropping DTR is what resets an STM32 CDC board.
	// Detection that reboots the modem every time it looks at one would knock a
	// running node off the air for the length of a firmware boot.
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | bits
	t.Cflag &^= unix.HUPCL

	// Even parity for the ROM bootloader. PARODD is already cleared above, so
	// enabling PARENB alone gives 8E1. INPCK stays off deliberately: a parity
	// error should reach the protocol layer as a wrong byte rather than be
	// silently dropped or marked by the driver, because both bootloader and modem
	// protocols carry their own checksums and can say so honestly.
	if parity == ParityEven {
		t.Cflag |= unix.PARENB
	}

	// VMIN 0 / VTIME n: return whatever has arrived after n tenths of a second
	// of silence, including nothing at all.
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = deciseconds(readTimeout)
	t.Ispeed, t.Ospeed = bits, bits

	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		p.Close()
		return nil, fmt.Errorf("modem: configure %s: %w", path, err)
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		p.Close()
		return nil, fmt.Errorf("modem: %s: %w", path, err)
	}
	// Discard anything already buffered. On a port MMDVM-Host only just let go
	// of, that is the tail of its traffic, and reading it back would look like
	// a modem answering the wrong question.
	_ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIOFLUSH)
	return p, nil
}

// deciseconds converts to VTIME's unit, clamped to the byte the field is.
// A zero timeout would mean "block forever", which is never what a probe wants.
func deciseconds(d time.Duration) uint8 {
	n := (d + 99*time.Millisecond) / (100 * time.Millisecond)
	switch {
	case n < 1:
		return 1
	case n > 255:
		return 255
	}
	return uint8(n)
}

func (p *serialPort) Read(b []byte) (int, error) {
	n, err := unix.Read(p.fd, b)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		// VTIME expired. Reporting this as (0, nil) would spin io.ReadFull
		// forever; silence is an outcome, so it gets an error.
		return 0, ErrTimeout
	}
	return n, nil
}

func (p *serialPort) Write(b []byte) (int, error) { return unix.Write(p.fd, b) }

func (p *serialPort) Close() error {
	if p.fd < 0 {
		return nil
	}
	err := unix.Close(p.fd)
	p.fd = -1
	return err
}
