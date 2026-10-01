// Package server wires the HTTP routes together: token-authenticated update
// ingestion, OIDC-guarded dashboard pages, the live WebSocket feed, and token
// management. Run starts it on the fixed port 8775.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ydethe/webprogress/internal/auth"
	"github.com/ydethe/webprogress/internal/bus"
	"github.com/ydethe/webprogress/internal/config"
	"github.com/ydethe/webprogress/internal/models"
	"github.com/ydethe/webprogress/internal/storage"
)

// Port is the fixed port the server listens on.
const Port = "8775"

// Version is the server build version advertised from GET /version. It defaults
// to "dev" and is meant to be overridden at build time with the linker, e.g.
// `go build -ldflags "-X github.com/ydethe/webprogress/internal/server.Version=1.2.3"`.
var Version = "dev"

//go:embed web/templates/*.html
var templatesFS embed.FS

//go:embed web/static/*
var staticFS embed.FS

// Server holds the dependencies shared across handlers.
type Server struct {
	cfg   *config.Settings
	store *storage.Store
	hub   *bus.Hub
	auth  *auth.Auth
	tmpl  *template.Template

	upgrader websocket.Upgrader
}

// New constructs a Server from its dependencies.
func New(cfg *config.Settings, store *storage.Store, hub *bus.Hub, a *auth.Auth) (*Server, error) {
	tmpl, err := template.ParseFS(templatesFS, "web/templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:      cfg,
		store:    store,
		hub:      hub,
		auth:     a,
		tmpl:     tmpl,
		upgrader: websocket.Upgrader{},
	}, nil
}

// Handler returns the fully wired HTTP handler (routes + auth guard).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("POST /handler", s.handleIngest)

	mux.HandleFunc("GET /login", s.auth.Login)
	mux.HandleFunc("GET /auth", s.auth.Callback)
	mux.HandleFunc("GET /logout", s.auth.Logout)

	staticSub, _ := fs.Sub(staticFS, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	mux.HandleFunc("GET /{$}", s.handleDashboard)
	mux.HandleFunc("GET /ws", s.handleWS)
	mux.HandleFunc("POST /tokens/create", s.handleTokenCreate)
	mux.HandleFunc("POST /tokens/revoke", s.handleTokenRevoke)

	return s.auth.Middleware(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleVersion advertises the server's identity, build version, and wire
// protocol version. A client performs this handshake before it starts reporting
// so it can adapt the update message it sends to the protocol the server speaks.
// It is unauthenticated, like /health, so any client can probe it first.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, models.ServerInfo{
		Name:     "webprogress",
		Version:  Version,
		Protocol: models.ProtocolVersion,
	})
}

// handleIngest authenticates an update by its token, stamps the sender address,
// and publishes it for live delivery. It never blocks the caller and returns 401
// for an unknown, revoked, or empty token.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	var payload models.ClientPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	userSub, ok := s.store.ResolveToken(r.Context(), payload.Key)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	payload.UserSrcAddress = clientHost(r)
	s.hub.Publish(bus.RoutedPayload{UserSub: userSub, Payload: payload})
	w.WriteHeader(http.StatusOK)
}

// wsMessage is the per-task frame pushed to the browser. Beyond the fields used
// to draw the bar (key/label/value/colour) it carries the derived detail the
// dashboard reveals when a task card is unfolded. This frame is internal to the
// browser and independent of the Python wire contract (models.ClientPayload), so
// it may grow freely without touching client compatibility.
type wsMessage struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	Value       float64 `json:"value"`
	Colour      string  `json:"colour"`
	Script      string  `json:"script"`
	Description string  `json:"description"`
	Host        string  `json:"host"`
	Login       string  `json:"login"`
	SrcAddress  string  `json:"src_address"`
	Progress    float64 `json:"progress"`
	Total       float64 `json:"total"`
	Elapsed     float64 `json:"elapsed"`
	Rate        float64 `json:"rate"`
	Unit        string  `json:"unit"`
	Remaining   float64 `json:"remaining"`
	ETA         string  `json:"eta"`
}

// handleWS upgrades to a WebSocket, subscribes to the hub, and forwards only the
// updates addressed to the connected viewer (per-user isolation at render time).
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	user, ok := s.auth.CurrentUser(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	updates, cancel := s.hub.Subscribe()
	defer cancel()

	// Detect client disconnect: a failed read unblocks the writer loop.
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	for routed := range updates {
		if routed.UserSub != user.Sub {
			continue
		}
		p := routed.Payload
		msg := wsMessage{
			Key:         p.TaskKey(),
			Label:       dashboardLabel(p),
			Value:       p.Fraction(),
			Colour:      p.Colour,
			Script:      p.ScriptName(),
			Description: p.Description,
			Host:        p.UserHostname,
			Login:       p.UserLogin,
			SrcAddress:  p.UserSrcAddress,
			Progress:    p.Progress,
			Total:       p.Total,
			Elapsed:     p.Elapsed,
			Rate:        p.Rate,
			Unit:        p.Unit,
			Remaining:   p.RemainingTime(),
			ETA:         p.ETA().Format(time.RFC3339),
		}
		if err := conn.WriteJSON(msg); err != nil {
			return
		}
	}
}

// dashboardData is the template model for the dashboard page.
type dashboardData struct {
	Name     string
	Tokens   []storage.Token
	NewToken string
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	user, ok := s.auth.CurrentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	tokens, err := s.store.ListTokens(r.Context(), user.Sub)
	if err != nil {
		http.Error(w, "cannot list tokens", http.StatusInternalServerError)
		return
	}
	data := dashboardData{
		Name:     user.Name,
		Tokens:   tokens,
		NewToken: s.auth.PopFlash(w, r, "new_token"),
	}
	if err := s.tmpl.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		log.Printf("render dashboard: %v", err)
	}
}

func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	user, ok := s.auth.CurrentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	label := r.FormValue("label")
	if label == "" {
		label = "token"
	}
	plaintext, err := s.store.CreateToken(r.Context(), user.Sub, label)
	if err != nil {
		http.Error(w, "cannot create token", http.StatusInternalServerError)
		return
	}
	s.auth.Flash(w, r, "new_token", plaintext)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleTokenRevoke(w http.ResponseWriter, r *http.Request) {
	user, ok := s.auth.CurrentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if hash := r.FormValue("token_hash"); hash != "" {
		if err := s.store.RevokeToken(r.Context(), user.Sub, hash); err != nil {
			http.Error(w, "cannot revoke token", http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// Run loads configuration, validates OIDC, opens storage, wires the server, and
// serves on the fixed port until the process is stopped.
func Run() error {
	cfg := config.Load()
	cfg.LogConfig()
	if err := cfg.RequireOIDC(); err != nil {
		return err
	}

	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	a, err := auth.New(context.Background(), cfg, store)
	if err != nil {
		return err
	}

	srv, err := New(cfg, store, bus.New(), a)
	if err != nil {
		return err
	}

	addr := ":" + Port
	log.Printf("webprogress server version %s (protocol v%d)", Version, models.ProtocolVersion)
	log.Printf("webprogress server listening on %s", addr)
	return http.ListenAndServe(addr, srv.Handler())
}

// dashboardLabel is the per-task subitem label. The dashboard groups tasks under
// their script and deployable (host), so the host no longer needs to appear in
// the label itself — the description alone identifies the task within its group.
func dashboardLabel(p models.ClientPayload) string {
	desc := p.Description
	if desc == "" {
		desc = "task"
	}
	return desc
}

// clientHost returns the source host of the request (direct TCP peer), matching
// the Python server. Behind a reverse proxy this is the proxy's address.
func clientHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
