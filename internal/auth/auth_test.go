package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ydethe/webprogress/internal/config"
)

func TestNoAuthCurrentUserAlwaysSignedIn(t *testing.T) {
	a := NewNoAuth(&config.Settings{SessionSecret: "test"}, nil)
	if !a.NoAuth() {
		t.Fatal("NoAuth() should report true")
	}
	u, ok := a.CurrentUser(httptest.NewRequest("GET", "/", nil))
	if !ok || u.Sub != NoAuthSub {
		t.Fatalf("CurrentUser = %+v, %v; want the local no-auth user", u, ok)
	}
}

func TestNoAuthMiddlewarePassesThrough(t *testing.T) {
	a := NewNoAuth(&config.Settings{SessionSecret: "test"}, nil)
	reached := false
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	// A protected path that would otherwise redirect to /login.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !reached || rec.Code != http.StatusOK {
		t.Fatalf("protected request not served: reached=%v code=%d", reached, rec.Code)
	}
}

func TestNilAuthEnforcesByDefault(t *testing.T) {
	var a *Auth
	if a.NoAuth() {
		t.Fatal("a nil Auth must not report no-auth")
	}
}
