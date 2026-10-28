package detect

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A config cache built elsewhere (a copied project) points storage:link,
// logs and views at the other folder; one built here only hides .env edits.
func TestConfigCache(t *testing.T) {
	root := t.TempDir()
	if ws := checkConfigCache(&Detection{Path: root}); ws != nil {
		t.Fatalf("no cache: got %+v", ws)
	}
	cache := filepath.Join(root, "bootstrap", "cache", "config.php")
	os.MkdirAll(filepath.Dir(cache), 0o755)

	quoted := strings.ReplaceAll(filepath.Join(root, "storage", "app", "public"), `\`, `\\`) // as var_export writes it
	os.WriteFile(cache, []byte(`<?php return array('filesystems' => array('root' => '`+quoted+`'));`), 0o644)
	if ws := checkConfigCache(&Detection{Path: root}); len(ws) != 1 || ws[0].Code != "config_cache" {
		t.Errorf("cache built here: got %+v", ws)
	}

	os.WriteFile(cache, []byte(`<?php return array('filesystems' => array('root' => 'E:\\dev\\other-app\\storage\\app/public'));`), 0o644)
	ws := checkConfigCache(&Detection{Path: root})
	if len(ws) != 1 || ws[0].Code != "config_cache_stale" || ws[0].Command != "php artisan optimize:clear" {
		t.Errorf("cache from another folder: got %+v", ws)
	}
}

func TestVitePort(t *testing.T) {
	root := t.TempDir()
	if port, strict := VitePort(root); port != 0 || strict {
		t.Errorf("no config: %d %v", port, strict)
	}
	os.WriteFile(filepath.Join(root, "vite.config.ts"), []byte(`export default defineConfig({
  server: { host: 'localhost', port: 5199, strictPort: true },
  plugins: [laravel({ input: ['resources/js/app.ts'] })],
})`), 0o644)
	if port, strict := VitePort(root); port != 5199 || !strict {
		t.Errorf("pinned port: got %d %v", port, strict)
	}
}

// A project once served by Apache: storage/ is yours, but logs/ isn't.
func TestCheckWritableSubfolders(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions and a non-root user")
	}
	root := t.TempDir()
	for _, d := range []string{"storage/logs", "storage/framework/views", "bootstrap/cache"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	if ws := checkWritable(root); ws != nil {
		t.Fatalf("all writable: got %+v", ws)
	}
	logs := filepath.Join(root, "storage", "logs")
	os.Chmod(logs, 0o555)
	defer os.Chmod(logs, 0o755)
	ws := checkWritable(root)
	if len(ws) != 1 || !strings.Contains(ws[0].Message, "storage/logs") || strings.Contains(ws[0].Message, "bootstrap/cache") {
		t.Errorf("read-only storage/logs: got %+v", ws)
	}
}

func TestCheckWindowsDisk(t *testing.T) {
	if ws := checkWindowsDisk("/mnt/d/projects/app", true); len(ws) != 1 || ws[0].Code != "wsl_windows_disk" {
		t.Errorf("WSL on /mnt/d: got %+v", ws)
	}
	if ws := checkWindowsDisk("/home/me/app", true); ws != nil {
		t.Errorf("WSL in Linux: got %+v", ws)
	}
	if ws := checkWindowsDisk("/mnt/d/projects/app", false); ws != nil {
		t.Errorf("not WSL: got %+v", ws)
	}
}
