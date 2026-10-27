//go:build windows

package units

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
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

// pidAlive asks Windows whether a process exists.
func pidAlive(pid int) bool {
	out, _ := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
	return strings.Contains(string(out), " "+strconv.Itoa(pid)+" ")
}

func TestSupervisorLogsAndStopsTheTree(t *testing.T) {
	m, err := New(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	spec := Spec{Unit: "laranest-t-web.service", Name: "web", Description: "test",
		Cmd: []string{"cmd.exe", "/c", "echo hello from the child& ping -n 60 127.0.0.1 >nul"}}

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

	// ping is a grandchild of the supervised cmd.exe: the job must take it too.
	out, _ := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"(Get-CimInstance Win32_Process -Filter 'ParentProcessId="+strconv.Itoa(int(st.PID))+"').ProcessId").Output()
	var children []int
	for _, line := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(line); err == nil {
			children = append(children, pid)
		}
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
	if pidAlive(int(st.PID)) {
		t.Error("the process survived Stop")
	}
	for _, pid := range children {
		if pidAlive(pid) {
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
	spec := Spec{Unit: "laranest-t-queue.service", Name: "queue", Description: "test", Cmd: []string{"cmd.exe", "/c", "exit 3"}}
	if err := m.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, spec.Unit, func(s Status) bool { return s.Restarts >= 2 })
	if err := m.Stop(context.Background(), spec.Unit); err != nil {
		t.Fatal(err)
	}
	// Starting it again after a stop works.
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
	err = m.Start(context.Background(), Spec{Unit: "laranest-t-x.service", Cmd: []string{`C:\nope\missing.exe`}})
	if err == nil {
		t.Fatal("want an error for a missing program")
	}
	if st := m.Status(context.Background(), "laranest-t-x.service", "x"); st.State != StateFailed {
		t.Errorf("state = %s, want failed", st.State)
	}
}
