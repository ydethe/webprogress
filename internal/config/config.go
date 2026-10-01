// Package config loads the server's runtime settings from WEBPROGRESS_*
// environment variables (and an optional .env file), mirroring the Python
// pydantic-settings configuration.
package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"github.com/ydethe/webprogress/internal/notify"
)

// Settings holds OIDC, session, storage, and default notification configuration.
type Settings struct {
	OIDCClientID          string
	OIDCClientSecret      string
	OIDCServerMetadataURL string
	OIDCScope             string
	BaseURL               string
	SessionSecret         string
	DBPath                string

	// Notify* provide the server-wide default notification channel. They are
	// used only for a user who has not saved their own settings, which always
	// take precedence (see storage and server.effectiveNotify).
	NotifyChannel         string
	NotifyPushoverToken   string
	NotifyPushoverUser    string
	NotifySlackWebhookURL string
	NotifyWebhookURL      string
	NotifyWebhookHeader   string // optional "Name: Value" header for the custom webhook
	NotifyStallSeconds    int
}

// Load reads settings from the environment, after best-effort loading a .env
// file in the working directory (absent .env is not an error). Defaults match
// sample.env.
func Load() *Settings {
	// godotenv does not overwrite existing env vars, matching pydantic-settings
	// precedence (real environment wins over the file).
	_ = godotenv.Load()

	return &Settings{
		OIDCClientID:          env("WEBPROGRESS_OIDC_CLIENT_ID", ""),
		OIDCClientSecret:      env("WEBPROGRESS_OIDC_CLIENT_SECRET", ""),
		OIDCServerMetadataURL: env("WEBPROGRESS_OIDC_SERVER_METADATA_URL", ""),
		OIDCScope:             env("WEBPROGRESS_OIDC_SCOPE", "openid email profile"),
		BaseURL:               env("WEBPROGRESS_BASE_URL", "http://127.0.0.1:8775"),
		SessionSecret:         env("WEBPROGRESS_SESSION_SECRET", "change-me"),
		DBPath:                env("WEBPROGRESS_DB_PATH", "webprogress.db"),

		NotifyChannel:         env("WEBPROGRESS_NOTIFY_CHANNEL", ""),
		NotifyPushoverToken:   env("WEBPROGRESS_NOTIFY_PUSHOVER_TOKEN", ""),
		NotifyPushoverUser:    env("WEBPROGRESS_NOTIFY_PUSHOVER_USER", ""),
		NotifySlackWebhookURL: env("WEBPROGRESS_NOTIFY_SLACK_WEBHOOK_URL", ""),
		NotifyWebhookURL:      env("WEBPROGRESS_NOTIFY_WEBHOOK_URL", ""),
		NotifyWebhookHeader:   env("WEBPROGRESS_NOTIFY_WEBHOOK_HEADER", ""),
		NotifyStallSeconds:    envInt("WEBPROGRESS_NOTIFY_STALL_SECONDS", 0),
	}
}

// DefaultNotify is the server-wide fallback notification config, assembled from
// the WEBPROGRESS_NOTIFY_* env vars. A user's persisted settings override it.
func (s *Settings) DefaultNotify() notify.Config {
	headerName, headerValue := notify.ParseHeader(s.NotifyWebhookHeader)
	return notify.Config{
		Channel:            notify.Channel(s.NotifyChannel),
		PushoverToken:      s.NotifyPushoverToken,
		PushoverUser:       s.NotifyPushoverUser,
		SlackWebhookURL:    s.NotifySlackWebhookURL,
		WebhookURL:         s.NotifyWebhookURL,
		WebhookHeaderName:  headerName,
		WebhookHeaderValue: headerValue,
		StallSeconds:       s.NotifyStallSeconds,
	}
}

// RedirectURI is the OIDC callback, derived from BaseURL + "/auth". It must be
// registered as a redirect URI in the provider.
func (s *Settings) RedirectURI() string {
	return strings.TrimRight(s.BaseURL, "/") + "/auth"
}

// Scopes splits OIDCScope into the individual scope values.
func (s *Settings) Scopes() []string {
	return strings.Fields(s.OIDCScope)
}

// RequireOIDC fails fast if any required OIDC setting is empty.
func (s *Settings) RequireOIDC() error {
	var missing []string
	if s.OIDCClientID == "" {
		missing = append(missing, "WEBPROGRESS_OIDC_CLIENT_ID")
	}
	if s.OIDCClientSecret == "" {
		missing = append(missing, "WEBPROGRESS_OIDC_CLIENT_SECRET")
	}
	if s.OIDCServerMetadataURL == "" {
		missing = append(missing, "WEBPROGRESS_OIDC_SERVER_METADATA_URL")
	}
	if len(missing) > 0 {
		return fmt.Errorf("Missing OIDC configuration: %s", strings.Join(missing, ", "))
	}
	return nil
}

// LogConfig logs the effective settings, masking sensitive values.
func (s *Settings) LogConfig() {
	log.Println("webprogress settings:")
	log.Printf("  oidc_client_id = %s", s.OIDCClientID)
	log.Printf("  oidc_client_secret = %s", mask(s.OIDCClientSecret))
	log.Printf("  oidc_server_metadata_url = %s", s.OIDCServerMetadataURL)
	log.Printf("  oidc_scope = %s", s.OIDCScope)
	log.Printf("  base_url = %s", s.BaseURL)
	log.Printf("  session_secret = %s", mask(s.SessionSecret))
	log.Printf("  db_path = %s", s.DBPath)
	log.Printf("  redirect_uri = %s", s.RedirectURI())
	log.Printf("  notify_channel = %s", s.NotifyChannel)
	log.Printf("  notify_pushover_token = %s", mask(s.NotifyPushoverToken))
	log.Printf("  notify_pushover_user = %s", mask(s.NotifyPushoverUser))
	log.Printf("  notify_slack_webhook_url = %s", mask(s.NotifySlackWebhookURL))
	log.Printf("  notify_webhook_url = %s", mask(s.NotifyWebhookURL))
	log.Printf("  notify_webhook_header = %s", mask(s.NotifyWebhookHeader))
	log.Printf("  notify_stall_seconds = %d", s.NotifyStallSeconds)
}

func mask(v string) string {
	if v == "" {
		return ""
	}
	return "***"
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}
