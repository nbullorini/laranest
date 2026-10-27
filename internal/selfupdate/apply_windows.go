//go:build windows

package selfupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// Update downloads the latest build to the temp folder and runs its
// `install`, which stops the running daemon (and its apps), replaces the
// installed copy and starts the new one, which starts the apps again.
// fromDaemon: called by the daemon itself, which that install stops, so it
// runs detached.
func Update(ctx context.Context, fromDaemon bool) error {
	// Downloads of earlier updates can't delete themselves: clean them up now.
	if old, _ := filepath.Glob(filepath.Join(os.TempDir(), "laranest-update-*.exe")); old != nil {
		for _, f := range old {
			_ = os.Remove(f)
		}
	}
	tmp, err := Download(ctx, os.TempDir())
	if err != nil {
		return err
	}
	cmd := exec.Command(tmp, "install")
	if !fromDaemon {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	flags := uint32(windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags | windows.CREATE_BREAKAWAY_FROM_JOB}
	if err := cmd.Start(); err != nil {
		cmd.SysProcAttr.CreationFlags = flags // the job doesn't allow breaking away
		if err := cmd.Start(); err != nil {
			return err
		}
	}
	return cmd.Process.Release()
}
