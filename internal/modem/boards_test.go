package modem

import (
	"strings"
	"testing"
)

func TestBoardTableIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range Boards {
		if b.ID == "" || b.Name == "" {
			t.Errorf("board %+v has no ID or Name", b)
		}
		if seen[b.ID] {
			t.Errorf("duplicate board ID %q — IDs are stored in config and can never collide", b.ID)
		}
		seen[b.ID] = true
		if strings.ToLower(b.ID) != b.ID || strings.ContainsAny(b.ID, " -") {
			t.Errorf("board ID %q must be lower_snake_case: it is a stable config value", b.ID)
		}
		if len(b.HWTypes) == 0 {
			t.Errorf("board %q declares no HW_TYPE tokens, so detection can never match it", b.ID)
		}
		if b.TCXOHz != 0 && b.TCXOHz != 12_288_000 && b.TCXOHz != 14_745_600 {
			t.Errorf("board %q has TCXO %d Hz; the launch tier is 12.288 or 14.7456 MHz", b.ID, b.TCXOHz)
		}
	}
}

// The reference bench board is the acceptance criterion in issue #18, and it is
// the only entry anyone has held in their hands.
func TestOnlyBenchVerifiedBoardsClaimVerified(t *testing.T) {
	var verified []string
	for _, b := range Boards {
		if b.Verified {
			verified = append(verified, b.ID)
		}
	}
	if len(verified) != 1 || verified[0] != "mmdvm_hs_dual_hat" {
		t.Errorf("Verified boards = %v; only hardware actually bench-tested may claim it", verified)
	}
}

func TestResolveBenchDualHatIsUnambiguousOnGPIO(t *testing.T) {
	d := ParseDescription("MMDVM_HS_Dual_Hat-v1.6.1 20230526 14.7456MHz dual ADF7021 FW by CA6JAU GitID #899fc2a")
	r := Resolve(d, TransportGPIO)
	if r.TCXOHz != 14_745_600 || r.TCXOAssumed {
		t.Errorf("TCXO = %d assumed=%v; the firmware reported it outright", r.TCXOHz, r.TCXOAssumed)
	}
	if !r.Dual {
		t.Error("Dual = false for a dual-ADF7021 board")
	}
	// Three GPIO boards share this identity string, which is exactly the case
	// the issue means by "explicit picker where not": the family is certain,
	// the product is not.
	if len(r.Candidates) < 2 {
		t.Fatalf("Candidates = %v; the Dual Hat string is shared by several products", r.Candidates)
	}
	if !contains(r.Candidates, "mmdvm_hs_dual_hat") {
		t.Errorf("Candidates = %v, missing the bench board", r.Candidates)
	}
	if r.BoardID != "" {
		t.Errorf("BoardID = %q; an ambiguous match must not silently pick one", r.BoardID)
	}
}

func TestResolveTransportSeparatesSiblingProducts(t *testing.T) {
	// The GPIO/USB sibling pairs are indistinguishable on the wire. How the
	// modem was found is the one fact that tells them apart.
	d := ParseDescription("NanoDV-v1.5.1 20200609 14.7456MHz ADF7021 FW by CA6JAU")
	gpio, usb := Resolve(d, TransportGPIO), Resolve(d, TransportUSB)
	if gpio.BoardID != "nanodv_npi" {
		t.Errorf("GPIO NanoDV resolved to %q/%v, want nanodv_npi", gpio.BoardID, gpio.Candidates)
	}
	if usb.BoardID != "nanodv_usb" {
		t.Errorf("USB NanoDV resolved to %q/%v, want nanodv_usb", usb.BoardID, usb.Candidates)
	}
}

func TestResolveRadioCountExcludesTheWrongFamily(t *testing.T) {
	// A two-radio board cannot be a one-radio product. This is the sharpest
	// discriminator the wire gives us, and it is what stops an operator being
	// offered a simplex board for a duplex hat.
	d := ParseDescription("MMDVM_HS_Dual_Hat-v1.6.1 20230526 14.7456MHz dual ADF7021 FW by CA6JAU")
	for _, id := range Resolve(d, TransportAny).Candidates {
		b, _ := BoardByID(id)
		if !b.Dual {
			t.Errorf("simplex board %q offered for a dual-ADF7021 modem", id)
		}
	}
}

func TestResolveUnreportedOscillatorDoesNotExcludeTheRightBoard(t *testing.T) {
	// Pre-1.5 firmware reports no MHz field. Filtering on a value the modem
	// never gave would rule out every board.
	d := ParseDescription("MMDVM_HS_Dual_Hat-v1.4.17 20181124 dual ADF7021 FW by CA6JAU")
	r := Resolve(d, TransportGPIO)
	if len(r.Candidates) == 0 {
		t.Fatal("old firmware with no MHz field resolved to nothing")
	}
	// Every board it could be uses the same oscillator, so the value is
	// recoverable — but it must be flagged as inferred, not reported.
	if r.TCXOHz != 14_745_600 || !r.TCXOAssumed {
		t.Errorf("TCXO = %d assumed=%v, want 14745600 assumed=true", r.TCXOHz, r.TCXOAssumed)
	}
}

func TestResolveUnknownBoardAsksRatherThanGuesses(t *testing.T) {
	r := Resolve(ParseDescription("BrandNewThing-v2.0.0 20260101 ADF7021 FW by NOCALL"), TransportUSB)
	if r.BoardID != "" || len(r.Candidates) != 0 {
		t.Errorf("unknown board resolved to %q/%v; it must fall through to the picker", r.BoardID, r.Candidates)
	}
	if r.TCXOAssumed {
		t.Error("TCXO assumed for a board nothing in the table matched")
	}
}

func TestResolveNeverInventsAnOscillator(t *testing.T) {
	// Two candidate boards disagreeing about the oscillator must yield nothing.
	// A reference frequency guessed wrong detunes the radio; a tie is resolved
	// by asking, not by picking.
	d := ParseDescription("MMDVM_HS-v1.4.17 20181124 ADF7021 FW by CA6JAU")
	r := Resolve(d, TransportAny)
	if r.TCXOHz != 0 {
		t.Errorf("TCXO = %d for candidates %v that do not agree", r.TCXOHz, r.Candidates)
	}
}

func TestBoardIDsAreSortedAndComplete(t *testing.T) {
	ids := BoardIDs()
	if len(ids) != len(Boards) {
		t.Fatalf("BoardIDs() has %d entries, table has %d", len(ids), len(Boards))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i-1] > ids[i] {
			t.Fatalf("BoardIDs() not sorted at %d: %q > %q", i, ids[i-1], ids[i])
		}
	}
	if _, ok := BoardByID("nope"); ok {
		t.Error("BoardByID matched an ID that is not in the table")
	}
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
