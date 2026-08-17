package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/store"
)

// Weather alert broadcasting: which hazards, for which counties, onto which
// talkgroups.
//
// The node subscribes to a public NWS alert feed and, when a hazard matching the
// operator's policy is issued, transmits a short message to the configured
// talkgroups and queues a voice announcement. Everything here is CONFIGURATION;
// the ingest, the policy evaluation and the delivery live elsewhere.
//
// # Why this is broadcast and not subscription
//
// An earlier design had operators' users subscribe to alerts individually by
// texting the node. That needs the node to RECEIVE short data, which on a duplex
// node it cannot do — see KN4OQW/waypoint#263, where a message from a radio is
// discarded inside the modem with no trace on any surface the node owns.
// Transmission is unaffected by that, so a broadcast feature is buildable today
// and a subscription one is not.
//
// # The two axes an operator actually thinks in
//
// The feed labels every hazard with a VTEC significance — W warning, A watch,
// Y advisory, S statement — and separately with a phenomenon code (TO tornado,
// SV severe thunderstorm, FF flash flood, and so on). An operator does not think
// in VTEC. They think "tell me about tornado warnings and severe thunderstorm
// warnings, and tornado watches, but not every flood advisory".
//
// So the policy is a class row per significance, plus per-event overrides that
// win over the row. That covers the sentence above without anyone learning what
// a significance code is, and it is the shape the panel renders.

// DefaultWXBroker is the public read-only feed. The credential is trivial on
// purpose and is published in the feed's own documentation: it is a read-only
// account on public NWS data, and the broker's ACL is what makes that safe
// rather than the secrecy of the password. It is still stored as an editable
// field rather than a constant, because a node pointed at a different feed
// should not need a new build.
const (
	DefaultWXBroker   = "wss://mqtt.wxalerts.org/mqtt"
	DefaultWXUsername = "wxalerts"
	DefaultWXPassword = "wxalerts"
)

// DefaultWXTalkgroup is where alerts go when the operator has not said
// otherwise. TG 9 is the local talkgroup on essentially every hotspot: it does
// not leave the node, which is the right default for something that transmits
// automatically.
const DefaultWXTalkgroup = 9

// Alert-gating defaults. These are the same shape as the message path's own
// channel-clear rules and exist for the same reason: a transmission on top of
// somebody else's is lost, and MMDVM-Host discards a network frame that arrives
// while the slot is busy without logging it.
const (
	DefaultWXHoldoff  = "2s"
	DefaultWXMaxDefer = "120s"
)

// sameCodePattern is the six-digit SAME/FIPS code a NOAA weather radio is
// programmed with: P SS CCC, where P is 0 for a whole county, SS is the state
// FIPS and CCC the county. Santa Rosa County, Florida is 012113.
var sameCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

// ugcPattern is the NWS Universal Geographic Code: two letters of state, then C
// for a county or Z for a forecast zone, then three digits.
var ugcPattern = regexp.MustCompile(`^[A-Z]{2}[CZ][0-9]{3}$`)

// wxSignificances are the VTEC significance codes this feature routes on. The
// remaining codes (F forecast, O outlook, N synopsis) are deliberately absent:
// nothing in them is a hazard a station should interrupt a talkgroup for, and a
// row nobody should switch on is a row better not offered.
var wxSignificances = []string{"W", "A", "Y", "S"}

// WXCounty is one monitored county. The SAME code is what the subscription is
// built from; everything else is carried so the panel can show a name rather
// than six digits, and so a county picked today still reads correctly if the
// lookup service is unreachable tomorrow.
type WXCounty struct {
	SAME  string `json:"same"`
	UGC   string `json:"ugc,omitempty"`
	Name  string `json:"name,omitempty"`
	State string `json:"state,omitempty"`
	WFO   string `json:"wfo,omitempty"`
}

// WXRule is what to do with one class of hazard. Both channels are independent:
// an operator may want a text message for advisories but reserve voice for
// warnings, and that is a reasonable thing to want.
type WXRule struct {
	SMS   bool `json:"sms"`
	Voice bool `json:"voice"`
}

// WXOverride pins one event to its own rule, beating the significance row.
//
// Event is matched against the feed's `event` string ("Tornado Warning"), not
// against the phenomenon code, because that is what an operator sees in the
// panel's list of hazards this node has actually received. Matching is
// case-insensitive; see wxNormalizeEvent.
type WXOverride struct {
	Event string `json:"event"`
	WXRule
}

// WX is the weather broadcast configuration.
type WX struct {
	Enabled bool `json:"enabled"`

	Broker   string `json:"broker"`
	Username string `json:"username"`
	// Password is write-only in the View like every other stored secret, even
	// though this one is public. The rule is worth keeping uniform: a projection
	// that returns some passwords and not others is a rule nobody can audit.
	Password string `json:"password"`

	Counties []WXCounty `json:"counties"`

	// Talkgroups is plural because an operator may serve more than one, and one
	// alert then produces one transmission per talkgroup. Order is preserved and
	// meaningful: it is the order they go out in.
	Talkgroups []uint32 `json:"talkgroups"`

	// Classes is keyed by significance code. A missing row means "do nothing",
	// which is why DefaultWX writes all four.
	Classes map[string]WXRule `json:"classes"`

	Overrides []WXOverride `json:"overrides"`

	// AnnounceActions are the VTEC actions worth transmitting for. NEW alone is
	// the default and very nearly the only sane one: CON is a continuation that
	// the office reissues every few minutes, and announcing those would put the
	// same tornado warning on the air a dozen times.
	AnnounceActions []string `json:"announce_actions"`

	Holdoff  string `json:"holdoff"`
	MaxDefer string `json:"max_defer"`
}

// wxActions are the VTEC action codes the feed emits.
var wxActions = []string{"NEW", "CON", "EXT", "EXA", "EXB", "CAN", "EXP", "UPG"}

// DefaultWX is a node that has the feature switched off but is otherwise ready
// to turn on: the public feed, TG 9, warnings by text and voice, watches by text.
//
// Advisories and statements default to nothing. They are the bulk of the feed by
// volume and almost none of them justify interrupting a talkgroup, so an
// operator who wants them should say so.
func DefaultWX() WX {
	return WX{
		Enabled:    false,
		Broker:     DefaultWXBroker,
		Username:   DefaultWXUsername,
		Password:   DefaultWXPassword,
		Counties:   []WXCounty{},
		Talkgroups: []uint32{DefaultWXTalkgroup},
		Classes: map[string]WXRule{
			"W": {SMS: true, Voice: true},
			"A": {SMS: true, Voice: false},
			"Y": {SMS: false, Voice: false},
			"S": {SMS: false, Voice: false},
		},
		Overrides:       []WXOverride{},
		AnnounceActions: []string{"NEW"},
		Holdoff:         DefaultWXHoldoff,
		MaxDefer:        DefaultWXMaxDefer,
	}
}

// wxNormalizeEvent folds an event name for comparison. The feed is consistent
// about capitalisation but an operator typing an override is not, and an
// override that silently fails to match is worse than one refused at save.
func wxNormalizeEvent(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// ValidateWX refuses a configuration that cannot work, with a reason that says
// what to do about it.
//
// Everything here is checked even when the feature is switched off. An operator
// who fills the panel in, saves, and turns it on next week should meet the
// mistake now rather than during severe weather.
func ValidateWX(w WX) error {
	if b := strings.TrimSpace(w.Broker); b != "" {
		u, err := url.Parse(b)
		if err != nil || u.Host == "" {
			return fmt.Errorf("the alert feed address %q is not a URL; it should look like %s", w.Broker, DefaultWXBroker)
		}
		switch u.Scheme {
		case "ws", "wss":
		default:
			return fmt.Errorf("the alert feed address must be ws:// or wss:// (the feed is MQTT over websockets), got %q", u.Scheme)
		}
	}

	seenSAME := map[string]bool{}
	for _, c := range w.Counties {
		same := strings.TrimSpace(c.SAME)
		if !sameCodePattern.MatchString(same) {
			return fmt.Errorf("county SAME code %q is not six digits; a NOAA weather radio code looks like 012113", c.SAME)
		}
		if seenSAME[same] {
			return fmt.Errorf("county SAME code %s is listed twice; each county subscribes once", same)
		}
		seenSAME[same] = true
		if u := strings.TrimSpace(c.UGC); u != "" && !ugcPattern.MatchString(u) {
			return fmt.Errorf("county UGC %q is not a zone code; it looks like FLC113", c.UGC)
		}
	}

	if len(w.Talkgroups) == 0 {
		return fmt.Errorf("at least one talkgroup is needed; alerts have nowhere to go otherwise")
	}
	seenTG := map[uint32]bool{}
	for _, tg := range w.Talkgroups {
		// A DMR address is 24 bits. A larger value is silently truncated on the
		// air, so it is refused here rather than transmitted at a talkgroup
		// nobody is listening to.
		if tg == 0 || tg > 0xFFFFFF {
			return fmt.Errorf("talkgroup %d is not a 24-bit DMR address (1 to 16777215)", tg)
		}
		if seenTG[tg] {
			return fmt.Errorf("talkgroup %d is listed twice; it would receive every alert twice", tg)
		}
		seenTG[tg] = true
	}

	for sig := range w.Classes {
		if !wxContains(wxSignificances, sig) {
			return fmt.Errorf("%q is not a hazard class; the classes are W (warning), A (watch), Y (advisory) and S (statement)", sig)
		}
	}

	seenEvent := map[string]bool{}
	for _, o := range w.Overrides {
		e := strings.TrimSpace(o.Event)
		if e == "" {
			return fmt.Errorf("an alert-type override has no alert type; give it a name like \"Tornado Warning\" or remove it")
		}
		key := wxNormalizeEvent(e)
		if seenEvent[key] {
			return fmt.Errorf("there are two overrides for %q; the second would never apply", e)
		}
		seenEvent[key] = true
	}

	if len(w.AnnounceActions) == 0 {
		return fmt.Errorf("at least one alert action is needed; NEW alone is the usual choice")
	}
	for _, a := range w.AnnounceActions {
		if !wxContains(wxActions, strings.ToUpper(strings.TrimSpace(a))) {
			return fmt.Errorf("%q is not an alert action; they are %s", a, strings.Join(wxActions, " "))
		}
	}

	for _, d := range []struct{ field, val string }{
		{"holdoff", w.Holdoff},
		{"max_defer", w.MaxDefer},
	} {
		if strings.TrimSpace(d.val) == "" {
			continue
		}
		v, err := time.ParseDuration(d.val)
		if err != nil {
			return fmt.Errorf("%s %q is not a duration; it looks like \"2s\" or \"120s\"", d.field, d.val)
		}
		if v < 0 {
			return fmt.Errorf("%s cannot be negative, got %s", d.field, d.val)
		}
	}
	return nil
}

func wxContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// SetWX merges a partial body into the wx section and validates the result
// before writing, so a configuration that cannot work is refused at save rather
// than discovered when a warning is issued.
//
// The password follows the write-only rule the other secret-bearing sections
// use: a blank field keeps what is stored, because the View never returns it and
// a panel round-trip would otherwise erase it.
func SetWX(s *store.Store, raw []byte, by string) error {
	var w WX
	if _, err := s.GetInto("wx", &w); err != nil {
		return err
	}
	prevPassword := w.Password

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return err
	}
	if strings.TrimSpace(w.Password) == "" {
		w.Password = prevPassword
	}
	// Normalise before validating so the stored form is the one the ingest and
	// the policy read, and neither has to trim or upper-case defensively.
	w.Broker = strings.TrimSpace(w.Broker)
	w.Username = strings.TrimSpace(w.Username)
	for i := range w.Counties {
		w.Counties[i].SAME = strings.TrimSpace(w.Counties[i].SAME)
		w.Counties[i].UGC = strings.ToUpper(strings.TrimSpace(w.Counties[i].UGC))
	}
	for i := range w.AnnounceActions {
		w.AnnounceActions[i] = strings.ToUpper(strings.TrimSpace(w.AnnounceActions[i]))
	}
	for i := range w.Overrides {
		w.Overrides[i].Event = strings.TrimSpace(w.Overrides[i].Event)
	}

	if err := ValidateWX(w); err != nil {
		return err
	}
	return s.Set("wx", &w, by)
}

// WXSubscriptions is the set of MQTT topic filters the ingest subscribes to, one
// per monitored county, in stable order.
//
// The trailing "/#" is not optional and is the single easiest thing to get wrong
// here. The feed keys each alert by its ETN at the last topic level precisely
// because the messages are retained: one county can be under a tornado warning
// and a flash flood warning at the same time, and without that level the second
// publish overwrites the first. Subscribing to the county level alone would
// therefore show one hazard per county and hide the rest.
func (w WX) WXSubscriptions() []string {
	out := make([]string, 0, len(w.Counties))
	for _, c := range w.Counties {
		same := strings.TrimSpace(c.SAME)
		if same == "" {
			continue
		}
		out = append(out, "wxalerts/nws/v1/same/"+same+"/#")
	}
	return out
}

// RuleFor resolves the policy for one alert: an event override if there is one,
// otherwise the significance row, otherwise nothing.
//
// "Otherwise nothing" is deliberate. An unknown significance — the feed grows a
// code, or an alert arrives without VTEC — must not fall through to a default
// that transmits. A hazard nobody configured is a hazard nobody asked to have
// announced on their talkgroup.
func (w WX) RuleFor(event, significance string) WXRule {
	key := wxNormalizeEvent(event)
	for _, o := range w.Overrides {
		if wxNormalizeEvent(o.Event) == key {
			return o.WXRule
		}
	}
	return w.Classes[strings.ToUpper(strings.TrimSpace(significance))]
}

// ShouldAnnounce reports whether an alert's action is one the operator asked to
// be told about. Retained deliveries are NOT filtered here — that is the ingest's
// job and it is a different question, since a retained message can carry action
// NEW and still be a hazard from three hours ago.
func (w WX) ShouldAnnounce(action string) bool {
	return wxContains(w.AnnounceActions, strings.ToUpper(strings.TrimSpace(action)))
}
