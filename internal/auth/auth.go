// Package auth handles web-UI authentication: it delegates sign-in to an external
// OIDC provider, manages the signed session cookie, and guards protected pages.
// Update ingestion (POST /handler) authenticates by token instead and bypasses
// this layer entirely.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/sessions"
	"golang.org/x/oauth2"

	"github.com/ydethe/webprogress/internal/config"
	"github.com/ydethe/webprogress/internal/storage"
)

const sessionName = "webprogress_session"

// unrestricted paths bypass the session guard. The ingest and health endpoints
// self-authenticate or are intentionally public; the login round-trip routes
// must be reachable while signed out.
var unrestricted = map[string]bool{
	"/login":   true,
	"/auth":    true,
	"/logout":  true,
	"/handler": true,
	"/health":  true,
}

// User is the identity stored in the session after a successful sign-in.
type User struct {
	Sub   string
	Email string
	Name  string
}

// Auth bundles the OIDC client, session store, and user persistence.
type Auth struct {
	cfg      *config.Settings
	store    *storage.Store
	cookies  *sessions.CookieStore
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// New builds an Auth by discovering the OIDC provider from its metadata URL.
func New(ctx context.Context, cfg *config.Settings, store *storage.Store) (*Auth, error) {
	issuer := strings.TrimSuffix(cfg.OIDCServerMetadataURL, "/.well-known/openid-configuration")
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}

	cookies := sessions.NewCookieStore([]byte(cfg.SessionSecret))
	cookies.Options = &sessions.Options{
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 60 * 60, // one week
	}

	return &Auth{
		cfg:     cfg,
		store:   store,
		cookies: cookies,
		oauth: oauth2.Config{
			ClientID:     cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURI(),
			Scopes:       cfg.Scopes(),
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID}),
	}, nil
}

// Login begins sign-in: it stashes a fresh state and nonce in the session and
// redirects the browser to the provider's authorization endpoint.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	session, _ := a.cookies.Get(r, sessionName)
	state := randomString()
	nonce := randomString()
	session.Values["oauth_state"] = state
	session.Values["oauth_nonce"] = nonce
	if err := session.Save(r, w); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, a.oauth.AuthCodeURL(state, oidc.Nonce(nonce)), http.StatusFound)
}

// Callback is the provider's redirect target. It verifies the exchange, upserts
// the user, marks the session authenticated, and returns to the dashboard.
func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session, _ := a.cookies.Get(r, sessionName)

	wantState, _ := session.Values["oauth_state"].(string)
	if wantState == "" || r.URL.Query().Get("state") != wantState {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	oauth2Token, err := a.oauth.Exchange(ctx, r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}
	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "no id_token in response", http.StatusBadGateway)
		return
	}
	idToken, err := a.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		http.Error(w, "id_token verification failed", http.StatusBadGateway)
		return
	}
	wantNonce, _ := session.Values["oauth_nonce"].(string)
	if idToken.Nonce != wantNonce {
		http.Error(w, "invalid id_token nonce", http.StatusBadRequest)
		return
	}

	var claims struct {
		Sub               string `json:"sub"`
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "cannot read id_token claims", http.StatusBadGateway)
		return
	}
	name := firstNonEmpty(claims.Name, claims.PreferredUsername, claims.Email, claims.Sub)

	if err := a.store.UpsertUser(ctx, claims.Sub, claims.Email, name); err != nil {
		http.Error(w, "cannot persist user", http.StatusInternalServerError)
		return
	}

	delete(session.Values, "oauth_state")
	delete(session.Values, "oauth_nonce")
	session.Values["authenticated"] = true
	session.Values["sub"] = claims.Sub
	session.Values["email"] = claims.Email
	session.Values["name"] = name
	if err := session.Save(r, w); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// Logout clears the local session and returns to login. It does not sign the user
// out of the provider.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	session, _ := a.cookies.Get(r, sessionName)
	session.Options.MaxAge = -1
	_ = session.Save(r, w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

// CurrentUser returns the signed-in user for a request, or (zero, false).
func (a *Auth) CurrentUser(r *http.Request) (User, bool) {
	session, _ := a.cookies.Get(r, sessionName)
	if auth, _ := session.Values["authenticated"].(bool); !auth {
		return User{}, false
	}
	sub, _ := session.Values["sub"].(string)
	email, _ := session.Values["email"].(string)
	name, _ := session.Values["name"].(string)
	return User{Sub: sub, Email: email, Name: name}, true
}

// Flash stores a one-time message in the session, surfaced once by PopFlash.
func (a *Auth) Flash(w http.ResponseWriter, r *http.Request, key, value string) {
	session, _ := a.cookies.Get(r, sessionName)
	session.AddFlash(value, key)
	_ = session.Save(r, w)
}

// PopFlash returns and clears a one-time message stored under key.
func (a *Auth) PopFlash(w http.ResponseWriter, r *http.Request, key string) string {
	session, _ := a.cookies.Get(r, sessionName)
	flashes := session.Flashes(key)
	if len(flashes) == 0 {
		return ""
	}
	_ = session.Save(r, w)
	if v, ok := flashes[0].(string); ok {
		return v
	}
	return ""
}

// Middleware redirects unauthenticated requests for protected paths to /login.
// Unrestricted routes and static assets are always allowed.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.CurrentUser(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		if unrestricted[r.URL.Path] || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	})
}

func randomString() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failures are not recoverable; surface loudly rather than
		// issuing a predictable value.
		log.Fatalf("crypto/rand failed: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
