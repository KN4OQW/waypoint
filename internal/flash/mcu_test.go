package flash

import (
	"time"
)

// An emulated STM32 ROM bootloader.
//
// This is a state machine rather than a recorded script, and the difference
// matters for one test in particular: proving that an interrupted flash is
// recoverable by retry (RFC-0019's second acceptance criterion) means modelling
// a part whose flash is half-written and which then boots into its bootloader
// again with those contents intact. A script cannot express that; a device that
// holds state can.
//
// It validates every checksum the protocol defines and NACKs when one is wrong,
// so the tests do not merely assert that this package's framing matches this
// file's expectations — a device that accepted anything would prove nothing.
type mcu struct {
	flash    []byte // contents from base upward, 0xFF where erased
	base     uint32
	synced   bool
	version  byte
	commands []Command
	id       uint16
	rdp      bool // readout-protected: refuses READ_MEMORY outright

	out []byte // queued for the host to read

	// One-shot parser state: the number of bytes the current stage still wants,
	// and what to do once they arrive.
	need    int
	handler func([]byte)
	buf     []byte
	cmd     Command

	chunks   int  // WRITE_MEMORY chunks applied
	dieAfter int  // >0: go silent after that many chunks (a power cut mid-write)
	dead     bool // stopped answering
	running  bool // left the bootloader via GO
}

const testFlashSize = 128 * 1024

// newMCU is a stock STM32F103 medium-density bootloader: version 2.2, ERASE
// 0x43 (not the extended form), product ID 0x0410. This is the part on every
// board in the launch tier, including the bench Dual Hat.
func newMCU() *mcu {
	m := &mcu{
		base:    0x08000000,
		flash:   make([]byte, testFlashSize),
		version: 0x22,
		id:      0x0410,
		commands: []Command{
			CmdGet, 0x01, CmdGetID, CmdReadMemory, CmdGo, CmdWriteMemory,
			CmdErase, 0x63, 0x73, 0x82, CmdReadoutUnprotect,
		},
	}
	m.eraseAll()
	return m
}

// newMCUExtendedErase is a later-generation bootloader: EXTENDED_ERASE 0x44 and
// no 0x43 at all. The F4/F7 parts of the fast-follow tier (#25) answer this way.
func newMCUExtendedErase() *mcu {
	m := newMCU()
	m.version = 0x31
	m.id = 0x0419
	m.commands = []Command{CmdGet, 0x01, CmdGetID, CmdReadMemory, CmdGo, CmdWriteMemory, CmdExtendedErase}
	return m
}

func (m *mcu) eraseAll() {
	for i := range m.flash {
		m.flash[i] = 0xFF
	}
}

func (m *mcu) supports(c Command) bool {
	for _, got := range m.commands {
		if got == c {
			return true
		}
	}
	return false
}

// powerCycle models the board being reset back into its bootloader with flash
// left exactly as an interrupted write left it.
func (m *mcu) powerCycle() {
	m.dead, m.synced, m.running = false, false, false
	m.need, m.handler, m.buf, m.out = 0, nil, nil, nil
	m.chunks = 0
}

func (m *mcu) Write(p []byte) (int, error) {
	for _, b := range p {
		m.feed(b)
	}
	return len(p), nil
}

func (m *mcu) Read(p []byte) (int, error) {
	if len(m.out) == 0 {
		return 0, ErrSilent
	}
	n := copy(p, m.out)
	m.out = m.out[n:]
	return n, nil
}

func (m *mcu) emit(b ...byte) { m.out = append(m.out, b...) }

// expect queues the next stage: n more bytes, then h.
func (m *mcu) expect(n int, h func([]byte)) {
	m.need, m.handler, m.buf = n, h, nil
}

func (m *mcu) reset() { m.need, m.handler, m.buf = 0, nil, nil }

func (m *mcu) feed(b byte) {
	if m.dead || m.running {
		return
	}
	if m.need > 0 {
		m.buf = append(m.buf, b)
		if len(m.buf) < m.need {
			return
		}
		h, buf := m.handler, m.buf
		m.reset()
		h(buf)
		return
	}
	if b == initByte {
		// An already-synchronised bootloader NACKs a second training byte. That
		// is not an error and Connect must not treat it as one.
		if m.synced {
			m.emit(nack)
			return
		}
		m.synced = true
		m.emit(ack)
		return
	}
	m.cmd = Command(b)
	m.expect(1, m.checkComplement)
}

func (m *mcu) checkComplement(buf []byte) {
	if buf[0] != ^byte(m.cmd) {
		m.emit(nack)
		return
	}
	if !m.supports(m.cmd) {
		m.emit(nack)
		return
	}
	switch m.cmd {
	case CmdGet:
		m.emit(ack, byte(len(m.commands)), m.version)
		for _, c := range m.commands {
			m.emit(byte(c))
		}
		m.emit(ack)
	case CmdGetID:
		m.emit(ack, 0x01, byte(m.id>>8), byte(m.id), ack)
	case CmdReadMemory:
		if m.rdp {
			m.emit(nack)
			return
		}
		m.emit(ack)
		m.expect(5, m.readAddress)
	case CmdWriteMemory:
		m.emit(ack)
		m.expect(5, m.writeAddress)
	case CmdErase:
		m.emit(ack)
		m.expect(1, m.eraseParams)
	case CmdExtendedErase:
		m.emit(ack)
		m.expect(2, m.extendedEraseParams)
	case CmdGo:
		m.emit(ack)
		m.expect(5, m.goAddress)
	default:
		m.emit(nack)
	}
}

// address decodes the protocol's 4-byte-plus-checksum address frame.
func (m *mcu) address(buf []byte) (uint32, bool) {
	if xor(buf[:4]) != buf[4] {
		return 0, false
	}
	return uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3]), true
}

func (m *mcu) offsetOf(addr uint32, n int) (int, bool) {
	if addr < m.base {
		return 0, false
	}
	off := int(addr - m.base)
	if off+n > len(m.flash) {
		return 0, false
	}
	return off, true
}

func (m *mcu) readAddress(buf []byte) {
	addr, ok := m.address(buf)
	if _, inRange := m.offsetOf(addr, 1); !ok || !inRange {
		m.emit(nack)
		return
	}
	m.emit(ack)
	m.expect(2, func(lb []byte) {
		if lb[1] != ^lb[0] {
			m.emit(nack)
			return
		}
		n := int(lb[0]) + 1
		off, ok := m.offsetOf(addr, n)
		if !ok {
			m.emit(nack)
			return
		}
		m.emit(ack)
		m.emit(m.flash[off : off+n]...)
	})
}

func (m *mcu) writeAddress(buf []byte) {
	addr, ok := m.address(buf)
	// AN3155: the address stage is acknowledged only if the address is valid and
	// its checksum is correct — an out-of-range address is refused here, before
	// any data is sent, not after.
	if _, inRange := m.offsetOf(addr, 1); !ok || !inRange {
		m.emit(nack)
		return
	}
	m.emit(ack)
	m.expect(1, func(lb []byte) {
		n := int(lb[0]) + 1
		// n data bytes plus the checksum, which covers the length byte too.
		m.expect(n+1, func(data []byte) {
			if xor(append([]byte{lb[0]}, data[:n]...)) != data[n] {
				m.emit(nack)
				return
			}
			if n%4 != 0 {
				m.emit(nack) // the real bootloader refuses a non-word-multiple
				return
			}
			off, ok := m.offsetOf(addr, n)
			if !ok {
				m.emit(nack)
				return
			}
			copy(m.flash[off:off+n], data[:n])
			m.chunks++
			if m.dieAfter > 0 && m.chunks >= m.dieAfter {
				// The power cut: this chunk reached flash, and nothing after it
				// does. No ACK is sent, which is what the host would observe.
				m.dead = true
				return
			}
			m.emit(ack)
		})
	})
}

func (m *mcu) eraseParams(buf []byte) {
	if buf[0] == 0xFF { // global erase, checksum follows
		m.expect(1, func(ck []byte) {
			if ck[0] != 0x00 {
				m.emit(nack)
				return
			}
			m.eraseAll()
			m.emit(ack)
		})
		return
	}
	m.expect(int(buf[0])+2, func(pages []byte) { m.emit(ack) })
}

func (m *mcu) extendedEraseParams(buf []byte) {
	if buf[0] == 0xFF && buf[1] == 0xFF { // mass erase
		m.expect(1, func(ck []byte) {
			if ck[0] != xor(buf) {
				m.emit(nack)
				return
			}
			m.eraseAll()
			m.emit(ack)
		})
		return
	}
	m.emit(nack)
}

func (m *mcu) goAddress(buf []byte) {
	if _, ok := m.address(buf); !ok {
		m.emit(nack)
		return
	}
	m.emit(ack)
	m.running = true
}

// --- a clock that does not sleep -----------------------------------------

// testClock advances only when the code under test pauses, so a two-second
// timeout costs a test nothing.
type testClock struct{ t time.Time }

func newClock() *testClock { return &testClock{t: time.Unix(1_800_000_000, 0)} }

func (c *testClock) now() time.Time        { return c.t }
func (c *testClock) pause(d time.Duration) { c.t = c.t.Add(d) }
func (c *testClock) opts() Option          { return WithClock(c.now, c.pause) }

// silentPort answers nothing at all — a port with no board behind it.
type silentPort struct{}

func (silentPort) Write(p []byte) (int, error) { return len(p), nil }
func (silentPort) Read([]byte) (int, error)    { return 0, ErrSilent }

// Close satisfies io.ReadWriteCloser so the emulated part can stand in for a
// serial port in the engine's tests. Closing a port does not power-cycle a
// board, so it deliberately changes nothing.
func (m *mcu) Close() error { return nil }
