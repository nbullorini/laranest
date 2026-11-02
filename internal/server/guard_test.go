package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nbullorini/laranest/internal/config"
)

func TestGuardToken(t *testing.T) {
	h := guard(config.Config{Domain: "localhost"}, "s3cret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	cases := []struct {
		name, method, target, auth string
		want                       int
	}{
		{"api without token", "GET", "/api/apps", "", http.StatusUnauthorized},
		{"api with wrong token", "POST", "/api/apps", "Bearer nope", http.StatusUnauthorized},
		{"api with token", "POST", "/api/apps", "Bearer s3cret", http.StatusTeapot},
		{"token in the query (EventSource)", "GET", "/api/apps/x/logs/web?token=s3cret", "", http.StatusTeapot},
		{"health stays open", "GET", "/api/health", "", http.StatusTeapot},
		{"the UI stays open", "GET", "/", "", http.StatusTeapot},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "http://localhost:9150"+c.target, nil)
		if c.auth != "" {
			r.Header.Set("Authorization", c.auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, w.Code, c.want)
		}
	}
	// An empty token (unreadable file) must never open the API.
	open := guard(config.Config{Domain: "localhost"}, "", http.NotFoundHandler())
	w := httptest.NewRecorder()
	open.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost:9150/api/apps", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("empty token: got %d", w.Code)
	}
}
