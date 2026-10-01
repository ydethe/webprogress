// Package storage is the SQLite-backed persistence for users and tokens.
//
// Tokens are minted server-side; only their SHA-256 hash and a short non-secret
// prefix are stored, so the plaintext is shown once at creation and can never be
// recovered. A token is bound to exactly one user (user_sub) — that binding is
// how an incoming update is routed.
package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ydethe/webprogress/internal/notify"
)

// prefixLen is the number of leading plaintext characters kept for display.
const prefixLen = 8

// Store wraps the database handle and the operations on it.
type Store struct {
	db *sql.DB
}

// Token is a user's active (non-revoked) token as shown in the UI.
type Token struct {
	Hash      string
	Label     string
	Prefix    string
	CreatedAt string
}

// Open opens (creating if needed) the SQLite database at path and ensures the
// schema exists. SQLite tolerates only one writer at a time, so the busy_timeout
// pragma lets concurrent operations wait briefly rather than fail with
// "database is locked".
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.initDB(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initDB() error {
	const schema = `
CREATE TABLE IF NOT EXISTS users (
    sub        TEXT PRIMARY KEY,
    email      TEXT,
    name       TEXT,
    created_at TEXT
);
CREATE TABLE IF NOT EXISTS tokens (
    token_hash TEXT PRIMARY KEY,
    user_sub   TEXT NOT NULL,
    label      TEXT,
    prefix     TEXT,
    created_at TEXT,
    revoked    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS notify_settings (
    user_sub       TEXT PRIMARY KEY,
    channel        TEXT    NOT NULL DEFAULT '',
    pushover_token TEXT    NOT NULL DEFAULT '',
    pushover_user  TEXT    NOT NULL DEFAULT '',
    slack_webhook  TEXT    NOT NULL DEFAULT '',
    webhook_url    TEXT    NOT NULL DEFAULT '',
    stall_seconds  INTEGER NOT NULL DEFAULT 0,
    updated_at     TEXT
);`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Columns added after the initial release; ADD COLUMN on an existing DB is a
	// no-op we tolerate (SQLite reports a duplicate-column error we ignore).
	return s.addColumns(
		"ALTER TABLE notify_settings ADD COLUMN webhook_header_name TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE notify_settings ADD COLUMN webhook_header_value TEXT NOT NULL DEFAULT ''",
	)
}

// addColumns runs idempotent ALTER TABLE ... ADD COLUMN statements, ignoring the
// "duplicate column name" error so startup is safe on an already-migrated DB.
func (s *Store) addColumns(stmts ...string) error {
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	return nil
}

// UpsertUser creates a user on first sign-in or refreshes email/name on
// subsequent sign-ins. created_at is preserved across refreshes.
func (s *Store) UpsertUser(ctx context.Context, sub, email, name string) error {
	const q = `
INSERT INTO users (sub, email, name, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(sub) DO UPDATE SET email = excluded.email, name = excluded.name`
	_, err := s.db.ExecContext(ctx, q, sub, email, name, now())
	return err
}

// CreateToken mints a new token for a user, stores only its hash and prefix, and
// returns the full plaintext value once. The caller must surface it immediately;
// it is never persisted and cannot be retrieved again.
func (s *Store) CreateToken(ctx context.Context, sub, label string) (string, error) {
	plaintext, err := randomToken()
	if err != nil {
		return "", err
	}
	const q = `
INSERT INTO tokens (token_hash, user_sub, label, prefix, created_at, revoked)
VALUES (?, ?, ?, ?, ?, 0)`
	if _, err := s.db.ExecContext(ctx, q, hashToken(plaintext), sub, label, plaintext[:prefixLen], now()); err != nil {
		return "", err
	}
	return plaintext, nil
}

// ResolveToken returns the owning user's sub for a token value if the token
// exists and is not revoked. An empty, unknown, or revoked token yields
// ("", false). This is the ingest authentication primitive.
func (s *Store) ResolveToken(ctx context.Context, plaintext string) (string, bool) {
	if plaintext == "" {
		return "", false
	}
	const q = `SELECT user_sub FROM tokens WHERE token_hash = ? AND revoked = 0`
	var sub string
	switch err := s.db.QueryRowContext(ctx, q, hashToken(plaintext)).Scan(&sub); {
	case err == nil:
		return sub, true
	case errors.Is(err, sql.ErrNoRows):
		return "", false
	default:
		return "", false
	}
}

// ListTokens returns a user's active (non-revoked) tokens, newest first.
func (s *Store) ListTokens(ctx context.Context, sub string) ([]Token, error) {
	const q = `
SELECT token_hash, label, prefix, created_at
FROM tokens WHERE user_sub = ? AND revoked = 0
ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, sub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.Hash, &t.Label, &t.Prefix, &t.CreatedAt); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// RevokeToken marks one of the user's own tokens revoked, effective immediately.
// It is scoped by user_sub so a user cannot revoke another user's token.
func (s *Store) RevokeToken(ctx context.Context, sub, tokenHash string) error {
	const q = `UPDATE tokens SET revoked = 1 WHERE token_hash = ? AND user_sub = ?`
	_, err := s.db.ExecContext(ctx, q, tokenHash, sub)
	return err
}

// GetNotifyConfig returns a user's persisted notification config and whether a
// row exists for them. Callers treat "no row" as "fall back to env defaults";
// a stored row always takes precedence, even one that disables notifications.
func (s *Store) GetNotifyConfig(ctx context.Context, sub string) (notify.Config, bool, error) {
	const q = `
SELECT channel, pushover_token, pushover_user, slack_webhook, webhook_url,
       webhook_header_name, webhook_header_value, stall_seconds
FROM notify_settings WHERE user_sub = ?`
	var (
		cfg     notify.Config
		channel string
	)
	err := s.db.QueryRowContext(ctx, q, sub).Scan(
		&channel, &cfg.PushoverToken, &cfg.PushoverUser,
		&cfg.SlackWebhookURL, &cfg.WebhookURL,
		&cfg.WebhookHeaderName, &cfg.WebhookHeaderValue, &cfg.StallSeconds)
	switch {
	case err == nil:
		cfg.Channel = notify.Channel(channel)
		return cfg, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return notify.Config{}, false, nil
	default:
		return notify.Config{}, false, err
	}
}

// SaveNotifyConfig upserts a user's notification config, replacing any prior row.
func (s *Store) SaveNotifyConfig(ctx context.Context, sub string, cfg notify.Config) error {
	const q = `
INSERT INTO notify_settings
    (user_sub, channel, pushover_token, pushover_user, slack_webhook, webhook_url,
     webhook_header_name, webhook_header_value, stall_seconds, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(user_sub) DO UPDATE SET
    channel              = excluded.channel,
    pushover_token       = excluded.pushover_token,
    pushover_user        = excluded.pushover_user,
    slack_webhook        = excluded.slack_webhook,
    webhook_url          = excluded.webhook_url,
    webhook_header_name  = excluded.webhook_header_name,
    webhook_header_value = excluded.webhook_header_value,
    stall_seconds        = excluded.stall_seconds,
    updated_at           = excluded.updated_at`
	_, err := s.db.ExecContext(ctx, q,
		sub, string(cfg.Channel), cfg.PushoverToken, cfg.PushoverUser,
		cfg.SlackWebhookURL, cfg.WebhookURL,
		cfg.WebhookHeaderName, cfg.WebhookHeaderValue, cfg.StallSeconds, now())
	return err
}

// ClearNotifyConfig removes a user's persisted config so they fall back to the
// server-wide env defaults again.
func (s *Store) ClearNotifyConfig(ctx context.Context, sub string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notify_settings WHERE user_sub = ?`, sub)
	return err
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// randomToken returns a URL-safe token with at least prefixLen characters.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
