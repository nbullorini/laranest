package apps

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nbullorini/laranest/internal/detect"
	"github.com/nbullorini/laranest/internal/php"
)

// The project's laranest.json (see detect.ProjectConfig): its settings are
// applied when the app is added and again whenever the file changes (a git
// pull), on start and on re-detect. Changes made in the panel stay on this
// machine until then, or until they are saved to the file.

// applyProjectLocked validates the file's settings and applies them to a.
func (m *Manager) applyProjectLocked(a *App, c *detect.ProjectConfig) error {
	commands, custom, webMode := a.Commands, a.Custom, a.WebMode
	if c.Commands != nil {
		commands = c.Commands
	}
	if c.Custom != nil {
		custom = make([]CustomProcess, len(c.Custom))
		for i, p := range c.Custom {
			custom[i] = CustomProcess{Name: p.Name, Command: p.Command, Disabled: p.Disabled}
		}
	}
	if c.WebMode != nil {
		webMode = *c.WebMode
	}
	if err := validateAdvanced(commands, custom, webMode); err != nil {
		return err
	}
	if c.PHP != nil && *c.PHP != "" {
		if _, _, err := m.rt.Plan(*c.PHP, php.Requirement{}); err != nil {
			return err
		}
	}
	if c.PHP != nil {
		a.PHP = *c.PHP
	}
	if c.HTTPS != nil {
		a.HTTPS = *c.HTTPS
	}
	a.Processes = supported(c.ApplyProcesses(a.Processes))
	a.Commands, a.Custom, a.WebMode = commands, custom, webMode
	if a.WebMode != WebModePort {
		a.WebPort = 0
	}
	m.ensureReverbPortLocked(a)
	return nil
}

// syncProjectLocked applies the project's laranest.json if it changed since
// it was last applied. A broken file is reported (see the app's warnings)
// and leaves the settings as they are.
func (m *Manager) syncProjectLocked(a *App) (changed bool) {
	c, sum, err := detect.ReadProject(a.Path)
	if err != nil || sum == a.ProjectSum {
		return false
	}
	if c == nil { // removed: the settings stay
		a.ProjectSum = ""
		return false
	}
	if err := m.applyProjectLocked(a, c); err != nil {
		slog.Warn("laranest.json not applied", "app", a.ID, "err", err)
		return false
	}
	a.ProjectSum = sum
	slog.Info("applied laranest.json", "app", a.ID)
	return true
}

// SaveProject writes the app's settings to its laranest.json, for the team.
func (m *Manager) SaveProject(id string) (View, error) {
	m.mu.Lock()
	a := m.store.apps[id]
	if a == nil {
		m.mu.Unlock()
		return View{}, ErrNotFound
	}
	c := detect.ProjectConfig{HTTPS: &a.HTTPS, Processes: detect.ProcessMap(a.Processes)}
	if a.PHP != "" {
		c.PHP = &a.PHP
	}
	if old, _, _ := detect.ReadProject(a.Path); runtime.GOOS == "windows" && old != nil && old.Processes["horizon"] {
		// Windows runs queue:work instead of Horizon: don't take it away
		// from the team's Linux and macOS machines.
		c.Processes["horizon"], c.Processes["queue"] = true, old.Processes["queue"]
	}
	if len(a.Commands) > 0 {
		c.Commands = a.Commands
	}
	for _, p := range a.Custom {
		c.Custom = append(c.Custom, detect.CustomCommand{Name: p.Name, Command: p.Command, Disabled: p.Disabled})
	}
	if a.WebMode != "" {
		c.WebMode = &a.WebMode
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false) // commands may have & and > in them
	if err := enc.Encode(c); err != nil {
		m.mu.Unlock()
		return View{}, err
	}
	err := os.WriteFile(filepath.Join(a.Path, detect.ProjectFile), buf.Bytes(), 0o644)
	if err == nil {
		sum := sha256.Sum256(buf.Bytes())
		a.ProjectSum = hex.EncodeToString(sum[:])
		err = m.store.save()
	}
	m.mu.Unlock()
	if err != nil {
		return View{}, err
	}
	return m.view(a), nil
}

// projectWarning reports a laranest.json that can't be used.
func (m *Manager) projectWarning(a *App) *detect.Warning {
	c, _, err := detect.ReadProject(a.Path)
	if err == nil && c != nil {
		m.mu.Lock()
		probe := *a
		err = m.applyProjectLocked(&probe, c)
		m.mu.Unlock()
	}
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, detect.ProjectFile) {
		msg = detect.ProjectFile + ": " + msg
	}
	return &detect.Warning{Level: detect.LevelError, Code: "laranest_json", Message: msg,
		Hint: "Fix the file, or save this app's settings to it from Settings. Until then its settings stay as they are."}
}
