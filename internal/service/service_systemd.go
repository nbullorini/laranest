//go:build linux

package service

import (
	"fmt"
	"os/exec"

	"github.com/nbullorini/laranest/internal/systemd"
)

// Install installs binary as the service (with envPath as its PATH) and starts it.
func Install(binary, envPath string) error { return systemd.Install(binary, envPath) }

// Uninstall stops every app process and removes the service.
func Uninstall() error { return systemd.Uninstall() }

// Status is "active", "inactive"… (systemd's ActiveState).
func Status() string { return systemd.Status() }

// Hint tells how to start the service when the panel is unreachable.
const Hint = "systemctl --user start laranest"

// Restart restarts the service with a new binary in place (after an update).
// fromDaemon: asked by the service itself, which is about to be restarted.
func Restart(fromDaemon bool) error {
	restart := exec.Command("systemctl", "--user", "restart", "laranest.service")
	if fromDaemon {
		// A child of the daemon lives in the service's cgroup, which systemd
		// kills while restarting it, so systemctl would die mid-way: run it
		// as a transient unit of its own instead.
		restart = exec.Command("systemd-run", "--user", "--collect", "--quiet",
			"systemctl", "--user", "restart", "laranest.service")
	}
	if out, err := restart.CombinedOutput(); err != nil {
		return fmt.Errorf("installed, but restarting the service failed (systemctl --user restart laranest): %w\n%s", err, out)
	}
	return nil
}

// MarkRunning is a no-op: systemd tracks the daemon.
func MarkRunning() func() { return func() {} }
