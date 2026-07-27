package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/KN4OQW/waypoint/internal/store"
)

// Update channels. Today the channel gates only the waypointd signed-manifest
// update (RFC-0014); the apt side serves both channels from the same bookworm
// suite, so a channel change does not (yet) change the apt source. Mapping a
// channel to an apt suite is a follow-up documented in docs/updates.md.
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

// DefaultQuietWindow is when opt-in auto-apply runs: 04:00 local, a low-traffic
// hour for a hotspot. Off by default (AutoApply=false).
const DefaultQuietWindow = "04:00"

// UpdatePrefs is the operator-set update policy (RFC-0001 store section "update").
// It is edited from the Updates tab like any other config section; the machine-set
// check state (last check, available cache) lives separately in UpdateState so an
// operator save never clobbers it and vice-versa.
type UpdatePrefs struct {
	Channel string `json:"channel"` // ChannelStable | ChannelBeta
	// CheckEnabled gates every *unattended* check: the periodic signed-manifest
	// fetch and the periodic apt refresh. Turning it off means this node makes no
	// outbound request unless an operator asks for one — the "check now" controls
	// and the CLI modes still work, and nothing else about the node changes
	// (GOVERNANCE.md principle 2 / issue #15). Auto-apply rides on the check, so it
	// stops with it; a manual apply does not. Default on. The check carries no
	// device identifier either way — see docs/updates.md, "What leaves the device".
	CheckEnabled bool   `json:"check_enabled"`
	AutoApply    bool   `json:"auto_apply"`   // apply stack updates automatically in the quiet window
	QuietWindow  string `json:"quiet_window"` // "HH:MM" local time auto-apply runs
}

// DefaultUpdate is the out-of-the-box policy: stable channel, automatic checks on
// (they are anonymous, and an operator who does not want them turns them off),
// notify-and-click (auto-apply off), quiet window at 04:00 for when an operator
// does opt in.
func DefaultUpdate() UpdatePrefs {
	return UpdatePrefs{Channel: ChannelStable, CheckEnabled: true, AutoApply: false, QuietWindow: DefaultQuietWindow}
}

// BackfillUpdateCheckEnabled seeds check_enabled=true on an update row written
// before the field existed. A plain decode reads the missing key as Go's false,
// which would silently switch a node's update checks off across an upgrade — so
// this inspects the *stored JSON* for the key rather than the decoded value. A row
// that already carries the field is left alone in either state: an operator who
// opted out stays opted out. Returns whether it wrote.
func BackfillUpdateCheckEnabled(s *store.Store) (bool, error) {
	raw, ok, err := s.Get("update")
	if err != nil || !ok {
		return false, err
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return false, err
	}
	if _, ok := present["check_enabled"]; ok {
		return false, nil
	}
	var u UpdatePrefs
	if err := json.Unmarshal(raw, &u); err != nil {
		return false, err
	}
	u.CheckEnabled = true
	return true, s.Set("update", &u, "backfill")
}

// ValidateUpdate enforces the channel enum and the HH:MM quiet-window format.
func ValidateUpdate(u UpdatePrefs) error {
	if u.Channel != ChannelStable && u.Channel != ChannelBeta {
		return fmt.Errorf("update channel must be %q or %q, got %q", ChannelStable, ChannelBeta, u.Channel)
	}
	if _, err := time.Parse("15:04", u.QuietWindow); err != nil {
		return fmt.Errorf("update quiet_window must be HH:MM (24-hour), got %q", u.QuietWindow)
	}
	return nil
}

// SetUpdate merges a partial JSON body into the update section and writes it back
// (unknown fields rejected, unspecified fields preserved), validating the merged
// policy so an invalid channel or quiet window is refused at save time.
func SetUpdate(s *store.Store, raw []byte, by string) error {
	var u UpdatePrefs
	if _, err := s.GetInto("update", &u); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&u); err != nil {
		return err
	}
	if err := ValidateUpdate(u); err != nil {
		return err
	}
	return s.Set("update", &u, by)
}

// --- machine-written check state (separate key so operator PUTs never touch it) ---

// updateStateKey holds the last-check timestamp and the cached available-updates
// blob. It is written by the update-check code, not the operator UI.
const updateStateKey = "update_state"

// UpdateState is the cached result of the last update check. Available is an opaque
// JSON blob whose shape the update API owns (the parsed stack updates + the binary
// manifest availability), kept out of the typed config model so this package need
// not depend on the updater packages.
type UpdateState struct {
	LastCheck     time.Time       `json:"last_check"`
	Available     json.RawMessage `json:"available,omitempty"`
	LastAutoApply time.Time       `json:"last_auto_apply,omitempty"` // when auto-apply last ran (quiet-window throttle)
	// Binary is the same thing for the waypointd signed-manifest check — an opaque
	// blob whose shape the update API owns — with its own stamp, because the two
	// checks are independent (a node can have the apt repo without the manifest,
	// and vice versa).
	Binary          json.RawMessage `json:"binary,omitempty"`
	LastBinaryCheck time.Time       `json:"last_binary_check,omitempty"`
}

// GetUpdateState reads the cached check state (zero value if never checked).
func GetUpdateState(s *store.Store) (UpdateState, error) {
	var st UpdateState
	if _, err := s.GetInto(updateStateKey, &st); err != nil {
		return UpdateState{}, err
	}
	return st, nil
}

// SetUpdateState persists the cached check state.
func SetUpdateState(s *store.Store, st UpdateState, by string) error {
	return s.Set(updateStateKey, &st, by)
}
