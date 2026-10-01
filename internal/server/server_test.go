package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ydethe/webprogress/internal/bus"
	"github.com/ydethe/webprogress/internal/models"
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
