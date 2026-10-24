// Package mail is a small SMTP sender for the portal's transactional email
// (password reset, run-done notifications). Configuration lives in the DB (managed
// in the web UI), never in config.yaml. Three transport modes cover the common
// providers: implicit TLS (465), STARTTLS (587), and plain (25 / localhost).
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// Config is an SMTP endpoint + sender identity.
type Config struct {
	Host     string
	Port     int
	User     string // "" → no auth
	Pass     string
	From     string
	Security string // "tls" (implicit) | "starttls" | "none"
}

// Enabled reports whether the config is complete enough to send.
func (c Config) Enabled() bool {
	return c.Host != "" && c.Port > 0 && c.From != ""
}

// stripCRLF removes carriage returns / line feeds so a value can't inject extra
// headers (or a body) when written into a header line.
func stripCRLF(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// sanitizeRecipients validates and canonicalizes recipient addresses so untrusted
// input cannot be used as malformed SMTP envelope/header values.
func sanitizeRecipients(to []string) ([]string, error) {
	out := make([]string, 0, len(to))
	for _, raw := range to {
		v := strings.TrimSpace(raw)
		if v == "" {
			return nil, errors.New("mail: empty recipient")
		}
		addr, err := netmail.ParseAddress(v)
		if err != nil || addr == nil || strings.TrimSpace(addr.Address) == "" {
			return nil, fmt.Errorf("mail: invalid recipient %q", raw)
		}
		if strings.ContainsAny(addr.Address, "\r\n") {
			return nil, fmt.Errorf("mail: invalid recipient %q", raw)
		}
		out = append(out, addr.Address)
	}
	return out, nil
}

// BuildMessage assembles an RFC 5322 HTML message. The subject is RFC 2047 encoded
// so non-ASCII (e.g. Chinese) survives; From/To are stripped of CR/LF to prevent
// header injection.
func BuildMessage(from string, to []string, subject, htmlBody string, date time.Time) []byte {
	safeTo := make([]string, len(to))
	for i, a := range to {
		safeTo[i] = stripCRLF(a)
	}
	var b strings.Builder
	b.WriteString("From: " + stripCRLF(from) + "\r\n")
	b.WriteString("To: " + strings.Join(safeTo, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.BEncoding.Encode("UTF-8", subject) + "\r\n")
	b.WriteString("Date: " + date.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	return []byte(b.String())
}

// Send delivers one HTML message. It negotiates TLS per Security and authenticates
// when a user is set.
func (c Config) Send(to []string, subject, htmlBody string) error {
	_, err := c.Attempt(context.Background(), to, subject, htmlBody)
	return err
}

// Result distinguishes confirmed acceptance from a lost final DATA response.
// Accepted means the relay accepted the message, not that a user received it.
type Result struct {
	Accepted  bool
	Uncertain bool
}

// Attempt sends once. Its deadline covers the entire SMTP conversation, including
// TLS negotiation and cleanup. Callers own retry policy and must not blindly
// retry an uncertain result.
func (c Config) Attempt(ctx context.Context, to []string, subject, htmlBody string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := Result{}
	if !c.Enabled() {
		return result, errors.New("mail: not configured")
	}
	if len(to) == 0 {
		return result, errors.New("mail: no recipients")
	}
	safeTo, err := sanitizeRecipients(to)
	if err != nil {
		return result, err
	}
	msg := BuildMessage(c.From, safeTo, subject, htmlBody, time.Now())
	addr := net.JoinHostPort(c.Host, fmt.Sprint(c.Port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return result, fmt.Errorf("mail: dial: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return result, fmt.Errorf("mail: deadline: %w", err)
	}
	var transport net.Conn = conn
	if c.Security == "tls" {
		secure := tls.Client(conn, &tls.Config{ServerName: c.Host})
		if err := secure.HandshakeContext(ctx); err != nil {
			return result, fmt.Errorf("mail: tls: %w", err)
		}
		transport = secure
	}

	cl, err := smtp.NewClient(transport, c.Host)
	if err != nil {
		conn.Close()
		return result, fmt.Errorf("mail: client: %w", err)
	}
	defer cl.Close()

	if c.Security == "starttls" {
		if err := cl.StartTLS(&tls.Config{ServerName: c.Host}); err != nil {
			return result, fmt.Errorf("mail: starttls: %w", err)
		}
	}
	if c.User != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.User, c.Pass, c.Host)); err != nil {
			return result, fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return result, fmt.Errorf("mail: from: %w", err)
	}
	for _, rcpt := range safeTo {
		if err := cl.Rcpt(rcpt); err != nil {
			return result, fmt.Errorf("mail: rcpt %s: %w", rcpt, err)
		}
	}
	wc, err := cl.Data()
	if err != nil {
		return result, fmt.Errorf("mail: data: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		// Do not close the DATA writer after a failed write: that would terminate
		// and potentially submit a truncated body. Closing the connection aborts it.
		return result, fmt.Errorf("mail: write: %w", err)
	}
	if err := wc.Close(); err != nil {
		var rejection *textproto.Error
		result.Uncertain = !errors.As(err, &rejection)
		return result, fmt.Errorf("mail: close: %w", err)
	}
	result.Accepted = true
	// QUIT is cleanup after final DATA acceptance. Its failure cannot undo the
	// accepted message and must not trigger a duplicate send.
	_ = cl.Quit()
	return result, nil
}
