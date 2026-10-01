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
	// handleIngest only needs the store, hub, config, and cadence tracker;
	// auth/templates are unused here. The tracker uses a 30s default cadence.
	return &Server{store: store, hub: bus.New(), cfg: &config.Settings{}, cadence: newCadenceTracker(30)}
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

func TestIngestStampsLivenessThresholds(t *testing.T) {
	s := newIngestServer(t) // helper uses a 30s default cadence
	ctx := context.Background()
	plaintext, err := s.store.CreateToken(ctx, "user-1", "t")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	ch, cancel := s.hub.Subscribe()
	defer cancel()

	// The first update has no observed cadence yet, so the server stamps the
	// thresholds derived from the default interval (stall 2×, dead 10×). The
	// client advertises nothing about liveness.
	body := `{"user_hostname":"h","description":"d","progress":1,"total":100,"key":"` + plaintext + `"}`
	req := httptest.NewRequest("POST", "/handler", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.7:5555"
	s.handleIngest(httptest.NewRecorder(), req)

	select {
	case routed := <-ch:
		if routed.StallSeconds != 60 || routed.DeadSeconds != 300 {
			t.Errorf("thresholds = %v/%v, want 60/300 from the default cadence", routed.StallSeconds, routed.DeadSeconds)
		}
	case <-time.After(time.Second):
		t.Fatal("no payload published")
	}
}

func TestIngestStampsInstanceIDAndNewRunGetsNewCard(t *testing.T) {
	s := newIngestServer(t)
	ctx := context.Background()
	plaintext, err := s.store.CreateToken(ctx, "user-1", "t")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	ch, cancel := s.hub.Subscribe()
	defer cancel()

	// The client assigns the uuid; two updates of the same run carry the same one,
	// a restart carries a new one.
	post := func(uuid, progress string) bus.RoutedPayload {
		body := `{"user_hostname":"h","script":"s","description":"d","uuid":"` + uuid +
			`","progress":` + progress + `,"total":100,"key":"` + plaintext + `"}`
		req := httptest.NewRequest("POST", "/handler", strings.NewReader(body))
		req.RemoteAddr = "203.0.113.7:5555"
		s.handleIngest(httptest.NewRecorder(), req)
		select {
		case routed := <-ch:
			return routed
		case <-time.After(time.Second):
			t.Fatal("no payload published")
			return bus.RoutedPayload{}
		}
	}

	// The stamped card id is the client's uuid.
	if got := post("run-a", "40").InstanceID; got != "run-a" {
		t.Fatalf("instance id = %q, want the client uuid %q", got, "run-a")
	}
	// Same run (same uuid, progress advances) keeps the same card.
	if same := post("run-a", "80").InstanceID; same != "run-a" {
		t.Fatalf("advancing run changed card: %q != %q", same, "run-a")
	}
	// A restart (new uuid) opens a new card instead of reviving the old.
	if restarted := post("run-b", "5").InstanceID; restarted == "run-a" {
		t.Fatal("restart reused the old card id")
	}
}

// A client too old to send a uuid falls back to the task key, so its updates
// still route to a card (the pre-v3 behaviour, which cannot tell runs apart).
func TestIngestFallsBackToTaskKeyWithoutUUID(t *testing.T) {
	s := newIngestServer(t)
	ctx := context.Background()
	plaintext, err := s.store.CreateToken(ctx, "user-1", "t")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	ch, cancel := s.hub.Subscribe()
	defer cancel()

	body := `{"user_hostname":"h","script":"s","description":"d","progress":1,"total":100,"key":"` + plaintext + `"}`
	req := httptest.NewRequest("POST", "/handler", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.7:5555"
	s.handleIngest(httptest.NewRecorder(), req)

	select {
	case routed := <-ch:
		if routed.InstanceID != "s:h:d" {
			t.Errorf("InstanceID = %q, want the TaskKey fallback %q", routed.InstanceID, "s:h:d")
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
