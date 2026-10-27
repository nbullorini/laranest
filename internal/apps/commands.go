package apps

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-shellwords"

	"github.com/nbullorini/laranest/internal/detect"
	"github.com/nbullorini/laranest/internal/units"
)

// Commands run from the panel: the same `laranest php|composer|npm|npx|node …`
// a user would type in the project's folder, with their output kept as a
// per-app history.

// commandTools are the laranest subcommands the panel may run: the ones that
// run project tooling, never Laranest's own (install, uninstall, serve…).
var commandTools = []string{"php", "composer", "npm", "npx", "node"}

const (
	maxCommands      = 30        // history entries kept per app
	maxCommandOutput = 256 << 10 // bytes of output kept per command (the tail)
)

// Command is one command run from the panel.
type Command struct {
	ID         string     `json:"id"`
	Line       string     `json:"line"` // as typed, without "laranest"
	Args       []string   `json:"args"`
	Status     string     `json:"status"` // running | ok | failed | stopped
	ExitCode   *int       `json:"exit_code,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Fixes is the warning code a Run button started it for; Unresolved is
	// set when it exited fine but that warning is still there (tools such as
	// `artisan storage:link` report success even when they failed).
	Fixes      string `json:"fixes,omitempty"`
	Unresolved bool   `json:"unresolved,omitempty"`
	Output     string `json:"output"` // empty in lists
}

type runningCommand struct {
	mu     sync.Mutex
	cmd    Command
	out    []byte
	stop   func()
	halted bool
}

func (r *runningCommand) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.out = append(r.out, p...)
	if len(r.out) > maxCommandOutput {
		r.out = append(r.out[:0:0], r.out[len(r.out)-maxCommandOutput:]...)
	}
	return len(p), nil
}

func (r *runningCommand) snapshot(withOutput bool) Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.cmd
	if withOutput {
		c.Output = string(r.out)
	}
	return c
}

// commandStore keeps every app's history in memory, persisted per app.
type commandStore struct {
	mu   sync.Mutex
	dir  string
	apps map[string][]*runningCommand // app id → oldest first
}

func (s *commandStore) file(appID string) string { return filepath.Join(s.dir, appID+".json") }

// list loads (once) and returns an app's history.
func (s *commandStore) list(appID string) []*runningCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.apps == nil {
		s.apps = map[string][]*runningCommand{}
	}
	if cmds, ok := s.apps[appID]; ok {
		return cmds
	}
	var saved []Command
	if data, err := os.ReadFile(s.file(appID)); err == nil {
		_ = json.Unmarshal(data, &saved)
	}
	cmds := make([]*runningCommand, 0, len(saved))
	for _, c := range saved {
		if c.Status == "running" { // the daemon stopped while it ran
			c.Status = "stopped"
		}
		out := []byte(c.Output)
		c.Output = ""
		cmds = append(cmds, &runningCommand{cmd: c, out: out})
	}
	s.apps[appID] = cmds
	return cmds
}

func (s *commandStore) add(appID string, r *runningCommand) {
	s.list(appID)
	s.mu.Lock()
	cmds := append(s.apps[appID], r)
	if len(cmds) > maxCommands {
		cmds = cmds[len(cmds)-maxCommands:]
	}
	s.apps[appID] = cmds
	s.mu.Unlock()
	s.save(appID)
}

func (s *commandStore) save(appID string) {
	cmds := s.list(appID)
	out := make([]Command, len(cmds))
	for i, r := range cmds {
		out[i] = r.snapshot(true)
	}
	data, err := json.Marshal(out)
	if err != nil {
		return
	}
	_ = os.MkdirAll(s.dir, 0o755)
	tmp := s.file(appID) + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, s.file(appID))
	}
}

func (s *commandStore) find(appID, cmdID string) *runningCommand {
	for _, r := range s.list(appID) {
		if r.cmd.ID == cmdID {
			return r
		}
	}
	return nil
}

func (s *commandStore) rename(from, to string) {
	s.list(from)
	s.mu.Lock()
	s.apps[to], s.apps[from] = s.apps[from], nil
	delete(s.apps, from)
	s.mu.Unlock()
	_ = os.Rename(s.file(from), s.file(to))
}

func (s *commandStore) forget(appID string) {
	s.mu.Lock()
	delete(s.apps, appID)
	s.mu.Unlock()
	_ = os.Remove(s.file(appID))
}

// ParseCommand turns what the user typed ("php artisan migrate",
// "laranest npm run build") into laranest's arguments.
func ParseCommand(line string) ([]string, error) {
	args, err := shellwords.Parse(strings.TrimSpace(line))
	if err != nil {
		return nil, fmt.Errorf("cannot parse the command: %w", err)
	}
	if len(args) > 0 && args[0] == "laranest" {
		args = args[1:]
	}
	if len(args) == 0 {
		return nil, errors.New("empty command")
	}
	if !slices.Contains(commandTools, args[0]) {
		return nil, fmt.Errorf("only %s commands can run from the panel", strings.Join(commandTools, ", "))
	}
	return args, nil
}

// Commands lists an app's command history, newest first, without output.
func (m *Manager) Commands(id string) ([]Command, error) {
	if _, err := m.Get(id); err != nil {
		return nil, err
	}
	cmds := m.cmds.list(id)
	out := make([]Command, 0, len(cmds))
	for i := len(cmds) - 1; i >= 0; i-- {
		out = append(out, cmds[i].snapshot(false))
	}
	return out, nil
}

// CommandOutput returns one command with its output so far.
func (m *Manager) CommandOutput(id, cmdID string) (Command, error) {
	r := m.cmds.find(id, cmdID)
	if r == nil {
		return Command{}, ErrNotFound
	}
	return r.snapshot(true), nil
}

// RunCommand starts `laranest <line>` in the app's folder, as the user would
// from a terminal there, and returns right away; the output accumulates in
// the history. Stdin is empty, so artisan and composer don't stop to ask.
// fixes is the code of the warning it should fix ("" if typed by hand).
func (m *Manager) RunCommand(id, line, fixes string) (Command, error) {
	m.mu.Lock()
	a := m.store.apps[id]
	m.mu.Unlock()
	if a == nil {
		return Command{}, ErrNotFound
	}
	args, err := ParseCommand(line)
	if err != nil {
		return Command{}, err
	}
	self, err := os.Executable()
	if err != nil {
		return Command{}, err
	}
	r := &runningCommand{cmd: Command{
		ID: newCommandID(), Line: strings.Join(quoteArgs(args), " "), Args: args,
		Status: "running", StartedAt: time.Now(), Fixes: fixes,
	}}
	cmd := exec.Command(self, args...)
	cmd.Dir = a.Path
	// Tools color their output for terminals only; the panel renders ANSI colors.
	cmd.Env = append(os.Environ(), "FORCE_COLOR=1", "COMPOSER_NO_INTERACTION=1")
	cmd.Stdout, cmd.Stderr = r, r
	stop, err := units.StartGroup(cmd)
	if err != nil {
		return Command{}, err
	}
	r.stop = stop
	m.cmds.add(id, r)

	go func() {
		err := cmd.Wait()
		stop() // anything it left behind (e.g. a dev server started in the background)
		now := time.Now()
		code := cmd.ProcessState.ExitCode()
		r.mu.Lock()
		r.cmd.FinishedAt, r.cmd.ExitCode = &now, &code
		switch {
		case r.halted:
			r.cmd.Status = "stopped"
		case err == nil:
			r.cmd.Status = "ok"
		default:
			r.cmd.Status = "failed"
		}
		r.mu.Unlock()
		// The command may have fixed (or caused) warnings: check again.
		m.forgetServices(id)
		if args[0] == "composer" {
			_, _ = m.Redetect(id)
		}
		if fixes != "" && err == nil {
			if v, verr := m.Get(id); verr == nil && slices.ContainsFunc(v.Warnings, func(w detect.Warning) bool { return w.Code == fixes }) {
				r.mu.Lock()
				r.cmd.Unresolved = true
				r.mu.Unlock()
			}
		}
		m.cmds.save(id)
	}()
	return r.snapshot(false), nil
}

// StopCommand stops a running command and everything it started.
func (m *Manager) StopCommand(id, cmdID string) error {
	r := m.cmds.find(id, cmdID)
	if r == nil {
		return ErrNotFound
	}
	r.mu.Lock()
	running := r.cmd.Status == "running" && r.stop != nil
	if running {
		r.halted = true
	}
	r.mu.Unlock()
	if running {
		r.stop()
	}
	return nil
}

func newCommandID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// quoteArgs shows args as they could be typed again.
func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"'") {
			a = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		}
		out[i] = a
	}
	return out
}
