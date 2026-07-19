package config

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/KN4OQW/waypoint/internal/store"
)

// DefaultHistoryRetentionDays is the out-of-the-box event-history window: a week
// of last-heard / event log kept in events.db before the nightly prune. Chosen as
// a sane default for the target hardware (a Pi Zero W's SD card) — long enough to
// answer "who did I hear yesterday / over the weekend", short enough to keep the
// database small under sustained traffic. The operator raises or lowers it (or
// sets 0 for keep-forever) from the Station Settings tab.
const DefaultHistoryRetentionDays = 7

// DefaultHistory is the retention policy a fresh store seeds and a store created
// before this section existed backfills to (RFC-0004).
func DefaultHistory() History {
	return History{RetentionDays: DefaultHistoryRetentionDays}
}

// DefaultHADiscoveryPrefix is Home Assistant's own default discovery topic root.
const DefaultHADiscoveryPrefix = "homeassistant"

// DefaultHomeAssistant is the off-by-default integration a fresh store seeds and
// an older store backfills to (#9). Disabled so a node never publishes discovery
// topics until the operator opts in; the prefix is Home Assistant's default so it
// works out of the box once enabled.
func DefaultHomeAssistant() HomeAssistant {
	return HomeAssistant{Enabled: false, DiscoveryPrefix: DefaultHADiscoveryPrefix}
}

// ValidateHistory enforces the one rule the type cannot: retention cannot be
// negative. 0 is legal and means keep forever (the nightly prune is disabled);
// any positive count is a day window.
func ValidateHistory(h History) error {
	if h.RetentionDays < 0 {
		return fmt.Errorf("history retention_days must be >= 0 (0 = keep forever), got %d", h.RetentionDays)
	}
	return nil
}

// SetHistory merges a partial JSON body into the history section and writes it
// back, exactly like SetSection (unknown fields rejected, unspecified fields
// preserved), but validates the merged policy before committing so a negative
// retention is rejected at save time. The history section is routed here instead
// of through the generic SetSection so the rule is enforced on every API write.
func SetHistory(s *store.Store, raw []byte, by string) error {
	var h History
	if _, err := s.GetInto("history", &h); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return err
	}
	if err := ValidateHistory(h); err != nil {
		return err
	}
	return s.Set("history", &h, by)
}
