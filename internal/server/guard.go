package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/nbullorini/laranest/internal/config"
)

// guard rejects requests that could come from a malicious web page: the panel
// runs commands on the host, so it must only answer to its own hostnames
// (anti DNS-rebinding) and only accept same-origin mutations (anti CSRF).
// The API also needs the owner's token: loopback is reachable by every user
// of the machine, and the panel runs commands as its owner. The UI's files
// and /api/health (version only) stay open, so the page can ask for the token.
func guard(cfg config.Config, token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHost(cfg, r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if !isSafeMethod(r.Method) && !sameOrigin(r) {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/health" && !validToken(r, token) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or wrong Laranest token: open the panel with `laranest open`"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// validToken accepts "Authorization: Bearer <token>", or a token query
// parameter for what can't send headers (the browser's EventSource).
func validToken(r *http.Request, token string) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		got = r.URL.Query().Get("token")
	}
	return token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func allowedHost(cfg config.Config, hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))

	switch host {
	case "localhost", "127.0.0.1", "::1", "laranest." + cfg.Domain:
		return true
	}
	return false
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin requires the Origin header (sent by browsers on mutating requests)
// to match the Host. Requests without Origin come from non-browser clients
// such as the CLI or curl, which DNS rebinding and CSRF cannot reach.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "same-origin"
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
