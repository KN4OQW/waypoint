package privhelper

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

// Property 1: frames round-trip, and several in a row stay in sync — the whole
// job of the length prefix.
func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer

	want := []Request{}
	for i, m := range Methods() {
		req, err := NewRequest(uint64(i+1), m, map[string]string{"n": string(m)})
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteFrame(&buf, req); err != nil {
			t.Fatal(err)
		}
		want = append(want, req)
	}

	for i, w := range want {
		var got Request
		if err := ReadFrame(&buf, &got); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if got.ID != w.ID || got.Method != w.Method || got.Version != ProtocolVersion {
			t.Errorf("frame %d = %+v, want %+v", i, got, w)
		}
		if string(got.Params) != string(w.Params) {
			t.Errorf("frame %d params = %s, want %s", i, got.Params, w.Params)
		}
	}

	// The stream is drained; a further read is a clean close.
	var extra Request
	if err := ReadFrame(&buf, &extra); !errors.Is(err, io.EOF) {
		t.Errorf("read past the last frame = %v, want io.EOF", err)
	}
}

// Property 2: an oversized length header is refused before anything is allocated.
// The length prefix is attacker-supplied, and a root process must not be talked
// into a huge allocation by four bytes.
func TestReadFrameRefusesOversizedLengthWithoutAllocating(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 4_000_000_000) // ~4 GiB claimed
	// Deliberately only the header and a few bytes: if ReadFrame tried to read the
	// body it would block or fail on EOF rather than reject the length outright.
	r := bytes.NewReader(append(hdr[:], 'x', 'y', 'z'))

	var got Request
	err := ReadFrame(r, &got)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("ReadFrame = %v, want ErrFrameTooLarge", err)
	}
	if !strings.Contains(err.Error(), "4000000000") {
		t.Errorf("the error does not report the claimed size: %v", err)
	}
}

// Property 3: a frame this end would produce that is too large is refused on the
// way out too, so a peer never has to defend against us.
func TestWriteFrameRefusesOversizedBody(t *testing.T) {
	req, err := NewRequest(1, MethodInstallSSHKey, map[string]string{
		"key": strings.Repeat("A", MaxFrameSize+1),
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, req); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("WriteFrame = %v, want ErrFrameTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a rejected frame still wrote %d bytes", buf.Len())
	}
}

// Property 4: a zero-length frame is never valid.
func TestReadFrameRejectsEmptyFrame(t *testing.T) {
	var hdr [4]byte // length 0
	if err := ReadFrame(bytes.NewReader(hdr[:]), &Request{}); !errors.Is(err, ErrEmptyFrame) {
		t.Errorf("ReadFrame = %v, want ErrEmptyFrame", err)
	}
}

// Property 5: a peer that dies mid-frame is distinguishable from one that closed
// cleanly between frames. A server treats the first as an error and the second as
// a normal disconnect.
func TestReadFrameDistinguishesTruncationFromCleanClose(t *testing.T) {
	var buf bytes.Buffer
	req, err := NewRequest(1, MethodLockRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&buf, req); err != nil {
		t.Fatal(err)
	}
	full := buf.Bytes()

	// Cut the body short.
	truncated := full[:len(full)-3]
	if err := ReadFrame(bytes.NewReader(truncated), &Request{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated frame = %v, want io.ErrUnexpectedEOF", err)
	}
	// Cut the header short.
	if err := ReadFrame(bytes.NewReader(full[:2]), &Request{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated header = %v, want io.ErrUnexpectedEOF", err)
	}
	// Nothing at all is a clean close.
	if err := ReadFrame(bytes.NewReader(nil), &Request{}); !errors.Is(err, io.EOF) {
		t.Errorf("empty stream = %v, want io.EOF", err)
	}
}

// countingWriter records how many Write calls it received.
type countingWriter struct {
	writes int
	buf    bytes.Buffer
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.writes++
	return c.buf.Write(p)
}

// Property 6: a frame goes out in exactly one Write. Two writes would let a
// concurrent writer interleave between the header and the body and desynchronise
// the stream — a failure that only appears under load.
func TestWriteFrameIsASingleWrite(t *testing.T) {
	var w countingWriter
	req, err := NewRequest(7, MethodSetHostname, SetHostnameRequest{Hostname: "hs-shack"})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&w, req); err != nil {
		t.Fatal(err)
	}
	if w.writes != 1 {
		t.Errorf("WriteFrame made %d writes, want 1", w.writes)
	}
	if got := binary.BigEndian.Uint32(w.buf.Bytes()[:4]); int(got) != w.buf.Len()-4 {
		t.Errorf("length prefix %d does not match the %d-byte body", got, w.buf.Len()-4)
	}
}

// Property 7: errors cross the socket classified, and come back out as Go errors
// a caller can errors.As and switch on.
func TestErrorResponseRoundTrip(t *testing.T) {
	resp := NewErrorResponse(42, Errorf(CodeConflict, "no recovery account exists"))
	if resp.ID != 42 {
		t.Errorf("ID = %d, wanted the request's id echoed back", resp.ID)
	}

	var buf bytes.Buffer
	if err := WriteFrame(&buf, resp); err != nil {
		t.Fatal(err)
	}
	var got Response
	if err := ReadFrame(&buf, &got); err != nil {
		t.Fatal(err)
	}
	if got.Error == nil {
		t.Fatal("the error did not survive the round trip")
	}
	if CodeOf(got.Error) != CodeConflict {
		t.Errorf("code = %q, want %q", CodeOf(got.Error), CodeConflict)
	}
	if !strings.Contains(got.Error.Error(), "no recovery account") {
		t.Errorf("message lost: %v", got.Error)
	}
	if got.Result != nil {
		t.Error("an error response also carried a result")
	}
}

// Property 8: an error with no code of its own still crosses the wire
// classified, so nothing arrives at the caller unlabelled.
func TestUncodedErrorBecomesInternal(t *testing.T) {
	resp := NewErrorResponse(1, errors.New("useradd exited 1"))
	if resp.Error == nil || resp.Error.Code != CodeInternal {
		t.Fatalf("Error = %+v, want CodeInternal", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "useradd") {
		t.Errorf("the original message was lost: %+v", resp.Error)
	}
	if CodeOf(nil) != "" {
		t.Error("CodeOf(nil) should be empty, not a code")
	}
	if CodeOf(errors.New("plain")) != CodeInternal {
		t.Error("an uncoded error should classify as internal")
	}
}

// Property 9: a version mismatch — which an A/B rollback can produce, since
// waypointd and the helper are separate artifacts — reports itself plainly
// instead of failing as a confusing decode error inside a request.
func TestCheckVersion(t *testing.T) {
	if err := CheckVersion(ProtocolVersion); err != nil {
		t.Errorf("the current version was rejected: %v", err)
	}
	err := CheckVersion(ProtocolVersion + 1)
	if CodeOf(err) != CodeProtocol {
		t.Fatalf("code = %q, want %q", CodeOf(err), CodeProtocol)
	}
	if !strings.Contains(err.Error(), "this build speaks") {
		t.Errorf("the version error does not say what this build speaks: %v", err)
	}
}

// Property 10: every method has a distinct name and the list is complete, so a
// server's dispatch table and this protocol cannot silently disagree.
func TestMethodsAreDistinct(t *testing.T) {
	seen := map[Method]bool{}
	for _, m := range Methods() {
		if m == "" {
			t.Error("an empty method name is in the list")
		}
		if seen[m] {
			t.Errorf("method %q appears twice", m)
		}
		seen[m] = true
	}
	if len(seen) != 13 {
		t.Errorf("Methods() has %d entries; update this count deliberately when the protocol grows", len(seen))
	}
}

// Property 11: params survive as raw JSON, so a server can decode them into the
// concrete request type after dispatching on the method.
func TestParamsDecodeIntoRequestType(t *testing.T) {
	want := CreateRecoveryUserRequest{Username: "rescue", Password: "correct horse", Sudo: true}
	req, err := NewRequest(1, MethodCreateRecoveryUser, want)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteFrame(&buf, req); err != nil {
		t.Fatal(err)
	}
	var wire Request
	if err := ReadFrame(&buf, &wire); err != nil {
		t.Fatal(err)
	}
	if err := CheckVersion(wire.Version); err != nil {
		t.Fatal(err)
	}

	var got CreateRecoveryUserRequest
	if err := json.Unmarshal(wire.Params, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("params = %+v, want %+v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("a request that validated before the wire failed after it: %v", err)
	}
}
