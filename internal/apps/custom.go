package apps

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/mattn/go-shellwords"

	"github.com/nbullorini/laranest/internal/tools"
)

// Advanced settings: what Laranest runs can be adjusted per app when the
// automatic setup isn't enough. Built-in processes can get another command,
// custom processes can be added, and the web can be served by a command
// listening on a port (Octane, a custom server) instead of PHP-FPM/php-cgi.

// CustomProcess is a process the user added to an app.
type CustomProcess struct {
	Name     string `json:"name"`
	Command  string `json:"command"`
	Disabled bool   `json:"disabled,omitempty"`
}

// WebModePort serves the app through a command listening on {port} instead
// of PHP-FPM / php-cgi.
const WebModePort = "port"

// overridable are the built-in processes whose command can be replaced
// (web only in port mode: PHP-FPM has no command line of its own).
var overridable = []string{"web", "queue", "horizon", "scheduler", "reverb", "vite"}

var (
	customName  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,29}$`)
	placeholder = regexp.MustCompile(`\{([a-z_]+)\}`)
)

// validateAdvanced checks commands, custom processes and the web mode before
// anything is saved or restarted.
func validateAdvanced(commands map[string]string, custom []CustomProcess, webMode string) error {
	if webMode != "" && webMode != WebModePort {
		return fmt.Errorf("unknown web mode %q", webMode)
	}
	for name, line := range commands {
		if !slices.Contains(overridable, name) {
			return fmt.Errorf("the command of %q can't be changed", name)
		}
		if strings.TrimSpace(line) == "" {
			continue // cleared: back to the automatic command
		}
		if err := validateCommand(name, line, name == "web"); err != nil {
			return err
		}
	}
	if webMode == WebModePort {
		web := commands["web"]
		if strings.TrimSpace(web) == "" {
			return errors.New("serving the web through a port needs its command (e.g. php artisan octane:start --port={port})")
		}
		if !strings.Contains(web, "{port}") {
			return errors.New("the web command must listen on {port}: Laranest picks it and points the app's URL there")
		}
	}
	seen := map[string]bool{}
	for _, c := range custom {
		if !customName.MatchString(c.Name) {
			return fmt.Errorf("%q is not a valid process name (lowercase letters, digits and dashes)", c.Name)
		}
		if slices.Contains(processOrder, c.Name) {
			return fmt.Errorf("%q is a built-in process: edit its command instead", c.Name)
		}
		if seen[c.Name] {
			return fmt.Errorf("there are two processes named %q", c.Name)
		}
		seen[c.Name] = true
		if err := validateCommand(c.Name, c.Command, false); err != nil {
			return err
		}
	}
	return nil
}

func validateCommand(name, line string, allowPort bool) error {
	for _, m := range placeholder.FindAllStringSubmatch(line, -1) {
		switch m[1] {
		case "url", "path", "reverb_port":
		case "port":
			if !allowPort {
				return fmt.Errorf("%s: {port} is only for the web command", name)
			}
		default:
			return fmt.Errorf("%s: unknown placeholder {%s} (use {url}, {path}, {reverb_port} or, for the web, {port})", name, m[1])
		}
	}
	args, err := shellwords.Parse(line)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if len(args) == 0 {
		return fmt.Errorf("%s: the command is empty", name)
	}
	return nil
}

// defaultCommands describes what each built-in process runs when not
// overridden, for the settings to show next to the override fields.
func defaultCommands(a *App) map[string]string {
	scheduler := "php artisan schedule:work"
	if major := laravelMajor(a); major > 0 && major < 8 {
		scheduler = "php artisan schedule:run (every minute)"
	}
	return map[string]string{
		"web":       "PHP-FPM (php-cgi on Windows)",
		"queue":     "php artisan queue:work --tries=3",
		"horizon":   "php artisan horizon",
		"scheduler": scheduler,
		"reverb":    "php artisan reverb:start --host=127.0.0.1 --port={reverb_port}",
		"vite":      "npm run dev",
	}
}

// commandLines lists every command an app will run that the user wrote.
func commandLines(a *App) []string {
	var lines []string
	for _, name := range enabledBuiltins(a) {
		if line, ok := a.Commands[name]; ok {
			lines = append(lines, line)
		}
	}
	for _, c := range a.Custom {
		if !c.Disabled {
			lines = append(lines, c.Command)
		}
	}
	return lines
}

// usesTool reports whether a user command starts with one of names (to
// download Node or Composer before starting).
func usesTool(a *App, names ...string) bool {
	for _, line := range commandLines(a) {
		if args, err := shellwords.Parse(line); err == nil && len(args) > 0 && slices.Contains(names, args[0]) {
			return true
		}
	}
	return false
}

// resolveCommand turns a user command into argv: placeholders filled in,
// php/composer/npm/npx/node mapped to the app's own runtimes, and any other
// program looked up in the processes' PATH (systemd doesn't search it).
func (m *Manager) resolveCommand(a *App, line string, prep prepared, path string) ([]string, error) {
	vars := map[string]string{"url": m.appURL(a), "path": a.Path, "port": strconv.Itoa(a.WebPort),
		"reverb_port": strconv.Itoa(a.ReverbPort)}
	line = placeholder.ReplaceAllStringFunc(line, func(p string) string { return vars[p[1:len(p)-1]] })
	args, err := shellwords.Parse(line)
	if err != nil || len(args) == 0 {
		return nil, fmt.Errorf("invalid command %q", line)
	}
	switch args[0] {
	case "php":
		return prep.rt.Command(args[1:]...), nil
	case "composer":
		return prep.rt.Command(append([]string{prep.composer}, args[1:]...)...), nil
	case "npm", "npx", "node":
		return tools.NodeCommand(prep.nodeBin, args[0], args[1:]...), nil
	}
	if !filepath.IsAbs(args[0]) {
		found := lookIn(path, args[0])
		if found == "" {
			return nil, fmt.Errorf("%q is not installed (not found in PATH)", args[0])
		}
		args[0] = found
	}
	return args, nil
}

// lookIn finds an executable in a PATH-style list (with PATHEXT on Windows).
func lookIn(path, name string) string {
	exts := []string{""}
	if runtime.GOOS == "windows" {
		exts = []string{".exe", ".cmd", ".bat", ""}
	}
	for _, dir := range filepath.SplitList(path) {
		for _, ext := range exts {
			p := filepath.Join(dir, name+ext)
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() &&
				(runtime.GOOS == "windows" || info.Mode()&0o111 != 0) {
				return p
			}
		}
	}
	return ""
}

// processNames is every process an app may have, enabled or not (to stop
// them all, or to show the logs of one).
func processNames(a *App) []string {
	names := slices.Clone(processOrder)
	for _, c := range a.Custom {
		names = append(names, c.Name)
	}
	return names
}
