// Package dmrids reads the shared DMRIds.dat callsign<->ID lookup — the same file
// (/usr/local/etc/DMRIds.dat) every gateway already consumes (render.go "DMR Id
// Lookup" / NXDN "Id Lookup"). It is the SINGLE reader for that file in Waypoint:
// the mode-bus frame layer resolves a DMR/NXDN source ID to a callsign for the
// YSF side, and a YSF callsign back to a DMR/NXDN ID, through this one table (a
// bus adds no second lookup file — RFC-0003 §3).
//
// The file is the RadioID.net "DMRIds.dat" export the daemons use: whitespace-
// separated lines, first field the decimal DMR ID, second the callsign, the rest
// (name, city, …) ignored. Blank lines and '#'/';' comments are skipped. NXDN
// shares the DMR ID space, so the same table serves both.
package dmrids

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
)

// Table is an in-memory DMRIds.dat: id<->callsign both ways. It satisfies the
// frame layer's Resolver interface (CallsignForID / IDForCallsign) without the
// frame library importing any file I/O — the hub loads the table once and hands
// it in.
type Table struct {
	idToCall map[uint32]string
	callToID map[string]uint32
}

// New returns an empty table (every lookup misses). Useful as a null resolver in
// tests and when no DMRIds.dat is present.
func New() *Table {
	return &Table{idToCall: map[uint32]string{}, callToID: map[string]uint32{}}
}

// Parse reads DMRIds.dat content into a Table. It is tolerant: a malformed line
// (non-numeric id, too few fields) is skipped, never fatal — a bad line in a
// downloaded list must not take out the whole lookup. On a duplicate id the first
// wins (matching CDMRLookup's "insert if absent" behaviour).
func Parse(r io.Reader) (*Table, error) {
	t := New()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		id64, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil {
			continue
		}
		id := uint32(id64)
		call := strings.ToUpper(fields[1])
		if _, ok := t.idToCall[id]; !ok {
			t.idToCall[id] = call
		}
		if _, ok := t.callToID[call]; !ok {
			t.callToID[call] = id
		}
	}
	return t, sc.Err()
}

// Load reads and parses a DMRIds.dat file from disk. A missing file yields an
// empty table and no error (the lookup simply misses) so a node without the file
// still runs — every resolution falls back to the numeric id.
func Load(path string) (*Table, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return New(), nil
		}
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// CallsignForID returns the callsign for a DMR/NXDN id, or "" if unknown.
func (t *Table) CallsignForID(id uint32) string { return t.idToCall[id] }

// IDForCallsign returns the DMR/NXDN id for a callsign (case-insensitive), or 0 if
// unknown.
func (t *Table) IDForCallsign(cs string) uint32 {
	return t.callToID[strings.ToUpper(strings.TrimSpace(cs))]
}

// Len reports how many ids the table holds (for logging/diagnostics).
func (t *Table) Len() int { return len(t.idToCall) }

// Add inserts a mapping (first-wins, like Parse). Exposed so tests and the hub can
// seed synthetic entries without a file.
func (t *Table) Add(id uint32, callsign string) {
	call := strings.ToUpper(strings.TrimSpace(callsign))
	if _, ok := t.idToCall[id]; !ok {
		t.idToCall[id] = call
	}
	if _, ok := t.callToID[call]; !ok {
		t.callToID[call] = id
	}
}
