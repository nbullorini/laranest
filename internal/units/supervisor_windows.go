//go:build windows

package units

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows a process's group is a Job Object created to kill its
// processes when closed, which also happens if the daemon dies: nothing can
// outlive it.
type procGroup = windows.Handle

func killGroup(g procGroup)    { _ = windows.TerminateJobObject(g, 1) }
func stopGroup(g procGroup)    { _ = windows.TerminateJobObject(g, 1) }
func releaseGroup(g procGroup) { _ = windows.CloseHandle(g) }

// reapOrphans has nothing to do: Job Objects die with the daemon.
func reapOrphans(stateDir string) {}

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// spawn starts spec suspended, puts it in a new Job Object (so nothing it
// starts can escape it) and resumes it. Its output goes to log.
func spawn(s Spec, log *logBuf) (*exec.Cmd, procGroup, error) {
	if len(s.Cmd) == 0 {
		return nil, 0, errors.New("empty command")
	}
	job, err := newJob()
	if err != nil {
		return nil, 0, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		windows.CloseHandle(job)
		return nil, 0, err
	}
	cmd := exec.Command(s.Cmd[0], s.Cmd[1:]...)
	cmd.Dir = s.Dir
	cmd.Env = append(os.Environ(), s.Env...) // later entries win (case-insensitively on Windows)
	cmd.Stdout, cmd.Stderr = w, w
	err = startInJob(cmd, job)
	w.Close() // the child has its copy; EOF arrives when the whole tree exits
	if err != nil {
		r.Close()
		windows.CloseHandle(job)
		return nil, 0, err
	}
	go log.copy(r)
	return cmd, job, nil
}

// startInJob starts cmd suspended, with a hidden console of its own (child
// console programs like php and node inherit it instead of popping up
// windows), puts it in job and resumes it.
func startInJob(cmd *exec.Cmd, job windows.Handle) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, h)
		if err == nil {
			if r1, _, e := ntResumeProcess.Call(uintptr(h)); r1 != 0 {
				err = fmt.Errorf("resuming the process: %v", e)
			}
		}
		windows.CloseHandle(h)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	return err
}

// StartGroup starts cmd in a Job Object of its own and returns a function
// that kills everything in it: `npm run dev` or `composer` leave children
// that killing cmd alone would orphan. Call stop after Wait too: it takes
// whatever the command left running and frees the job.
func StartGroup(cmd *exec.Cmd) (stop func(), err error) {
	job, err := newJob()
	if err != nil {
		return nil, err
	}
	if err := startInJob(cmd, job); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = windows.TerminateJobObject(job, 1)
			windows.CloseHandle(job)
		})
	}, nil
}

// newJob creates a Job Object that kills its processes when closed, which
// also happens if the daemon dies.
func newJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}
