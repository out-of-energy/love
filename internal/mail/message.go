package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"time"
)

// BuildMessage renders a complete RFC 5322 message with both an HTML and a
// plain-text part.
//
// A text alternative is not politeness. A message that carries only HTML is
// more likely to be filed as bulk mail, and it is the only part some clients
// will ever show.
//
// The output is pure: same inputs, same bytes apart from the generated boundary
// and message id. That is what makes it testable by parsing it back.
func BuildMessage(from, to, subject, htmlBody, textBody string, date time.Time) ([]byte, error) {
	fromAddr, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("bad sender address %q: %w", from, err)
	}
	toAddr, err := mail.ParseAddress(to)
	if err != nil {
		return nil, fmt.Errorf("bad recipient address %q: %w", to, err)
	}

	boundary, err := randomToken(16)
	if err != nil {
		return nil, err
	}
	messageID, err := randomToken(12)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	header := func(key, value string) {
		fmt.Fprintf(&buf, "%s: %s\r\n", key, value)
	}

	header("From", formatAddress(fromAddr))
	header("To", formatAddress(toAddr))
	// The subject is Chinese, so it must be encoded rather than sent raw. The
	// Q encoding is what every mail client understands.
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", date.Format(time.RFC1123Z))
	header("Message-ID", fmt.Sprintf("<%s@love.local>", messageID))
	header("MIME-Version", "1.0")
	// Marks the message as machine-generated without claiming to be an
	// auto-reply, which is a distinction RFC 3834 cares about.
	header("X-Mailer", "love")
	header("Content-Type", fmt.Sprintf("multipart/alternative; boundary=%q", boundary))
	buf.WriteString("\r\n")

	writePart := func(contentType, body string) error {
		fmt.Fprintf(&buf, "--%s\r\n", boundary)
		fmt.Fprintf(&buf, "Content-Type: %s\r\n", contentType)
		buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")

		qp := quotedprintable.NewWriter(&buf)
		if _, err := qp.Write([]byte(body)); err != nil {
			return err
		}
		if err := qp.Close(); err != nil {
			return err
		}
		buf.WriteString("\r\n")
		return nil
	}

	// Text first: clients that show only one part show the first.
	if err := writePart(`text/plain; charset="utf-8"`, textBody); err != nil {
		return nil, err
	}
	if err := writePart(`text/html; charset="utf-8"`, htmlBody); err != nil {
		return nil, err
	}

	fmt.Fprintf(&buf, "--%s--\r\n", boundary)
	return buf.Bytes(), nil
}

// randomToken returns n random bytes as hex, for boundaries and message ids.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// formatAddress writes a bare address when there is no display name.
//
// mail.Address.String always adds angle brackets, so a plain "From: <me@x>"
// would come out where every other mail tool writes "From: me@x". It is legal
// either way, but it looks like something went wrong and it makes golden
// comparisons against other tools needlessly different.
func formatAddress(a *mail.Address) string {
	if a.Name == "" {
		return a.Address
	}
	return a.String()
}
