package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ydethe/webprogress/internal/auth"
	"github.com/ydethe/webprogress/internal/bus"
	"github.com/ydethe/webprogress/internal/config"
	"github.com/ydethe/webprogress/internal/models"
	"github.com/ydethe/webprogress/internal/notify"
	"github.com/ydethe/webprogress/internal/storage"
)

func newIngestServer(t *testing.T) *Server {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	// handleIngest only needs the store and hub; auth/templates are unused here.
	return &Server{store: store, hub: bus.New()}
}

func TestIngestUnknownToken(t *testing.T) {
	s := newIngestServer(t)
	body := `{"user_hostname":"h","description":"d","progress":1,"total":2,"key":"bogus"}`
	req := httptest.NewRequest("POST", "/handler", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.7:5555"
	rec := httptest.NewRecorder()

	s.handleIngest(rec, req)

	if rec.Code != 401 {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestIngestValidTokenPublishesAndStampsAddress(t *testing.T) {
	s := newIngestServer(t)
	ctx := context.Background()
	plaintext, err := s.store.CreateToken(ctx, "user-1", "t")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	ch, cancel := s.hub.Subscribe()
	defer cancel()

	body := `{"user_hostname":"h","description":"d","progress":3,"total":6,"key":"` + plaintext + `"}`
	req := httptest.NewRequest("POST", "/handler", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.7:5555"
	rec := httptest.NewRecorder()

	s.handleIngest(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	select {
	case routed := <-ch:
		if routed.UserSub != "user-1" {
			t.Errorf("UserSub = %q", routed.UserSub)
		}
		if routed.Payload.UserSrcAddress != "203.0.113.7" {
			t.Errorf("src address not stamped: %q", routed.Payload.UserSrcAddress)
		}
		if routed.Payload.Fraction() != 0.5 {
			t.Errorf("fraction = %v", routed.Payload.Fraction())
		}
	case <-time.After(time.Second):
		t.Fatal("no payload published")
	}
}

func TestEffectiveNotifyPrefersPersisted(t *testing.T) {
	s := newIngestServer(t)
	s.cfg = &config.Settings{
		NotifyChannel:         "slack",
		NotifySlackWebhookURL: "https://env-default",
	}
	ctx := context.Background()

	// With no persisted row, the env default is in force.
	cfg, persisted := s.effectiveNotify(ctx, "user-1")
	if persisted || cfg.Channel != notify.ChannelSlack || cfg.SlackWebhookURL != "https://env-default" {
		t.Fatalf("env default not used: persisted=%v cfg=%+v", persisted, cfg)
	}

	// A saved config wins over the env default.
	saved := notify.Config{Channel: notify.ChannelWebhook, WebhookURL: "https://user-choice"}
	if err := s.store.SaveNotifyConfig(ctx, "user-1", saved); err != nil {
		t.Fatalf("SaveNotifyConfig: %v", err)
	}
	cfg, persisted = s.effectiveNotify(ctx, "user-1")
	if !persisted || cfg.Channel != notify.ChannelWebhook || cfg.WebhookURL != "https://user-choice" {
		t.Fatalf("persisted config not preferred: persisted=%v cfg=%+v", persisted, cfg)
	}

	// Another user is unaffected and still sees the env default.
	if cfg, persisted := s.effectiveNotify(ctx, "user-2"); persisted || cfg.WebhookURL == "https://user-choice" {
		t.Fatalf("persisted config leaked across users: persisted=%v cfg=%+v", persisted, cfg)
	}
}

func TestIngestNoAuthRoutesWithoutToken(t *testing.T) {
	s := newIngestServer(t)
	s.auth = auth.NewNoAuth(&config.Settings{SessionSecret: "test"}, s.store)

	ch, cancel := s.hub.Subscribe()
	defer cancel()

	// No key at all, yet it is accepted and routed to the local no-auth user.
	body := `{"user_hostname":"h","description":"d","progress":1,"total":2}`
	req := httptest.NewRequest("POST", "/handler", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.7:5555"
	rec := httptest.NewRecorder()

	s.handleIngest(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	select {
	case routed := <-ch:
		if routed.UserSub != auth.NoAuthSub {
			t.Errorf("UserSub = %q, want %q", routed.UserSub, auth.NoAuthSub)
		}
	case <-time.After(time.Second):
		t.Fatal("no payload published")
	}
}

func TestHealth(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("health: %d %s", rec.Code, rec.Body.String())
	}
}

func TestVersionHandshake(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest("GET", "/version", nil)
	rec := httptest.NewRecorder()
	s.handleVersion(rec, req)
	if rec.Code != 200 {
		t.Fatalf("version: status %d", rec.Code)
	}
	var info models.ServerInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if info.Name != "webprogress" {
		t.Errorf("name = %q", info.Name)
	}
	if info.Protocol != models.ProtocolVersion {
		t.Errorf("protocol = %d, want %d", info.Protocol, models.ProtocolVersion)
	}
	if info.Version == "" {
		t.Error("version must be advertised")
	}
}
