// Package config resolves Laranest's on-disk locations and persisted settings.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
)

const DefaultPanelPort = 9150

// Config is the daemon's persisted settings (~/.config/laranest/config.json).
type Config struct {
	// PanelAddr is where the panel UI/API listens. Always loopback.
	PanelAddr string `json:"panel_addr"`
	// HTTPPort / HTTPSPort are where the app proxy listens.
	HTTPPort  int `json:"http_port"`
	HTTPSPort int `json:"https_port"`
	// Domain is the suffix used for app hostnames, e.g. "localhost" → miapp.localhost.
	Domain string `json:"domain"`
	// ScanDirs are where the panel looks for Laravel projects to suggest. Empty = home.
	ScanDirs []string `json:"scan_dirs"`
	// DefaultPHP is the version `laranest php` uses outside registered apps. Empty = newest installed.
	DefaultPHP string `json:"default_php"`
	// PHPManifestURL lists Laranest's own PHP builds. Empty = the built-in URL.
	PHPManifestURL string `json:"php_manifest_url,omitempty"`
	// UseSystemPHP also uses apt's PHP (advanced: for extensions our builds
	// lack). Off by default so Laranest never depends on what's installed.
	UseSystemPHP bool `json:"use_system_php,omitempty"`
	// WSL: distro name and Windows profile dir (/mnt/c/Users/x), captured by
	// `laranest install` because services can't call Windows programs.
	WSLDistro      string `json:"wsl_distro,omitempty"`
	WindowsProfile string `json:"windows_profile,omitempty"`
}

func Default() Config {
	return Config{
		PanelAddr: fmt.Sprintf("127.0.0.1:%d", DefaultPanelPort),
		HTTPPort:  80,
		Domain:    "localhost",
	}
}

// ConfigDir returns $XDG_CONFIG_HOME/laranest (default ~/.config/laranest;
// %LOCALAPPDATA%\Laranest\config on Windows).
func ConfigDir() (string, error) {
	return dir("config", "XDG_CONFIG_HOME", ".config")
}

// DataDir returns $XDG_DATA_HOME/laranest (default ~/.local/share/laranest),
// where downloaded runtimes (PHP, Composer, Node) live.
func DataDir() (string, error) {
	return dir("data", "XDG_DATA_HOME", filepath.Join(".local", "share"))
}

// StateDir returns $XDG_STATE_HOME/laranest (default ~/.local/state/laranest).
func StateDir() (string, error) {
	return dir("state", "XDG_STATE_HOME", filepath.Join(".local", "state"))
}

// dir resolves one of Laranest's directories. LARANEST_HOME (one root for
// all three, used by tests and portable setups) wins over the XDG variable,
// which wins over the platform default.
func dir(kind, xdgVar, homeRel string) (string, error) {
	if root := os.Getenv("LARANEST_HOME"); root != "" {
		return filepath.Join(root, kind), nil
	}
	if d := os.Getenv(xdgVar); d != "" {
		return filepath.Join(d, "laranest"), nil
	}
	if runtime.GOOS == "windows" {
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			return "", errors.New("%LOCALAPPDATA% is not set")
		}
		return filepath.Join(local, "Laranest", kind), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, homeRel, "laranest"), nil
}

func configFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config file, falling back to defaults for a missing file or unset fields.
func Load() (Config, error) {
	cfg := Default()
	path, err := configFile()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// First run: don't default to a port we can't use (unprivileged, or taken by apache/nginx).
		cfg.HTTPPort = firstUsablePort(80, 8080, 8081, 8082, 8088, 8090)
		cfg.HTTPSPort = firstUsablePort(443, 8443, 9443)
		// On Windows the panel port may be taken by WSL's localhost forwarding
		// of a Laranest running inside WSL.
		cfg.PanelAddr = fmt.Sprintf("127.0.0.1:%d", firstUsablePort(DefaultPanelPort, DefaultPanelPort+1, DefaultPanelPort+2, DefaultPanelPort+3))
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	cfg.HTTPPort = 0 // so a file without it (older versions, hand-written) gets a usable one
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = firstUsablePort(80, 8080, 8081, 8082, 8088, 8090)
	}
	if cfg.HTTPSPort == 0 {
		cfg.HTTPSPort = firstUsablePort(443, 8443, 9443)
	}
	return cfg, nil
}

// Save writes the config atomically.
func Save(cfg Config) error {
	path, err := configFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// firstUsablePort returns the first candidate we can bind on loopback.
func firstUsablePort(candidates ...int) int {
	for _, p := range candidates {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			ln.Close()
			return p
		}
	}
	return candidates[len(candidates)-1]
}
