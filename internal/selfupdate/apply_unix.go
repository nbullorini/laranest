//go:build !windows

package selfupdate

import (
	"context"
	"os"
	"path/filepath"

	"github.com/nbullorini/laranest/internal/service"
)

// Update downloads the latest build, swaps it in place of the running binary
// (renaming over it is atomic, and running processes keep the old file) and
// restarts the service (systemd on Linux: the apps' units keep running;
// launchd on macOS). fromDaemon: called by the service itself, which is
// about to be restarted.
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
	return service.Restart(fromDaemon)
}
