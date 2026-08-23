package main

import (
	"errors"
	"os"
	"testing"
)

// TestMain refuses to let the test binary talk to the real systemd.
//
// systemctlRun is a package variable so a test can substitute a fake, and the
// tests that care about what waypointd asks systemd to do already substitute one.
// The gap was the DEFAULT: it was the real exec.Command, and every stubbing test
// restores that default in t.Cleanup, so any test reaching the apply or restart
// path without a stub of its own shelled out to the actual systemctl.
//
// On a developer's desktop that is not a test failure — it is a polkit dialog
// ("Authentication is required to manage system service or unit files") blocking
// the run until somebody clicks Cancel, once per call. On CI it is worse: the
// runner has no polkit, so systemctl fails silently and the test passes for the
// wrong reason.
//
// Defaulting to a refusal makes both cases loud and local. A test that needs to
// observe systemctl still substitutes its own fake; one that reaches this has
// found a path nobody stubbed, and the error says so.
func TestMain(m *testing.M) {
	systemctlRun = func(args ...string) ([]byte, error) {
		return nil, errors.New("systemctl is not available under go test: " +
			"substitute systemctlRun in this test if it means to exercise unit management")
	}
	os.Exit(m.Run())
}
