package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

// oneBusWith builds a single enabled bus with one attachment per mode, so the
// attachment-validity matrix can be driven through the real model boundary
// (ValidateBuses), not just the pure helper. credentials_ref is left blank
// (allowed) so a row's verdict turns only on its mode set.
func oneBusWith(modes ...Mode) ([]Bus, []Attachment) {
	buses := []Bus{{ID: "A", Name: "Bus A", Enabled: true}}
	att := make([]Attachment, 0, len(modes))
	for _, m := range modes {
		att = append(att, Attachment{BusID: "A", Mode: m})
	}
	return buses, att
}

// TestAttachmentValidityMatrix is the release-blocking table from RFC-0003 §6.3:
// {set of attached modes} -> {valid | invalid, reason}. Every case runs through
// ValidateBuses (the write-time boundary), and an invalid case asserts the exact
// human-readable reason the UI surfaces verbatim (RFC-0003 §2).
func TestAttachmentValidityMatrix(t *testing.T) {
	cases := []struct {
		name   string
		modes  []Mode
		reason string // "" means valid
	}{
		// Reframe-tier subsets of {DMR,YSF,NXDN} are all valid (§2 rule 1).
		{"empty", nil, ""},
		{"dmr only", []Mode{ModeDMR}, ""},
		{"ysf only", []Mode{ModeYSF}, ""},
		{"nxdn only", []Mode{ModeNXDN}, ""},
		{"dmr+ysf", []Mode{ModeDMR, ModeYSF}, ""},
		{"dmr+nxdn", []Mode{ModeDMR, ModeNXDN}, ""},
		{"ysf+nxdn", []Mode{ModeYSF, ModeNXDN}, ""},
		{"dmr+ysf+nxdn", []Mode{ModeDMR, ModeYSF, ModeNXDN}, ""},

		// D-Star has only DSTAR2YSF: with anything but YSF there is no converter.
		{"dstar+dmr", []Mode{ModeDStar, ModeDMR}, "no converter for D-Star<->DMR"},
		{"dstar+nxdn", []Mode{ModeDStar, ModeNXDN}, "no converter for D-Star<->NXDN"},
		{"dstar+p25", []Mode{ModeDStar, ModeP25}, "no converter for D-Star<->P25"},
		{"dstar+m17", []Mode{ModeDStar, ModeM17}, "no converter for D-Star<->M17"},

		// A converter exists for these pairs, but only the deferred transcode tier
		// can run it (§2 rule 1, tier deferred).
		{"dstar+ysf", []Mode{ModeDStar, ModeYSF}, "transcode tier not available"},
		{"p25+dmr", []Mode{ModeP25, ModeDMR}, "transcode tier not available"},
		{"p25+ysf", []Mode{ModeP25, ModeYSF}, "transcode tier not available"},
		{"m17+dmr", []Mode{ModeM17, ModeDMR}, "transcode tier not available"},
		{"m17+ysf", []Mode{ModeM17, ModeYSF}, "transcode tier not available"},

		// Transcode-tier pairs with no converter at all.
		{"p25+nxdn", []Mode{ModeP25, ModeNXDN}, "no converter for P25<->NXDN"},
		{"m17+nxdn", []Mode{ModeM17, ModeNXDN}, "no converter for M17<->NXDN"},
		{"p25+m17", []Mode{ModeP25, ModeM17}, "no converter for P25<->M17"},

		// A lone transcode-tier mode is still outside committed scope.
		{"dstar alone", []Mode{ModeDStar}, "transcode tier not available"},
		{"p25 alone", []Mode{ModeP25}, "transcode tier not available"},
		{"m17 alone", []Mode{ModeM17}, "transcode tier not available"},

		// Modes that can never attach, named individually.
		{"ysf-vw", []Mode{ModeYSFVW}, "YSF VW is outside the reframe envelope (DN only)"},
		{"fm", []Mode{ModeFM}, "FM is not a bus-capable mode"},
		{"pocsag", []Mode{ModePOCSAG}, "POCSAG is not a bus-capable mode"},

		// No-converter refusal wins over transcode-tier when both apply on one bus.
		{"dstar+dmr+ysf", []Mode{ModeDStar, ModeDMR, ModeYSF}, "no converter for D-Star<->DMR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Pure helper: exact reason, order-independent (§6.3 is a set matrix).
			ok, reason := busModeSetReason(tc.modes)
			if tc.reason == "" {
				if !ok {
					t.Fatalf("busModeSetReason(%v) = invalid %q, want valid", tc.modes, reason)
				}
			} else {
				if ok {
					t.Fatalf("busModeSetReason(%v) = valid, want invalid %q", tc.modes, tc.reason)
				}
				if reason != tc.reason {
					t.Fatalf("busModeSetReason(%v) reason = %q, want %q", tc.modes, reason, tc.reason)
				}
			}

			// Full boundary: ValidateBuses wraps the same reason as "bus %q: <reason>".
			buses, att := oneBusWith(tc.modes...)
			err := ValidateBuses(buses, att, nil)
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("ValidateBuses(%v) = %v, want nil", tc.modes, err)
				}
			} else {
				if err == nil {
					t.Fatalf("ValidateBuses(%v) = nil, want error %q", tc.modes, tc.reason)
				}
				if want := "bus \"A\": " + tc.reason; err.Error() != want {
					t.Fatalf("ValidateBuses(%v) = %q, want %q", tc.modes, err.Error(), want)
				}
			}

			// Set-membership is order-independent: reversing the modes cannot change
			// the verdict (a defensive check on the §6.3 "set of modes" contract).
			rev := make([]Mode, len(tc.modes))
			for i, m := range tc.modes {
				rev[len(tc.modes)-1-i] = m
			}
			if ok2, _ := busModeSetReason(rev); ok2 != ok {
				t.Fatalf("busModeSetReason order-dependent: %v=%v but reversed=%v", tc.modes, ok, ok2)
			}
		})
	}
}

// TestValidateBusesCrossSection covers the rules ValidateBuses enforces across
// sections (RFC-0003 §4, §5): a mode on more than one bus, a dangling
// credentials_ref, and a dangling bus_id are each refused with a specific reason.
func TestValidateBusesCrossSection(t *testing.T) {
	networks := []Network{{Name: "BM"}}

	t.Run("duplicate mode across buses", func(t *testing.T) {
		buses := []Bus{{ID: "A", Enabled: true}, {ID: "B", Enabled: true}}
		att := []Attachment{
			{BusID: "A", Mode: ModeDMR},
			{BusID: "B", Mode: ModeDMR}, // same mode, second bus — §5 rule 3
		}
		err := ValidateBuses(buses, att, networks)
		if err == nil {
			t.Fatal("want error for a mode on two buses, got nil")
		}
		want := `mode DMR is attached to more than one bus ("A" and "B")`
		if err.Error() != want {
			t.Fatalf("reason = %q, want %q", err.Error(), want)
		}
	})

	t.Run("duplicate mode on same bus", func(t *testing.T) {
		buses := []Bus{{ID: "A", Enabled: true}}
		att := []Attachment{{BusID: "A", Mode: ModeYSF}, {BusID: "A", Mode: ModeYSF}}
		if err := ValidateBuses(buses, att, networks); err == nil {
			t.Fatal("want error for a mode attached twice to one bus, got nil")
		}
	})

	t.Run("dangling credentials_ref", func(t *testing.T) {
		buses := []Bus{{ID: "A", Enabled: true}}
		att := []Attachment{{BusID: "A", Mode: ModeDMR, CredentialsRef: "nope"}}
		err := ValidateBuses(buses, att, networks)
		if err == nil {
			t.Fatal("want error for a credentials_ref matching no network, got nil")
		}
		want := `attachment for DMR references unknown network "nope"`
		if err.Error() != want {
			t.Fatalf("reason = %q, want %q", err.Error(), want)
		}
	})

	t.Run("resolving credentials_ref is accepted", func(t *testing.T) {
		buses := []Bus{{ID: "A", Enabled: true}}
		att := []Attachment{{BusID: "A", Mode: ModeDMR, CredentialsRef: "BM"}}
		if err := ValidateBuses(buses, att, networks); err != nil {
			t.Fatalf("credentials_ref pointing at an existing network should validate, got %v", err)
		}
	})

	t.Run("dangling bus_id", func(t *testing.T) {
		buses := []Bus{{ID: "A", Enabled: true}}
		att := []Attachment{{BusID: "ghost", Mode: ModeDMR}}
		err := ValidateBuses(buses, att, networks)
		if err == nil {
			t.Fatal("want error for an attachment on a non-existent bus, got nil")
		}
		want := `attachment for DMR references unknown bus "ghost"`
		if err.Error() != want {
			t.Fatalf("reason = %q, want %q", err.Error(), want)
		}
	})

	t.Run("duplicate bus id", func(t *testing.T) {
		buses := []Bus{{ID: "A"}, {ID: "A"}}
		if err := ValidateBuses(buses, nil, networks); err == nil {
			t.Fatal("want error for a duplicate bus id, got nil")
		}
	})
}

// busFixture is a fully-populated, valid set of buses+attachments exercising all
// three reframe modes and every translation field, so a round-trip cannot be
// masked by an empty field.
func busFixture() ([]Bus, []Attachment) {
	buses := []Bus{
		{ID: "local", Name: "Local Bus A", Enabled: true},
		{ID: "spare", Name: "Spare", Enabled: false},
	}
	att := []Attachment{
		{
			BusID: "local", Mode: ModeDMR, CredentialsRef: "BM_3102_United_States",
			Slot: "2", DefaultTG: "9", TGMap: map[string]string{"290": "310", "0": "9"},
		},
		{
			BusID: "local", Mode: ModeYSF,
			Target: "FCS00290", WiresXPassthrough: true,
		},
		{
			BusID: "local", Mode: ModeNXDN,
			ID: "12345", TG: "20", DefaultID: "65519",
		},
	}
	return buses, att
}

// seedNetworksForBuses gives busFixture a network its DMR attachment can resolve.
func seedNetworksForBuses(t *testing.T, s interface {
	Set(string, any, string) error
}) {
	t.Helper()
	nets := []Network{{Name: "BM_3102_United_States", Type: NetBrandmeister, Primary: true, Enabled: true}}
	if err := s.Set("networks", nets, "seed"); err != nil {
		t.Fatal(err)
	}
}

// TestBusSectionRoundTrip writes buses/attachments through the real validating
// setters, reads them back via Load, and asserts semantic equality of every
// translation param (RFC-0003 §6.3 round-trip). It then disables a bus and
// asserts NO attachment rows are deleted (RFC-0001 disable-preserves-data).
func TestBusSectionRoundTrip(t *testing.T) {
	s := memStore(t)
	seedNetworksForBuses(t, s)
	buses, att := busFixture()

	busJSON, _ := json.Marshal(buses)
	attJSON, _ := json.Marshal(att)
	// Attachments reference the buses, so write buses first.
	if err := SetBuses(s, busJSON, "test"); err != nil {
		t.Fatalf("SetBuses: %v", err)
	}
	if err := SetAttachments(s, attJSON, "test"); err != nil {
		t.Fatalf("SetAttachments: %v", err)
	}

	m, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Buses, buses) {
		t.Fatalf("buses round-trip lost data:\n want %+v\n  got %+v", buses, m.Buses)
	}
	if !reflect.DeepEqual(m.Attachments, att) {
		t.Fatalf("attachments round-trip lost data:\n want %+v\n  got %+v", att, m.Attachments)
	}

	// Byte-level round-trip: re-marshalling the loaded sections reproduces the input.
	if got, _ := json.Marshal(m.Buses); string(got) != string(busJSON) {
		t.Fatalf("buses not byte-identical:\n want %s\n  got %s", busJSON, got)
	}
	if got, _ := json.Marshal(m.Attachments); string(got) != string(attJSON) {
		t.Fatalf("attachments not byte-identical:\n want %s\n  got %s", attJSON, got)
	}

	// Disable a bus (enabled=false) and assert no attachment rows are deleted.
	disabled := append([]Bus(nil), buses...)
	disabled[0].Enabled = false
	disJSON, _ := json.Marshal(disabled)
	if err := SetBuses(s, disJSON, "test"); err != nil {
		t.Fatalf("disabling a bus should be accepted: %v", err)
	}
	after, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}
	if after.Buses[0].Enabled {
		t.Fatal("bus was not disabled")
	}
	if !reflect.DeepEqual(after.Attachments, att) {
		t.Fatalf("disabling a bus deleted or altered attachment rows:\n want %+v\n  got %+v", att, after.Attachments)
	}
}

// TestSetBusesRejectsUnknownField / TestSetAttachmentsRejectsUnknownField: the
// two setters reject schema drift like SetSection (model.go), so a typo'd field
// is a caller error, not silently dropped.
func TestSetBusesRejectsUnknownField(t *testing.T) {
	s := memStore(t)
	if err := SetBuses(s, []byte(`[{"id":"A","bogus":true}]`), "test"); err == nil {
		t.Fatal("SetBuses accepted an unknown field")
	}
}

func TestSetAttachmentsRejectsUnknownField(t *testing.T) {
	s := memStore(t)
	if err := s.Set("buses", []Bus{{ID: "A", Enabled: true}}, "seed"); err != nil {
		t.Fatal(err)
	}
	if err := SetAttachments(s, []byte(`[{"bus_id":"A","mode":"dmr","bogus":1}]`), "test"); err == nil {
		t.Fatal("SetAttachments accepted an unknown field")
	}
}

// TestSetAttachmentsRefusesInvalid confirms the validator runs on the write path:
// an attachment set that is invalid (a mode with no converter) is refused by
// SetAttachments, so an invalid bus can never be persisted (RFC-0003 §2).
func TestSetAttachmentsRefusesInvalid(t *testing.T) {
	s := memStore(t)
	if err := s.Set("buses", []Bus{{ID: "A", Enabled: true}}, "seed"); err != nil {
		t.Fatal(err)
	}
	body := []byte(`[{"bus_id":"A","mode":"dstar"},{"bus_id":"A","mode":"dmr"}]`)
	if err := SetAttachments(s, body, "test"); err == nil {
		t.Fatal("SetAttachments persisted an invalid bus")
	}
	// Nothing was written.
	m, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Attachments) != 0 {
		t.Fatalf("invalid attachments were persisted: %+v", m.Attachments)
	}
}

// TestSetBusesRejectsOrphanedAttachment guards the reverse direction: removing a
// bus that an attachment still references is refused by SetBuses, so the store
// never holds a dangling attachment (RFC-0003 §4).
func TestSetBusesRejectsOrphanedAttachment(t *testing.T) {
	s := memStore(t)
	seedNetworksForBuses(t, s)
	if err := s.Set("buses", []Bus{{ID: "A", Enabled: true}}, "seed"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("attachments", []Attachment{{BusID: "A", Mode: ModeDMR}}, "seed"); err != nil {
		t.Fatal(err)
	}
	// Try to remove bus A while its DMR attachment still points at it.
	if err := SetBuses(s, []byte(`[{"id":"B","enabled":true}]`), "test"); err == nil {
		t.Fatal("SetBuses removed a bus still referenced by an attachment")
	}
}
