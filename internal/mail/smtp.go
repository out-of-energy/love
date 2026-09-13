package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SenderConfig is everything needed to reach a mail server.
type SenderConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       string
}

// Complete reports whether the configuration is usable, and says what is
// missing when it is not.
func (c SenderConfig) Complete() error {
	switch {
	case c.Host == "":
		return fmt.Errorf("SMTP_HOST is not set")
	case c.Port == 0:
		return fmt.Errorf("SMTP_PORT is not set")
	case c.From == "":
		return fmt.Errorf("SMTP_USER is not set")
	case c.To == "":
		return fmt.Errorf("no recipient: set LOVE_MAIL_TO or SMTP_USER")
	case c.Password == "":
		return fmt.Errorf("no mail password: set SMTP_PASSWORD_FILE (preferred) or QQ_SMTP_AUTH_CODE")
	}
	return nil
}

// Dialer opens the connection to the mail server.
//
// It is a field rather than a hard-coded tls.Dial so the transport can be
// exercised against a local server without certificates, which is the only way
// to test the SMTP dialogue at all.
type Dialer func(ctx context.Context, addr string) (net.Conn, error)

// Sender delivers one message.
type Sender struct {
	Config SenderConfig
	Dial   Dialer
	Now    func() time.Time
}

// NewSender returns a sender that connects with implicit TLS, which is what
// port 465 expects.
func NewSender(cfg SenderConfig) *Sender {
	return &Sender{Config: cfg, Dial: tlsDial, Now: time.Now}
}

func tlsDial(ctx context.Context, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	tlsConn := tls.Client(conn, &tls.Config{ServerName: host})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return tlsConn, nil
}

// Send builds and delivers one message.
func (s *Sender) Send(ctx context.Context, subject, htmlBody, textBody string) error {
	if err := s.Config.Complete(); err != nil {
		return err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	message, err := BuildMessage(s.Config.From, s.Config.To, subject, htmlBody, textBody, now())
	if err != nil {
		return err
	}
	return s.SendRaw(ctx, message)
}

// SendRaw delivers an already-built message.
func (s *Sender) SendRaw(ctx context.Context, message []byte) error {
	addr := net.JoinHostPort(s.Config.Host, strconv.Itoa(s.Config.Port))

	conn, err := s.Dial(ctx, addr)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", addr, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}

	client, err := smtp.NewClient(conn, s.Config.Host)
	if err != nil {
		return fmt.Errorf("the server did not speak SMTP: %w", err)
	}
	defer client.Close()

	if s.Config.Username != "" {
		auth := smtp.PlainAuth("", s.Config.Username, s.Config.Password, s.Config.Host)
		if err := client.Auth(auth); err != nil {
			// The overwhelmingly common cause is using the account password
			// rather than the authorization code, so say so.
			return fmt.Errorf("authentication failed (%w); QQ Mail needs the 16-digit "+
				"authorization code, not the account password", err)
		}
	}

	if err := client.Mail(addressOnly(s.Config.From)); err != nil {
		return fmt.Errorf("the server rejected the sender: %w", err)
	}
	if err := client.Rcpt(addressOnly(s.Config.To)); err != nil {
		return fmt.Errorf("the server rejected the recipient: %w", err)
	}

	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(message); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// ConfigFromEnv reads SMTP settings from the environment.
//
// The password is read from a file when one is named, and from the environment
// only as a fallback. Environment variables are inherited by every child
// process and end up in logs and crash reports; a file with restrictive
// permissions does not.
func ConfigFromEnv() (SenderConfig, error) {
	cfg := SenderConfig{
		Host:     envOr("SMTP_HOST", "smtp.qq.com"),
		Username: os.Getenv("SMTP_USER"),
	}

	port := envOr("SMTP_PORT", "465")
	parsed, err := strconv.Atoi(port)
	if err != nil {
		return SenderConfig{}, fmt.Errorf("SMTP_PORT is not a number: %q", port)
	}
	cfg.Port = parsed

	cfg.From = envOr("SMTP_FROM", cfg.Username)
	cfg.To = envOr("LOVE_MAIL_TO", cfg.Username)

	if path := os.Getenv("SMTP_PASSWORD_FILE"); path != "" {
		data, err := os.ReadFile(expandHome(path))
		if err != nil {
			return SenderConfig{}, fmt.Errorf("cannot read SMTP_PASSWORD_FILE: %w", err)
		}
		cfg.Password = strings.TrimSpace(string(data))
	} else {
		cfg.Password = envOr("SMTP_PASSWORD", os.Getenv("QQ_SMTP_AUTH_CODE"))
	}

	return cfg, cfg.Complete()
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// expandHome resolves a leading ~, which a shell would have expanded but an
// environment variable does not. A password file named as ~/.ewh/smtp-password
// would otherwise be looked for literally, under a directory called "~".
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

// addressOnly strips a display name, since the SMTP envelope wants a bare
// address.
func addressOnly(s string) string {
	if i := strings.LastIndex(s, "<"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j > 0 {
			return s[i+1 : i+j]
		}
	}
	return s
}
