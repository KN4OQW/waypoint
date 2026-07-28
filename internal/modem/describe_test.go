package modem

import "testing"

// Real identity strings from the firmware generations in the field. The parser
// is positional in none of these: the MHz field only appears from the 1.5.x era,
// full-size MMDVM uses a different shape entirely, and a board that answers with
// something this table has never seen still has to come back describable.
func TestParseDescription(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Description
	}{
		{
			name: "bench dual hat (docs/on-hardware-report.md)",
			raw:  "MMDVM_HS_Dual_Hat-v1.6.1 20230526 14.7456MHz dual ADF7021 FW by CA6JAU GitID #899fc2a",
			want: Description{
				HWType: "MMDVM_HS_Dual_Hat", Firmware: "1.6.1", Built: "20230526",
				TCXOHz: 14_745_600, Dual: true, ADF7021: true, Author: "CA6JAU", GitID: "899fc2a",
			},
		},
		{
			name: "simplex hat, 12.288 MHz",
			raw:  "MMDVM_HS_Hat-v1.5.1 20210423 12.288MHz ADF7021 FW by CA6JAU GitID #29b0d34",
			want: Description{
				HWType: "MMDVM_HS_Hat", Firmware: "1.5.1", Built: "20210423",
				TCXOHz: 12_288_000, ADF7021: true, Author: "CA6JAU", GitID: "29b0d34",
			},
		},
		{
			name: "pre-1.5 firmware reports no oscillator",
			raw:  "MMDVM_HS_Dual_Hat-v1.4.17 20181124 dual ADF7021 FW by CA6JAU GitID #28d0ba0",
			want: Description{
				HWType: "MMDVM_HS_Dual_Hat", Firmware: "1.4.17", Built: "20181124",
				TCXOHz: 0, Dual: true, ADF7021: true, Author: "CA6JAU", GitID: "28d0ba0",
			},
		},
		{
			name: "ZUMspot",
			raw:  "ZUMspot-v1.5.2 20210227 14.7456MHz ADF7021 FW by CA6JAU GitID #4bd0d21",
			want: Description{
				HWType: "ZUMspot", Firmware: "1.5.2", Built: "20210227",
				TCXOHz: 14_745_600, ADF7021: true, Author: "CA6JAU", GitID: "4bd0d21",
			},
		},
		{
			name: "Nano hotSPOT",
			raw:  "Nano_hotSPOT-v1.5.1 20200609 12.288MHz ADF7021 FW by CA6JAU GitID #a4e6f9c",
			want: Description{
				HWType: "Nano_hotSPOT", Firmware: "1.5.1", Built: "20200609",
				TCXOHz: 12_288_000, ADF7021: true, Author: "CA6JAU", GitID: "a4e6f9c",
			},
		},
		{
			name: "full-size MMDVM is a different grammar and must not crash",
			raw:  "MMDVM 20200621 (D-Star/DMR/System Fusion/P25/NXDN/POCSAG/FM) STM32F722 FW by G4KLX",
			want: Description{
				HWType: "MMDVM", Built: "20200621", MCU: "STM32F722", Author: "G4KLX",
			},
		},
		{
			name: "an unrecognised modem is still describable",
			raw:  "SomeNewBoard",
			want: Description{HWType: "SomeNewBoard"},
		},
		{
			name: "empty",
			raw:  "",
			want: Description{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseDescription(tc.raw)
			tc.want.Raw = got.Raw // Raw is echoed verbatim; compare the parsed fields
			if got != tc.want {
				t.Errorf("ParseDescription(%q)\n got %+v\nwant %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestDualQualifiesOnlyTheChipItPrecedes(t *testing.T) {
	// "dual" is an adjective on ADF7021, not a word that appears somewhere in
	// the line. A board named "Dual" that carries one radio is simplex.
	d := ParseDescription("Dual_Something-v1.0.0 20200101 12.288MHz ADF7021 FW by NOCALL")
	if d.Dual {
		t.Errorf("Dual = true for a single-radio board: %+v", d)
	}
}

func TestTCXOParsingAvoidsFloat(t *testing.T) {
	// 14.7456 MHz is exactly the value that does not survive a trip through
	// binary floating point, and the repo's rule is that frequencies never take
	// that trip (see config.Modem).
	for raw, want := range map[string]int{
		"12.288":   12_288_000,
		"14.7456":  14_745_600,
		"20":       20_000_000,
		"0.000001": 1,
		"nonsense": 0,
	} {
		if got := megahertzToHz(raw); got != want {
			t.Errorf("megahertzToHz(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestTCXOLabel(t *testing.T) {
	// Operators know these oscillators by their printed label, not by a Hz count.
	for hz, want := range map[int]string{
		14_745_600: "14.7456 MHz",
		12_288_000: "12.288 MHz",
		20_000_000: "20 MHz",
		0:          "",
	} {
		if got := TCXOLabel(hz); got != want {
			t.Errorf("TCXOLabel(%d) = %q, want %q", hz, got, want)
		}
	}
}
