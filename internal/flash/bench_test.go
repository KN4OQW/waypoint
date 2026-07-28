//go:build bench

package flash

// Hardware smoke tests, built only with `-tags bench` and run on a node with a
// modem actually fitted. They are the bridge between the emulated part the rest
// of this package is tested against and a real one: everything here talks to
// the physical board, and none of it writes to flash.
//
//	GOOS=linux GOARCH=arm GOARM=6 go test -tags bench -c ./internal/flash
//	scp flash.test <node>:  &&  ssh <node> 'sudo systemctl stop waypoint-mmdvm && sudo ./flash.test -test.v'
//
// Stopping MMDVM-Host first is the caller's job on purpose. These are run by
// hand on a bench, and a test binary that quietly takes a node off the air is a
// worse idea than one that says it could not open the port.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/modem"
)

func benchPort() string {
	if p := os.Getenv("WAYPOINT_BENCH_PORT"); p != "" {
		return p
	}
	return "/dev/ttyAMA0"
}

// TestBenchChipSelection proves the label matching picks the header controller
// on real hardware. On a Pi 3 running kernel 6.12 the header chip's sysfs base
// is 512 and the raspberrypi-exp-gpio expander is also present with lines at
// the same offsets — the two conditions that break every hardcoded flashing
// script in the hobby.
func TestBenchChipSelection(t *testing.T) {
	chips, err := enumerateChips("/dev")
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	for _, c := range chips {
		t.Logf("%s: label=%q lines=%d", c.path, c.label, c.lines)
	}
	chip, err := pickChip(chips,
		func(c chardevChip) string { return c.label },
		func(c chardevChip) int { return c.lines },
		headerChips, DefaultResetLine)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	t.Logf("chose %s (%s), %d lines", chip.path, chip.label, chip.lines)
	if chip.label == "raspberrypi-exp-gpio" {
		t.Fatal("picked the GPIO expander — its lines 20 and 21 are not the modem's")
	}
}

// TestBenchClaimLines claims BOOT0 and reset without resetting the board.
// Claiming must be safe on a node that is on the air: the kernel drives a
// freshly claimed output low, so a naive request would assert reset.
func TestBenchClaimLines(t *testing.T) {
	lines, err := OpenLines(LineConfig{})
	if err != nil {
		t.Fatalf("OpenLines: %v", err)
	}
	defer lines.Close()
	t.Logf("claimed: %s", lines.Describe())
}

// TestBenchReadBootloader is the whole GPIO path, minus the write: reset the
// board into its ROM bootloader, synchronise at 8E1, and ask what it is. It
// proves the reset sequence, the parity, the autobaud and the AN3155 framing
// against a real STM32 rather than an emulated one.
//
// Nothing is erased and nothing is written. The board is returned to its
// firmware at the end, which is also what happens if this test fails.
func TestBenchReadBootloader(t *testing.T) {
	ctx := context.Background()

	lines, err := OpenLines(LineConfig{})
	if err != nil {
		t.Fatalf("OpenLines: %v", err)
	}
	defer lines.Close()
	t.Logf("lines: %s", lines.Describe())

	if err := lines.EnterBootloader(ctx); err != nil {
		t.Fatalf("EnterBootloader: %v", err)
	}
	defer func() {
		if err := lines.EnterApplication(ctx); err != nil {
			t.Errorf("returning the board to its firmware: %v", err)
		}
	}()

	port, err := modem.OpenPort(benchPort(), 115200, modem.ParityEven, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("open %s: %v", benchPort(), err)
	}
	defer port.Close()

	bl, err := Connect(silenceAdapterFor(port), DefaultTimeouts())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	info := bl.Info()
	t.Logf("bootloader version %s, part 0x%04X", info.VersionString(), info.ProductID)
	t.Logf("commands: %v", info.Commands)

	erase, ok := info.EraseCommand()
	if !ok {
		t.Fatal("this bootloader advertises no erase command")
	}
	t.Logf("erase command: %v", erase)

	// Read the first words back. On a board with firmware this is the vector
	// table: initial stack pointer then reset vector, both pointing into the
	// part's own memory map — a cheap sanity check that we are reading flash
	// rather than noise.
	head, err := bl.Read(0x08000000, 8)
	if err != nil {
		t.Fatalf("read the vector table: %v", err)
	}
	sp := uint32(head[0]) | uint32(head[1])<<8 | uint32(head[2])<<16 | uint32(head[3])<<24
	pc := uint32(head[4]) | uint32(head[5])<<8 | uint32(head[6])<<16 | uint32(head[7])<<24
	t.Logf("vector table: initial SP 0x%08X, reset vector 0x%08X", sp, pc)
	if sp>>24 != 0x20 {
		t.Errorf("initial stack pointer 0x%08X is not in SRAM — this does not look like flashed firmware", sp)
	}
	if pc>>24 != 0x08 {
		t.Errorf("reset vector 0x%08X is not in flash", pc)
	}
}

// silenceAdapterFor is the same mapping flash.go applies when it opens a real
// port: the serial layer's silence becomes this package's.
func silenceAdapterFor(rw interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
}) silenceAdapter {
	return silenceAdapter{rw: rw}
}

// TestBenchModemAnswers runs detection against the board after the bootloader
// tests have had it. It is the closing check of a bench run: the excursion into
// the ROM bootloader must leave the modem exactly as it was, and the identity
// string coming back is the proof.
func TestBenchModemAnswers(t *testing.T) {
	d := &modem.Detector{} // no Holder: the caller has already stopped MMDVM-Host
	res, err := d.Detect(context.Background(), false)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	for _, s := range res.Scanned {
		t.Logf("%s: %s %s", s.Port, s.Outcome, s.Detail)
	}
	if res.Identity == nil {
		t.Fatal("no modem answered after the bootloader tests")
	}
	id := res.Identity
	t.Logf("identity: %s", id.Description)
	t.Logf("board=%s firmware=%s tcxo=%d duplex=%v git=%s",
		id.BoardID, id.Firmware, id.TCXOHz, id.Duplex, id.GitID)
}
