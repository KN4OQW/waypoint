package dmrids

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
)

// Record is one DMRIds.dat row, as the settings page needs it: the ID, the
// callsign exactly as the table spells it, and whatever the row carries after the
// callsign (the operator's first name, in the RadioID export).
//
// Name exists only to tell two rows apart. A callsign with several IDs is common:
// in the 310,364-row August 2026 export, 20,765 distinct callsigns carry more than
// one ID, and the worst (ZL6AREC) carries seventy. Consecutive IDs like 3101900
// and 3101901 side by side tell an operator nothing, and the name at least
// confirms the block is theirs. It is not identity: nothing in Waypoint decides
// anything from it.
//
// Note what the export does NOT contain: every one of those 310,364 IDs is seven
// digits. The nine-digit hotspot IDs an operator derives from their base ID are
// not issued rows and will never be found here, so a lookup can offer the base ID
// and nothing else — the operator still appends their own two digits.
type Record struct {
	ID       uint32 `json:"id"`
	Callsign string `json:"callsign"`
	Name     string `json:"name,omitempty"`
}

// maxNameLen caps the trailing text a Record carries, in bytes. The export's names
// are first names; anything longer means the file is not what we think it is, and
// a corrupt megabyte-long line must not become a megabyte-long JSON field.
const maxNameLen = 64

// LookupCallsign returns every DMRIds.dat row at path whose callsign is cs,
// case-insensitively, in file order (which is ascending by ID — the export is
// sorted that way). The bool reports that the scan stopped at limit with matches
// left unread, so a caller can say so rather than quietly showing a prefix. A
// limit of zero or less means no limit.
//
// It streams rather than going through Load, and that is the whole point of this
// function existing beside a perfectly good Table:
//
//   - Table cannot answer this question. It is first-wins in both directions
//     (Parse), which is right for the frame layer — one ID resolves to one
//     callsign — and exactly wrong here, where the several IDs behind one
//     callsign ARE the answer the operator has to choose from.
//   - Table costs memory this does not have. Loading the 6.6 MB August 2026
//     export measured 35.5 MB of live heap — two maps and 620k strings over 310k
//     rows — on a node whose smallest supported board (Pi Zero) has 512 MB total.
//     A scan holds one 64 KB buffer and the matched rows: measured at net zero
//     retained heap for a 61-match callsign.
//
// The scan itself measured 11.8 ms over that same export on a Ryzen 7 5700G with
// the file in page cache (BenchmarkLookupCallsign). A Pi Zero reading from SD will
// be some multiple of that, and it is still an operator pressing a button and
// waiting, not anything on a request path — so the linear scan buys its simplicity
// honestly and no prefix index is built. If this ever moves somewhere hot, that is
// the moment to measure again.
//
// A missing file yields no records and no error, matching Load: a node that has
// never reached the internet has no table, and "no suggestions" is the right
// answer there, not a failure.
func LookupCallsign(path, cs string, limit int) ([]Record, bool, error) {
	want := normalizeCallsign(cs)
	if want == "" {
		return nil, false, nil
	}
	out, truncated, err := scanCallsign(path, want, limit)
	if err != nil || len(out) > 0 {
		return out, truncated, err
	}
	// A portable/mobile suffix is on the operator's callsign, not on their DMR ID:
	// RadioID issues against the base call, and only 12 of the 310,364 rows in the
	// export carry a suffix at all. So an operator whose General callsign reads
	// KN4OQW/P gets their base row rather than nothing. Only ever as a fallback —
	// a bare query must not start matching other people's /M and /P rows.
	if base := baseCallsign(want); base != want {
		return scanCallsign(path, base, limit)
	}
	return out, truncated, nil
}

func scanCallsign(path, want string, limit int) ([]Record, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	// Close's error is deliberately dropped: this handle is read-only, so there is
	// no buffered write for Close to report failing, and the scan's own result is
	// already in hand by the time it runs.
	defer func() { _ = f.Close() }()

	wantB := []byte(want)
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		// sc.Bytes(), not sc.Text(): Text allocates a string for every one of the
		// 310k lines, and all but a few of them are thrown away immediately.
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] == '#' || line[0] == ';' {
			continue
		}
		idField, rest := nextField(line)
		callField, nameRest := nextField(rest)
		if len(callField) == 0 || !bytes.EqualFold(callField, wantB) {
			continue
		}
		id64, err := strconv.ParseUint(string(idField), 10, 32)
		if err != nil {
			continue // same tolerance as Parse: a bad line is skipped, never fatal
		}
		if limit > 0 && len(out) >= limit {
			return out, true, nil
		}
		out = append(out, Record{
			ID:       uint32(id64),
			Callsign: strings.ToUpper(string(callField)),
			Name:     collapse(nameRest),
		})
	}
	return out, false, sc.Err()
}

// normalizeCallsign is the shape a query and a table row are compared in: trimmed
// and upper-cased. It does NOT strip a suffix — LookupCallsign decides when to do
// that, and only as a fallback.
func normalizeCallsign(cs string) string {
	return strings.ToUpper(strings.TrimSpace(cs))
}

// baseCallsign drops a portable/mobile suffix: KN4OQW/P -> KN4OQW, GB3HB-L ->
// GB3HB. A callsign with neither separator comes back unchanged.
func baseCallsign(cs string) string {
	if i := strings.IndexAny(cs, "/-"); i > 0 {
		return cs[:i]
	}
	return cs
}

// nextField splits off the leading whitespace-delimited field, returning it and
// the remainder. It matches what strings.Fields would do for the first two fields
// of a row, without allocating: the export is tab-separated, but older and
// hand-edited copies are not, and Parse has always been tolerant of both.
func nextField(b []byte) (field, rest []byte) {
	i := 0
	for i < len(b) && isSpace(b[i]) {
		i++
	}
	start := i
	for i < len(b) && !isSpace(b[i]) {
		i++
	}
	return b[start:i], b[i:]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f'
}

// collapse renders a row's trailing text as a single line: runs of whitespace
// become one space, so a tab-separated "Clint\tMilton FL\tUnited States" from an
// older export reads as one string in the picker instead of carrying tabs into
// the DOM. Truncation is byte-wise but backs off any partial UTF-8 sequence, so a
// non-ASCII name is cut short rather than turned into replacement characters.
func collapse(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) <= maxNameLen {
		return s
	}
	cut := maxNameLen
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}
