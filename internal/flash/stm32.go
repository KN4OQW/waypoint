// Package flash writes firmware to the modem's microcontroller (RFC-0019).
//
// Two bootloaders can be reached on the boards Waypoint supports: the STM32's
// mask-programmed ROM bootloader over a UART (this file), and the Maple DFU
// bootloader over USB (dfu.go). The ROM path is the one that cannot be bricked
// — BOOT0 held high across a reset enters it whatever is in flash, including
// nothing at all — and preserving that property is why Waypoint drives BOOT0
// and nRST itself instead of asking an operator to hold a jumper.
//
// This file is the wire protocol and nothing else: ST's AN3155 framing over an
// io.ReadWriter, with no serial port, no GPIO lines and no firmware catalog in
// sight. internal/modem/protocol.go makes the same split for the same reason —
// the fiddly part gets tested against an emulated MCU rather than against a hat
// on the bench, and the I/O layer stays thin enough to read in one sitting.
package flash

import (
	"errors"
	"fmt"
	"io"
	"time"
)

// Protocol bytes (AN3155). The command set is larger than this; what is absent
// is absent deliberately. There is no READOUT_PROTECT here, no WRITE_PROTECT,
// and no option-byte access: those brick or lock a board in ways an operator
// cannot undo from a web page, and nothing Waypoint does needs them.
const (
	initByte byte = 0x7F // autobaud training byte
	ack      byte = 0x79
	nack     byte = 0x1F
)

// Command is one bootloader command byte.
type Command byte

// The commands Waypoint issues.
const (
	CmdGet              Command = 0x00 // bootloader version + the command list
	CmdGetID            Command = 0x02 // product ID
	CmdReadMemory       Command = 0x11 // readback, for verification
	CmdGo               Command = 0x21 // jump to an address
	CmdWriteMemory      Command = 0x31
	CmdErase            Command = 0x43 // page/global erase (older bootloaders)
	CmdExtendedErase    Command = 0x44 // page/mass erase (newer bootloaders)
	CmdReadoutUnprotect Command = 0x92 // clears RDP, mass-erasing flash as it does
)

func (c Command) String() string {
	switch c {
	case CmdGet:
		return "GET"
	case CmdGetID:
		return "GET_ID"
	case CmdReadMemory:
		return "READ_MEMORY"
	case CmdGo:
		return "GO"
	case CmdWriteMemory:
		return "WRITE_MEMORY"
	case CmdErase:
		return "ERASE"
	case CmdExtendedErase:
		return "EXTENDED_ERASE"
	case CmdReadoutUnprotect:
		return "READOUT_UNPROTECT"
	}
	return fmt.Sprintf("command 0x%02X", byte(c))
}

// maxChunk is the transfer size for one WRITE_MEMORY or READ_MEMORY. The
// protocol's length field is a byte holding "count - 1", so 256 is the ceiling
// and there is nothing to gain by going lower.
const maxChunk = 256

// ErrSilent reports a read that returned nothing before the port's own silence
// window expired.
//
// The protocol layer never touches a serial port, so it cannot produce this
// itself: the port adapter maps its driver-level timeout onto this sentinel,
// and everything here treats it as "not yet" rather than "failed". That
// distinction is what lets a mass erase — which can hold the bus silent for
// tens of seconds while the part is busy — share one code path with an ACK that
// should arrive in milliseconds.
var ErrSilent = errors.New("flash: the bootloader did not answer")

// ErrNotResponding reports a board that never synchronised. It is the honest
// outcome for "there is nothing in the bootloader on this port" and is worth
// distinguishing from a protocol fault, because the remedy is different: check
// the wiring and the BOOT0 line, not the firmware.
var ErrNotResponding = errors.New("flash: no bootloader answered on this port")

// NACKError is a command the bootloader refused.
type NACKError struct {
	Command Command
	Stage   string // "command", "address", "length", "data", "parameters"
}

func (e *NACKError) Error() string {
	return fmt.Sprintf("flash: bootloader refused %s at the %s stage", e.Command, e.Stage)
}

// ProtectedError reports flash the bootloader will not let us touch — readout
// protection (RDP), the state a board ships in when its maker did not want the
// firmware read back.
//
// It is separated from a plain NACK because the remedy is drastic and must be
// the operator's decision: clearing RDP mass-erases the part. A board in this
// state can still be written; it just cannot be verified by readback, and
// Waypoint will not silently accept an unverifiable flash.
type ProtectedError struct{ Command Command }

func (e *ProtectedError) Error() string {
	return fmt.Sprintf("flash: %s refused — this board's flash is readout-protected; "+
		"clearing that protection erases it completely, so it is not done automatically", e.Command)
}

// VerifyError is a readback that did not match what was written.
type VerifyError struct {
	Address    uint32
	Offset     int
	Want, Got  byte
	TotalBytes int
}

func (e *VerifyError) Error() string {
	return fmt.Sprintf("flash: verification failed %d bytes in (address 0x%08X): wrote 0x%02X, read back 0x%02X",
		e.Offset, e.Address+uint32(e.Offset), e.Want, e.Got)
}

// Timeouts bound each class of operation.
//
// They differ by two orders of magnitude, which is the whole reason they are
// named separately: an ACK to a command byte is a round trip, while a mass
// erase is the part standing still with its flash controller busy and its UART
// unattended. One timeout covering both would either declare a healthy erase
// dead or wait half a minute to notice an absent board.
type Timeouts struct {
	Sync      time.Duration // one autobaud attempt
	ACK       time.Duration // a command's acknowledgement
	Erase     time.Duration // a page erase
	MassErase time.Duration // the whole part
}

// DefaultTimeouts are sized for an STM32F103 at 115200 baud.
func DefaultTimeouts() Timeouts {
	return Timeouts{
		Sync:      2 * time.Second,
		ACK:       2 * time.Second,
		Erase:     15 * time.Second,
		MassErase: 40 * time.Second,
	}
}

func (t Timeouts) orDefaults() Timeouts {
	d := DefaultTimeouts()
	if t.Sync <= 0 {
		t.Sync = d.Sync
	}
	if t.ACK <= 0 {
		t.ACK = d.ACK
	}
	if t.Erase <= 0 {
		t.Erase = d.Erase
	}
	if t.MassErase <= 0 {
		t.MassErase = d.MassErase
	}
	return t
}

// Info is what the bootloader says about itself.
type Info struct {
	// Version is the bootloader's own version, BCD-packed: 0x22 is 2.2.
	Version byte
	// Commands is the list it advertises. This is read rather than assumed
	// because the erase command is not the same across parts (see EraseCommand).
	Commands []Command
	// ProductID identifies the die: 0x0410 is STM32F103 medium-density, the
	// part every board in the launch tier carries.
	ProductID uint16
}

// VersionString renders the BCD version as "2.2".
func (i Info) VersionString() string {
	return fmt.Sprintf("%d.%d", i.Version>>4, i.Version&0x0F)
}

// Supports reports whether the bootloader advertised a command.
func (i Info) Supports(c Command) bool {
	for _, got := range i.Commands {
		if got == c {
			return true
		}
	}
	return false
}

// EraseCommand returns the erase command this bootloader actually has.
//
// This is asked rather than hardcoded, and that is the point. Medium-density F1
// bootloaders (version 2.2 — every board in the launch tier) offer ERASE 0x43
// and do not understand 0x44; later parts, including the F4/F7 the fast-follow
// tier will bring, offer only EXTENDED_ERASE 0x44. A hardcoded choice works
// perfectly until the first board of the other kind, and then fails in a way
// that looks like a dead modem. Asking costs one round trip that has already
// happened by the time this is called.
func (i Info) EraseCommand() (Command, bool) {
	switch {
	case i.Supports(CmdExtendedErase):
		return CmdExtendedErase, true
	case i.Supports(CmdErase):
		return CmdErase, true
	}
	return 0, false
}

// Bootloader is a synchronised ROM bootloader on the other end of an
// io.ReadWriter.
type Bootloader struct {
	rw   io.ReadWriter
	t    Timeouts
	info Info

	now   func() time.Time
	pause func(time.Duration)
}

// Option configures a Bootloader. The clock and the retry pause are injectable
// so a test can drive a silent device without actually waiting.
type Option func(*Bootloader)

// WithClock replaces the clock and the inter-read pause.
func WithClock(now func() time.Time, pause func(time.Duration)) Option {
	return func(b *Bootloader) { b.now, b.pause = now, pause }
}

// Connect synchronises with the bootloader and reads its capabilities.
//
// The autobaud handshake has one wrinkle worth knowing, because it is exactly
// what a retry after an interrupted flash walks into: a bootloader that is
// ALREADY synchronised answers a second 0x7F with a NACK, not an ACK. That is
// not a failure — it means "I am already listening at this speed" — and
// treating it as one would make the second attempt at a flash fail where the
// first succeeded, which is precisely the recovery path RFC-0019 promises.
func Connect(rw io.ReadWriter, t Timeouts, opts ...Option) (*Bootloader, error) {
	b := &Bootloader{rw: rw, t: t.orDefaults(), now: time.Now, pause: func(d time.Duration) { time.Sleep(d) }}
	for _, o := range opts {
		o(b)
	}

	if err := b.sync(); err != nil {
		return nil, err
	}
	info, err := b.get()
	if err != nil {
		return nil, err
	}
	b.info = info
	if id, err := b.getID(); err == nil {
		b.info.ProductID = id
	}
	// GET_ID failing is not fatal. It is a nicety — it tells us which die we are
	// talking to, which is worth reporting and worth checking a firmware image
	// against — but a bootloader that answered GET is a bootloader we can write.
	return b, nil
}

// Info returns what the bootloader reported at connect time.
func (b *Bootloader) Info() Info { return b.info }

// sync sends the training byte until the part answers, or the window closes.
func (b *Bootloader) sync() error {
	deadline := b.now().Add(b.t.Sync)
	for {
		if _, err := b.rw.Write([]byte{initByte}); err != nil {
			return fmt.Errorf("flash: write sync byte: %w", err)
		}
		r, err := b.readByte(deadline)
		switch {
		case err == nil && (r == ack || r == nack):
			// NACK here means "already synchronised" — see Connect.
			return nil
		case err != nil && !errors.Is(err, ErrSilent):
			return err
		}
		if !b.now().Before(deadline) {
			return ErrNotResponding
		}
		b.pause(50 * time.Millisecond)
	}
}

// get issues GET: bootloader version and the command list.
func (b *Bootloader) get() (Info, error) {
	if err := b.command(CmdGet); err != nil {
		return Info{}, err
	}
	// ACK, N, version, N command bytes, ACK — where N counts the command bytes,
	// the version byte being the "+1" in the protocol's "number of bytes - 1".
	n, err := b.readByte(b.now().Add(b.t.ACK))
	if err != nil {
		return Info{}, err
	}
	version, err := b.readByte(b.now().Add(b.t.ACK))
	if err != nil {
		return Info{}, err
	}
	info := Info{Version: version}
	for i := 0; i < int(n); i++ {
		c, err := b.readByte(b.now().Add(b.t.ACK))
		if err != nil {
			return Info{}, err
		}
		info.Commands = append(info.Commands, Command(c))
	}
	if err := b.waitACK(CmdGet, "data", b.t.ACK); err != nil {
		return Info{}, err
	}
	return info, nil
}

// getID issues GET_ID: the product (die) identifier.
func (b *Bootloader) getID() (uint16, error) {
	if err := b.command(CmdGetID); err != nil {
		return 0, err
	}
	n, err := b.readByte(b.now().Add(b.t.ACK))
	if err != nil {
		return 0, err
	}
	id := uint16(0)
	for i := 0; i <= int(n); i++ {
		v, err := b.readByte(b.now().Add(b.t.ACK))
		if err != nil {
			return 0, err
		}
		id = id<<8 | uint16(v)
	}
	if err := b.waitACK(CmdGetID, "data", b.t.ACK); err != nil {
		return 0, err
	}
	return id, nil
}

// MassErase erases the whole of flash, using whichever erase command the
// bootloader advertised.
func (b *Bootloader) MassErase() error {
	cmd, ok := b.info.EraseCommand()
	if !ok {
		return errors.New("flash: this bootloader advertises no erase command")
	}
	if err := b.command(cmd); err != nil {
		return err
	}
	// The two commands spell "everything" differently: the older one with a
	// single 0xFF and a zero checksum, the newer with a two-byte 0xFFFF special
	// code whose checksum is the XOR of those bytes.
	var params []byte
	if cmd == CmdExtendedErase {
		params = []byte{0xFF, 0xFF, 0x00}
	} else {
		params = []byte{0xFF, 0x00}
	}
	if err := b.write(params); err != nil {
		return err
	}
	return b.waitACK(cmd, "parameters", b.t.MassErase)
}

// Write programs data at addr, in protocol-sized chunks, reporting progress as
// it goes. progress may be nil.
//
// The tail is padded to a word boundary with 0xFF — erased-flash value — because
// the bootloader refuses a write whose length is not a multiple of four and a
// firmware image's length is whatever the linker produced. Padding with 0xFF
// leaves those bytes indistinguishable from flash that was never written.
func (b *Bootloader) Write(addr uint32, data []byte, progress func(done, total int)) error {
	if len(data) == 0 {
		return errors.New("flash: nothing to write")
	}
	total := len(data)
	for off := 0; off < total; off += maxChunk {
		end := off + maxChunk
		if end > total {
			end = total
		}
		chunk := data[off:end]
		if pad := len(chunk) % 4; pad != 0 {
			padded := make([]byte, len(chunk), len(chunk)+4-pad)
			copy(padded, chunk)
			for i := 0; i < 4-pad; i++ {
				padded = append(padded, 0xFF)
			}
			chunk = padded
		}
		if err := b.writeChunk(addr+uint32(off), chunk); err != nil {
			return err
		}
		if progress != nil {
			progress(end, total)
		}
	}
	return nil
}

func (b *Bootloader) writeChunk(addr uint32, chunk []byte) error {
	if err := b.command(CmdWriteMemory); err != nil {
		return err
	}
	if err := b.address(CmdWriteMemory, addr); err != nil {
		return err
	}
	// Length byte is "count - 1"; the checksum covers it as well as the data.
	frame := make([]byte, 0, len(chunk)+2)
	frame = append(frame, byte(len(chunk)-1))
	frame = append(frame, chunk...)
	frame = append(frame, xor(frame))
	if err := b.write(frame); err != nil {
		return err
	}
	return b.waitACK(CmdWriteMemory, "data", b.t.ACK)
}

// Read reads n bytes from addr.
func (b *Bootloader) Read(addr uint32, n int) ([]byte, error) {
	out := make([]byte, 0, n)
	for len(out) < n {
		want := n - len(out)
		if want > maxChunk {
			want = maxChunk
		}
		chunk, err := b.readChunk(addr+uint32(len(out)), want)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
	}
	return out, nil
}

func (b *Bootloader) readChunk(addr uint32, n int) ([]byte, error) {
	if err := b.command(CmdReadMemory); err != nil {
		// A board whose flash is readout-protected refuses this command outright
		// while answering everything else normally. Saying "protected" rather
		// than "refused" is the difference between an operator who knows what to
		// do and one who files a bug.
		var ne *NACKError
		if errors.As(err, &ne) && ne.Stage == "command" {
			return nil, &ProtectedError{Command: CmdReadMemory}
		}
		return nil, err
	}
	if err := b.address(CmdReadMemory, addr); err != nil {
		return nil, err
	}
	count := byte(n - 1)
	if err := b.write([]byte{count, ^count}); err != nil {
		return nil, err
	}
	if err := b.waitACK(CmdReadMemory, "length", b.t.ACK); err != nil {
		return nil, err
	}
	out := make([]byte, n)
	deadline := b.now().Add(b.t.ACK)
	for i := range out {
		v, err := b.readByte(deadline)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// Verify reads flash back and compares it against want.
//
// This exists because an ACK to a write says the bootloader accepted the bytes,
// not that flash holds them. The difference shows up on a part whose flash is
// tired, on a board browning out under a marginal supply, and on any interrupted
// write — which is to say, in exactly the cases where an operator most needs to
// be told the truth. Readback is a second pass over the whole image and roughly
// doubles the time a flash takes; it is not optional for that reason.
func (b *Bootloader) Verify(addr uint32, want []byte, progress func(done, total int)) error {
	total := len(want)
	for off := 0; off < total; off += maxChunk {
		end := off + maxChunk
		if end > total {
			end = total
		}
		got, err := b.readChunk(addr+uint32(off), end-off)
		if err != nil {
			return err
		}
		for i := range got {
			if got[i] != want[off+i] {
				return &VerifyError{
					Address: addr, Offset: off + i,
					Want: want[off+i], Got: got[i], TotalBytes: total,
				}
			}
		}
		if progress != nil {
			progress(end, total)
		}
	}
	return nil
}

// Go jumps to addr, leaving the bootloader and running what was flashed.
//
// Waypoint calls this only when it has no other way to restart the board. On the
// GPIO path the exit is a reset with BOOT0 low, because that reproduces exactly
// what happens at the operator's next power-up: GO enters the application with
// the bootloader's clock and peripheral configuration still in place, which is a
// subtly different machine and a poor thing to validate a flash against.
func (b *Bootloader) Go(addr uint32) error {
	if err := b.command(CmdGo); err != nil {
		return err
	}
	return b.address(CmdGo, addr)
}

// --- framing ------------------------------------------------------------

// command writes a command byte and its complement, and waits for the ACK.
func (b *Bootloader) command(c Command) error {
	if err := b.write([]byte{byte(c), ^byte(c)}); err != nil {
		return err
	}
	return b.waitACK(c, "command", b.t.ACK)
}

// address writes a 32-bit address, MSB first, followed by its XOR checksum.
func (b *Bootloader) address(c Command, addr uint32) error {
	buf := []byte{byte(addr >> 24), byte(addr >> 16), byte(addr >> 8), byte(addr)}
	buf = append(buf, xor(buf))
	if err := b.write(buf); err != nil {
		return err
	}
	return b.waitACK(c, "address", b.t.ACK)
}

func (b *Bootloader) write(p []byte) error {
	if _, err := b.rw.Write(p); err != nil {
		return fmt.Errorf("flash: write: %w", err)
	}
	return nil
}

// waitACK reads one byte and insists it is an ACK.
func (b *Bootloader) waitACK(c Command, stage string, timeout time.Duration) error {
	v, err := b.readByte(b.now().Add(timeout))
	if err != nil {
		return err
	}
	switch v {
	case ack:
		return nil
	case nack:
		return &NACKError{Command: c, Stage: stage}
	}
	return fmt.Errorf("flash: %s at the %s stage: expected ACK or NACK, got 0x%02X", c, stage, v)
}

// readByte reads one byte, treating silence as "not yet" until the deadline.
//
// Silence is the normal state of the bus during an erase, so it cannot be an
// error until time runs out. Any OTHER read error is fatal immediately: a closed
// descriptor or a vanished USB device will not improve by being asked again, and
// retrying one would spin until the deadline for no reason.
func (b *Bootloader) readByte(deadline time.Time) (byte, error) {
	var buf [1]byte
	for {
		n, err := b.rw.Read(buf[:])
		switch {
		case n > 0:
			return buf[0], nil
		case err != nil && !errors.Is(err, ErrSilent):
			return 0, fmt.Errorf("flash: read: %w", err)
		}
		if !b.now().Before(deadline) {
			return 0, ErrSilent
		}
		b.pause(5 * time.Millisecond)
	}
}

// xor is the protocol's checksum: every byte of the frame folded together.
func xor(p []byte) byte {
	var c byte
	for _, v := range p {
		c ^= v
	}
	return c
}
