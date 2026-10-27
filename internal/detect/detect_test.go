package detect

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func laravelProject(t *testing.T, env string, extraRequire string) string {
	return writeFiles(t, map[string]string{
		"artisan":                  "",
		"composer.json":            `{"require":{"php":"^8.2","laravel/framework":"^12.0"` + extraRequire + `}}`,
		"composer.lock":            `{"packages":[{"name":"laravel/framework","version":"v12.3.0"}]}`,
		".env":                     env,
		"vendor/autoload.php":      "",
		"storage/.keep":            "",
		"public/index.php":         "",
		"bootstrap/cache/.keep":    "",
		"database/database.sqlite": "",
	})
}

func TestInspectNotLaravel(t *testing.T) {
	root := writeFiles(t, map[string]string{"composer.json": `{"require":{}}`})
	if _, err := Inspect(root, nil); err != ErrNotLaravel {
		t.Fatalf("got %v, want ErrNotLaravel", err)
	}
}

func TestInspectRecommendations(t *testing.T) {
	root := laravelProject(t, "QUEUE_CONNECTION=redis\nBROADCAST_CONNECTION=reverb\n",
		`,"laravel/horizon":"^5","laravel/reverb":"^1"`)
	d, err := Inspect(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.LaravelVersion != "12.3.0" || d.PHPConstraint != "^8.2" {
		t.Errorf("versions: %q %q", d.LaravelVersion, d.PHPConstraint)
	}
	want := Processes{Horizon: true, Reverb: true}
	if runtime.GOOS == "windows" {
		want = Processes{Queue: true, Reverb: true} // Horizon needs pcntl
	}
	if d.Recommended != want {
		t.Errorf("recommended = %+v, want %+v", d.Recommended, want)
	}
}

func codes(ws []Warning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Code)
	}
	return out
}

func TestCheck(t *testing.T) {
	root := laravelProject(t, "APP_KEY=\nAPP_URL=http://localhost\nDB_CONNECTION=sqlite\nMAIL_MAILER=log\n", "")
	d, _ := Inspect(root, nil)
	ws := Check(d, Context{URL: "http://demo.localhost"})

	got := codes(ws)
	for _, c := range []string{"app_key", "app_url"} {
		if !slices.Contains(got, c) {
			t.Errorf("missing warning %q in %v", c, got)
		}
	}
	for _, w := range ws {
		if w.Code == "app_url" && (w.Fix["APP_URL"] != "http://demo.localhost" || w.Current["APP_URL"] != "http://localhost") {
			t.Errorf("app_url fix/current = %v / %v", w.Fix, w.Current)
		}
	}
}

func TestCheckServiceDown(t *testing.T) {
	// Port 1 on loopback is never listening.
	root := laravelProject(t, "APP_KEY=base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nDB_CONNECTION=mysql\nDB_DATABASE=app\nDB_HOST=127.0.0.1\nDB_PORT=1\n", "")
	d, _ := Inspect(root, nil)
	if got := codes(CheckServices(d, loadEnv(root), Context{})); !slices.Contains(got, "db_down") {
		t.Errorf("expected db_down, got %v", got)
	}
}

// The UI iterates warnings directly, so "no warnings" must serialize as [] (not null).
func TestCheckNoWarningsIsEmptySlice(t *testing.T) {
	root := laravelProject(t, "APP_KEY=base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nAPP_URL=http://demo.localhost\nDB_CONNECTION=sqlite\n", "")
	d, _ := Inspect(root, nil)
	ws := Check(d, Context{URL: "http://demo.localhost"})
	if ws == nil || len(ws) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v", ws)
	}
}

func TestCheckViteDevServer(t *testing.T) {
	const appURL = "http://demo.localhost"
	// Mimics a Vite server whose cors.origin only allows plain localhost.
	vite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "http://localhost" {
			w.Header().Set("Access-Control-Allow-Origin", "http://localhost")
		}
	}))
	defer vite.Close()

	root := laravelProject(t, "APP_KEY=base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n", "")
	os.WriteFile(filepath.Join(root, "vite.config.js"), []byte("server: { cors: { origin: [/localhost/] } }"), 0o644)
	os.WriteFile(filepath.Join(root, "public", "hot"), []byte(vite.URL), 0o644)
	d := &Detection{Path: root}

	if w := checkViteDevServer(d, appURL); w == nil || w.Code != "vite_cors" {
		t.Fatalf("want vite_cors, got %+v", w)
	}
	if w := checkViteDevServer(d, "http://localhost"); w != nil {
		t.Fatalf("allowed origin should pass, got %+v", w)
	}
	vite.Close()
	if w := checkViteDevServer(d, appURL); w == nil || w.Code != "vite_hot_stale" {
		t.Fatalf("want vite_hot_stale, got %+v", w)
	}
}

func TestPHPRequirement(t *testing.T) {
	for v, want := range map[string]string{"8.83.1": "8.1", "^10.0": "8.3", "v5.5.40": "7.4", "13.38.0": "", "": ""} {
		if got := MaxPHPForLaravel(v); got != want {
			t.Errorf("MaxPHPForLaravel(%q) = %q, want %q", v, got, want)
		}
	}
	root := writeFiles(t, map[string]string{
		"artisan":       "",
		"composer.json": `{"require":{"php":"^7.4 || ^8.0","laravel/framework":"^8.0"}}`,
		"composer.lock": `{"packages":[{"name":"laravel/framework","version":"v8.83.1","require":{"php":"^7.3|^8.0"}},
			{"name":"old/lib","version":"1.0","require":{"php":">=7.2 <8.2"}}],
			"packages-dev":[{"name":"dev/only","version":"1.0","require":{"php":"<7.0"}}]}`,
	})
	d, err := Inspect(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.PHPMax != "8.1" || !slices.Equal(d.LockPHP, []string{">=7.2 <8.2", "^7.3|^8.0"}) {
		t.Errorf("PHPMax=%q LockPHP=%v", d.PHPMax, d.LockPHP)
	}
}
