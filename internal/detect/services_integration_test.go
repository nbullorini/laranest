//go:build integration

// Real-service checks. Start the services first (see test/e2e/services.sh):
//
//	go test -tags integration ./internal/detect -run Integration -v
package detect

import (
	"slices"
	"strings"
	"testing"
)

func TestIntegrationServices(t *testing.T) {
	root := laravelProject(t, "APP_KEY=base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n", "")
	d, _ := Inspect(root, nil)
	mods := map[string]bool{"redis": true}

	mysql := map[string]string{"DB_CONNECTION": "mysql", "DB_HOST": "127.0.0.1", "DB_PORT": "33901", "DB_DATABASE": "e2e", "DB_USERNAME": "app", "DB_PASSWORD": "apppass"}
	pg := map[string]string{"DB_CONNECTION": "pgsql", "DB_HOST": "127.0.0.1", "DB_PORT": "54901", "DB_DATABASE": "e2e", "DB_USERNAME": "app", "DB_PASSWORD": "secret"}
	with := func(base map[string]string, kv ...string) map[string]string {
		out := map[string]string{"DB_CONNECTION": "sqlite", "DB_DATABASE": ":memory:"}
		for k, v := range base {
			out[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i]] = kv[i+1]
		}
		return out
	}

	cases := []struct {
		name string
		env  map[string]string
		want []string // codes that must appear ("" = none of db_/redis_/mail_)
	}{
		{"mysql ok but unmigrated", with(mysql), []string{"db_unmigrated"}},
		{"mysql wrong password", with(mysql, "DB_PASSWORD", "nope"), []string{"db_auth"}},
		{"mysql missing database", with(mysql, "DB_USERNAME", "root", "DB_PASSWORD", "secret", "DB_DATABASE", "nope"), []string{"db_missing"}},
		{"mysql user without access", with(mysql, "DB_DATABASE", "mysql"), []string{"db_auth"}},
		{"mysql down", with(mysql, "DB_PORT", "1"), []string{"db_down"}},
		{"pgsql ok but unmigrated", with(pg), []string{"db_unmigrated"}},
		{"pgsql wrong password", with(pg, "DB_PASSWORD", "nope"), []string{"db_auth"}},
		{"pgsql missing database", with(pg, "DB_DATABASE", "nope"), []string{"db_missing"}},
		{"redis ok", with(nil, "CACHE_STORE", "redis", "REDIS_PORT", "63901", "REDIS_PASSWORD", "s3cret"), nil},
		{"redis no password", with(nil, "CACHE_STORE", "redis", "REDIS_PORT", "63901", "REDIS_PASSWORD", "null"), []string{"redis_auth"}},
		{"redis wrong password", with(nil, "CACHE_STORE", "redis", "REDIS_PORT", "63901", "REDIS_PASSWORD", "x"), []string{"redis_auth"}},
		{"redis bad db", with(nil, "QUEUE_CONNECTION", "redis", "REDIS_PORT", "63901", "REDIS_PASSWORD", "s3cret", "REDIS_DB", "99"), []string{"redis_db"}},
		{"smtp mailpit ok", with(nil, "MAIL_MAILER", "smtp", "MAIL_HOST", "127.0.0.1", "MAIL_PORT", "10925"), nil},
		{"smtp on a redis port", with(nil, "MAIL_MAILER", "smtp", "MAIL_HOST", "127.0.0.1", "MAIL_PORT", "63901"), []string{"mail_protocol"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := CheckServices(d, c.env, Context{Modules: mods, PHPVersion: "8.4"})
			got := codes(ws)
			for _, w := range c.want {
				if !slices.Contains(got, w) {
					t.Errorf("want %s, got %v", w, ws)
				}
			}
			if c.want == nil {
				for _, g := range got {
					if strings.HasPrefix(g, "db_") || strings.HasPrefix(g, "redis_") || strings.HasPrefix(g, "mail_") {
						t.Errorf("unexpected %s: %+v", g, ws)
					}
				}
			}
			for _, w := range ws {
				t.Logf("  %s: %s", w.Code, w.Message)
			}
		})
	}
}
