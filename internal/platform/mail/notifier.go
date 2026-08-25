package mail

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// DefaultResetPath is where the web client listens for a reset link.
const DefaultResetPath = "/reset-password"

// Notifier composes the transactional messages and hands them to a Mailer. It
// takes plain strings, so the module that uses it stays unaware of both the
// wording and the transport.
type Notifier struct {
	mailer    Mailer
	appURL    string
	resetPath string
	codeTTL   time.Duration
	linkTTL   time.Duration
}

// NewNotifier builds the notifier. appURL is the public base URL of the web
// client, which is what the reset link has to point at.
func NewNotifier(mailer Mailer, appURL, resetPath string, codeTTL, linkTTL time.Duration) *Notifier {
	if resetPath == "" {
		resetPath = DefaultResetPath
	}
	return &Notifier{
		mailer:    mailer,
		appURL:    strings.TrimRight(appURL, "/"),
		resetPath: "/" + strings.TrimLeft(resetPath, "/"),
		codeTTL:   codeTTL,
		linkTTL:   linkTTL,
	}
}

// SendVerificationCode emails the six digit code.
func (n *Notifier) SendVerificationCode(ctx context.Context, email, code string) error {
	body := fmt.Sprintf(`Welcome to Nihongo.

Your verification code is:

    %s

It expires in %s. If you did not create an account, ignore this message.
`, code, humanDuration(n.codeTTL))

	return n.mailer.Send(ctx, Message{
		To:      email,
		Subject: "Your Nihongo verification code",
		Body:    body,
	})
}

// SendPasswordReset emails the reset link built from the token.
func (n *Notifier) SendPasswordReset(ctx context.Context, email, token string) error {
	body := fmt.Sprintf(`Someone asked to reset the password of this Nihongo account.

Open this link to choose a new one:

    %s

It expires in %s and can only be used once. If it was not you, ignore this
message: nothing has changed.
`, n.resetURL(token), humanDuration(n.linkTTL))

	return n.mailer.Send(ctx, Message{
		To:      email,
		Subject: "Reset your Nihongo password",
		Body:    body,
	})
}

// resetURL puts the token in the query string, escaped.
func (n *Notifier) resetURL(token string) string {
	query := url.Values{"token": {token}}
	return n.appURL + n.resetPath + "?" + query.Encode()
}

// humanDuration renders a TTL the way a person would read it.
func humanDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "a moment"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}
