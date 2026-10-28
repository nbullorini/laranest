package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeAPI answers like the daemon for the calls under test.
func fakeAPI(t *testing.T) *httptest.Server {
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"shop","url":"https://shop.localhost","state":"running","php_version":"8.4",
			"detection":{"laravel_version":"12.1.0"},"procs":[{"name":"web","state":"running"}],
			"warnings":[{"level":"info","code":"storage_link"},{"level":"error","code":"db_down"}]}]`))
	})
	mux.HandleFunc("POST /api/apps/shop/commands", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["line"] != "php artisan migrate" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"error":"only php, composer, npm, npx, node commands can run from the panel"}`))
			return
		}
		w.Write([]byte(`{"id":"c1","status":"running"}`))
	})
	mux.HandleFunc("GET /api/apps/shop/commands/c1", func(w http.ResponseWriter, r *http.Request) {
		if polls.Add(1) < 2 {
			w.Write([]byte(`{"id":"c1","status":"running","output":"Migrating…"}`))
			return
		}
		w.Write([]byte(`{"id":"c1","status":"ok","exit_code":0,"output":"Migrating… DONE"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func connect(t *testing.T) *mcp.ClientSession {
	api := fakeAPI(t)
	s := newServer(&client{base: api.URL + "/api", http: api.Client()})
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func text(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestTools(t *testing.T) {
	cs := connect(t)
	ctx := context.Background()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		have[tool.Name] = tool
	}
	for _, name := range []string{"list_apps", "get_app", "add_app", "update_app", "remove_app", "start_app",
		"stop_app", "run_command", "get_logs", "fix_warning", "set_env", "find_projects", "inspect_project"} {
		if have[name] == nil {
			t.Errorf("missing tool %s", name)
		}
	}
	if a := have["list_apps"].Annotations; a == nil || !a.ReadOnlyHint {
		t.Error("list_apps must be read-only")
	}
	if a := have["remove_app"].Annotations; a == nil || a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Error("remove_app must be marked destructive")
	}
}

func TestListAppsSummary(t *testing.T) {
	cs := connect(t)
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_apps"})
	if err != nil {
		t.Fatal(err)
	}
	var apps []map[string]any
	if err := json.Unmarshal([]byte(text(t, r)), &apps); err != nil {
		t.Fatal(err)
	}
	a := apps[0]
	if a["id"] != "shop" || a["laravel"] != "12.1.0" || a["warnings"] != float64(1) {
		t.Errorf("summary = %v (info warnings don't count)", a)
	}
}

func TestRunCommandWaits(t *testing.T) {
	cs := connect(t)
	ctx := context.Background()
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "run_command",
		Arguments: map[string]any{"id": "shop", "command": "php artisan migrate"}})
	if err != nil {
		t.Fatal(err)
	}
	if out := text(t, r); r.IsError || !strings.Contains(out, `"status": "ok"`) || !strings.Contains(out, "DONE") {
		t.Errorf("want the finished command, got %s", out)
	}
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "run_command",
		Arguments: map[string]any{"id": "shop", "command": "rm -rf /"}})
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError || !strings.Contains(text(t, r), "only php") {
		t.Errorf("want the API's refusal as a tool error, got %s", text(t, r))
	}
}
