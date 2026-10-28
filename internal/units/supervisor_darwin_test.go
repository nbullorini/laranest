//go:build darwin

package units

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func waitState(t *testing.T, m *Manager, unit string, cond func(Status) bool) Status {
	t.Helper()
	for range 100 {
		if st := m.Status(context.Background(), unit, "x"); cond(st) {
			return st
		}
		time.Sleep(100 * time.Millisecond)
	}
	st := m.Status(context.Background(), unit, "x")
	t.Fatalf("%s never reached the expected state: %+v", unit, st)
	return st
}

func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// waitGone waits for a process to exit (a killed child is reaped by init).
func waitGone(pid int) bool {
	for range 50 {
		if !pidAlive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func childrenOf(pid int) []int {
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if p, err := strconv.Atoi(f); err == nil {
			pids = append(pids, p)
		}
	}
	return pids
}

func TestSupervisorLogsAndStopsTheTree(t *testing.T) {
	m, err := New(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	spec := Spec{Unit: "laranest-t-web.service", Name: "web", Description: "test",
		Cmd: []string{"/bin/sh", "-c", "echo hello from the child; sleep 60 & wait"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan Line, 16)
	go Follow(ctx, spec.Unit, 10, func(l Line) { got <- l })
	if err := m.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, m, spec.Unit, func(s Status) bool { return s.State == StateRunning })

	deadline := time.After(10 * time.Second)
	for seen := false; !seen; {
		select {
		case l := <-got:
			seen = l.Stream == "out" && strings.Contains(l.Text, "hello from the child")
		case <-deadline:
			t.Fatal("the process output never reached the followers")
		}
	}

	// sleep is a child of the supervised shell: stopping the group takes it too.
	var children []int
	for range 50 {
		if children = childrenOf(int(st.PID)); len(children) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(children) == 0 {
		t.Fatal("found no child processes to check")
	}
	if err := m.Stop(context.Background(), spec.Unit); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(context.Background(), spec.Unit, "web"); st.State != StateStopped {
		t.Errorf("state after stop = %s", st.State)
	}
	if !waitGone(int(st.PID)) {
		t.Error("the process survived Stop")
	}
	for _, pid := range children {
		if !waitGone(pid) {
			t.Errorf("child %d survived Stop", pid)
		}
	}
}

func TestSupervisorRestartsCrashes(t *testing.T) {
	m, err := New(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	spec := Spec{Unit: "laranest-t-queue.service", Name: "queue", Description: "test", Cmd: []string{"/bin/sh", "-c", "exit 3"}}
	if err := m.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, spec.Unit, func(s Status) bool { return s.Restarts >= 2 })
	if err := m.Stop(context.Background(), spec.Unit); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorReportsMissingPrograms(t *testing.T) {
	m, err := New(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	err = m.Start(context.Background(), Spec{Unit: "laranest-t-x.service", Cmd: []string{"/nope/missing"}})
	if err == nil {
		t.Fatal("want an error for a missing program")
	}
	if st := m.Status(context.Background(), "laranest-t-x.service", "x"); st.State != StateFailed {
		t.Errorf("state = %s, want failed", st.State)
	}
}

// A daemon that died without stopping its processes leaves their groups
// recorded: the next one kills them, but never after a reboot.
func TestSupervisorReapsOrphans(t *testing.T) {
	dir := t.TempDir()
	orphan := exec.Command("/bin/sh", "-c", "sleep 60 & wait")
	orphan.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	go orphan.Wait()
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		t.Fatal(err)
	}
	write := func(boot int64) {
		data, _ := json.Marshal(savedGroups{Boot: boot, IDs: []int{orphan.Process.Pid}})
		if err := os.WriteFile(filepath.Join(dir, "process-groups.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(tv.Sec - 3600) // recorded before a reboot: left alone
	m, err := New(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	if !pidAlive(orphan.Process.Pid) {
		t.Fatal("killed a group recorded before the last boot")
	}

	write(tv.Sec)
	if m, err = New(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if !waitGone(orphan.Process.Pid) {
		t.Error("the orphaned group survived")
	}
}
