//go:build !windows

package systemd

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

const UnitName = "laranest.service"

const unitTemplate = `[Unit]
Description=Laranest - local Laravel app manager
After=network.target

[Service]
Type=simple
Environment="PATH=%s"
ExecStart=%s serve
Restart=on-failure
RestartSec=2
TimeoutStopSec=20

[Install]
WantedBy=default.target
`

func unitPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "systemd", "user", UnitName), nil
}

// Install writes the unit pointing at binary (with the given PATH), enables lingering so the service
// survives logout (and WSL session closes), and starts it.
func Install(binary, envPath string) error {
	// Linger first: WSL doesn't open logind sessions, so on a fresh distro the
	// user's systemd manager (and its bus) only exists once lingering is on.
	if err := enableLinger(); err != nil {
		return err
	}
	if err := waitUserBus(); err != nil {
		return err
	}
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(unitTemplate, envPath, binary)), 0o644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", "--now", UnitName); err != nil {
		return err
	}
	// Restart in case the unit was already running an older binary.
	return systemctl("restart", UnitName)
}

// waitUserBus waits for the user manager's D-Bus socket after enabling
// linger, and points this process (and the systemctl calls) at it.
func waitUserBus() error {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
		os.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	}
	bus := filepath.Join(runtimeDir, "bus")
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		os.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+bus)
	}
	for range 50 {
		if _, err := os.Stat(bus); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("the systemd user manager did not start (%s is missing). On WSL, check [boot] systemd=true in /etc/wsl.conf and run `wsl --shutdown`", bus)
}

// Uninstall stops every app process, then stops, disables and removes the unit.
func Uninstall() error {
	path, err := unitPath()
	if err != nil {
		return err
	}
	_ = systemctl("stop", "laranest.slice")
	_ = systemctl("disable", "--now", UnitName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return systemctl("daemon-reload")
}

// Status returns systemd's ActiveState for the unit (e.g. "active", "inactive").
func Status() string {
	out, err := exec.Command("systemctl", "--user", "show", "-p", "ActiveState", "--value", UnitName).Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

func enableLinger() error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join("/var/lib/systemd/linger", u.Username)); err == nil {
		return nil
	}
	if out, err := exec.Command("loginctl", "enable-linger", u.Username).CombinedOutput(); err != nil {
		return fmt.Errorf("loginctl enable-linger: %w\n%s(run: sudo loginctl enable-linger %s)", err, out, u.Username)
	}
	return nil
}
