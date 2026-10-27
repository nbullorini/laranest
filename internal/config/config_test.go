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
