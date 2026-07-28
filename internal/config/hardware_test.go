package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/modem"
)

// benchIdentity is the reference bench board's detection record: the Dual Hat on
// the GPIO UART, with three products sharing its identity string.
func benchIdentity() modem.Identity {
	return modem.Identity{
		Port: "/dev/ttyAMA0", Transport: "gpio", Baud: 115200,
		Protocol:    1,
		Description: "MMDVM_HS_Dual_Hat-v1.6.1 20230526 14.7456MHz dual ADF7021 FW by CA6JAU GitID #899fc2a",
		HWType:      "MMDVM_HS_Dual_Hat", Firmware: "1.6.1",
		TCXOHz: 14_745_600, Duplex: true,
		Candidates: []string{"mmdvm_hs_dual_hat", "zumspot_duplex", "lonestar_dual"},
		Modes:      modem.Version{Protocol: 1}.Modes(),
	}
}

func TestHardwareStateIsNotAConfigSection(t *testing.T) {
	// A record of what the hardware said is not a preference. If it were in the
	// section map, PUT /api/config/hardware_state would exist and an operator
	// could assert a modem that is not there.
	if _, ok := (&Model{}).sections()[hardwareStateKey]; ok {
		t.Fatalf("%q is a config section; it must be machine-written only", hardwareStateKey)
	}
}

func TestHardwareStateRoundTrips(t *testing.T) {
	s := memStore(t)
	st, err := GetHardwareState(s)
	if err != nil {
		t.Fatal(err)
	}
	if st.Identity != nil {
		t.Fatal("a node that has never been probed reported a modem")
	}
	id := benchIdentity()
	want := HardwareState{Identity: &id, CheckedAt: "2026-07-27T12:00:00Z"}
	if err := SetHardwareState(s, want, "test"); err != nil {
		t.Fatal(err)
	}
	got, err := GetHardwareState(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Identity == nil || got.Identity.Description != id.Description || got.CheckedAt != want.CheckedAt {
		t.Fatalf("round trip lost the detection: %+v", got)
	}
}

// The crossing issue #136 is about: a detection that never reaches the config
// is a detection that did nothing.
func TestAdoptWritesTheDetectionIntoTheConfig(t *testing.T) {
	s := memStore(t)
	a, err := AdoptDetection(s, benchIdentity(), "mmdvm_hs_dual_hat", "test")
	if err != nil {
		t.Fatal(err)
	}
	var m Modem
	if _, err := s.GetInto("modem", &m); err != nil {
		t.Fatal(err)
	}
	if m.Port != "/dev/ttyAMA0" || m.UARTSpeed != "115200" {
		t.Errorf("modem port/speed = %q/%q", m.Port, m.UARTSpeed)
	}
	if m.Board != "mmdvm_hs_dual_hat" || m.TCXOHz != "14745600" {
		t.Errorf("modem board/tcxo = %q/%q", m.Board, m.TCXOHz)
	}
	if len(a.Changed) == 0 {
		t.Error("Adoption reported no changes; a silent adopt is how an operator stops knowing what their node is configured for")
	}
	for _, c := range a.Changed {
		if !strings.Contains(c, "→") {
			t.Errorf("change %q does not say what it changed from and to", c)
		}
	}
}

func TestAdoptAsksWhenTheIdentityMatchesSeveralBoards(t *testing.T) {
	// "Explicit picker where not [possible]" — issue #18. Three products answer
	// to the Dual Hat's identity string and nothing on the wire separates them.
	s := memStore(t)
	if _, err := AdoptDetection(s, benchIdentity(), "", "test"); !errors.Is(err, ErrAmbiguousBoard) {
		t.Fatalf("err = %v, want ErrAmbiguousBoard", err)
	}
	var m Modem
	if _, err := s.GetInto("modem", &m); err != nil {
		t.Fatal(err)
	}
	if m.Port != "" {
		t.Error("a refused adoption still wrote to the config")
	}
}

func TestAdoptRefusesABoardTheHardwareRulesOut(t *testing.T) {
	// The picker resolves an ambiguity; it does not overrule the modem. A
	// simplex board is not an answer to a dual-ADF7021 identity string.
	s := memStore(t)
	_, err := AdoptDetection(s, benchIdentity(), "mmdvm_hs_hat", "test")
	if err == nil {
		t.Fatal("adopted a board the modem's own identity excludes")
	}
	if !strings.Contains(err.Error(), "mmdvm_hs_hat") {
		t.Errorf("err = %v; it should name the board it refused", err)
	}
}

func TestAdoptRefusesABoardThatDoesNotExist(t *testing.T) {
	s := memStore(t)
	if _, err := AdoptDetection(s, benchIdentity(), "not_a_board", "test"); err == nil {
		t.Fatal("adopted a board that is not in the table")
	}
}

func TestAdoptNeverWritesAnAssumedOscillator(t *testing.T) {
	// Old firmware reports no MHz field, so the value is inferred from the board
	// table. A reference frequency guessed wrong detunes the radio, and a value
	// in the config reads as a fact.
	s := memStore(t)
	id := benchIdentity()
	id.TCXOAssumed = true
	if _, err := AdoptDetection(s, id, "mmdvm_hs_dual_hat", "test"); err != nil {
		t.Fatal(err)
	}
	var m Modem
	if _, err := s.GetInto("modem", &m); err != nil {
		t.Fatal(err)
	}
	if m.TCXOHz != "" {
		t.Errorf("tcxo_hz = %q; an assumed oscillator must not be written as configuration", m.TCXOHz)
	}
	if m.Board != "mmdvm_hs_dual_hat" {
		t.Error("the rest of the adoption was dropped along with the oscillator")
	}
}

func TestAdoptAnUnrecognisedButWorkingModem(t *testing.T) {
	// A modem nothing in the table matches is still a modem, and its port is
	// still worth having. The board stays unnamed rather than invented.
	s := memStore(t)
	id := modem.Identity{Port: "/dev/ttyACM0", Baud: 115200, Description: "BrandNewThing-v2.0.0"}
	if _, err := AdoptDetection(s, id, "", "test"); err != nil {
		t.Fatalf("refused to adopt an unrecognised modem: %v", err)
	}
	var m Modem
	if _, err := s.GetInto("modem", &m); err != nil {
		t.Fatal(err)
	}
	if m.Port != "/dev/ttyACM0" {
		t.Errorf("port = %q, want the port it was found on", m.Port)
	}
	if m.Board != "" {
		t.Errorf("board = %q; an unrecognised board must not be guessed at", m.Board)
	}
}

func TestValidateModem(t *testing.T) {
	if err := ValidateModem(Modem{Board: "nope"}); err == nil {
		t.Error("accepted a board that is not in the table")
	}
	if err := ValidateModem(Modem{TCXOHz: "14.7456 MHz"}); err == nil {
		t.Error("accepted an oscillator that is not a number of Hz")
	}
	if err := ValidateModem(Modem{Board: "mmdvm_hs_dual_hat", TCXOHz: "14745600"}); err != nil {
		t.Errorf("rejected a valid modem section: %v", err)
	}
	if err := ValidateModem(Modem{}); err != nil {
		t.Errorf("rejected a node that has never been told what it is: %v", err)
	}
}

func TestSetModemRefusesNonsenseAtSaveTime(t *testing.T) {
	s := memStore(t)
	if err := SetModem(s, []byte(`{"board":"nope"}`), "test"); err == nil {
		t.Fatal("SetModem persisted an unknown board")
	}
	if err := SetModem(s, []byte(`{"port":"/dev/ttyAMA0"}`), "test"); err != nil {
		t.Fatal(err)
	}
	// A partial body must not drop the fields it does not mention.
	if err := SetModem(s, []byte(`{"board":"mmdvm_hs_dual_hat"}`), "test"); err != nil {
		t.Fatal(err)
	}
	var m Modem
	if _, err := s.GetInto("modem", &m); err != nil {
		t.Fatal(err)
	}
	if m.Port != "/dev/ttyAMA0" || m.Board != "mmdvm_hs_dual_hat" {
		t.Fatalf("merge dropped a field: %+v", m)
	}
}

func TestNoWarningsWithoutEvidence(t *testing.T) {
	// Warning about a configuration on the strength of no detection is worse
	// than silence.
	m := &Model{General: General{Duplex: true}, Modes: Modes{DMR: true, FM: true}}
	if w := HardwareWarnings(m, HardwareState{}); len(w) != 0 {
		t.Fatalf("warned with no detection to warn from: %+v", w)
	}
}

func TestDuplexOnASimplexBoardIsAnError(t *testing.T) {
	// The host starts, reports itself healthy, and never keys.
	id := benchIdentity()
	id.Duplex = false
	id.HWType = "MMDVM_HS_Hat"
	m := &Model{General: General{Duplex: true}, Modem: Modem{Port: "/dev/ttyAMA0"}}
	w := findWarning(HardwareWarnings(m, HardwareState{Identity: &id}), "general.duplex")
	if w == nil {
		t.Fatal("duplex on a single-ADF7021 board produced no warning")
	}
	if w.Severity != SeverityError {
		t.Errorf("severity = %q, want error — this configuration cannot work", w.Severity)
	}
}

func TestAModeTheFirmwareDoesNotCarryIsAnError(t *testing.T) {
	id := benchIdentity()
	id.Protocol = 2
	id.Modes = modem.Version{Protocol: 2, Cap1: modem.CapDMR | modem.CapYSF}.Modes()
	m := &Model{Modes: Modes{DMR: true, M17: true, POCSAG: true}, Modem: Modem{Port: "/dev/ttyAMA0"}}
	warns := HardwareWarnings(m, HardwareState{Identity: &id})
	for _, mode := range []string{"modes.m17", "modes.pocsag"} {
		if findWarning(warns, mode) == nil {
			t.Errorf("%s enabled on firmware without it produced no warning", mode)
		}
	}
	if findWarning(warns, "modes.dmr") != nil {
		t.Error("warned about a mode the firmware does carry")
	}
}

func TestProtocol1CapabilitiesNeverRefuseAMode(t *testing.T) {
	// On protocol-1 firmware the capability bits are MMDVM-Host's assumption,
	// not the modem's answer. Refusing a mode on the strength of a guess is
	// exactly what modem.Modes.Known exists to prevent.
	id := benchIdentity() // protocol 1
	m := &Model{Modes: Modes{M17: true, FM: true}, Modem: Modem{Port: "/dev/ttyAMA0"}}
	for _, w := range HardwareWarnings(m, HardwareState{Identity: &id}) {
		if strings.HasPrefix(w.Field, "modes.") {
			t.Errorf("warned about %s from an assumption, not a fact: %s", w.Field, w.Message)
		}
	}
}

func TestAPortThatIsNotWhereTheModemIsIsAnError(t *testing.T) {
	// MMDVM-Host exits when its port has no modem, and stackupdate's health gate
	// reads that as a bad update.
	id := benchIdentity()
	m := &Model{Modem: Modem{Port: "/dev/ttyACM0"}}
	w := findWarning(HardwareWarnings(m, HardwareState{Identity: &id}), "modem.port")
	if w == nil || w.Severity != SeverityError {
		t.Fatalf("port mismatch warning = %+v", w)
	}
	if !strings.Contains(w.Message, "/dev/ttyAMA0") || !strings.Contains(w.Message, "/dev/ttyACM0") {
		t.Errorf("message names neither port: %s", w.Message)
	}
}

func TestOscillatorMismatchIsAWarningInLabelsOperatorsRecognise(t *testing.T) {
	id := benchIdentity()
	m := &Model{Modem: Modem{Port: "/dev/ttyAMA0", TCXOHz: "12288000"}}
	w := findWarning(HardwareWarnings(m, HardwareState{Identity: &id}), "modem.tcxo_hz")
	if w == nil {
		t.Fatal("no warning for a node configured for the wrong reference oscillator")
	}
	// Operators know these by their printed label, not by a Hz count.
	if !strings.Contains(w.Message, "12.288 MHz") || !strings.Contains(w.Message, "14.7456 MHz") {
		t.Errorf("message uses Hz counts rather than the labels on the parts: %s", w.Message)
	}
}

func TestBoardThatContradictsTheHardwareIsAWarning(t *testing.T) {
	id := benchIdentity()
	m := &Model{Modem: Modem{Port: "/dev/ttyAMA0", Board: "mmdvm_hs_hat"}}
	if findWarning(HardwareWarnings(m, HardwareState{Identity: &id}), "modem.board") == nil {
		t.Fatal("a configured board the attached modem cannot be produced no warning")
	}
}

func TestAConsistentNodeIsSilent(t *testing.T) {
	id := benchIdentity()
	m := &Model{
		General: General{Duplex: true},
		Modem:   Modem{Port: "/dev/ttyAMA0", Board: "mmdvm_hs_dual_hat", TCXOHz: "14745600"},
		Modes:   Modes{DMR: true, YSF: true},
	}
	if w := HardwareWarnings(m, HardwareState{Identity: &id}); len(w) != 0 {
		t.Fatalf("a correctly configured node was warned at: %+v", w)
	}
}

func findWarning(ws []HardwareWarning, field string) *HardwareWarning {
	for i := range ws {
		if ws[i].Field == field {
			return &ws[i]
		}
	}
	return nil
}
