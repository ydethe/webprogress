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
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ydethe/webprogress/internal/auth"
	"github.com/ydethe/webprogress/internal/bus"
	"github.com/ydethe/webprogress/internal/config"
	"github.com/ydethe/webprogress/internal/models"
	"github.com/ydethe/webprogress/internal/notify"
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
	mux.HandleFunc("POST /settings/notify", s.handleNotifySave)
	mux.HandleFunc("POST /settings/notify/reset", s.handleNotifyReset)
	mux.HandleFunc("POST /settings/notify/test", s.handleNotifyTest)

	return s.auth.Middleware(mux)
}

// effectiveNotify returns the notification config in force for a user: their
// persisted settings if they have saved any, otherwise the server-wide env
// defaults. The bool reports whether the config came from persisted settings.
func (s *Server) effectiveNotify(ctx context.Context, sub string) (notify.Config, bool) {
	if cfg, ok, err := s.store.GetNotifyConfig(ctx, sub); err == nil && ok {
		return cfg, true
	}
	return s.cfg.DefaultNotify(), false
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
	// In no-auth mode every update belongs to the single local user and no token
	// is required; otherwise the token authenticates and routes the sender.
	userSub := auth.NoAuthSub
	if !s.auth.NoAuth() {
		sub, ok := s.store.ResolveToken(r.Context(), payload.Key)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		userSub = sub
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
	Name      string
	Tokens    []storage.Token
	NewToken  string
	Version   string
	Protocol  int
	Notify    notifyView
	NotifyMsg string // one-time flash shown after saving/testing settings
}

// notifyView is the flattened notification config the Settings form renders and
// edits. Channel is a plain string so html/template comparisons stay simple.
type notifyView struct {
	Channel            string
	PushoverToken      string
	PushoverUser       string
	SlackWebhook       string
	WebhookURL         string
	WebhookHeaderName  string
	WebhookHeaderValue string
	StallSeconds       int
	Persisted          bool // true when these values come from the user's saved settings
}

func toNotifyView(cfg notify.Config, persisted bool) notifyView {
	return notifyView{
		Channel:            string(cfg.Channel),
		PushoverToken:      cfg.PushoverToken,
		PushoverUser:       cfg.PushoverUser,
		SlackWebhook:       cfg.SlackWebhookURL,
		WebhookURL:         cfg.WebhookURL,
		WebhookHeaderName:  cfg.WebhookHeaderName,
		WebhookHeaderValue: cfg.WebhookHeaderValue,
		StallSeconds:       cfg.StallSeconds,
		Persisted:          persisted,
	}
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
	notifyCfg, persisted := s.effectiveNotify(r.Context(), user.Sub)
	data := dashboardData{
		Name:      user.Name,
		Tokens:    tokens,
		NewToken:  s.auth.PopFlash(w, r, "new_token"),
		Version:   Version,
		Protocol:  models.ProtocolVersion,
		Notify:    toNotifyView(notifyCfg, persisted),
		NotifyMsg: s.auth.PopFlash(w, r, "notify_msg"),
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

// notifyConfigFromForm builds a notify.Config from the Settings form fields.
func notifyConfigFromForm(r *http.Request) notify.Config {
	stall, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("stall_seconds")))
	return notify.Config{
		Channel:            notify.Channel(strings.TrimSpace(r.FormValue("channel"))),
		PushoverToken:      strings.TrimSpace(r.FormValue("pushover_token")),
		PushoverUser:       strings.TrimSpace(r.FormValue("pushover_user")),
		SlackWebhookURL:    strings.TrimSpace(r.FormValue("slack_webhook")),
		WebhookURL:         strings.TrimSpace(r.FormValue("webhook_url")),
		WebhookHeaderName:  strings.TrimSpace(r.FormValue("webhook_header_name")),
		WebhookHeaderValue: strings.TrimSpace(r.FormValue("webhook_header_value")),
		StallSeconds:       stall,
	}
}

// handleNotifySave validates and persists the signed-in user's notification
// settings. A persisted config always wins over the env-var defaults.
func (s *Server) handleNotifySave(w http.ResponseWriter, r *http.Request) {
	user, ok := s.auth.CurrentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	cfg := notifyConfigFromForm(r)
	if err := cfg.Validate(); err != nil {
		s.auth.Flash(w, r, "notify_msg", "Could not save: "+err.Error())
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if err := s.store.SaveNotifyConfig(r.Context(), user.Sub, cfg); err != nil {
		http.Error(w, "cannot save notification settings", http.StatusInternalServerError)
		return
	}
	s.auth.Flash(w, r, "notify_msg", "Notification settings saved.")
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleNotifyReset drops the user's saved settings so they fall back to the
// server-wide env defaults.
func (s *Server) handleNotifyReset(w http.ResponseWriter, r *http.Request) {
	user, ok := s.auth.CurrentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := s.store.ClearNotifyConfig(r.Context(), user.Sub); err != nil {
		http.Error(w, "cannot reset notification settings", http.StatusInternalServerError)
		return
	}
	s.auth.Flash(w, r, "notify_msg", "Reverted to server defaults.")
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleNotifyTest sends a one-off test notification over the user's effective
// config so they can confirm the channel works.
func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	user, ok := s.auth.CurrentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	cfg, _ := s.effectiveNotify(r.Context(), user.Sub)
	switch {
	case !cfg.Enabled():
		s.auth.Flash(w, r, "notify_msg", "No channel configured — nothing to test.")
	default:
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := notify.Send(ctx, nil, cfg, notify.TestMessage()); err != nil {
			s.auth.Flash(w, r, "notify_msg", "Test failed: "+err.Error())
		} else {
			s.auth.Flash(w, r, "notify_msg", "Test notification sent.")
		}
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// Run loads configuration, opens storage, wires the server, and serves on the
// fixed port until the process is stopped. With noAuth set, OIDC is skipped
// entirely (for testing): the dashboard needs no login and ingest needs no
// token — see the --noauth flag.
func Run(noAuth bool) error {
	cfg := config.Load()
	cfg.LogConfig()

	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	var a *auth.Auth
	if noAuth {
		log.Printf("webprogress: --noauth set; authentication is DISABLED — do not use in production")
		// Record the local user so its tokens and settings persist like any other.
		if err := store.UpsertUser(context.Background(), auth.NoAuthSub, "local@localhost", "Local (no auth)"); err != nil {
			return err
		}
		a = auth.NewNoAuth(cfg, store)
	} else {
		if err := cfg.RequireOIDC(); err != nil {
			return err
		}
		a, err = auth.New(context.Background(), cfg, store)
		if err != nil {
			return err
		}
	}

	hub := bus.New()
	srv, err := New(cfg, store, hub, a)
	if err != nil {
		return err
	}

	// The notification dispatcher watches the same bus as the dashboard and
	// delivers completion/stall alerts over each user's effective channel.
	dispatcher := notify.NewDispatcher(func(ctx context.Context, sub string) notify.Config {
		cfg, _ := srv.effectiveNotify(ctx, sub)
		return cfg
	})
	go dispatcher.Run(context.Background(), hub)

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
