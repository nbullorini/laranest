//go:build !windows

package selfupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Update downloads the latest build, swaps it in place of the running binary
// (renaming over it is atomic, and running processes keep the old file) and
// restarts the service. The apps' units keep running meanwhile. fromDaemon:
// called by the service itself, which systemd is about to restart.
func Update(ctx context.Context, fromDaemon bool) error {
	exe, err := executable()
	if err != nil {
		return err
	}
	tmp, err := Download(ctx, filepath.Dir(exe)) // same dir, so the rename is atomic
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	args := []string{"--user", "restart", "laranest.service"}
	if fromDaemon {
		args = []string{"--user", "--no-block", "restart", "laranest.service"} // don't wait for our own death
	}
	if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("installed, but restarting the service failed (systemctl --user restart laranest): %w\n%s", err, out)
	}
	return nil
}
