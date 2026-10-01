// Package config loads the server's runtime settings from WEBPROGRESS_*
// environment variables (and an optional .env file), mirroring the Python
// pydantic-settings configuration.
package config

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// Settings holds OIDC, session, and storage configuration.
type Settings struct {
	OIDCClientID          string
	OIDCClientSecret      string
	OIDCServerMetadataURL string
	OIDCScope             string
	BaseURL               string
	SessionSecret         string
	DBPath                string
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
