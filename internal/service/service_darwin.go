//go:build darwin

package service

import (
	"bytes"
	"fmt"
	"html"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nbullorini/laranest/internal/config"
)

// On macOS Laranest is a launchd LaunchAgent of the user: started at login,
// restarted by launchd if it crashes (not when it stops cleanly, so
// `laranest uninstall` and updates can stop it). No administrator rights.

const label = "dev.laranest"

// Hint tells how to start the service when the panel is unreachable.
const Hint = "launchctl kickstart gui/$(id -u)/" + label + " (or run laranest install)"

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%[1]s</string>
	<key>ProgramArguments</key>
	<array><string>%[2]s</string><string>serve</string></array>
	<key>EnvironmentVariables</key>
	<dict><key>PATH</key><string>%[3]s</string></dict>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key>
	<dict><key>SuccessfulExit</key><false/></dict>
	<key>StandardOutPath</key><string>%[4]s</string>
	<key>StandardErrorPath</key><string>%[4]s</string>
</dict>
</plist>
`

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// domain is the user's launchd domain: gui/<uid> in a desktop session,
// user/<uid> otherwise (e.g. over SSH).
func domains() []string {
	uid := strconv.Itoa(os.Getuid())
	return []string{"gui/" + uid, "user/" + uid}
}

// Install writes the LaunchAgent for binary (with envPath as its PATH) and
// (re)starts it.
func Install(binary, envPath string) error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	state, err := config.StateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		return err
	}
	esc := html.EscapeString
	plist := fmt.Sprintf(plistTemplate, label, esc(binary), esc(envPath), esc(filepath.Join(state, "laranest.log")))
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return err
	}
	bootout() // an older version, if it runs
	var errs []string
	for _, d := range domains() {
		out, err := exec.Command("launchctl", "bootstrap", d, path).CombinedOutput()
		if err == nil {
			return waitHealthy(20 * time.Second)
		}
		errs = append(errs, fmt.Sprintf("%s: %s", d, bytes.TrimSpace(out)))
	}
	return fmt.Errorf("launchctl bootstrap failed: %s", strings.Join(errs, "; "))
}

// Uninstall stops the daemon (which stops every app) and removes the agent.
func Uninstall() error {
	bootout()
	path, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func bootout() {
	for _, d := range domains() {
		_ = exec.Command("launchctl", "bootout", d+"/"+label).Run()
	}
}

// loaded returns the domain the agent is loaded in, and whether it runs.
func loaded() (domain string, running bool) {
	for _, d := range domains() {
		if out, err := exec.Command("launchctl", "print", d+"/"+label).Output(); err == nil {
			return d, bytes.Contains(out, []byte("state = running"))
		}
	}
	return "", false
}

// Status is "active" while launchd runs the agent, else "inactive".
func Status() string {
	if _, running := loaded(); running {
		return "active"
	}
	return "inactive"
}

// Restart restarts the agent with a new binary in place (after an update).
// From the daemon itself, launchctl runs detached: launchd stops the daemon
// while handling it.
func Restart(fromDaemon bool) error {
	d, _ := loaded()
	if d == "" {
		return fmt.Errorf("installed, but Laranest is not loaded in launchd: run laranest install")
	}
	cmd := exec.Command("launchctl", "kickstart", "-k", d+"/"+label)
	if fromDaemon {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("installed, but restarting Laranest failed: %w", err)
		}
		return cmd.Process.Release()
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("installed, but restarting Laranest failed: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// MarkRunning is a no-op: launchd tracks the daemon.
func MarkRunning() func() { return func() {} }

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
	state, _ := config.StateDir()
	return fmt.Errorf("Laranest did not start on %s: see %s", cfg.PanelAddr, filepath.Join(state, "laranest.log"))
}
