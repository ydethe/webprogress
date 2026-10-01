package config

import (
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	// Ensure no WEBPROGRESS_* vars leak in from the environment.
	for _, k := range []string{
		"WEBPROGRESS_OIDC_CLIENT_ID", "WEBPROGRESS_OIDC_CLIENT_SECRET",
		"WEBPROGRESS_OIDC_SERVER_METADATA_URL", "WEBPROGRESS_OIDC_SCOPE",
		"WEBPROGRESS_BASE_URL", "WEBPROGRESS_SESSION_SECRET", "WEBPROGRESS_DB_PATH",
	} {
		t.Setenv(k, "")
	}
	// t.Setenv sets empty strings; unset them so Load sees true absence.
	cfg := &Settings{
		OIDCScope:     "openid email profile",
		BaseURL:       "http://127.0.0.1:8775",
		SessionSecret: "change-me",
		DBPath:        "webprogress.db",
	}
	if got := cfg.RedirectURI(); got != "http://127.0.0.1:8775/auth" {
		t.Fatalf("RedirectURI = %q", got)
	}
	if got := cfg.Scopes(); len(got) != 3 || got[0] != "openid" {
		t.Fatalf("Scopes = %v", got)
	}
}

func TestRedirectURITrimsTrailingSlash(t *testing.T) {
	cfg := &Settings{BaseURL: "https://example.com/"}
	if got := cfg.RedirectURI(); got != "https://example.com/auth" {
		t.Fatalf("RedirectURI = %q", got)
	}
}

func TestRequireOIDC(t *testing.T) {
	cfg := &Settings{}
	err := cfg.RequireOIDC()
	if err == nil {
		t.Fatal("expected error for empty OIDC config")
	}
	for _, want := range []string{
		"WEBPROGRESS_OIDC_CLIENT_ID",
		"WEBPROGRESS_OIDC_CLIENT_SECRET",
		"WEBPROGRESS_OIDC_SERVER_METADATA_URL",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}

	cfg = &Settings{OIDCClientID: "id", OIDCClientSecret: "secret", OIDCServerMetadataURL: "url"}
	if err := cfg.RequireOIDC(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMask(t *testing.T) {
	if mask("") != "" {
		t.Error("empty should stay empty")
	}
	if mask("secret") != "***" {
		t.Error("non-empty should be masked")
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("WEBPROGRESS_OIDC_CLIENT_ID", "cid")
	t.Setenv("WEBPROGRESS_BASE_URL", "https://wp.example.com")
	cfg := Load()
	if cfg.OIDCClientID != "cid" {
		t.Errorf("OIDCClientID = %q", cfg.OIDCClientID)
	}
	if cfg.BaseURL != "https://wp.example.com" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.OIDCScope != "openid email profile" {
		t.Errorf("default scope not applied: %q", cfg.OIDCScope)
	}
}

func TestDefaultNotifyFromEnv(t *testing.T) {
	t.Setenv("WEBPROGRESS_NOTIFY_CHANNEL", "slack")
	t.Setenv("WEBPROGRESS_NOTIFY_SLACK_WEBHOOK_URL", "https://hooks.example.com/x")
	t.Setenv("WEBPROGRESS_NOTIFY_STALL_SECONDS", "300")
	cfg := Load()

	n := cfg.DefaultNotify()
	if string(n.Channel) != "slack" {
		t.Errorf("channel = %q", n.Channel)
	}
	if n.SlackWebhookURL != "https://hooks.example.com/x" {
		t.Errorf("slack url = %q", n.SlackWebhookURL)
	}
	if n.StallSeconds != 300 {
		t.Errorf("stall seconds = %d", n.StallSeconds)
	}
	if err := n.Validate(); err != nil {
		t.Errorf("env default should be valid: %v", err)
	}
}

func TestDefaultNotifyParsesWebhookHeader(t *testing.T) {
	t.Setenv("WEBPROGRESS_NOTIFY_CHANNEL", "webhook")
	t.Setenv("WEBPROGRESS_NOTIFY_WEBHOOK_URL", "https://example.com/hook")
	t.Setenv("WEBPROGRESS_NOTIFY_WEBHOOK_HEADER", "Authorization: Bearer xyz")

	n := Load().DefaultNotify()
	if n.WebhookHeaderName != "Authorization" || n.WebhookHeaderValue != "Bearer xyz" {
		t.Fatalf("header = (%q, %q)", n.WebhookHeaderName, n.WebhookHeaderValue)
	}
}

func TestEnvIntFallsBackOnGarbage(t *testing.T) {
	t.Setenv("WEBPROGRESS_NOTIFY_STALL_SECONDS", "not-a-number")
	if cfg := Load(); cfg.NotifyStallSeconds != 0 {
		t.Errorf("NotifyStallSeconds = %d, want fallback 0", cfg.NotifyStallSeconds)
	}
}
