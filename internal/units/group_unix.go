//go:build !windows

package units

import (
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// StartGroup starts cmd in a process group of its own and returns a function
// that stops the whole group (SIGTERM, then SIGKILL): `npm run dev` or
// `composer` leave children that killing cmd alone would orphan. Call stop
// after Wait too: it takes whatever the command left running.
func StartGroup(cmd *exec.Cmd) (stop func(), err error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pgid := cmd.Process.Pid
	var once sync.Once
	return func() {
		once.Do(func() {
			if syscall.Kill(-pgid, syscall.SIGTERM) == nil {
				time.AfterFunc(5*time.Second, func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
			}
		})
	}, nil
}
