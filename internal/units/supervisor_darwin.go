//go:build darwin

package units

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// On macOS a process's group is a Unix process group of its own: stopping
// it signals everything the command started (npm → node → esbuild…).
type procGroup = int // the group id: the child's pid

func spawn(s Spec, log *logBuf) (*exec.Cmd, procGroup, error) {
	if len(s.Cmd) == 0 {
		return nil, 0, errors.New("empty command")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, 0, err
	}
	cmd := exec.Command(s.Cmd[0], s.Cmd[1:]...)
	cmd.Dir = s.Dir
	cmd.Env = append(os.Environ(), s.Env...) // later entries win
	cmd.Stdout, cmd.Stderr = w, w
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Start()
	w.Close() // the child has its copy; EOF arrives when the whole tree exits
	if err != nil {
		r.Close()
		return nil, 0, err
	}
	go log.copy(r)
	groups.track(cmd.Process.Pid, true)
	return cmd, cmd.Process.Pid, nil
}

// killGroup takes what a process left running after it exited.
func killGroup(g procGroup) { _ = syscall.Kill(-g, syscall.SIGKILL) }

// stopGroup asks the whole group to stop (queue workers finish their job),
// then kills what is still there.
func stopGroup(g procGroup) {
	_ = syscall.Kill(-g, syscall.SIGTERM)
	time.AfterFunc(8*time.Second, func() { _ = syscall.Kill(-g, syscall.SIGKILL) })
}

func releaseGroup(g procGroup) { groups.track(g, false) }

// groups records the process groups running, so that if the daemon dies
// without stopping them (a crash, a kill -9), the next start can: unlike
// Windows' Job Objects, process groups outlive their parent. The boot time
// makes sure ids from before a reboot, possibly reused, are never touched.
type groupSet struct {
	sync.Mutex
	path string
	boot int64
	ids  map[int]bool
}

var groups = &groupSet{ids: map[int]bool{}}

type savedGroups struct {
	Boot int64 `json:"boot"`
	IDs  []int `json:"ids"`
}

func reapOrphans(stateDir string) {
	groups.Lock()
	defer groups.Unlock()
	groups.path = filepath.Join(stateDir, "process-groups.json")
	if tv, err := unix.SysctlTimeval("kern.boottime"); err == nil {
		groups.boot = tv.Sec
	}
	var saved savedGroups
	if data, err := os.ReadFile(groups.path); err == nil && json.Unmarshal(data, &saved) == nil && saved.Boot == groups.boot {
		for _, id := range saved.IDs {
			_ = syscall.Kill(-id, syscall.SIGKILL)
		}
	}
	groups.ids = map[int]bool{}
	saveGroupsLocked()
}

func (g *groupSet) track(id int, running bool) {
	g.Lock()
	defer g.Unlock()
	if running {
		g.ids[id] = true
	} else {
		delete(g.ids, id)
	}
	saveGroupsLocked()
}

func saveGroupsLocked() {
	if groups.path == "" {
		return
	}
	saved := savedGroups{Boot: groups.boot, IDs: []int{}}
	for id := range groups.ids {
		saved.IDs = append(saved.IDs, id)
	}
	if data, err := json.Marshal(saved); err == nil {
		_ = os.WriteFile(groups.path, data, 0o644)
	}
}
