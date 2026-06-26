package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/braydin72/mediaforge/internal/config"
)

// SMTPClient sends email notifications via SMTP.
// It holds a pointer to the config so credential/settings changes take effect
// without rebuilding the client.
type SMTPClient struct {
	cfg *config.EmailNotificationConfig

	// sendMailFunc is swapped in tests to avoid real network calls.
	sendMailFunc func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error
}

// NewSMTPClient creates a client backed by the given config pointer.
func NewSMTPClient(cfg *config.EmailNotificationConfig) *SMTPClient {
	return &SMTPClient{cfg: cfg}
}

func (c *SMTPClient) Name() string { return "email" }

// IsConfigured returns true when all required fields are set and email is enabled.
func (c *SMTPClient) IsConfigured() bool {
	return c.cfg != nil &&
		c.cfg.Enabled &&
		c.cfg.SMTPHost != "" &&
		c.cfg.Username != "" &&
		c.cfg.Password != "" &&
		c.cfg.From != "" &&
		c.cfg.To != ""
}

// Send delivers a single email.
func (c *SMTPClient) Send(_ context.Context, subject, body string) error {
	if !c.IsConfigured() {
		return fmt.Errorf("SMTP not configured")
	}

	addr := fmt.Sprintf("%s:%d", c.cfg.SMTPHost, c.cfg.SMTPPort)
	auth := smtp.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.SMTPHost)
	msg := buildMessage(c.cfg.From, c.cfg.To, subject, body)
	to := []string{c.cfg.To}

	// Injected send function (tests only).
	if c.sendMailFunc != nil {
		return c.sendMailFunc(addr, auth, c.cfg.From, to, msg)
	}

	// Port 465 = implicit TLS (SMTPS).
	if c.cfg.SMTPPort == 465 {
		return c.sendSMTPS(addr, auth, to, msg)
	}

	// Port 587 with STARTTLS (Gmail, most modern providers).
	if c.cfg.SMTPTLS {
		return c.sendSTARTTLS(addr, auth, to, msg)
	}

	// Port 25 or plain SMTP (internal mail servers, no TLS).
	return smtp.SendMail(addr, nil, c.cfg.From, to, msg)
}

func (c *SMTPClient) sendSTARTTLS(addr string, auth smtp.Auth, to []string, msg []byte) error {
	conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		return fmt.Errorf("SMTP connect: %w", err)
	}

	client, err := smtp.NewClient(conn, c.cfg.SMTPHost)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP handshake: %w", err)
	}
	defer client.Close()

	if err := client.StartTLS(&tls.Config{ServerName: c.cfg.SMTPHost}); err != nil {
		return fmt.Errorf("STARTTLS: %w", err)
	}
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("SMTP auth: %w", err)
	}
	return sendViaClient(client, c.cfg.From, to, msg)
}

func (c *SMTPClient) sendSMTPS(addr string, auth smtp.Auth, to []string, msg []byte) error {
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 15 * time.Second},
		"tcp", addr,
		&tls.Config{ServerName: c.cfg.SMTPHost},
	)
	if err != nil {
		return fmt.Errorf("SMTPS connect: %w", err)
	}

	client, err := smtp.NewClient(conn, c.cfg.SMTPHost)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer client.Close()

	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("SMTP auth: %w", err)
	}
	return sendViaClient(client, c.cfg.From, to, msg)
}

func sendViaClient(client *smtp.Client, from string, to []string, msg []byte) error {
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return err
		}
	}
	wc, err := client.Data()
	if err != nil {
		return err
	}
	if _, err = wc.Write(msg); err != nil {
		return err
	}
	if err = wc.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// buildMessage constructs a minimal RFC 2822 email message.
func buildMessage(from, to, subject, body string) []byte {
	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString("Subject: " + subject + "\r\n")
	sb.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	sb.WriteString("\r\n")
	// Normalise line endings for SMTP.
	sb.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(sb.String())
}
