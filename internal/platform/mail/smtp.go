package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTPConfig describes the upstream server.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	// TLS selects how the connection is secured: "starttls" (the usual
	// choice on port 587), "tls" (implicit TLS, port 465) or "none",
	// which is only reasonable against a relay on localhost.
	TLS string
	// Timeout bounds the whole exchange. Without it a hung server would
	// hold an HTTP request open for as long as the kernel allows.
	Timeout time.Duration
}

// TLS modes.
const (
	TLSStartTLS = "starttls"
	TLSImplicit = "tls"
	TLSNone     = "none"
)

// SMTPMailer sends through a real server using the standard library, which
// keeps the build CGO free.
type SMTPMailer struct {
	cfg SMTPConfig
}

// NewSMTPMailer builds the production mailer.
func NewSMTPMailer(cfg SMTPConfig) *SMTPMailer {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &SMTPMailer{cfg: cfg}
}

// Send delivers one message. The context bounds the dial; the deadline set on
// the connection bounds everything after it, because net/smtp itself is not
// context aware.
func (m *SMTPMailer) Send(ctx context.Context, msg Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}

	address := net.JoinHostPort(m.cfg.Host, fmt.Sprint(m.cfg.Port))
	dialer := &net.Dialer{Timeout: m.cfg.Timeout}

	var (
		conn net.Conn
		err  error
	)
	if m.cfg.TLS == TLSImplicit {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("dial smtp server %s: %w", address, err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(m.cfg.Timeout)); err != nil {
		return fmt.Errorf("set smtp deadline: %w", err)
	}

	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return fmt.Errorf("open smtp session: %w", err)
	}
	defer func() { _ = client.Close() }()

	if m.cfg.TLS == TLSStartTLS {
		if err := client.StartTLS(&tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("start tls: %w", err)
		}
	}

	if m.cfg.Username != "" {
		auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("authenticate against smtp server: %w", err)
		}
	}

	if err := client.Mail(m.cfg.From); err != nil {
		return fmt.Errorf("set sender: %w", err)
	}
	if err := client.Rcpt(msg.To); err != nil {
		return fmt.Errorf("set recipient: %w", err)
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("open message body: %w", err)
	}
	if _, err := writer.Write(m.render(msg)); err != nil {
		return fmt.Errorf("write message body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close message body: %w", err)
	}

	if err := client.Quit(); err != nil {
		return fmt.Errorf("close smtp session: %w", err)
	}
	return nil
}

// render builds the RFC 5322 message. Message.Validate has already rejected
// line breaks in the header fields.
func (m *SMTPMailer) render(msg Message) []byte {
	var builder strings.Builder
	builder.WriteString("From: " + m.cfg.From + "\r\n")
	builder.WriteString("To: " + msg.To + "\r\n")
	builder.WriteString("Subject: " + msg.Subject + "\r\n")
	builder.WriteString("MIME-Version: 1.0\r\n")
	builder.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	builder.WriteString("\r\n")
	// Dot stuffing: a lone "." on its own line would end the message early.
	builder.WriteString(strings.ReplaceAll(msg.Body, "\r\n.\r\n", "\r\n..\r\n"))
	builder.WriteString("\r\n")
	return []byte(builder.String())
}
