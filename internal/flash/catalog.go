package flash

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/modem"
)

// The firmware catalog (RFC-0019 §2).
//
// Pi-Star's flashing scripts encode the answer in their own names —
// pistar-mmdvmhshatflash hs_dual_hat-12mhz — which asks an operator to state
// their board's reference oscillator, a fact almost nobody was told and one
// that fails silently when wrong: the radio is simply off frequency and
// transmits anyway. Detection already reads the oscillator off the wire, so
// the catalog is keyed on what detection produces and the operator is asked
// nothing they have not already been told by their own hardware.
//
// Every entry names boards from internal/modem's table. That join is the whole
// design: a board added there becomes flashable by being named here, and no
// filename is ever parsed to recover a fact about hardware.

// Variant is one firmware image.
type Variant struct {
	ID       string   `json:"id"`
	BoardIDs []string `json:"board_ids"` // modem.Board IDs this image is for
	TCXOHz   int      `json:"tcxo_hz"`
	Duplex   bool     `json:"duplex"`
	// Transport is "gpio" or "usb". The same board design sold in both forms
	// needs different images: the USB build links above a bootloader.
	Transport string `json:"transport"`

	// LoadAddress is where the image starts. On a GPIO board that is the base of
	// flash; on a USB board it is above the DFU bootloader, and writing below it
	// is the one operation that can brick a board (RFC-0019 §1).
	LoadAddress Hex32 `json:"load_address"`

	// ProductID, when set, is the die the image was built for. It is checked
	// against what the bootloader reports, which is the cheap guard against an
	// F1 image reaching an F4 part.
	ProductID Hex16 `json:"product_id,omitempty"`

	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	SigURL string `json:"sig_url"`

	Notes string `json:"notes,omitempty"`
}

// Catalog is a firmware release: one version, many board variants.
type Catalog struct {
	Version  string    `json:"version"`
	Variants []Variant `json:"variants"`
}

// Hex32 and Hex16 carry the "0x08000000" spelling a catalog uses for addresses
// and part IDs. JSON has no hex literal, and a decimal load address in a
// human-edited file is an invitation to a typo nobody would spot in review.
type (
	Hex32 uint32
	Hex16 uint16
)

func (h *Hex32) UnmarshalJSON(b []byte) error {
	v, err := parseHex(b, 32)
	*h = Hex32(v)
	return err
}

func (h Hex32) MarshalJSON() ([]byte, error) {
	return []byte(`"` + fmt.Sprintf("0x%08X", uint32(h)) + `"`), nil
}

func (h *Hex16) UnmarshalJSON(b []byte) error {
	v, err := parseHex(b, 16)
	*h = Hex16(v)
	return err
}

func (h Hex16) MarshalJSON() ([]byte, error) {
	return []byte(`"` + fmt.Sprintf("0x%04X", uint16(h)) + `"`), nil
}

func parseHex(b []byte, bits int) (uint64, error) {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return 0, nil
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X"), 16, bits)
	if err != nil {
		return 0, fmt.Errorf("flash: %q is not a %d-bit hex value", s, bits)
	}
	return v, nil
}

// ParseCatalog decodes and checks a catalog.
//
// It validates rather than trusting, even though the bytes arrived
// signature-verified: a signature proves the file is ours, not that it is
// coherent. An entry with no URL or no digest would fail later, mid-flash, with
// the modem already off the air.
func ParseCatalog(b []byte) (Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return Catalog{}, fmt.Errorf("flash: catalog is not valid JSON: %w", err)
	}
	if c.Version == "" {
		return Catalog{}, fmt.Errorf("flash: catalog has no version")
	}
	if len(c.Variants) == 0 {
		return Catalog{}, fmt.Errorf("flash: catalog %s lists no firmware", c.Version)
	}
	seen := map[string]bool{}
	for _, v := range c.Variants {
		switch {
		case v.ID == "":
			return Catalog{}, fmt.Errorf("flash: catalog %s has a variant with no id", c.Version)
		case seen[v.ID]:
			return Catalog{}, fmt.Errorf("flash: catalog %s lists %q twice", c.Version, v.ID)
		case len(v.BoardIDs) == 0:
			return Catalog{}, fmt.Errorf("flash: variant %s names no boards", v.ID)
		case v.URL == "":
			return Catalog{}, fmt.Errorf("flash: variant %s has no url", v.ID)
		case v.SHA256 == "":
			return Catalog{}, fmt.Errorf("flash: variant %s has no sha256", v.ID)
		case v.Transport != "gpio" && v.Transport != "usb":
			return Catalog{}, fmt.Errorf("flash: variant %s has transport %q, want gpio or usb", v.ID, v.Transport)
		}
		for _, id := range v.BoardIDs {
			if _, ok := modem.BoardByID(id); !ok {
				// A catalog naming a board this build has never heard of is a
				// firmware release ahead of the software. Saying so beats
				// silently offering an image for hardware nothing can identify.
				return Catalog{}, fmt.Errorf("flash: variant %s names board %q, which this version does not know", v.ID, id)
			}
		}
		seen[v.ID] = true
	}
	return c, nil
}

// ByID returns a variant by its catalog id.
func (c Catalog) ByID(id string) (Variant, bool) {
	for _, v := range c.Variants {
		if v.ID == id {
			return v, true
		}
	}
	return Variant{}, false
}

// MatchError is a refusal to choose firmware, with the reason an operator needs.
//
// Refusing is a feature. A wrong-oscillator image does not fail loudly — it
// produces a node transmitting off frequency, which is worse than an unflashed
// node and far harder to diagnose — so an ambiguous match becomes a question
// rather than a guess.
type MatchError struct {
	Reason  string
	Choices []string // catalog variant ids the operator may pick between
}

func (e *MatchError) Error() string { return "flash: " + e.Reason }

// MatchFor chooses the firmware for a detected modem.
//
// The three outcomes mirror modem.Resolution's, because they need the same
// three things from the operator: adopt silently, ask between candidates, or
// explain why nothing fits.
func (c Catalog) MatchFor(id modem.Identity) (Variant, error) {
	boards := id.Candidates
	if id.BoardID != "" {
		boards = []string{id.BoardID}
	}
	if len(boards) == 0 {
		return Variant{}, &MatchError{
			Reason:  "detection could not tell which board this is, so there is no way to know which firmware fits it",
			Choices: c.variantIDs(),
		}
	}

	// An assumed oscillator is not evidence. modem.Resolve fills TCXOHz from the
	// board table when the firmware is too old to report one, and flashing on
	// that assumption is exactly the failure this whole design exists to avoid.
	if id.TCXOAssumed {
		return Variant{}, &MatchError{
			Reason: "this firmware is too old to report its reference oscillator, and the board table only guesses it — " +
				"choose the firmware that matches the oscillator fitted to your board",
			Choices: c.variantIDsFor(boards, id),
		}
	}

	matches := c.candidates(boards, id)
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Variant{}, &MatchError{
			Reason: fmt.Sprintf("no firmware in catalog %s fits %s", c.Version, describe(id)),
		}
	default:
		return Variant{}, &MatchError{
			Reason:  fmt.Sprintf("more than one firmware fits %s", describe(id)),
			Choices: variantIDs(matches),
		}
	}
}

// candidates filters the catalog by everything the modem actually told us.
func (c Catalog) candidates(boards []string, id modem.Identity) []Variant {
	seen := map[string]bool{}
	var out []Variant
	for _, v := range c.Variants {
		if !v.forAnyBoard(boards) {
			continue
		}
		// Duplex is a hard filter, not a preference: a single-radio board running
		// a dual image reports capabilities it does not have, and a duplex board
		// running a simplex image loses half of itself.
		if v.Duplex != id.Duplex {
			continue
		}
		if id.TCXOHz != 0 && v.TCXOHz != 0 && v.TCXOHz != id.TCXOHz {
			continue
		}
		if id.Transport != "" && v.Transport != id.Transport {
			continue
		}
		if seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		out = append(out, v)
	}
	return out
}

func (v Variant) forAnyBoard(boards []string) bool {
	for _, want := range boards {
		for _, got := range v.BoardIDs {
			if got == want {
				return true
			}
		}
	}
	return false
}

// Describe renders a variant the way it should appear next to a flash button.
func (v Variant) Describe() string {
	parts := []string{v.ID}
	if v.TCXOHz != 0 {
		parts = append(parts, modem.TCXOLabel(v.TCXOHz))
	}
	if v.Duplex {
		parts = append(parts, "duplex")
	}
	return strings.Join(parts, ", ")
}

func (c Catalog) variantIDs() []string { return variantIDs(c.Variants) }

func (c Catalog) variantIDsFor(boards []string, id modem.Identity) []string {
	// Deliberately ignores the oscillator: this list is what the operator picks
	// FROM when the oscillator is the unknown.
	var out []Variant
	for _, v := range c.Variants {
		if v.forAnyBoard(boards) && v.Duplex == id.Duplex {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return c.variantIDs()
	}
	return variantIDs(out)
}

func variantIDs(vs []Variant) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.ID)
	}
	sort.Strings(out)
	return out
}

// describe renders what was detected, for a refusal an operator can act on.
func describe(id modem.Identity) string {
	parts := []string{}
	if id.HWType != "" {
		parts = append(parts, id.HWType)
	}
	if id.BoardID != "" {
		if b, ok := modem.BoardByID(id.BoardID); ok {
			parts = append(parts, b.Name)
		}
	}
	if id.TCXOHz != 0 {
		parts = append(parts, modem.TCXOLabel(id.TCXOHz))
	}
	if id.Duplex {
		parts = append(parts, "duplex")
	}
	if id.Transport != "" {
		parts = append(parts, "on "+id.Transport)
	}
	if len(parts) == 0 {
		return "this modem"
	}
	return strings.Join(parts, " ")
}
