// Package wxfeed subscribes to the public NWS alert feed and turns what arrives
// into decisions about what to put on the air.
//
// # The one thing that makes this dangerous
//
// The feed's alert messages are RETAINED. Subscribing therefore delivers every
// hazard currently in effect immediately, and again after every reconnect. On a
// dashboard that is a redraw. On a station that transmits automatically it is a
// string of messages and voice announcements going out over the air for hazards
// the operator already knows about, every time the link flaps.
//
// The MQTT retain flag is the discriminator and it is per-message:
// Retained()==true is state synchronisation and must announce nothing;
// Retained()==false is live. Everything in this package is arranged around not
// getting that wrong, and Decide returns the reason it reached its verdict so a
// test can assert on it rather than on a side effect.
//
// # Tombstones
//
// A hazard that ends is signalled by a zero-length retained publish on every
// topic it appeared on. That clears state and announces nothing, ever.
package wxfeed

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Alert is the subset of the feed's payload this feature acts on. The feed
// carries more (description, instruction, geometry, sources); those are ignored
// here rather than modelled, because a field nothing reads is a field that goes
// stale without anyone noticing.
type Alert struct {
	ID           int64    `json:"id"`
	VTEC         string   `json:"vtec"`
	Event        string   `json:"event"`
	Office       string   `json:"office"`
	Phenomena    string   `json:"phenomena"`
	Significance string   `json:"significance"`
	ETN          int      `json:"etn"`
	Action       string   `json:"action"`
	Status       string   `json:"status"`
	Severity     string   `json:"severity"`
	Headline     string   `json:"headline"`
	SAME         []string `json:"same"`

	// Ends is when the hazard is over. Expires is CAP's update deadline and is
	// NOT the same thing — an alert routinely outlives its `expires` while the
	// office reissues it. Anything reasoning about "is this still on" uses Ends.
	Ends    time.Time `json:"ends"`
	Expires time.Time `json:"expires"`
	Onset   time.Time `json:"onset"`
}

// DedupKey identifies a hazard across the several county topics it appears on.
//
// One alert is published once per county it covers, with an identical payload,
// so a node monitoring three counties in a warning's footprint receives it three
// times. VTEC identifies the hazard; alerts sourced from api.weather.gov carry
// none, and fall back to the row id.
func (a Alert) DedupKey() string {
	if v := strings.TrimSpace(a.VTEC); v != "" {
		return v
	}
	return fmt.Sprintf("id%d", a.ID)
}

// Verdict is what to do with one delivery, and why.
type Verdict struct {
	Announce bool
	Clear    bool
	// Reason is for logs, tests and the event history. It is never shown to an
	// operator, so it is a Go string rather than a translated one.
	Reason string
}

// Decision inputs the caller supplies. Keeping them explicit rather than
// reaching for config here keeps this package testable without a store.
type Policy interface {
	// ShouldAnnounce reports whether an action code is one to announce.
	ShouldAnnounce(action string) bool
	// Announces reports whether the alert's class produces any transmission at
	// all. An alert nobody routes is not worth deduping or storing.
	Announces(event, significance string) bool
}

// Decide is the whole safety argument in one function.
//
// It is deliberately pure: no clock, no store, no network. Everything that could
// put a transmission on the air is a consequence of what this returns, so it can
// be exhaustively tested.
func Decide(topic string, payload []byte, retained bool, seen func(key, action string) bool, p Policy) (Alert, Verdict) {
	// A zero-length payload is a tombstone on whatever topic carried it. It
	// clears state and never announces. It is checked before parsing because
	// there is nothing to parse.
	if len(payload) == 0 {
		return Alert{}, Verdict{Clear: true, Reason: "tombstone"}
	}

	var a Alert
	if err := json.Unmarshal(payload, &a); err != nil {
		return Alert{}, Verdict{Reason: "unparseable payload"}
	}

	// Retained means "this is what is already true", not "this just happened".
	// It is checked before the action and before dedup, because a retained
	// message legitimately carries action NEW for a hazard issued hours ago and
	// every other test would pass it.
	if retained {
		return a, Verdict{Reason: "retained: state sync, not an event"}
	}

	if st := strings.ToLower(strings.TrimSpace(a.Status)); st == "expired" || st == "cancelled" {
		return a, Verdict{Clear: true, Reason: "status " + st}
	}
	if !p.Announces(a.Event, a.Significance) {
		return a, Verdict{Reason: "no channel configured for this class"}
	}
	if !p.ShouldAnnounce(a.Action) {
		return a, Verdict{Reason: "action " + a.Action + " is not announced"}
	}
	// The same hazard arrives once per monitored county. Dedup is on
	// (hazard, action) rather than the hazard alone, so a later EXT or CAN can
	// still be announced if the operator asked for those.
	if seen != nil && seen(a.DedupKey(), a.Action) {
		return a, Verdict{Reason: "already announced"}
	}
	return a, Verdict{Announce: true, Reason: "live " + a.Action}
}

// SMSText renders an alert as the short message that goes on the air.
//
// The budget is small and the important words are at the front, because a radio
// shows the beginning of a message and an operator reading it on a two-line
// display should learn what and until when before anything else. `ends` is
// rendered in the node's local time: a station's users are local to it, and UTC
// on a handheld display helps nobody.
func SMSText(a Alert, now time.Time, loc *time.Location, budget int) string {
	if loc == nil {
		loc = time.UTC
	}
	head := strings.TrimSpace(a.Event)
	if head == "" {
		head = "Weather alert"
	}
	out := head
	if !a.Ends.IsZero() && a.Ends.After(now) {
		out += " until " + a.Ends.In(loc).Format("3:04 PM")
	}
	if h := strings.TrimSpace(a.Headline); h != "" {
		out += ". " + h
	}
	return truncateOnWord(out, budget)
}

// truncateOnWord cuts to a budget without splitting a word, because a message
// ending mid-word reads as a fault in the node rather than as a long alert.
func truncateOnWord(s string, budget int) string {
	if budget <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= budget {
		return s
	}
	cut := string(r[:budget])
	if i := strings.LastIndexAny(cut, " \t\n"); i > budget/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " .,;:") + "…"
}
