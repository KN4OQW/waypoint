package config

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/KN4OQW/waypoint/internal/store"
)

// The notification settings (SMTP), store-owned like the rest of the System tab.
//
// This section renders to NO daemon INI. Nothing in the upstream stack sends
// mail; the notifier is Waypoint's own, and this is configuration for it. It
// lives in the store for the same reason the MQTT data plane does — it is an
// operator decision about how this node behaves, and the store is where those
// live — and it is deliberately absent from Model.sections()' render path.
//
// The password is a write-only secret and follows exactly the rule the MQTT
// broker password, the DMR network passwords, the ircDDB password and the DAPNET
// AuthKey already follow: the View carries HasPassword and never the value, and a
// blank on write keeps what is stored so a panel that never received the secret
// cannot erase it by saving the form back.

// SMTPDefaultPort is what a blank port means. 587 is submission-with-STARTTLS,
// which is what essentially every provider expects today; 465 (implicit TLS) is
// the other common one and is selected by turning ImplicitTLS on.
const SMTPDefaultPort = "587"

// Notify is the notification configuration.
type Notify struct {
	// Enabled is the master switch. Off is the default and off means the
	// dispatcher runs with no sinks: notifications are still queued and closed,
	// nothing is sent, and nothing fails.
	Enabled bool `json:"enabled"`

	SMTPHost string `json:"smtp_host"`
	SMTPPort string `json:"smtp_port"`
	// SMTPImplicitTLS dials TLS directly (SMTPS, conventionally 465) instead of
	// connecting in plaintext and issuing STARTTLS.
	SMTPImplicitTLS bool `json:"smtp_implicit_tls"`
	// SMTPAllowPlaintext turns OFF the requirement that the connection become
	// encrypted. Named for what it permits rather than what it disables, so the
	// control reads as the concession it is. Default false: sending a mail
	// password in the clear is a decision an operator makes deliberately.
	SMTPAllowPlaintext bool `json:"smtp_allow_plaintext"`
	// SMTPInsecureSkipVerify accepts an unverifiable certificate. For a mail
	// server on the operator's own LAN with a self-signed certificate, which is a
	// real configuration.
	SMTPInsecureSkipVerify bool `json:"smtp_insecure_skip_verify"`

	SMTPUsername string `json:"smtp_username"`
	// SMTPPassword is a secret: never in the View, and blank on write keeps the
	// stored value.
	SMTPPassword string `json:"smtp_password"`
	// SMTPFrom is the envelope and header From. The operator's own address; the
	// node adds nothing identifying the hardware.
	SMTPFrom string `json:"smtp_from"`
}

// Port returns the configured port or the default.
func (n Notify) Port() string {
	if p := strings.TrimSpace(n.SMTPPort); p != "" {
		return p
	}
	return SMTPDefaultPort
}

// SMTPConfigured reports whether there is enough to attempt a send. It is the
// same question internal/notify asks, kept here so the panel can say "not
// configured" without reaching into the notifier.
func (n Notify) SMTPConfigured() bool {
	return strings.TrimSpace(n.SMTPHost) != "" &&
		strings.TrimSpace(n.SMTPFrom) != "" &&
		strings.TrimSpace(n.Port()) != ""
}

// SetNotify writes the notify section with the write-only-secret rule the DMR
// networks and the D-Star gateway already use: a blank incoming SMTPPassword
// means "keep the stored one", and a non-blank one replaces it.
//
// This is defence in depth rather than the only guard. SetSection already merges
// onto the stored section, so a panel that omits the field keeps the password
// either way — but the panel cannot render a password it was never sent, so it
// would have to remember to omit rather than submit an empty string, and getting
// that wrong silently erases the operator's credential. Reconciling after the
// merge means it cannot.
func SetNotify(s *store.Store, raw []byte, by string) error {
	var existing Notify
	if _, err := s.GetInto("notify", &existing); err != nil {
		return err
	}
	incoming := existing
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&incoming); err != nil {
		return err
	}
	if incoming.SMTPPassword == "" {
		incoming.SMTPPassword = existing.SMTPPassword
	}
	return s.Set("notify", &incoming, by)
}
