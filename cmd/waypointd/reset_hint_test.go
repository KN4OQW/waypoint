package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// A bench operator ran `waypointd reset-claim` on a store owned by root and got
// "attempt to write a readonly database (8)", which reads like a corrupted
// database rather than a missing `sudo`. They then spent a while locked out of a
// node whose reset command was working exactly as designed.

func TestReadonlyStoreErrorSaysWhatToDo(t *testing.T) {
	err := explainStoreError(errors.New("attempt to write a readonly database (8)"), "/var/lib/waypoint/config.db")
	got := err.Error()

	if !strings.Contains(got, "/var/lib/waypoint/config.db") {
		t.Error("the message does not say which file could not be written")
	}
	if os.Geteuid() != 0 && !strings.Contains(got, "sudo") {
		t.Errorf("running unprivileged, the message should suggest sudo:\n%s", got)
	}
	if !strings.Contains(got, "attempt to write a readonly database") {
		t.Error("the original error should still be there for anyone searching for it")
	}
}

// An error that is not about permissions must pass through untouched: guessing
// "try sudo" at a genuinely corrupt database would send an operator the wrong way.
func TestUnrelatedStoreErrorIsNotRewritten(t *testing.T) {
	orig := errors.New("database disk image is malformed")
	if got := explainStoreError(orig, "/tmp/x.db"); got != orig {
		t.Fatalf("error was rewritten: %v", got)
	}
}
