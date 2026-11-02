package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A config written without http_port (older versions, or by hand) must still
// get a usable port instead of the privileged default 80.
func TestLoadFillsMissingPort(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LARANEST_HOME", "") // it would win over XDG_CONFIG_HOME
	t.Setenv("XDG_CONFIG_HOME", dir)
	os.MkdirAll(filepath.Join(dir, "laranest"), 0o755)
	os.WriteFile(filepath.Join(dir, "laranest", "config.json"), []byte(`{"panel_addr":"127.0.0.1:9999"}`), 0o644)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PanelAddr != "127.0.0.1:9999" || cfg.HTTPPort == 0 || cfg.Domain != "localhost" {
		t.Errorf("unexpected config %+v", cfg)
	}
}

func TestLaranestHomeWins(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LARANEST_HOME", root)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for name, f := range map[string]func() (string, error){"config": ConfigDir, "data": DataDir, "state": StateDir} {
		if got, _ := f(); got != filepath.Join(root, name) {
			t.Errorf("%s dir = %s, want it under LARANEST_HOME", name, got)
		}
	}
}

func TestAvoidPorts(t *testing.T) {
	cfg := Config{PanelAddr: "127.0.0.1:9151", HTTPPort: 8080, HTTPSPort: 443}
	changes := cfg.AvoidPorts(map[int]bool{9150: true, 9151: true, 8080: true, 8081: true})
	if len(changes) != 2 {
		t.Errorf("changes = %q, want the panel and HTTP ports", changes)
	}
	if cfg.PanelAddr == "127.0.0.1:9150" || cfg.PanelAddr == "127.0.0.1:9151" || cfg.HTTPPort == 8080 || cfg.HTTPPort == 8081 || cfg.HTTPSPort != 443 {
		t.Errorf("unexpected config %+v", cfg)
	}

	same := Config{PanelAddr: "127.0.0.1:9160", HTTPPort: 80, HTTPSPort: 443}
	if changes := same.AvoidPorts(map[int]bool{8080: true}); len(changes) != 0 || same.PanelAddr != "127.0.0.1:9160" {
		t.Errorf("free ports must stay: %q %+v", changes, same)
	}
}
