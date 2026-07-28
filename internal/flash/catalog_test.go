package flash

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/modem"
)

// A catalog covering the shapes that actually exist: two oscillators for the
// same simplex hat, a duplex board, and a USB sibling.
func testCatalog() Catalog {
	return Catalog{
		Version: "v1.6.1-wp1",
		Variants: []Variant{
			{
				ID: "hs_hat-12m288", BoardIDs: []string{"mmdvm_hs_hat", "jumbospot"},
				TCXOHz: 12_288_000, Transport: "gpio", LoadAddress: 0x08000000,
				ProductID: 0x0410, URL: "https://example.invalid/hs_hat_12.bin", SHA256: "aa", SigURL: "https://example.invalid/hs_hat_12.bin.minisig",
			},
			{
				ID: "hs_hat-14m7456", BoardIDs: []string{"mmdvm_hs_hat", "jumbospot"},
				TCXOHz: 14_745_600, Transport: "gpio", LoadAddress: 0x08000000,
				ProductID: 0x0410, URL: "https://example.invalid/hs_hat_14.bin", SHA256: "bb", SigURL: "https://example.invalid/hs_hat_14.bin.minisig",
			},
			{
				ID: "hs_dual_hat-14m7456", BoardIDs: []string{"mmdvm_hs_dual_hat", "zumspot_duplex", "lonestar_dual"},
				TCXOHz: 14_745_600, Duplex: true, Transport: "gpio", LoadAddress: 0x08000000,
				ProductID: 0x0410, URL: "https://example.invalid/dual.bin", SHA256: "cc", SigURL: "https://example.invalid/dual.bin.minisig",
			},
			{
				ID: "zumspot_usb", BoardIDs: []string{"zumspot_usb"},
				TCXOHz: 14_745_600, Transport: "usb", LoadAddress: 0x08002000,
				ProductID: 0x0410, URL: "https://example.invalid/usb.bin", SHA256: "dd", SigURL: "https://example.invalid/usb.bin.minisig",
			},
		},
	}
}

func TestHexFieldsRoundTripThroughJSON(t *testing.T) {
	var v Variant
	raw := `{"id":"x","board_ids":["mmdvm_hs_hat"],"transport":"gpio",
	         "load_address":"0x08002000","product_id":"0x0410","url":"u","sha256":"s"}`
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v.LoadAddress != 0x08002000 {
		t.Errorf("load address = 0x%08X, want 0x08002000", uint32(v.LoadAddress))
	}
	if v.ProductID != 0x0410 {
		t.Errorf("product id = 0x%04X, want 0x0410", uint16(v.ProductID))
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"0x08002000"`) {
		t.Errorf("marshalled as %s, want the hex spelling back", b)
	}
}

func TestParseCatalogRejectsWhatWouldFailMidFlash(t *testing.T) {
	good := `{"version":"v1","variants":[{"id":"a","board_ids":["mmdvm_hs_hat"],
	          "transport":"gpio","load_address":"0x08000000","url":"u","sha256":"s"}]}`
	if _, err := ParseCatalog([]byte(good)); err != nil {
		t.Fatalf("a valid catalog was rejected: %v", err)
	}

	for _, tc := range []struct{ name, json, wantIn string }{
		{"no version", `{"variants":[{"id":"a"}]}`, "no version"},
		{"no variants", `{"version":"v1","variants":[]}`, "lists no firmware"},
		{"no id", `{"version":"v1","variants":[{"board_ids":["mmdvm_hs_hat"]}]}`, "no id"},
		{"duplicate id", `{"version":"v1","variants":[
			{"id":"a","board_ids":["mmdvm_hs_hat"],"transport":"gpio","url":"u","sha256":"s"},
			{"id":"a","board_ids":["mmdvm_hs_hat"],"transport":"gpio","url":"u","sha256":"s"}]}`, "twice"},
		{"no boards", `{"version":"v1","variants":[{"id":"a","transport":"gpio","url":"u","sha256":"s"}]}`, "names no boards"},
		{"no url", `{"version":"v1","variants":[{"id":"a","board_ids":["mmdvm_hs_hat"],"transport":"gpio","sha256":"s"}]}`, "no url"},
		{"no digest", `{"version":"v1","variants":[{"id":"a","board_ids":["mmdvm_hs_hat"],"transport":"gpio","url":"u"}]}`, "no sha256"},
		{"bad transport", `{"version":"v1","variants":[{"id":"a","board_ids":["mmdvm_hs_hat"],"transport":"i2c","url":"u","sha256":"s"}]}`, "transport"},
		{"unknown board", `{"version":"v1","variants":[{"id":"a","board_ids":["nsa_backdoor_hat"],"transport":"gpio","url":"u","sha256":"s"}]}`, "does not know"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCatalog([]byte(tc.json))
			if err == nil {
				t.Fatal("accepted a catalog that would have failed mid-flash")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("err = %v, want it to mention %q", err, tc.wantIn)
			}
		})
	}
}

func TestMatchForChoosesFromWhatTheModemReported(t *testing.T) {
	cat := testCatalog()

	for _, tc := range []struct {
		name string
		id   modem.Identity
		want string
	}{
		{
			name: "one board, oscillator reported",
			id: modem.Identity{
				BoardID: "mmdvm_hs_dual_hat", Candidates: []string{"mmdvm_hs_dual_hat"},
				TCXOHz: 14_745_600, Duplex: true, Transport: "gpio",
			},
			want: "hs_dual_hat-14m7456",
		},
		{
			// The common case: sibling boards that share an oscillator and a radio
			// count take the same image, so the ambiguity does not reach the operator.
			name: "several boards, one image",
			id: modem.Identity{
				Candidates: []string{"mmdvm_hs_hat", "jumbospot"},
				TCXOHz:     12_288_000, Transport: "gpio",
			},
			want: "hs_hat-12m288",
		},
		{
			name: "the oscillator is the discriminator",
			id: modem.Identity{
				BoardID: "mmdvm_hs_hat", TCXOHz: 14_745_600, Transport: "gpio",
			},
			want: "hs_hat-14m7456",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := cat.MatchFor(tc.id)
			if err != nil {
				t.Fatalf("MatchFor: %v", err)
			}
			if v.ID != tc.want {
				t.Errorf("matched %s, want %s", v.ID, tc.want)
			}
		})
	}
}

// An assumed oscillator is not evidence. modem.Resolve fills it in from the
// board table when the firmware is too old to report one, and flashing on that
// assumption produces a node transmitting off frequency — the exact failure
// this design exists to prevent.
func TestMatchForRefusesAnAssumedOscillator(t *testing.T) {
	cat := testCatalog()
	id := modem.Identity{
		BoardID: "mmdvm_hs_hat", TCXOHz: 12_288_000, TCXOAssumed: true, Transport: "gpio",
	}

	var me *MatchError
	if _, err := cat.MatchFor(id); !errors.As(err, &me) {
		t.Fatalf("err = %v, want a *MatchError", err)
	}
	if !strings.Contains(me.Reason, "oscillator") {
		t.Errorf("reason = %q, want it to name the oscillator", me.Reason)
	}
	// The operator has to be given something to choose BETWEEN, or the refusal is
	// a dead end.
	if len(me.Choices) != 2 {
		t.Errorf("choices = %v, want both oscillator variants of this board", me.Choices)
	}
}

func TestMatchForRefusesWhenSeveralImagesFit(t *testing.T) {
	cat := testCatalog()
	// Old firmware that reported no oscillator at all, on a board sold with both.
	id := modem.Identity{BoardID: "mmdvm_hs_hat", Transport: "gpio"}

	var me *MatchError
	_, err := cat.MatchFor(id)
	if !errors.As(err, &me) {
		t.Fatalf("err = %v, want a *MatchError", err)
	}
	if len(me.Choices) != 2 {
		t.Errorf("choices = %v, want the two candidates", me.Choices)
	}
}

// A single-radio board running a dual image reports capabilities it does not
// have; a duplex board running a simplex image loses half of itself.
func TestMatchForWillNotCrossTheDuplexLine(t *testing.T) {
	cat := testCatalog()
	id := modem.Identity{
		BoardID: "mmdvm_hs_dual_hat", TCXOHz: 14_745_600, Duplex: false, Transport: "gpio",
	}
	if v, err := cat.MatchFor(id); err == nil {
		t.Fatalf("matched %s for a simplex modem on a duplex board", v.ID)
	}
}

func TestMatchForSeparatesGPIOAndUSBSiblings(t *testing.T) {
	cat := testCatalog()
	id := modem.Identity{BoardID: "zumspot_usb", TCXOHz: 14_745_600, Transport: "usb"}

	v, err := cat.MatchFor(id)
	if err != nil {
		t.Fatalf("MatchFor: %v", err)
	}
	if v.Transport != "usb" || v.LoadAddress != 0x08002000 {
		t.Errorf("matched %s at 0x%08X, want the USB image above the bootloader", v.ID, uint32(v.LoadAddress))
	}
}

func TestMatchForExplainsWhenDetectionKnewNothing(t *testing.T) {
	cat := testCatalog()

	var me *MatchError
	if _, err := cat.MatchFor(modem.Identity{Transport: "gpio"}); !errors.As(err, &me) {
		t.Fatalf("err = %v, want a *MatchError", err)
	}
	if len(me.Choices) == 0 {
		t.Error("a refusal with no choices leaves the operator nowhere to go")
	}
}

func TestMatchForSaysSoWhenNothingFits(t *testing.T) {
	cat := testCatalog()
	// A known board with an oscillator no image was built for.
	id := modem.Identity{BoardID: "mmdvm_hs_hat", TCXOHz: 16_000_000, Transport: "gpio"}

	var me *MatchError
	_, err := cat.MatchFor(id)
	if !errors.As(err, &me) {
		t.Fatalf("err = %v, want a *MatchError", err)
	}
	if !strings.Contains(me.Reason, "no firmware") {
		t.Errorf("reason = %q, want it to say nothing fits", me.Reason)
	}
}

func TestVariantDescribeNamesTheOscillator(t *testing.T) {
	v := Variant{ID: "hs_dual_hat-14m7456", TCXOHz: 14_745_600, Duplex: true}
	got := v.Describe()
	if !strings.Contains(got, "14.7456 MHz") || !strings.Contains(got, "duplex") {
		t.Errorf("Describe = %q, want the oscillator and the duplex flag", got)
	}
}

// TestParseCatalogFile validates a catalog produced elsewhere — in practice the
// one the firmware repo's build emits. It skips unless pointed at a file, so it
// costs nothing in normal runs and is a one-command check that a firmware
// release and this build agree about which boards exist:
//
//	WAYPOINT_CATALOG=/path/to/firmware.json go test ./internal/flash -run CatalogFile -v
func TestParseCatalogFile(t *testing.T) {
	path := os.Getenv("WAYPOINT_CATALOG")
	if path == "" {
		t.Skip("set WAYPOINT_CATALOG to validate a firmware catalog")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := ParseCatalog(b)
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	t.Logf("catalog %s: %d variants", cat.Version, len(cat.Variants))
	for _, v := range cat.Variants {
		t.Logf("  %-38s %-5s 0x%08X  %s", v.ID, v.Transport, uint32(v.LoadAddress), v.Describe())
	}

	// Every board Waypoint knows should be flashable, or the catalog has a gap
	// an operator will meet as "no firmware fits your board".
	covered := map[string]bool{}
	for _, v := range cat.Variants {
		for _, id := range v.BoardIDs {
			covered[id] = true
		}
	}
	for _, id := range modem.BoardIDs() {
		if !covered[id] {
			t.Errorf("board %q has no firmware in this catalog", id)
		}
	}
}
