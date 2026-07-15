package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/store"
)

// lcdRowsFallback is the row count assumed when LCD.Rows is blank or unparseable,
// matching DefaultLCD and NewRenderer's fallback so validation and rendering agree.
const lcdRowsFallback = 4

// lcdRows parses the configured panel height, falling back to lcdRowsFallback for
// a blank or malformed value so a bad string never makes geometry validation lie.
func lcdRows(l LCD) int {
	if v, err := strconv.Atoi(strings.TrimSpace(l.Rows)); err == nil && v > 0 {
		return v
	}
	return lcdRowsFallback
}

// ValidateLCD enforces the one rule the template system cannot express in the
// type: a page must not declare more lines than the panel has rows. The renderer
// is geometry-agnostic — it truncates and pads to the configured geometry — but a
// page with more lines than rows is an operator mistake (lines that could never
// show), so it is rejected at save time rather than silently clipped. The error
// names the geometry (cols×rows) so the operator knows which panel they exceeded.
func ValidateLCD(l LCD) error {
	rows := lcdRows(l)
	cols := strings.TrimSpace(l.Cols)
	if cols == "" {
		cols = "20"
	}
	for _, p := range l.Pages {
		if len(p.Lines) > rows {
			name := strings.TrimSpace(p.Name)
			if name == "" {
				name = "(unnamed)"
			}
			return fmt.Errorf("LCD page %q has %d lines but the panel is %sx%d (max %d rows)",
				name, len(p.Lines), cols, rows, rows)
		}
	}
	return nil
}

// SetLCD merges a partial JSON body into the lcd section and writes it back,
// exactly like SetSection (unknown fields rejected, unspecified fields preserved),
// but validates the merged panel geometry before committing so an invalid page
// set is rejected at save time (ValidateLCD). The lcd section is routed here
// instead of through the generic SetSection so the geometry rule is enforced on
// every write from the API.
func SetLCD(s *store.Store, raw []byte, by string) error {
	var l LCD
	if _, err := s.GetInto("lcd", &l); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return err
	}
	if err := ValidateLCD(l); err != nil {
		return err
	}
	return s.Set("lcd", &l, by)
}
