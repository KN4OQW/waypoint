package config

import (
	"strings"
	"testing"
)

// stgModel is a working simplex DMR node with one network: the configuration the
// hint is about.
func stgModel() *Model {
	m := &Model{}
	m.Modes.DMR = true
	m.General.Callsign = "KN4OQW"
	m.General.Duplex = false
	m.DMR.ID = "3180202"
	m.DMR.ColorCode = "1"
	// Slot 2 on and real frequencies, so the only findings this model produces are
	// the ones a test asks for. Without them the station-wide frequency errors and
	// the dead-slot warning fire and drown out what is being measured.
	m.DMRNet.Slot2 = true
	m.Modem.RXFreqHz = "438800000"
	m.Modem.TXFreqHz = "433800000"
	m.Networks = []Network{{Name: "BM", Type: NetBrandmeister, Enabled: true}}
	return m
}

func findField(m *Model, field string) *ModeProblem {
	probs := m.ModeProblems()
	for i := range probs {
		if probs[i].Field == field {
			return &probs[i]
		}
	}
	return nil
}

// The hint fires on the configuration where the problem is possible, and says
// enough for an operator to act on it.
func TestStaticTalkgroupHintFires(t *testing.T) {
	got := findField(stgModel(), "general.duplex")
	if got == nil {
		t.Fatal("no hint on a simplex node with a DMR network")
	}
	if got.Severity != SeverityInfo {
		t.Errorf("severity = %q, want %q — this is a guess, not a fault", got.Severity, SeverityInfo)
	}
	if got.Mode != ModeDMR {
		t.Errorf("mode = %q, want dmr", got.Mode)
	}
	for _, want := range []string{"simplex", "static talkgroup", "SelfCare"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("message does not mention %q: %s", want, got.Message)
		}
	}
	// It must say what the symptom LOOKS like, because the whole problem is that
	// it looks like nothing at all.
	if !strings.Contains(got.Message, "nothing") {
		t.Errorf("message does not describe the symptom: %s", got.Message)
	}
	// And it must admit what Waypoint cannot see, or it reads as a diagnosis.
	if !strings.Contains(got.Message, "cannot") {
		t.Errorf("message does not say the static list is not visible from here: %s", got.Message)
	}
}

// Where it must stay quiet. Each of these is a node the hint does not apply to,
// and a hint that fires on every node is noise an operator learns to skip.
func TestStaticTalkgroupHintStaysQuiet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Model)
	}{
		{
			// The physical half does not apply: a duplex node listens on a different
			// frequency from the one it transmits on, so it can always hear the radio.
			name:   "duplex",
			mutate: func(m *Model) { m.General.Duplex = true },
		},
		{
			// No network means no talkgroup to be subscribed to.
			name:   "no networks at all",
			mutate: func(m *Model) { m.Networks = nil },
		},
		{
			name:   "every network disabled",
			mutate: func(m *Model) { m.Networks = []Network{{Name: "BM", Type: NetBrandmeister}} },
		},
		{
			// XLX renders its own section and carries no talkgroup subscriptions.
			name:   "only an XLX network",
			mutate: func(m *Model) { m.Networks = []Network{{Name: "XLX", Type: NetXLX, Enabled: true}} },
		},
		{
			// A mode that is off is never reported against, here as everywhere.
			name:   "DMR disabled",
			mutate: func(m *Model) { m.Modes.DMR = false },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := stgModel()
			tc.mutate(m)
			if got := findField(m, "general.duplex"); got != nil {
				t.Errorf("hint fired anyway: %s", got.Message)
			}
		})
	}
}

// A network type other than BrandMeister still gets it: static talkgroups are not
// a BrandMeister invention, and the message names SelfCare as an example rather
// than as the only place to look.
func TestStaticTalkgroupHintIsNotBrandmeisterOnly(t *testing.T) {
	m := stgModel()
	m.Networks = []Network{{Name: "TGIF", Type: NetTGIF, Enabled: true}}
	if findField(m, "general.duplex") == nil {
		t.Error("no hint on a simplex node with a non-BrandMeister network")
	}
}

// The tier has to stay out of the way of the two that mean something is wrong.
func TestInfoIsNotAnError(t *testing.T) {
	m := stgModel()
	if m.HasModeErrors() {
		t.Error("an informational hint made the node report a blocking error")
	}
	// And it must not be the only finding that ever appears — a node with a real
	// fault still reports it, at its own severity.
	m.DMRNet.Slot1, m.DMRNet.Slot2 = false, false
	var sev []string
	for _, p := range m.ModeProblems() {
		sev = append(sev, p.Severity)
	}
	if !hasSeverity(sev, SeverityWarning) || !hasSeverity(sev, SeverityInfo) {
		t.Errorf("severities present = %v, want both a warning and an info", sev)
	}
}

func hasSeverity(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
