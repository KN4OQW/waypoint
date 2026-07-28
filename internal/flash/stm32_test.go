package flash

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func connect(t *testing.T, m *mcu) *Bootloader {
	t.Helper()
	c := newClock()
	b, err := Connect(m, Timeouts{}, c.opts())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return b
}

func TestConnectReadsWhatTheBootloaderAdvertises(t *testing.T) {
	m := newMCU()
	b := connect(t, m)

	info := b.Info()
	if got := info.VersionString(); got != "2.2" {
		t.Errorf("version = %q, want 2.2", got)
	}
	if info.ProductID != 0x0410 {
		t.Errorf("product ID = 0x%04X, want 0x0410 (STM32F103 medium-density)", info.ProductID)
	}
	if !info.Supports(CmdWriteMemory) || !info.Supports(CmdErase) {
		t.Errorf("commands = %v, want WRITE_MEMORY and ERASE among them", info.Commands)
	}
	if info.Supports(CmdExtendedErase) {
		t.Error("this bootloader must not claim EXTENDED_ERASE — an F1 does not have it")
	}
}

// A bootloader that is already synchronised NACKs a second training byte. This
// is the exact state a retry after an interrupted flash finds the board in, so
// treating it as a failure would break the recovery path RFC-0019 promises.
func TestConnectAcceptsNACKFromAnAlreadySynchronisedBootloader(t *testing.T) {
	m := newMCU()
	m.synced = true

	if _, err := Connect(m, Timeouts{}, newClock().opts()); err != nil {
		t.Fatalf("Connect on an already-synced bootloader: %v", err)
	}
}

func TestConnectReportsASilentPort(t *testing.T) {
	c := newClock()
	_, err := Connect(silentPort{}, Timeouts{Sync: time.Second}, c.opts())
	if !errors.Is(err, ErrNotResponding) {
		t.Fatalf("err = %v, want ErrNotResponding", err)
	}
}

// The erase command is read from what GET advertised, never assumed. An F1
// offers 0x43 and does not understand 0x44; later parts offer only 0x44.
func TestEraseUsesTheCommandTheBootloaderAdvertised(t *testing.T) {
	for _, tc := range []struct {
		name string
		mcu  *mcu
		want Command
	}{
		{"medium-density F1", newMCU(), CmdErase},
		{"later part", newMCUExtendedErase(), CmdExtendedErase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.mcu
			b := connect(t, m)
			if got, ok := b.Info().EraseCommand(); !ok || got != tc.want {
				t.Fatalf("EraseCommand = %v (ok=%v), want %v", got, ok, tc.want)
			}
			// Dirty a byte so a successful erase is observable.
			m.flash[42] = 0x00
			if err := b.MassErase(); err != nil {
				t.Fatalf("MassErase: %v", err)
			}
			if m.flash[42] != 0xFF {
				t.Errorf("flash[42] = 0x%02X after mass erase, want 0xFF", m.flash[42])
			}
		})
	}
}

func TestMassEraseRefusedWhenNoEraseCommandExists(t *testing.T) {
	m := newMCU()
	m.commands = []Command{CmdGet, CmdGetID, CmdWriteMemory} // no erase of either kind
	b := connect(t, m)

	if err := b.MassErase(); err == nil {
		t.Fatal("MassErase succeeded on a bootloader with no erase command")
	}
}

// The device validates every checksum and NACKs a bad one, so this also proves
// the framing (address XOR, length-plus-data XOR) is what a real part expects.
func TestWriteProgramsFlashInChunks(t *testing.T) {
	m := newMCU()
	b := connect(t, m)

	image := make([]byte, 700) // two full chunks and a short one
	for i := range image {
		image[i] = byte(i * 7)
	}

	var last int
	var calls int
	err := b.Write(m.base, image, func(done, total int) {
		calls++
		if done < last {
			t.Errorf("progress went backwards: %d after %d", done, last)
		}
		if total != len(image) {
			t.Errorf("progress total = %d, want %d", total, len(image))
		}
		last = done
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if last != len(image) {
		t.Errorf("final progress = %d, want %d", last, len(image))
	}
	if calls != 3 {
		t.Errorf("progress calls = %d, want 3 (256+256+188)", calls)
	}
	if !bytes.Equal(m.flash[:len(image)], image) {
		t.Error("flash contents do not match the image written")
	}
}

// A firmware image's length is whatever the linker produced, but the bootloader
// refuses a write that is not a multiple of four. The tail is padded with the
// erased-flash value so the padding is indistinguishable from untouched flash.
func TestWritePadsTheTailToAWordBoundary(t *testing.T) {
	m := newMCU()
	b := connect(t, m)

	image := []byte{1, 2, 3, 4, 5, 6} // six bytes: two short of a word
	if err := b.Write(m.base, image, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !bytes.Equal(m.flash[:6], image) {
		t.Errorf("flash[:6] = %v, want %v", m.flash[:6], image)
	}
	if m.flash[6] != 0xFF || m.flash[7] != 0xFF {
		t.Errorf("padding = 0x%02X 0x%02X, want 0xFF 0xFF", m.flash[6], m.flash[7])
	}
}

func TestVerifyPassesOnAGoodFlash(t *testing.T) {
	m := newMCU()
	b := connect(t, m)

	image := bytes.Repeat([]byte{0xA5, 0x5A, 0x12, 0x34}, 200)
	if err := b.Write(m.base, image, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := b.Verify(m.base, image, nil); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// An ACK says the bootloader accepted the bytes, not that flash holds them.
func TestVerifyCatchesAByteThatDidNotStick(t *testing.T) {
	m := newMCU()
	b := connect(t, m)

	image := bytes.Repeat([]byte{0xA5, 0x5A, 0x12, 0x34}, 200)
	if err := b.Write(m.base, image, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	m.flash[300] ^= 0xFF // a byte that did not take

	var ve *VerifyError
	err := b.Verify(m.base, image, nil)
	if !errors.As(err, &ve) {
		t.Fatalf("Verify err = %v, want a *VerifyError", err)
	}
	if ve.Offset != 300 {
		t.Errorf("VerifyError.Offset = %d, want 300", ve.Offset)
	}
	if ve.Got != image[300]^0xFF || ve.Want != image[300] {
		t.Errorf("VerifyError want/got = 0x%02X/0x%02X, want 0x%02X/0x%02X",
			ve.Want, ve.Got, image[300], image[300]^0xFF)
	}
}

// RDP boards refuse READ_MEMORY outright while answering everything else, and
// the remedy — clearing protection, which mass-erases the part — is drastic
// enough that it must be named rather than attempted.
func TestReadbackOnAProtectedBoardSaysSo(t *testing.T) {
	m := newMCU()
	m.rdp = true
	b := connect(t, m)

	var pe *ProtectedError
	if _, err := b.Read(m.base, 16); !errors.As(err, &pe) {
		t.Fatalf("Read err = %v, want a *ProtectedError", err)
	}
}

// RFC-0019's second acceptance criterion, in unit form: a flash interrupted
// mid-write leaves a board that is corrupt as a modem but reachable exactly as
// it was, so a retry completes. The board keeps its half-written flash across
// the power cut — that is the state a retry has to cope with.
func TestAnInterruptedWriteIsRecoverableByRetry(t *testing.T) {
	m := newMCU()
	m.dieAfter = 2 // power lost after the second chunk reaches flash

	image := make([]byte, 1024)
	for i := range image {
		image[i] = byte(i)
	}

	b := connect(t, m)
	if err := b.Write(m.base, image, nil); err == nil {
		t.Fatal("Write succeeded through a power cut")
	}
	if bytes.Equal(m.flash[:len(image)], image) {
		t.Fatal("the emulated part finished the write it was supposed to die during")
	}

	// The operator retries. BOOT0 is still high, so the board comes back in its
	// bootloader with flash exactly as the interrupted write left it.
	m.dieAfter = 0
	m.powerCycle()

	b2 := connect(t, m)
	if err := b2.Write(m.base, image, nil); err != nil {
		t.Fatalf("retry Write: %v", err)
	}
	if err := b2.Verify(m.base, image, nil); err != nil {
		t.Fatalf("retry Verify: %v", err)
	}
}

// A bootloader that refuses READ_MEMORY at the command byte is almost always a
// readout-protected part rather than one lacking the command, so that is what
// an operator is told. The rarer case reads the same on the wire.
func TestARefusedReadCommandIsReportedAsProtection(t *testing.T) {
	m := newMCU()
	m.commands = []Command{CmdGet, 0x01, CmdGetID, CmdWriteMemory, CmdErase} // no READ_MEMORY
	b := connect(t, m)

	// Without READ_MEMORY the device NACKs the command byte itself, which this
	// package reads as readout protection — the overwhelmingly common cause.
	var pe *ProtectedError
	if _, err := b.Read(m.base, 4); !errors.As(err, &pe) {
		t.Fatalf("Read err = %v, want a *ProtectedError", err)
	}
}

func TestWriteOutsideFlashIsRefused(t *testing.T) {
	m := newMCU()
	b := connect(t, m)

	var ne *NACKError
	// A megabyte in, on a 128 KB part: past the end of flash entirely. (RAM would
	// be the wrong test — the bootloader will happily write RAM.)
	err := b.Write(m.base+0x100000, make([]byte, 4), nil)
	if !errors.As(err, &ne) {
		t.Fatalf("Write err = %v, want a *NACKError", err)
	}
	if ne.Command != CmdWriteMemory {
		t.Errorf("NACKError.Command = %v, want WRITE_MEMORY", ne.Command)
	}
	// The stage is the diagnostic: refused at the address means the part would
	// not accept where we asked it to write, not that the data was malformed.
	if ne.Stage != "address" {
		t.Errorf("NACKError.Stage = %q, want %q", ne.Stage, "address")
	}
}

func TestGoLeavesTheBootloader(t *testing.T) {
	m := newMCU()
	b := connect(t, m)

	if err := b.Go(m.base); err != nil {
		t.Fatalf("Go: %v", err)
	}
	if !m.running {
		t.Error("the part is still in its bootloader after GO")
	}
}

func TestEmptyWriteIsRefused(t *testing.T) {
	m := newMCU()
	b := connect(t, m)

	if err := b.Write(m.base, nil, nil); err == nil {
		t.Fatal("Write accepted an empty image")
	}
}
