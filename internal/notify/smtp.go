package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// The SMTP sink.
//
// # No new dependency
//
// This uses the standard library's net/smtp and builds the message by hand. The
// obvious alternative is a mail library, and it was not taken: what is needed
// here is one plaintext message, to one recipient, over one connection, and
// net/smtp does that. A dependency would buy attachments, HTML multipart and
// address parsing that this sink has no use for, and cost a supply-chain surface
// on a device whose whole security posture is about not having one.
//
// net/smtp is frozen rather than deprecated — the standard library will not grow
// features here — and that is fine for a sink that needs none. The one thing it
// genuinely lacks is implicit TLS (SMTPS on 465), because smtp.Dial assumes a
// plaintext connection to STARTTLS from. That is handled below by dialling the
// TLS connection ourselves and handing it to smtp.NewClient, which is the
// documented way round it and about six lines.
//
// # What it will not do
//
// It sends plain text and no HTML. A notification is a sentence and a link; HTML
// mail buys nothing and costs a multipart body, a second thing to escape, and a
// second way to leak. The From address is the operator's own configured one and
// the node adds no headers identifying the hardware.

// SMTPConfig is what the sink needs to reach a server. It mirrors the store
// section of the same name; see internal/config.
type SMTPConfig struct {
	Host string
	Port int
	// ImplicitTLS dials TLS directly (SMTPS, conventionally 465). When false the
	// sink connects in plaintext and issues STARTTLS if the server offers it.
	ImplicitTLS bool
	// RequireTLS refuses to send over a connection that never became encrypted.
	// Default true: a password sent in the clear to a mail server is a password on
	// the wire, and the operator has to opt out of that deliberately.
	RequireTLS bool
	Username   string
	Password   string
	From       string
	// InsecureSkipVerify disables certificate verification. It exists because a
	// mail server on the operator's own LAN with a self-signed certificate is a
	// real configuration, not because anyone should reach for it.
	InsecureSkipVerify bool
}

// Configured reports whether there is enough to attempt a send. A sink that is
// not configured declines rather than failing — a node with no mail server is not
// a node with a broken one.
func (c SMTPConfig) Configured() bool {
	return strings.TrimSpace(c.Host) != "" && c.Port > 0 && strings.TrimSpace(c.From) != ""
}

// SMTPSink delivers notifications as email.
type SMTPSink struct {
	// cfg is read per delivery rather than captured, so an operator correcting
	// the server address does not have to restart the daemon for the next retry
	// to use it.
	cfg     func() SMTPConfig
	station func() string
	// dial is the connection seam. Tests replace it; production leaves it nil and
	// gets the real one.
	dial func(ctx context.Context, cfg SMTPConfig) (*smtp.Client, error)
}

// NewSMTPSink builds the sink. cfg is called on every delivery.
func NewSMTPSink(cfg func() SMTPConfig, station func() string) *SMTPSink {
	if station == nil {
		station = func() string { return "" }
	}
	return &SMTPSink{cfg: cfg, station: station}
}

// Name identifies the sink in logs and in a parked notification's error.
func (s *SMTPSink) Name() string { return "smtp" }

// ErrNotConfigured is returned when there is no mail server to talk to. It is a
// delivery failure rather than ErrNotApplicable on purpose: the recipient DOES
// have an address, and the node simply cannot send to it yet. Retrying is right,
// and parking after the schedule leaves an operator a visible reason.
var ErrNotConfigured = errors.New("smtp: no mail server is configured")

// Deliver sends one notification to one recipient.
func (s *SMTPSink) Deliver(ctx context.Context, n Notification, r Recipient) error {
	to := strings.TrimSpace(r.Email)
	if to == "" {
		// The ordinary case: most people in a phonebook have no address. Not a
		// failure, not an attempt.
		return ErrNotApplicable
	}
	cfg := s.cfg()
	if !cfg.Configured() {
		return ErrNotConfigured
	}

	c, err := s.connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer c.Close() //nolint:errcheck // Quit below is what reports a protocol failure

	if err := s.authenticate(c, cfg); err != nil {
		return err
	}
	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write([]byte(s.message(n, r, cfg))); err != nil {
		_ = w.Close()
		return fmt.Errorf("writing the message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("closing the message: %w", err)
	}
	return c.Quit()
}

// connect opens a client, implicit-TLS or STARTTLS.
func (s *SMTPSink) connect(ctx context.Context, cfg SMTPConfig) (*smtp.Client, error) {
	if s.dial != nil {
		return s.dial(ctx, cfg)
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	tlsCfg := &tls.Config{
		ServerName:         cfg.Host,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // operator opt-in for a LAN server with a self-signed cert
		MinVersion:         tls.VersionTLS12,
	}
	d := &net.Dialer{}

	if cfg.ImplicitTLS {
		// net/smtp has no implicit-TLS dialer, so the TLS connection is made here
		// and handed over. This is the documented way round it.
		conn, err := tls.DialWithDialer(d, "tcp", addr, tlsCfg)
		if err != nil {
			return nil, fmt.Errorf("connecting to %s over TLS: %w", addr, err)
		}
		c, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("starting the SMTP session: %w", err)
		}
		return c, nil
	}

	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", addr, err)
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("starting the SMTP session: %w", err)
	}
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(tlsCfg); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("STARTTLS: %w", err)
		}
	} else if cfg.RequireTLS {
		_ = c.Close()
		// Refusing is the point. The alternative is sending the operator's mail
		// password over the LAN in the clear because a server did not offer
		// encryption, which is not a decision to make silently on their behalf.
		return nil, errors.New("the server does not offer STARTTLS and TLS is required; " +
			"either use a server that supports it or turn the TLS requirement off deliberately")
	}
	return c, nil
}

// authenticate logs in when a username is set.
//
// PLAIN over an encrypted connection is what essentially every provider expects.
// net/smtp refuses PLAIN on an unencrypted connection by design, and that refusal
// is left in place rather than worked around.
func (s *SMTPSink) authenticate(c *smtp.Client, cfg SMTPConfig) error {
	if strings.TrimSpace(cfg.Username) == "" {
		return nil
	}
	if ok, _ := c.Extension("AUTH"); !ok {
		return errors.New("a username is configured but the server offers no AUTH")
	}
	if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
		// The error is returned as-is minus anything that could carry the password;
		// net/smtp does not put credentials in its errors, and neither does this.
		return fmt.Errorf("authenticating as %q: %w", cfg.Username, err)
	}
	return nil
}

// message renders RFC-5322 headers and a plain-text body.
//
// Subject is Q-encoded so a non-ASCII station name or alert headline survives.
// Every header value is stripped of CR and LF: a payload field that reached a
// header could otherwise inject headers of its own, which is how a notification
// becomes an open relay.
func (s *SMTPSink) message(n Notification, r Recipient, cfg SMTPConfig) string {
	station := s.station()
	var b strings.Builder
	b.WriteString("From: " + headerSafe(cfg.From) + "\r\n")
	b.WriteString("To: " + headerSafe(r.Email) + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", headerSafe(n.Subject(station))) + "\r\n")
	b.WriteString("Date: " + n.CreatedAt.UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(n.Body(station), "\n", "\r\n"))
	return b.String()
}

// headerSafe folds CR and LF in a header value to a space. Header injection is
// the one way a notification payload could make this sink do something it was not
// asked to, so it is cut at the only place a payload reaches a header.
//
// A space rather than deletion: an alert headline that arrives with a line break
// in it is a real thing, and deleting the break runs the words together
// ("Tornado WarningIssued at..."), which reads like a bug in the alert. What
// matters for injection is that no NEW header line can begin, and a space
// achieves that while leaving the subject legible.
func headerSafe(v string) string {
	return strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(v)
}
