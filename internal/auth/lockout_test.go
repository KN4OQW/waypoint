package auth

import (
	"strings"
	"testing"
)

// The login screen is where a forgotten password is discovered, by someone who
// by definition cannot reach the settings page, the docs, or anything else this
// node serves. If the way back in is not on this screen, it is nowhere.

func TestLoginScreenTellsYouHowToGetBackIn(t *testing.T) {
	for _, want := range []string{
		"Forgotten your password?",
		"sudo waypointd reset-claim", // the shell route, with the sudo that it needs
		"waypoint-reset",             // the SD-card route
		"config.txt",                 // which partition, in terms a person can act on
		"docs/recovery.md",
	} {
		if !strings.Contains(loginPlaceholder, want) {
			t.Errorf("the login screen does not mention %q", want)
		}
	}
}

// The claim screen must NOT carry it: there is no password to have forgotten,
// and instructions for resetting a device nobody has claimed yet are noise at
// best and an invitation at worst.
func TestClaimScreenHasNoLockoutHelp(t *testing.T) {
	if strings.Contains(claimPlaceholder, "Forgotten your password?") {
		t.Error("the claim screen offers lockout recovery for a device with no credential yet")
	}
}

// It is a <details>, closed by default: an operator logging in normally should
// see a login form, not a page of recovery documentation.
func TestLockoutHelpIsCollapsed(t *testing.T) {
	if strings.Contains(loginPlaceholder, "<details class=\"help\" open") {
		t.Error("the recovery instructions are expanded by default")
	}
	if !strings.Contains(loginPlaceholder, "<summary>") {
		t.Error("the recovery instructions have no summary to click")
	}
}
