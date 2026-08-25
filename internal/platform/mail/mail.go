// Package mail sends the few transactional messages this API needs. It is
// deliberately small: no templating engine, no queue, no retries. On a
// Raspberry Pi serving one person, a failed send is recovered by the user
// asking for another code.
package mail

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Message is one outgoing email. Only plain text: an HTML body would need
// escaping rules and a multipart encoder for no benefit here.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Validate catches the mistakes worth catching before dialling anything.
func (m Message) Validate() error {
	switch {
	case strings.TrimSpace(m.To) == "":
		return fmt.Errorf("message has no recipient")
	case strings.TrimSpace(m.Subject) == "":
		return fmt.Errorf("message has no subject")
	case strings.ContainsAny(m.To, "\r\n"), strings.ContainsAny(m.Subject, "\r\n"):
		// A newline in a header field lets the caller inject headers of
		// their own, which is how open relays get abused.
		return fmt.Errorf("message headers must not contain line breaks")
	default:
		return nil
	}
}

// Mailer delivers a message. The auth module depends on an interface of its
// own, not on this one; both are satisfied by the implementations here.
type Mailer interface {
	Send(ctx context.Context, msg Message) error
}

// LogMailer writes the message to the log instead of sending it. It is the
// development default, so working on the signup flow needs no SMTP server and
// the verification code is right there in the terminal.
type LogMailer struct {
	logger *slog.Logger
}

// NewLogMailer builds the development mailer.
func NewLogMailer(logger *slog.Logger) *LogMailer {
	return &LogMailer{logger: logger}
}

// Send logs the whole message, body included.
func (m *LogMailer) Send(ctx context.Context, msg Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	m.logger.InfoContext(ctx, "email not sent (log mailer)",
		slog.String("to", msg.To),
		slog.String("subject", msg.Subject),
		slog.String("body", msg.Body),
	)
	return nil
}
