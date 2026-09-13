package mail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Message building.
// ---------------------------------------------------------------------------

func TestBuildMessageCarriesBothPartsAndDecodesBack(t *testing.T) {
	subject := "英语复习 · 2 个词 · 9月14日"
	htmlBody := `<p>maintain <b>/meɪnˈteɪn/</b></p>`
	textBody := "maintain /meɪnˈteɪn/\nELI5: To keep something working well.\n"

	raw, err := BuildMessage("love@example.com", "me@example.com", subject, htmlBody, textBody, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("the message does not parse as mail: %v", err)
	}

	decoded, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	if decoded != subject {
		t.Errorf("subject = %q, want %q", decoded, subject)
	}
	if from := parsed.Header.Get("From"); from != "love@example.com" {
		t.Errorf("From = %q", from)
	}

	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	if mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type = %q, want multipart/alternative", mediaType)
	}

	// The multipart reader decodes quoted-printable transparently, so this also
	// proves the transfer encoding round-trips the UTF-8 content.
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	var types, bodies []string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, part.Header.Get("Content-Type"))
		bodies = append(bodies, string(body))
	}

	if len(bodies) != 2 {
		t.Fatalf("got %d parts, want 2: %v", len(bodies), types)
	}
	// Text first: a client that shows only one part shows the first.
	if !strings.HasPrefix(types[0], "text/plain") || !strings.HasPrefix(types[1], "text/html") {
		t.Errorf("part order = %v, want text then html", types)
	}
	if !strings.Contains(bodies[0], "ELI5: To keep something working well.") {
		t.Errorf("text part = %q", bodies[0])
	}
	if !strings.Contains(bodies[1], "maintain") || !strings.Contains(bodies[1], "<b>") {
		t.Errorf("html part = %q", bodies[1])
	}
}

func TestBuildMessageRejectsBadAddresses(t *testing.T) {
	for _, tc := range []struct{ from, to string }{
		{"not an address", "me@example.com"},
		{"love@example.com", "also not"},
	} {
		if _, err := BuildMessage(tc.from, tc.to, "s", "<p></p>", "", time.Unix(0, 0)); err == nil {
			t.Errorf("BuildMessage(%q, %q) should have failed", tc.from, tc.to)
		}
	}
}

func TestBuildMessageUsesCRLFThroughout(t *testing.T) {
	raw, err := BuildMessage("a@example.com", "b@example.com", "s", "<p>x</p>", "x", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	// SMTP is a CRLF protocol; a bare LF in the header block breaks strict
	// servers and some relays rewrite it in ways that break signatures.
	headerEnd := bytes.Index(raw, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		t.Fatal("no header terminator")
	}
	if bytes.Contains(bytes.ReplaceAll(raw[:headerEnd], []byte("\r\n"), nil), []byte("\n")) {
		t.Error("the header block contains a bare LF")
	}
}

func TestEachMessageGetsAFreshBoundary(t *testing.T) {
	first, err := BuildMessage("a@example.com", "b@example.com", "s", "<p>1</p>", "1", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildMessage("a@example.com", "b@example.com", "s", "<p>1</p>", "1", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Error("two messages share a boundary, which risks a collision in a threaded client")
	}
}

// ---------------------------------------------------------------------------
// SMTP transport, against a local server that speaks just enough of the
// protocol to be wrong in the ways that matter.
// ---------------------------------------------------------------------------

type fakeSMTP struct {
	listener   net.Listener
	rejectAuth bool

	mu    sync.Mutex
	user  string
	pass  string
	from  string
	rcpts []string
	data  string
}

func startFakeSMTP(t *testing.T, rejectAuth bool) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSMTP{listener: ln, rejectAuth: rejectAuth}
	go s.serve()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *fakeSMTP) addr() string { return s.listener.Addr().String() }

func (s *fakeSMTP) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	say := func(format string, args ...any) {
		fmt.Fprintf(conn, format+"\r\n", args...)
	}

	say("220 fake ESMTP ready")
	inData := false
	var body strings.Builder

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		if inData {
			if line == "." {
				inData = false
				s.mu.Lock()
				s.data = body.String()
				s.mu.Unlock()
				body.Reset()
				say("250 OK queued")
				continue
			}
			body.WriteString(line)
			body.WriteString("\n")
			continue
		}

		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			say("250-fake")
			say("250-AUTH PLAIN")
			say("250 8BITMIME")
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				if raw, err := base64.StdEncoding.DecodeString(fields[2]); err == nil {
					parts := strings.Split(string(raw), "\x00")
					if len(parts) == 3 {
						s.mu.Lock()
						s.user, s.pass = parts[1], parts[2]
						s.mu.Unlock()
					}
				}
			}
			if s.rejectAuth {
				say("535 Authentication failed")
				continue
			}
			say("235 Accepted")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			s.mu.Lock()
			s.from = envelopeAddress(line, "MAIL FROM:")
			s.mu.Unlock()
			say("250 OK")

		case strings.HasPrefix(upper, "RCPT TO:"):
			s.mu.Lock()
			s.rcpts = append(s.rcpts, envelopeAddress(line, "RCPT TO:"))
			s.mu.Unlock()
			say("250 OK")
		case upper == "DATA":
			inData = true
			say("354 Start mail input")
		case upper == "QUIT":
			say("221 Bye")
			return
		case upper == "RSET":
			say("250 OK")
		default:
			say("250 OK")
		}
	}
}

// envelopeAddress pulls the address out of a MAIL FROM / RCPT TO command.
//
// The client appends ESMTP parameters when the server advertises them — Go's
// smtp package adds BODY=8BITMIME to MAIL FROM whenever the greeting offers
// 8BITMIME — so taking everything after the colon would capture "addr> BODY=..."
// and the test would be checking the wrong string.
func envelopeAddress(line, prefix string) string {
	rest := strings.TrimSpace(line[len(prefix):])
	if i := strings.Index(rest, "<"); i >= 0 {
		if j := strings.Index(rest[i:], ">"); j > 0 {
			return rest[i+1 : i+j]
		}
	}
	if fields := strings.Fields(rest); len(fields) > 0 {
		return strings.Trim(fields[0], "<>")
	}
	return rest
}

func (s *fakeSMTP) snapshot() (from string, rcpts []string, data string, user, pass string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.from, append([]string(nil), s.rcpts...), s.data, s.user, s.pass
}

// plainDial is injected in tests so the dialogue can be exercised without
// certificates. That injection is the reason the transport is testable at all.
func plainDial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

func TestSendDeliversTheMessage(t *testing.T) {
	server := startFakeSMTP(t, false)
	host, port, err := net.SplitHostPort(server.addr())
	if err != nil {
		t.Fatal(err)
	}

	sender := NewSender(SenderConfig{
		Host:     host,
		Port:     atoi(t, port),
		Username: "me@example.com",
		Password: "secret-code",
		From:     "me@example.com",
		To:       "me@example.com",
	})
	sender.Dial = plainDial

	if err := sender.Send(context.Background(), "英语复习", "<p>hi</p>", "hi"); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	from, rcpts, data, user, pass := server.snapshot()
	if from != "me@example.com" {
		t.Errorf("MAIL FROM = %q", from)
	}
	if len(rcpts) != 1 || rcpts[0] != "me@example.com" {
		t.Errorf("RCPT TO = %v", rcpts)
	}
	if user != "me@example.com" || pass != "secret-code" {
		t.Errorf("authenticated as %q/%q", user, pass)
	}
	if !strings.Contains(data, "hi") {
		t.Errorf("the body never arrived: %q", data)
	}
}

func TestSendExplainsAnAuthenticationFailure(t *testing.T) {
	server := startFakeSMTP(t, true)
	host, port, _ := net.SplitHostPort(server.addr())

	sender := NewSender(SenderConfig{
		Host: host, Port: atoi(t, port),
		Username: "me@example.com", Password: "wrong",
		From: "me@example.com", To: "me@example.com",
	})
	sender.Dial = plainDial

	err := sender.Send(context.Background(), "s", "<p>x</p>", "x")
	if err == nil {
		t.Fatal("expected an authentication error")
	}
	// The most common cause deserves to be named rather than left as 535.
	if !strings.Contains(err.Error(), "authorization code") {
		t.Errorf("the error should point at the authorization code: %q", err)
	}
}

func TestSendFailsClearlyWhenConfigurationIsIncomplete(t *testing.T) {
	sender := NewSender(SenderConfig{Host: "smtp.example.com", Port: 465})
	sender.Dial = plainDial

	err := sender.Send(context.Background(), "s", "<p>x</p>", "x")
	if err == nil {
		t.Fatal("expected a configuration error")
	}
	if !strings.Contains(err.Error(), "SMTP_USER") && !strings.Contains(err.Error(), "recipient") {
		t.Errorf("the error should name what is missing: %q", err)
	}
}

// ---------------------------------------------------------------------------
// Configuration.
// ---------------------------------------------------------------------------

func TestConfigFromEnv(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "smtp")
	if err := os.WriteFile(secret, []byte("deadbeefdeadbeef\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SMTP_HOST", "")
	t.Setenv("SMTP_PORT", "")
	t.Setenv("SMTP_USER", "848525382@qq.com")
	t.Setenv("SMTP_PASSWORD_FILE", secret)
	t.Setenv("SMTP_PASSWORD", "")
	t.Setenv("QQ_SMTP_AUTH_CODE", "")
	t.Setenv("LOVE_MAIL_TO", "")
	t.Setenv("SMTP_FROM", "")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "smtp.qq.com" || cfg.Port != 465 {
		t.Errorf("host/port = %s/%d, want smtp.qq.com/465", cfg.Host, cfg.Port)
	}
	if cfg.Password != "deadbeefdeadbeef" {
		t.Errorf("the password file should be read and trimmed, got %q", cfg.Password)
	}
	if cfg.To != "848525382@qq.com" {
		t.Errorf("To should default to the user, got %q", cfg.To)
	}
}

func TestConfigFromEnvFallsBackToTheEnvironmentVariable(t *testing.T) {
	t.Setenv("SMTP_PASSWORD_FILE", "")
	t.Setenv("SMTP_PASSWORD", "")
	t.Setenv("QQ_SMTP_AUTH_CODE", "inline-code")
	t.Setenv("SMTP_USER", "a@qq.com")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != "inline-code" {
		t.Errorf("password = %q", cfg.Password)
	}
}

func TestConfigFromEnvRejectsANonNumericPort(t *testing.T) {
	t.Setenv("SMTP_PORT", "not-a-port")
	t.Setenv("SMTP_USER", "a@qq.com")
	t.Setenv("QQ_SMTP_AUTH_CODE", "x")

	if _, err := ConfigFromEnv(); err == nil {
		t.Error("expected a port error")
	}
}

func TestConfigFromEnvReportsAMissingPassword(t *testing.T) {
	t.Setenv("SMTP_PASSWORD_FILE", "")
	t.Setenv("SMTP_PASSWORD", "")
	t.Setenv("QQ_SMTP_AUTH_CODE", "")
	t.Setenv("SMTP_USER", "a@qq.com")

	_, err := ConfigFromEnv()
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "SMTP_PASSWORD_FILE") {
		t.Errorf("the error should name the preferred source: %q", err)
	}
}

func TestAddressOnlyStripsADisplayName(t *testing.T) {
	cases := map[string]string{
		"me@example.com":       "me@example.com",
		"Me <me@example.com>":  "me@example.com",
		"<me@example.com>":     "me@example.com",
		"爱 <love@example.com>": "love@example.com",
	}
	for in, want := range cases {
		if got := addressOnly(in); got != want {
			t.Errorf("addressOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatalf("bad port %q: %v", s, err)
	}
	return n
}
