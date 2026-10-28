package detect

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/nbullorini/laranest/internal/dotenv"
	"github.com/nbullorini/laranest/internal/wsl"
)

type Level string

const (
	LevelError   Level = "error"   // the app will not work
	LevelWarning Level = "warning" // some feature will not work
	LevelInfo    Level = "info"
)

// Warning is something the user should fix. Fix, when present, is a set of
// .env changes the panel can apply after the user confirms the diff.
type Warning struct {
	Level   Level  `json:"level"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	// Command is a laranest command that fixes it, without "laranest" (e.g.
	// "npm install"): the panel offers to run it.
	Command string            `json:"command,omitempty"`
	Fix     map[string]string `json:"fix,omitempty"`
	// Current holds the existing .env values of the keys in Fix, to show a diff.
	Current map[string]string `json:"current,omitempty"`
}

// Context is how Laranest is going to run the app, which some checks compare against.
type Context struct {
	URL       string    // http://miapp.localhost
	Processes Processes // enabled processes
	PHPError  string    // non-empty if no suitable PHP runtime was found
	// PHPDownload is the version Laranest will download when the app starts.
	PHPDownload string
	// PHPVersion/PHPSource describe the runtime in use; Modules are its
	// extensions (nil = unknown, skip the extension check).
	PHPVersion string
	PHPSource  string
	Modules    map[string]bool
	// Running: the app is up, so CheckServices also requests its home page.
	Running      bool
	ReverbHost   string // public hostname proxied to Reverb
	ReverbPort   int    // public port of that hostname
	ReverbScheme string // http or https
}

// Check runs the live checks (env vars, reachable services, filesystem).
func Check(d *Detection, ctx Context) []Warning {
	if !d.IsLaravel {
		return []Warning{{Level: LevelError, Code: "not_laravel", Message: ErrNotLaravel.Error()}}
	}

	ws := []Warning{} // non-nil: the API must send [] rather than null
	add := func(w Warning) { ws = append(ws, w) }

	if ctx.PHPError != "" {
		add(Warning{Level: LevelError, Code: "php", Message: ctx.PHPError,
			Hint: "Pick another version in the app settings, or install it from the PHP view"})
	}
	if ctx.PHPDownload != "" {
		add(Warning{Level: LevelInfo, Code: "php_download",
			Message: "PHP " + ctx.PHPDownload + " will be downloaded automatically when the app starts (~70 MB, no sudo)"})
	}
	ws = append(ws, checkExtensions(d, ctx)...)
	if !exists(filepath.Join(d.Path, "vendor", "autoload.php")) {
		add(Warning{Level: LevelError, Code: "vendor", Message: "Composer dependencies are missing", Hint: "laranest composer install", Command: "composer install"})
	}

	ws = append(ws, checkConfigCache(d)...)

	envPath := filepath.Join(d.Path, ".env")
	env, err := dotenv.Parse(envPath)
	if err != nil {
		hint := "laranest php artisan key:generate"
		if exists(filepath.Join(d.Path, ".env.example")) {
			hint = "cp .env.example .env && laranest php artisan key:generate"
		}
		add(Warning{Level: LevelError, Code: "env_missing", Message: "There is no .env file", Hint: hint})
		return ws
	}

	// The fix carries a ready key: `key:generate` refuses when the .env has
	// no APP_KEY line at all (some .env.example files omit it).
	if env["APP_KEY"] == "" {
		add(Warning{Level: LevelError, Code: "app_key", Message: "APP_KEY is empty", Hint: "laranest php artisan key:generate",
			Fix: map[string]string{"APP_KEY": newAppKey(env["APP_CIPHER"])}, Current: currentValues(env, "APP_KEY")})
	} else if msg := invalidAppKey(env["APP_KEY"], env["APP_CIPHER"]); msg != "" {
		add(Warning{Level: LevelError, Code: "app_key", Message: msg, Hint: "laranest php artisan key:generate --force",
			Fix: map[string]string{"APP_KEY": newAppKey(env["APP_CIPHER"])}, Current: currentValues(env, "APP_KEY")})
	}
	if ctx.URL != "" && strings.TrimRight(env["APP_URL"], "/") != ctx.URL {
		add(Warning{Level: LevelWarning, Code: "app_url",
			Message: fmt.Sprintf("APP_URL is %q but the app is served at %s", env["APP_URL"], ctx.URL),
			Hint:    "It affects generated URLs, redirects and assets.",
			Fix:     map[string]string{"APP_URL": ctx.URL},
			Current: currentValues(env, "APP_URL")})
	}
	ws = append(ws, checkWritable(d.Path)...)
	ws = append(ws, checkWindowsDisk(d.Path, wsl.IsWSL())...)

	ws = append(ws, checkReverb(d, env, ctx)...)
	ws = append(ws, checkFrontend(d, ctx)...)
	return ws
}

// writableDirs are where Laravel writes while serving requests and running
// commands. Checking only storage/ misses the usual case of a project once
// served by Apache: storage/ is yours, but logs/ and framework/* belong to
// www-data.
var writableDirs = []string{
	"storage", "storage/logs", "storage/framework", "storage/framework/cache",
	"storage/framework/sessions", "storage/framework/views", "bootstrap/cache",
}

// checkWritable reports the folders Laravel must write to that your user
// can't. Laranest runs everything as your user, so fixing it is the user's
// call (it may need sudo or an administrator), never something it does.
func checkWritable(path string) []Warning {
	var bad []string
	for _, rel := range writableDirs {
		dir := filepath.Join(path, filepath.FromSlash(rel))
		if info, err := os.Stat(dir); err == nil && info.IsDir() && !writable(dir) {
			bad = append(bad, rel)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	hint := "Make them yours: sudo chown -R $USER storage bootstrap/cache (in the project)"
	if runtime.GOOS == "windows" {
		hint = `Give your user write access: icacls storage bootstrap\cache /grant "%USERNAME%":(OI)(CI)M /T ` +
			"(in the project; from an administrator terminal if the folder belongs to someone else), " +
			"move the project into a folder of yours, or allow PHP in Windows Security → Controlled folder access"
	}
	return []Warning{{Level: LevelError, Code: "storage",
		Message: "Laravel can't write to " + strings.Join(bad, ", ") + ": these folders are not writable by your user",
		Hint:    hint}}
}

// checkWindowsDisk warns about projects on a Windows drive seen from WSL
// (/mnt/c, /mnt/d…): every file access crosses the WSL/Windows boundary, and
// Vite's file watcher gets no change events there.
func checkWindowsDisk(path string, isWSL bool) []Warning {
	if !isWSL || !strings.HasPrefix(path, "/mnt/") {
		return nil
	}
	return []Warning{{Level: LevelWarning, Code: "wsl_windows_disk",
		Message: "The project is on a Windows drive (" + path + "): from WSL it is very slow, and Vite doesn't notice file changes",
		Hint: "Move it into Linux (e.g. ~/projects), or run it with Laranest for Windows; " +
			"for Vite, set server.watch.usePolling: true in vite.config"}}
}

// checkConfigCache looks at `php artisan config:cache`'s output. Built in
// another folder (a copied project, another machine) it points storage, logs,
// views and links there; in any case Laravel stops reading .env while it exists.
func checkConfigCache(d *Detection) []Warning {
	data, err := os.ReadFile(filepath.Join(d.Path, "bootstrap", "cache", "config.php"))
	if err != nil {
		return nil
	}
	cached := string(data)
	here := d.Path
	if runtime.GOOS == "windows" {
		cached, here = strings.ToLower(cached), strings.ToLower(here) // paths are case-insensitive
	}
	if strings.Contains(cached, "storage") && !strings.Contains(cached, here) &&
		!strings.Contains(cached, strings.ReplaceAll(here, `\`, `\\`)) { // var_export escapes backslashes
		return []Warning{{Level: LevelError, Code: "config_cache_stale",
			Message: "The cached configuration (bootstrap/cache/config.php) was built in another folder: " +
				"storage, logs and links point there, and .env is ignored",
			Hint: "laranest php artisan optimize:clear", Command: "php artisan optimize:clear"}}
	}
	return []Warning{{Level: LevelInfo, Code: "config_cache",
		Message: "The configuration is cached: changes to .env are ignored until it is cleared",
		Hint:    "laranest php artisan config:clear", Command: "php artisan config:clear"}}
}

func checkExtensions(d *Detection, ctx Context) []Warning {
	if ctx.Modules == nil {
		return nil
	}
	required := RequiredExtensions(d.Path)
	exts := make([]string, 0, len(required))
	for ext := range required {
		if !ctx.Modules[ext] {
			exts = append(exts, ext)
		}
	}
	sort.Strings(exts)

	var ws []Warning
	for _, ext := range exts {
		pkgs := required[ext]
		sort.Strings(pkgs)
		level := LevelError
		hint := "Laranest's PHP does not include it: build it with pecl and add it to its php.ini " +
			"(laranest which-php shows which one)"
		switch {
		case ctx.PHPSource == "system":
			hint = "Install it with: sudo apt install php" + ctx.PHPVersion + "-" + ext
		case runtime.GOOS == "windows" && (ext == "pcntl" || ext == "posix"):
			// Unix-only: the app runs, but Composer refuses to install without it.
			level = LevelWarning
			hint = "It doesn't exist on Windows. Install with `laranest composer install --ignore-platform-req=ext-pcntl " +
				"--ignore-platform-req=ext-posix`; Horizon can't run on Windows, so Laranest runs queue:work instead " +
				"(or use Laranest inside WSL)"
		case runtime.GOOS == "windows":
			hint = "PHP for Windows doesn't ship it: put its DLL (from PECL) in the ext folder of Laranest's PHP and " +
				"add extension=" + ext + " to its php.ini (laranest which-php shows where)"
		}
		ws = append(ws, Warning{Level: level, Code: "ext_" + ext,
			Message: fmt.Sprintf("PHP %s lacks the %s extension (required by %s)", ctx.PHPVersion, ext, strings.Join(pkgs, ", ")),
			Hint:    hint})
	}
	return ws
}

func usesRedis(env map[string]string, ctx Context) bool {
	if ctx.Processes.Horizon {
		return true
	}
	for _, k := range []string{"QUEUE_CONNECTION", "CACHE_STORE", "CACHE_DRIVER", "SESSION_DRIVER", "BROADCAST_CONNECTION"} {
		if env[k] == "redis" {
			return true
		}
	}
	return false
}

func checkReverb(d *Detection, env map[string]string, ctx Context) []Warning {
	if !ctx.Processes.Reverb {
		return nil
	}
	var ws []Warning
	for _, k := range []string{"REVERB_APP_ID", "REVERB_APP_KEY", "REVERB_APP_SECRET"} {
		if env[k] == "" {
			ws = append(ws, Warning{Level: LevelError, Code: "reverb_env", Message: k + " is not set", Hint: "laranest php artisan reverb:install"})
			return ws
		}
	}
	want := map[string]string{
		"REVERB_HOST":   ctx.ReverbHost,
		"REVERB_PORT":   fmt.Sprint(ctx.ReverbPort),
		"REVERB_SCHEME": firstNonEmpty(ctx.ReverbScheme, "http"),
	}
	fix := map[string]string{}
	var keys []string
	for k, v := range want {
		if env[k] != v {
			fix[k] = v
			keys = append(keys, k)
		}
	}
	if len(fix) > 0 {
		ws = append(ws, Warning{Level: LevelWarning, Code: "reverb_host",
			Message: "The REVERB_* variables do not point to " + ctx.ReverbHost,
			Hint:    "If the frontend uses VITE_REVERB_*, rebuild the assets after applying the change.",
			Fix:     fix,
			Current: currentValues(env, keys...)})
	}
	return ws
}

func checkFrontend(d *Detection, ctx Context) []Warning {
	if !d.UsesVite {
		return nil
	}
	if !exists(filepath.Join(d.Path, "node_modules")) {
		return []Warning{{Level: LevelWarning, Code: "node_modules", Message: "npm dependencies are missing", Hint: "laranest npm install", Command: "npm install"}}
	}
	if !ctx.Processes.Vite && !exists(filepath.Join(d.Path, "public", "build", "manifest.json")) && !exists(filepath.Join(d.Path, "public", "hot")) {
		return []Warning{{Level: LevelWarning, Code: "assets", Message: "The Vite assets are not built",
			Hint: "laranest npm run build, or enable the Vite process (dev server)", Command: "npm run build"}}
	}
	if w := checkViteDevServer(d, ctx.URL); w != nil {
		return []Warning{*w}
	}
	return nil
}

// checkViteDevServer probes the dev server named in public/hot the way the
// browser will: a request from the app's origin. It catches a stale hot file
// (server gone) and CORS configs that reject the app's hostname.
func checkViteDevServer(d *Detection, appURL string) *Warning {
	data, err := os.ReadFile(filepath.Join(d.Path, "public", "hot"))
	if err != nil || appURL == "" {
		return nil
	}
	devURL := strings.TrimSpace(string(data))

	req, err := http.NewRequest(http.MethodGet, devURL+"/@vite/client", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Origin", appURL)
	client := http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return &Warning{Level: LevelError, Code: "vite_hot_stale",
			Message: "public/hot exists but the Vite dev server (" + devURL + ") is not responding: the app will not load its JS/CSS",
			Hint:    "Enable the Vite process, or delete public/hot to use the built assets (laranest npm run build)"}
	}
	resp.Body.Close()

	if allowed := resp.Header.Get("Access-Control-Allow-Origin"); allowed == "*" || allowed == appURL {
		return nil
	}
	hint := "Add " + appURL + " to server.cors.origin in vite.config.js"
	if cfg := viteConfig(d.Path); strings.Contains(cfg, "cors") {
		hint = "vite.config.js overrides server.cors.origin without *.localhost. " +
			"Use Vite's default: import { defineConfig, defaultAllowedOrigins } from 'vite' " +
			"and origin: [defaultAllowedOrigins, …your other origins]"
	}
	return &Warning{Level: LevelError, Code: "vite_cors",
		Message: "The Vite dev server (" + devURL + ") rejects the origin " + appURL + " (CORS): the browser will not load the JS/CSS",
		Hint:    hint}
}

func viteConfig(root string) string {
	for _, name := range []string{"vite.config.js", "vite.config.ts", "vite.config.mjs", "vite.config.mts"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			return string(data)
		}
	}
	return ""
}

// newAppKey generates a key like `php artisan key:generate` does.
func newAppKey(cipher string) string {
	key := make([]byte, 32)
	if strings.HasPrefix(strings.ToLower(cipher), "aes-128") {
		key = key[:16]
	}
	_, _ = rand.Read(key)
	return "base64:" + base64.StdEncoding.EncodeToString(key)
}

// invalidAppKey mirrors Laravel's Encrypter: the key must be "base64:" +
// 32 bytes (16 for the aes-128 ciphers), or the raw bytes themselves.
func invalidAppKey(key, cipher string) string {
	want := 32
	if strings.HasPrefix(strings.ToLower(cipher), "aes-128") {
		want = 16
	}
	raw := []byte(key)
	if b64, ok := strings.CutPrefix(key, "base64:"); ok {
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return "APP_KEY is not valid base64"
		}
		raw = decoded
	}
	if len(raw) != want {
		return fmt.Sprintf("APP_KEY is not a valid key (%d bytes; %s needs %d): Laravel will fail with \"Unsupported cipher or incorrect key length\"",
			len(raw), firstNonEmpty(cipher, "aes-256-cbc"), want)
	}
	return ""
}

// currentValues picks the keys that exist in env (absent keys are new lines in the diff).
func currentValues(env map[string]string, keys ...string) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := env[k]; ok {
			out[k] = v
		}
	}
	return out
}

func loadEnv(root string) map[string]string {
	env, err := dotenv.Parse(filepath.Join(root, ".env"))
	if err != nil {
		return map[string]string{}
	}
	return env
}

func reachable(host, port string) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 700*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".laranest-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}
