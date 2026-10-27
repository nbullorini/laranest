//go:build windows

// laranestw starts the Laranest daemon without a console window. It is
// registered to run when the user signs in to Windows (`laranest install`),
// and built as a GUI program (-H windowsgui) so starting it shows nothing.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/nbullorini/laranest/internal/config"
)

func main() {
	self, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	cmd := exec.Command(filepath.Join(filepath.Dir(self), "laranest.exe"), "serve")
	if state, err := config.StateDir(); err == nil && os.MkdirAll(state, 0o755) == nil {
		path := filepath.Join(state, "laranest.log")
		if info, err := os.Stat(path); err == nil && info.Size() > 10<<20 {
			_ = os.Rename(path, path+".1")
		}
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
		}
	}
	// A hidden console of its own (the apps' php/node inherit it), outside
	// any job of whoever started us so it isn't killed with them.
	flags := uint32(windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags | windows.CREATE_BREAKAWAY_FROM_JOB}
	if err := cmd.Start(); err != nil {
		cmd.SysProcAttr.CreationFlags = flags // the job doesn't allow breaking away
		if err := cmd.Start(); err != nil {
			os.Exit(1)
		}
	}
}
