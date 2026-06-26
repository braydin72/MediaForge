package notify

import (
	"context"
	"errors"
	"net/smtp"
	"testing"

	"github.com/braydin72/mediaforge/internal/config"
)

func minimalEmailCfg() *config.EmailNotificationConfig {
	return &config.EmailNotificationConfig{
		Enabled:  true,
		SMTPHost: "smtp.example.com",
		SMTPPort: 587,
		SMTPTLS:  true,
		Username: "user@example.com",
		Password: "secret",
		From:     "user@example.com",
		To:       "dest@example.com",
	}
}

func TestSMTPClient_IsConfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  func() *config.EmailNotificationConfig
		want bool
	}{
		{
			name: "fully configured",
			cfg:  minimalEmailCfg,
			want: true,
		},
		{
			name: "disabled",
			cfg: func() *config.EmailNotificationConfig {
				c := minimalEmailCfg()
				c.Enabled = false
				return c
			},
			want: false,
		},
		{
			name: "missing host",
			cfg: func() *config.EmailNotificationConfig {
				c := minimalEmailCfg()
				c.SMTPHost = ""
				return c
			},
			want: false,
		},
		{
			name: "missing password",
			cfg: func() *config.EmailNotificationConfig {
				c := minimalEmailCfg()
				c.Password = ""
				return c
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewSMTPClient(tt.cfg())
			if got := c.IsConfigured(); got != tt.want {
				t.Errorf("IsConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSMTPClient_SendSuccess(t *testing.T) {
	cfg := minimalEmailCfg()
	c := NewSMTPClient(cfg)

	var capturedAddr string
	var capturedFrom string
	var capturedTo []string

	c.sendMailFunc = func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
		capturedAddr = addr
		capturedFrom = from
		capturedTo = to
		return nil
	}

	if err := c.Send(context.Background(), "Test Subject", "Test body"); err != nil {
		t.Fatalf("Send() returned unexpected error: %v", err)
	}

	if capturedAddr != "smtp.example.com:587" {
		t.Errorf("addr = %q, want %q", capturedAddr, "smtp.example.com:587")
	}
	if capturedFrom != cfg.From {
		t.Errorf("from = %q, want %q", capturedFrom, cfg.From)
	}
	if len(capturedTo) != 1 || capturedTo[0] != cfg.To {
		t.Errorf("to = %v, want [%q]", capturedTo, cfg.To)
	}
}

func TestSMTPClient_SendAuthFailure(t *testing.T) {
	cfg := minimalEmailCfg()
	c := NewSMTPClient(cfg)

	authErr := errors.New("535 5.7.8 Username and Password not accepted")
	c.sendMailFunc = func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
		return authErr
	}

	err := c.Send(context.Background(), "Subject", "Body")
	if err == nil {
		t.Fatal("expected error on auth failure, got nil")
	}
	if !errors.Is(err, authErr) {
		t.Errorf("error = %v, want to wrap %v", err, authErr)
	}
}

func TestSMTPClient_SendUnconfigured(t *testing.T) {
	cfg := minimalEmailCfg()
	cfg.Enabled = false
	c := NewSMTPClient(cfg)

	if err := c.Send(context.Background(), "Subject", "Body"); err == nil {
		t.Fatal("expected error when unconfigured, got nil")
	}
}

func TestBuildMessage(t *testing.T) {
	msg := buildMessage("from@example.com", "to@example.com", "Hello", "Line 1\nLine 2")
	s := string(msg)
	if !contains(s, "From: from@example.com") {
		t.Error("missing From header")
	}
	if !contains(s, "Subject: Hello") {
		t.Error("missing Subject header")
	}
	if !contains(s, "Line 1\r\nLine 2") {
		t.Error("line endings not converted to CRLF")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
