package dmrdata

import (
	"bufio"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Loading the burst fixtures under testdata. Two kinds live there and they prove
// different things:
//
//   - golden-*.txt were produced by g4klx MMDVMHost's own encoders (via the bench
//     harness's burst tool) for the exact construction proven on air. A byte
//     difference against these means this port has diverged from the C++ it was
//     ported from.
//   - capture-*.txt are real bursts recorded off the bench modem loopback. They
//     prove the decoder handles what actually arrives, including the framing this
//     package deliberately does not transmit.
//
// The DMR IDs in the capture fixtures are the maintainer's own and BrandMeister's;
// they are the addresses those messages really carried, and rewriting them would
// invalidate every checksum in the file.

type fixtureBurst struct {
	DataType DataType
	Payload  []byte
}

func loadFixture(t testing.TB, name string) []fixtureBurst {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	var out []fixtureBurst
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		dt, rest, ok := strings.Cut(text, " ")
		if !ok {
			t.Fatalf("%s:%d: want '<data-type> <hex>'", name, line)
		}
		n, err := strconv.ParseUint(dt, 16, 8)
		if err != nil {
			t.Fatalf("%s:%d: bad data type %q: %v", name, line, dt, err)
		}
		payload, err := hex.DecodeString(strings.TrimSpace(rest))
		if err != nil || len(payload) != BurstBytes {
			t.Fatalf("%s:%d: want %d bytes of hex, got %d (%v)", name, line, BurstBytes, len(payload), err)
		}
		out = append(out, fixtureBurst{DataType: DataType(n), Payload: payload})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("%s: no bursts", name)
	}
	return out
}

// allFixtures is every fixture file, for the checks that should hold over all of
// them — round-trip identity above all.
var allFixtures = []string{
	"golden-hello.txt",
	"golden-multiblock.txt",
	"golden-unicode.txt",
	"golden-maxlen.txt",
	"capture-brandmeister.txt",
	"capture-radio-etsi.txt",
	"capture-radio-tms.txt",
	"capture-nosync.txt",
}
