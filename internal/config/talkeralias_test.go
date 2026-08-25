package config

import "testing"

// zelloDMRModel is a node with one enabled bus carrying DMR and one Zello channel
// bridged onto it — the configuration this whole path exists for.
func zelloDMRModel() *Model {
	return &Model{
		General:       General{ID: "3180202", Callsign: "KN4OQW"},
		Modes:         Modes{DMR: true},
		DMR:           DMR{TalkerAlias: "callsign + name"},
		DMRNet:        DMRNet{ShimEnabled: true},
		Buses:         []Bus{{ID: "b1", Name: "Bus 1", Enabled: true}},
		Attachments:   []Attachment{{BusID: "b1", Mode: ModeDMR}},
		ZelloAccounts: []ZelloAccount{okAccount()},
		ZelloChannels: []ZelloChannel{okChannel()},
	}
}

// TestAnnouncedSourceIDIsTheNodesOwn. It must be General.ID and nothing else: the
// bus stamps its Zello audio with that id (busConfigFor), and MMDVM-Host drops an
// alias block whose source does not match the call the slot is carrying. A
// mismatch here would not fail loudly — it would silently name nobody.
func TestAnnouncedSourceIDIsTheNodesOwn(t *testing.T) {
	m := zelloDMRModel()
	got := m.TalkerAliasAnnouncedSourceIDs()
	if len(got) != 1 || got[0] != 3180202 {
		t.Fatalf("announced ids = %v, want [3180202]", got)
	}
	// DMR.Id overrides General.Id for the gateway login, and must NOT override here.
	m.DMR.ID = "3180299"
	if got := m.TalkerAliasAnnouncedSourceIDs(); len(got) != 1 || got[0] != 3180202 {
		t.Errorf("announced ids = %v with DMR.ID set, want [3180202]", got)
	}
}

// TestNoAnnouncedSourceIDsWithoutZelloOnADMRBus is the byte-identical claim: a
// node that has not bridged Zello onto DMR keeps naming callers from the phonebook
// exactly as it did before announcements existed.
func TestNoAnnouncedSourceIDsWithoutZelloOnADMRBus(t *testing.T) {
	for name, mutate := range map[string]func(*Model){
		"no zello channel at all": func(m *Model) { m.ZelloChannels = nil },
		"the channel is disabled": func(m *Model) { m.ZelloChannels[0].Enabled = false },
		"the account is disabled": func(m *Model) { m.ZelloAccounts[0].Enabled = false },
		"the bus is disabled":     func(m *Model) { m.Buses[0].Enabled = false },
		// A Zello channel on a bus with no DMR attachment never reaches a radio, so
		// there is no borrowed id to suppress.
		"the bus does not carry DMR": func(m *Model) { m.Attachments = []Attachment{{BusID: "b1", Mode: ModeYSF}} },
		// Nothing to attribute a transmission to.
		"no node id": func(m *Model) { m.General.ID = "" },
		// atoi-style parsing would yield 0 here; the check must refuse it outright
		// rather than declare id 0 announce-only.
		"a node id that is not a number": func(m *Model) { m.General.ID = "not-a-number" },
		"a zero node id":                 func(m *Model) { m.General.ID = "0" },
	} {
		t.Run(name, func(t *testing.T) {
			m := zelloDMRModel()
			mutate(m)
			if got := m.TalkerAliasAnnouncedSourceIDs(); len(got) != 0 {
				t.Errorf("announced ids = %v, want none", got)
			}
		})
	}
}

// TestNilModelHasNoAnnouncedSourceIDs: the injector reconciles from a model it may
// have failed to load.
func TestNilModelHasNoAnnouncedSourceIDs(t *testing.T) {
	var m *Model
	if got := m.TalkerAliasAnnouncedSourceIDs(); got != nil {
		t.Errorf("announced ids = %v on a nil model", got)
	}
}

// TestZelloAliasNeedsTheRelay: the relay is the only injection point on the box,
// so an operator who asked to name Zello callers and left it off has to be told.
func TestZelloAliasNeedsTheRelay(t *testing.T) {
	m := zelloDMRModel()
	m.DMRNet.ShimEnabled = false
	var msgs []string
	m.zelloTalkerAliasProblems(func(_, sev, msg string) {
		if sev != SeverityWarning {
			t.Errorf("severity = %q, want a warning — the node still works and the audio still flows", sev)
		}
		msgs = append(msgs, msg)
	})
	if len(msgs) != 1 {
		t.Fatalf("got %d problems, want 1: %v", len(msgs), msgs)
	}
	// Errors say what went wrong AND what to do.
	if !containsSub(msgs[0], "relay") {
		t.Errorf("the warning does not name the relay: %q", msgs[0])
	}
}

// TestZelloAliasProblemStaysQuietOnAWorkingNode. A check that fires on a good
// configuration is worse than no check, so every clean case is asserted beside the
// failing one.
func TestZelloAliasProblemStaysQuietOnAWorkingNode(t *testing.T) {
	for name, mutate := range map[string]func(*Model){
		"the relay is on":              func(m *Model) {},
		"no alias template was chosen": func(m *Model) { m.DMR.TalkerAlias = ""; m.DMRNet.ShimEnabled = false },
		"no zello channel":             func(m *Model) { m.ZelloChannels = nil; m.DMRNet.ShimEnabled = false },
		"zello is not on a DMR bus": func(m *Model) {
			m.Attachments = []Attachment{{BusID: "b1", Mode: ModeYSF}}
			m.DMRNet.ShimEnabled = false
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := zelloDMRModel()
			mutate(m)
			var n int
			m.zelloTalkerAliasProblems(func(_, _, _ string) { n++ })
			if n != 0 {
				t.Errorf("got %d problems on a configuration that works", n)
			}
		})
	}
}

// TestTheRelayWarningReachesTheDMRPanel: the check is only useful if it is wired
// into what an operator actually sees.
func TestTheRelayWarningReachesTheDMRPanel(t *testing.T) {
	m := zelloDMRModel()
	m.DMRNet.ShimEnabled = false
	var found bool
	for _, p := range m.dmrProblems() {
		if p.Mode == ModeDMR && containsSub(p.Message, "Zello callers cannot be named") {
			found = true
		}
	}
	if !found {
		t.Error("the Zello alias warning does not appear among the DMR problems")
	}
}

func containsSub(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
