package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ydethe/webprogress/internal/notify"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestCreateResolveRevoke(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	plaintext, err := store.CreateToken(ctx, "user-1", "laptop")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if len(plaintext) < prefixLen {
		t.Fatalf("token too short: %q", plaintext)
	}

	sub, ok := store.ResolveToken(ctx, plaintext)
	if !ok || sub != "user-1" {
		t.Fatalf("ResolveToken = %q, %v; want user-1, true", sub, ok)
	}

	// Unknown and empty tokens resolve to nothing.
	if _, ok := store.ResolveToken(ctx, "nope"); ok {
		t.Fatal("unknown token resolved")
	}
	if _, ok := store.ResolveToken(ctx, ""); ok {
		t.Fatal("empty token resolved")
	}

	// List shows the active token by prefix.
	tokens, err := store.ListTokens(ctx, "user-1")
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0].Prefix != plaintext[:prefixLen] || tokens[0].Label != "laptop" {
		t.Fatalf("unexpected tokens: %+v", tokens)
	}

	// Revoke makes it unresolvable and drops it from the list.
	if err := store.RevokeToken(ctx, "user-1", tokens[0].Hash); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if _, ok := store.ResolveToken(ctx, plaintext); ok {
		t.Fatal("revoked token still resolves")
	}
	tokens, _ = store.ListTokens(ctx, "user-1")
	if len(tokens) != 0 {
		t.Fatalf("revoked token still listed: %+v", tokens)
	}
}

func TestRevokeIsUserScoped(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	plaintext, _ := store.CreateToken(ctx, "owner", "t")
	tokens, _ := store.ListTokens(ctx, "owner")
	hash := tokens[0].Hash

	// A different user cannot revoke the owner's token.
	if err := store.RevokeToken(ctx, "attacker", hash); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if _, ok := store.ResolveToken(ctx, plaintext); !ok {
		t.Fatal("token was revoked by a non-owner")
	}
}

func TestNotifyConfigCRUD(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// No row yet: not found, and the returned config is the disabled zero value.
	cfg, ok, err := store.GetNotifyConfig(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetNotifyConfig: %v", err)
	}
	if ok || cfg.Enabled() {
		t.Fatalf("expected no persisted config, got ok=%v cfg=%+v", ok, cfg)
	}

	want := notify.Config{
		Channel:       notify.ChannelPushover,
		PushoverToken: "tok",
		PushoverUser:  "usr",
		StallSeconds:  120,
	}
	if err := store.SaveNotifyConfig(ctx, "user-1", want); err != nil {
		t.Fatalf("SaveNotifyConfig: %v", err)
	}
	got, ok, err := store.GetNotifyConfig(ctx, "user-1")
	if err != nil || !ok {
		t.Fatalf("GetNotifyConfig after save: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}

	// Upsert replaces the row in place.
	want.Channel = notify.ChannelSlack
	want.SlackWebhookURL = "https://hooks"
	if err := store.SaveNotifyConfig(ctx, "user-1", want); err != nil {
		t.Fatalf("SaveNotifyConfig update: %v", err)
	}
	got, _, _ = store.GetNotifyConfig(ctx, "user-1")
	if got.Channel != notify.ChannelSlack || got.SlackWebhookURL != "https://hooks" {
		t.Fatalf("update not applied: %+v", got)
	}

	// Configs are scoped per user.
	if _, ok, _ := store.GetNotifyConfig(ctx, "user-2"); ok {
		t.Fatal("another user should not see user-1's config")
	}

	// Clear reverts to "no row".
	if err := store.ClearNotifyConfig(ctx, "user-1"); err != nil {
		t.Fatalf("ClearNotifyConfig: %v", err)
	}
	if _, ok, _ := store.GetNotifyConfig(ctx, "user-1"); ok {
		t.Fatal("config still present after clear")
	}
}

func TestUpsertUserRefreshesButPreservesCreatedAt(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if err := store.UpsertUser(ctx, "sub-1", "a@example.com", "Alice"); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	var created1, email, name string
	row := store.db.QueryRowContext(ctx, "SELECT created_at, email, name FROM users WHERE sub = ?", "sub-1")
	if err := row.Scan(&created1, &email, &name); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Re-upsert with new email/name; created_at must not change.
	if err := store.UpsertUser(ctx, "sub-1", "b@example.com", "Alice B"); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	var created2 string
	row = store.db.QueryRowContext(ctx, "SELECT created_at, email, name FROM users WHERE sub = ?", "sub-1")
	if err := row.Scan(&created2, &email, &name); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if created1 != created2 {
		t.Fatalf("created_at changed: %q -> %q", created1, created2)
	}
	if email != "b@example.com" || name != "Alice B" {
		t.Fatalf("email/name not refreshed: %q %q", email, name)
	}
}
