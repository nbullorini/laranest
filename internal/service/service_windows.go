//go:build windows

package service

import (
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/nbullorini/laranest/internal/config"
	"github.com/nbullorini/laranest/internal/winenv"
)

// launcher is cmd/laranestw built as a GUI program: started at sign-in, it
// runs `laranest serve` without a console window (see `make windows`).
//
//go:embed laranestw.exe
var launcher []byte

const runName = "Laranest"

// Hint tells how to start the service when the panel is unreachable.
const Hint = "it starts when you sign in to Windows; to start it now run `laranest install`"

// Dir is where Laranest installs itself: %LOCALAPPDATA%\Programs\Laranest.
func Dir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Laranest")
}

// Install copies binary into Dir (unless it already runs from there),
// registers the launcher to run at sign-in, adds Dir to the user's PATH and
// (re)starts the daemon. envPath is unused: at sign-in the daemon gets the
// user's environment.
func Install(binary, envPath string) error {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	stopDaemon()
	target := filepath.Join(dir, "laranest.exe")
	if !sameFile(binary, target) {
		data, err := os.ReadFile(binary)
		if err != nil {
			return err
		}
		if err := replaceFile(target, data); err != nil {
			return err
		}
	}
	launcherPath := filepath.Join(dir, "laranestw.exe")
	if err := replaceFile(launcherPath, launcher); err != nil {
		return err
	}
	if err := winenv.SetRunAtSignIn(runName, `"`+launcherPath+`"`); err != nil {
		return fmt.Errorf("registering the sign-in launcher: %w", err)
	}
	if _, err := winenv.AddUserPath(dir); err != nil {
		return fmt.Errorf("adding %s to your PATH: %w", dir, err)
	}
	if err := exec.Command(launcherPath).Run(); err != nil {
		return fmt.Errorf("starting Laranest: %w", err)
	}
	return waitHealthy(20 * time.Second)
}

// Uninstall stops the daemon (and with it every app) and removes the sign-in
// entry and the PATH entries. The files stay in Dir.
func Uninstall() error {
	stopDaemon()
	var errs []error
	errs = append(errs, winenv.SetRunAtSignIn(runName, ""))
	_, err := winenv.RemoveUserPath(Dir())
	errs = append(errs, err)
	if data, err := config.DataDir(); err == nil {
		_, err = winenv.RemoveUserPath(filepath.Join(data, "shims"))
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Status is "active" while the daemon's process is alive, else "inactive".
func Status() string {
	if pid := daemonPID(); pid != 0 && alive(pid) {
		return "active"
	}
	return "inactive"
}

// MarkRunning records the daemon's PID (so install/uninstall can stop it)
// and returns a function that removes it.
func MarkRunning() func() {
	path, err := pidFile()
	if err != nil {
		return func() {}
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
	return func() { _ = os.Remove(path) }
}

func pidFile() (string, error) {
	state, err := config.StateDir()
	return filepath.Join(state, "laranest.pid"), err
}

func daemonPID() uint32 {
	path, err := pidFile()
	if err != nil {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return uint32(pid)
}

func alive(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259 // STILL_ACTIVE
}

// stopDaemon asks the running daemon to shut down (it stops the apps), and
// kills it if it doesn't within a few seconds.
func stopDaemon() {
	pid := daemonPID()
	if cfg, err := config.Load(); err == nil {
		client := http.Client{Timeout: 5 * time.Second}
		if resp, err := client.Post("http://"+cfg.PanelAddr+"/api/shutdown", "application/json", nil); err == nil {
			resp.Body.Close()
		}
	}
	if pid == 0 {
		return
	}
	for range 100 {
		if !alive(pid) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid); err == nil {
		_ = windows.TerminateProcess(h, 1)
		windows.CloseHandle(h)
	}
}

func waitHealthy(timeout time.Duration) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	client := http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if resp, err := client.Get("http://" + cfg.PanelAddr + "/api/health"); err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	logPath := ""
	if state, err := config.StateDir(); err == nil {
		logPath = filepath.Join(state, "laranest.log")
	}
	return fmt.Errorf("Laranest did not start on %s: see %s", cfg.PanelAddr, logPath)
}

// replaceFile writes data to path even if path is a running program: Windows
// can't overwrite or delete a running .exe, but it can rename it.
func replaceFile(path string, data []byte) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	old := path + ".old"
	_ = os.Remove(old)
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, old); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	_ = os.Remove(old) // fails while the old one still runs; cleaned up next time
	return nil
}

func sameFile(a, b string) bool {
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}
