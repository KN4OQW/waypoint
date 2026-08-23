package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The SMTP sink's decisions, without an SMTP server. Nothing here opens a socket:
// what is tested is what it decides to send, what it refuses, and what it never
// puts in a message.

func cfg() SMTPConfig {
	return SMTPConfig{
		Host: "mail.example.invalid", Port: 587, RequireTLS: true,
		Username: "kn4oqw", Password: "the-smtp-password", From: "node@example.invalid",
	}
}

func sink() *SMTPSink {
	return NewSMTPSink(cfg, func() string { return "KN4OQW" })
}

// TestNoEmailDeclines: a recipient with no address is ErrNotApplicable, which the
// dispatcher treats as a no-op rather than a failure. Returning a plain error
// here would park notifications for everyone in the phonebook without an address,
// which is most of them.
func TestNoEmailDeclines(t *testing.T) {
	for _, email := range []string{"", "   ", "\t"} {
		err := sink().Deliver(context.Background(), Notification{Type: EventSMSReceived}, Recipient{Email: email})
		if !errors.Is(err, ErrNotApplicable) {
			t.Errorf("Deliver with email %q = %v, want ErrNotApplicable", email, err)
		}
	}
}

// TestUnconfiguredIsAFailureNotADecline: the recipient HAS an address; the node
// simply cannot send yet. Retrying is right, and parking after the schedule
// leaves the operator a visible reason.
func TestUnconfiguredIsAFailureNotADecline(t *testing.T) {
	for name, c := range map[string]SMTPConfig{
		"no host": {Port: 587, From: "a@b.invalid"},
		"no port": {Host: "mail.invalid", From: "a@b.invalid"},
		"no from": {Host: "mail.invalid", Port: 587},
	} {
		s := NewSMTPSink(func() SMTPConfig { return c }, nil)
		err := s.Deliver(context.Background(), Notification{Type: EventSMSReceived}, Recipient{Email: "x@y.invalid"})
		if !errors.Is(err, ErrNotConfigured) {
			t.Errorf("%s: Deliver = %v, want ErrNotConfigured", name, err)
		}
		if errors.Is(err, ErrNotApplicable) {
			t.Errorf("%s: an unconfigured server must not decline as not-applicable", name)
		}
	}
}

// message renders without a network round trip.
func render(t *testing.T, typ EventType, payload map[string]any) string {
	t.Helper()
	n := Notification{
		Type: typ, CreatedAt: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC),
		Payload: payload,
	}
	return sink().message(n, Recipient{Callsign: "KN4OQW", Email: "clint@example.invalid"}, cfg())
}

// TestMessageShape: correct headers, plain text, CRLF line endings.
func TestMessageShape(t *testing.T) {
	msg := render(t, EventAccountCreated, map[string]any{"username": "kn4oqw"})
	for _, want := range []string{
		"From: node@example.invalid\r\n",
		"To: clint@example.invalid\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q", strings.TrimSuffix(want, "\r\n"))
		}
	}
	head, body, ok := strings.Cut(msg, "\r\n\r\n")
	if !ok {
		t.Fatal("no blank line separating headers from the body")
	}
	if strings.Contains(head, "\n\n") {
		t.Error("headers contain a bare LF pair")
	}
	if body == "" {
		t.Error("empty body")
	}
	if strings.Contains(msg, "<html") || strings.Contains(msg, "multipart") {
		t.Error("the message is not plain text")
	}
}

// TestMessageCarriesNoSecret is the one that matters. A notification is a
// sentence and a pointer, never the thing it is about: an account email must not
// carry the password an admin chose, and an SMS notification must not quote the
// message — that is correspondence, and copying it into email would move it onto
// a surface the operator never agreed to.
func TestMessageCarriesNoSecret(t *testing.T) {
	const (
		password = "the-initial-password-nobody-should-mail"
		smsText  = "MEET AT THE REPEATER AT SIX"
	)
	for name, msg := range map[string]string{
		"account_created": render(t, EventAccountCreated, map[string]any{
			"username": "kn4oqw", "password": password,
		}),
		"password_changed": render(t, EventPasswordChanged, map[string]any{
			"username": "kn4oqw", "password": password,
		}),
		"sms_received": render(t, EventSMSReceived, map[string]any{
			"from": "3180202", "text": smsText,
		}),
	} {
		for _, secret := range []string{password, smsText, cfg().Password} {
			if strings.Contains(msg, secret) {
				t.Errorf("%s: the message contains %q", name, secret)
			}
		}
	}
}

// TestHeaderInjectionIsCut: a payload field that reached a header could otherwise
// inject headers of its own, which is how a notification becomes an open relay.
func TestHeaderInjectionIsCut(t *testing.T) {
	msg := render(t, EventWXAlert, map[string]any{
		"headline": "Tornado Warning\r\nBcc: everyone@example.invalid\r\nX-Injected: yes",
	})
	head, _, _ := strings.Cut(msg, "\r\n\r\n")
	// Injection means a NEW HEADER LINE, so the assertion is on line starts. A
	// looser Contains() check would also fire on the harmless case where the text
	// "Bcc:" survives inside the subject VALUE, which is not injection and which
	// is exactly what this renderer does with it.
	for _, line := range strings.Split(head, "\r\n") {
		for _, bad := range []string{"Bcc:", "X-Injected:"} {
			if strings.HasPrefix(line, bad) {
				t.Errorf("a payload injected the header line %q", line)
			}
		}
	}
	// The headline still reaches the subject, folded onto one line.
	if !strings.Contains(head, "Tornado Warning") {
		t.Error("the alert headline was lost entirely; folding should keep it")
	}
	if strings.Count(head, "Subject:") != 1 {
		t.Error("more than one Subject header")
	}
}

// TestSubjectAndBodyTolerateAMissingPayload: a notification whose payload is empty
// or unparseable still renders something a person can read, because the
// alternative is a mail that says nothing or a delivery that fails on a field.
func TestSubjectAndBodyTolerateAMissingPayload(t *testing.T) {
	for _, typ := range []EventType{EventAccountCreated, EventPasswordChanged, EventSMSReceived, EventWXAlert} {
		n := Notification{Type: typ, Payload: DecodePayload("not json at all")}
		if s := n.Subject("KN4OQW"); s == "" || !strings.Contains(s, "KN4OQW") {
			t.Errorf("%s: subject = %q", typ, s)
		}
		if b := n.Body("KN4OQW"); strings.TrimSpace(b) == "" {
			t.Errorf("%s: empty body", typ)
		}
	}
}

// TestSubjectFallsBackWithoutAStation: a node whose callsign is not set yet still
// sends something identifiable rather than a subject starting with a colon.
func TestSubjectFallsBackWithoutAStation(t *testing.T) {
	n := Notification{Type: EventSMSReceived, Payload: map[string]any{}}
	if s := n.Subject(""); !strings.HasPrefix(s, "Waypoint") {
		t.Errorf("subject with no station = %q", s)
	}
	if b := n.Body(""); !strings.Contains(b, "your Waypoint node") {
		t.Errorf("body with no station = %q", b)
	}
}

// TestEventTypeValidity: the enum is closed, and an unrecognised type is not
// quietly treated as valid — the dispatcher parks on this.
func TestEventTypeValidity(t *testing.T) {
	for _, ok := range []EventType{EventAccountCreated, EventPasswordChanged, EventSMSReceived, EventWXAlert} {
		if !ok.Valid() {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []EventType{"", "wx_alert_cleared", "account_deleted", "ACCOUNT_CREATED"} {
		if EventType(bad).Valid() {
			t.Errorf("%q should not be valid", bad)
		}
	}
}
