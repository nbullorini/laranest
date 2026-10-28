package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- inputs (the SDK builds each tool's JSON schema from these) ----

type appID struct {
	ID string `json:"id" jsonschema:"the app's id (also its subdomain), as listed by list_apps"`
}

type pathInput struct {
	Path string `json:"path" jsonschema:"absolute path of the Laravel project folder"`
}

type processes struct {
	Queue     *bool `json:"queue,omitempty" jsonschema:"run a queue worker (queue:work)"`
	Horizon   *bool `json:"horizon,omitempty" jsonschema:"run Horizon instead of queue:work (not on Windows)"`
	Scheduler *bool `json:"scheduler,omitempty" jsonschema:"run the scheduler (schedule:work)"`
	Reverb    *bool `json:"reverb,omitempty" jsonschema:"run Reverb, served at wss://reverb-<id>.localhost"`
	Vite      *bool `json:"vite,omitempty" jsonschema:"run the Vite dev server (npm run dev)"`
}

type customProcess struct {
	Name     string `json:"name" jsonschema:"process name: lowercase letters, digits and dashes"`
	Command  string `json:"command" jsonschema:"command to run in the project folder (php, composer, npm, npx and node are the project's)"`
	Disabled bool   `json:"disabled,omitempty" jsonschema:"keep it configured but don't run it"`
}

type addInput struct {
	Path      string     `json:"path" jsonschema:"absolute path of the Laravel project folder"`
	ID        string     `json:"id,omitempty" jsonschema:"id and subdomain (lowercase letters, digits, dashes); default: from the folder name"`
	Group     string     `json:"group,omitempty" jsonschema:"group to show it under in the panel"`
	HTTPS     *bool      `json:"https,omitempty" jsonschema:"serve over HTTPS (default true)"`
	PHP       string     `json:"php,omitempty" jsonschema:"pin a PHP minor version such as 8.3; default: picked from composer.json/composer.lock"`
	Processes *processes `json:"processes,omitempty" jsonschema:"processes to run; default: what Laranest recommends for the project"`
	Start     bool       `json:"start,omitempty" jsonschema:"start it right away"`
}

type updateInput struct {
	ID        string            `json:"id" jsonschema:"the app's current id"`
	NewID     string            `json:"new_id,omitempty" jsonschema:"rename it (changes its subdomain)"`
	Group     *string           `json:"group,omitempty" jsonschema:"group; empty string removes it"`
	HTTPS     *bool             `json:"https,omitempty" jsonschema:"serve over HTTPS"`
	PHP       *string           `json:"php,omitempty" jsonschema:"pin a PHP minor version such as 8.3; empty string = automatic"`
	Processes *processes        `json:"processes,omitempty" jsonschema:"turn built-in processes on or off; omitted ones keep their current value"`
	Commands  map[string]string `json:"commands,omitempty" jsonschema:"advanced: replace the command of built-in processes (keys: web, queue, horizon, scheduler, reverb, vite); an empty value goes back to the automatic command; the whole map replaces the current one. The web command is only used with web_mode=port and must listen on {port}. Placeholders: {url}, {path}, {reverb_port}, {port} (web only)"`
	Custom    []customProcess   `json:"custom,omitempty" jsonschema:"advanced: the app's custom processes; the list replaces the current one (send [] to remove them all)"`
	WebMode   *string           `json:"web_mode,omitempty" jsonschema:"advanced: empty string = PHP-FPM/php-cgi (automatic); port = serve the app with commands.web (Octane, a custom server)"`
	Dumps     *bool             `json:"dumps,omitempty" jsonschema:"capture dump() and dd() in Laranest instead of the page or terminal (read them with get_dumps)"`
	Xdebug    *bool             `json:"xdebug,omitempty" jsonschema:"load Xdebug into the web process for step debugging (starts on a trigger; the IDE listens on port 9003)"`
}

type restartInput struct {
	ID      string `json:"id" jsonschema:"the app's id"`
	Process string `json:"process,omitempty" jsonschema:"restart only this process (web, queue, scheduler, a custom one…); default: the whole app"`
}

type envInput struct {
	ID     string            `json:"id" jsonschema:"the app's id"`
	Values map[string]string `json:"values" jsonschema:"keys and values to set in the project's .env (added or replaced; the rest of the file is kept)"`
}

type commandInput struct {
	ID      string `json:"id" jsonschema:"the app's id"`
	Command string `json:"command" jsonschema:"what you would type after 'laranest' in the project folder, e.g. 'php artisan migrate', 'composer install', 'npm run build'"`
	Wait    int    `json:"wait_seconds,omitempty" jsonschema:"how long to wait for it to finish (default 120, max 600); if it is still running, its id is returned to check later with get_command"`
}

type commandRef struct {
	ID        string `json:"id" jsonschema:"the app's id"`
	CommandID string `json:"command_id" jsonschema:"the command's id, from run_command or list_commands"`
}

type logsInput struct {
	ID      string `json:"id" jsonschema:"the app's id"`
	Process string `json:"process" jsonschema:"web, queue, horizon, scheduler, reverb, vite or a custom process name"`
	Lines   int    `json:"lines,omitempty" jsonschema:"how many of the last lines (default 100, max 500)"`
}

type laravelLogInput struct {
	ID     string `json:"id" jsonschema:"the app's id"`
	Level  string `json:"level,omitempty" jsonschema:"minimum level: debug, info, notice, warning, error (default), critical, alert, emergency"`
	Search string `json:"search,omitempty" jsonschema:"only entries containing this text (case-insensitive)"`
	File   string `json:"file,omitempty" jsonschema:"a file in storage/logs such as laravel-2026-10-28.log; default: the most recent"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many of the newest entries (default 10, max 100)"`
}

type dumpsInput struct {
	ID    string `json:"id" jsonschema:"the app's id"`
	Limit int    `json:"limit,omitempty" jsonschema:"how many of the newest dumps (default 10, max 50)"`
}

type fixInput struct {
	ID   string `json:"id" jsonschema:"the app's id"`
	Code string `json:"code" jsonschema:"the warning's code, from get_app"`
}

type phpInput struct {
	Version string `json:"version" jsonschema:"PHP minor version, e.g. 8.4"`
}

type none struct{}

// ---- tools ----

func ptr(b bool) *bool { return &b }

func register(s *mcp.Server, c *client) {
	read := &mcp.ToolAnnotations{ReadOnlyHint: true}
	safe := &mcp.ToolAnnotations{DestructiveHint: ptr(false)}
	destructive := &mcp.ToolAnnotations{DestructiveHint: ptr(true)}

	mcp.AddTool(s, &mcp.Tool{Name: "laranest_status", Annotations: read,
		Description: "Laranest's version, ports, platform, installed PHP versions and proxy/HTTPS problems, if any."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ none) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodGet, "/system", nil))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_apps", Annotations: read,
		Description: "The apps registered in Laranest: id, URL, path, state (running/partial/stopped), PHP and Laravel versions, running processes and how many warnings each has."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ none) (*mcp.CallToolResult, any, error) {
			data, err := c.do(ctx, http.MethodGet, "/apps", nil)
			if err != nil {
				return result(nil, err)
			}
			var apps []map[string]any
			if err := json.Unmarshal(data, &apps); err != nil {
				return nil, nil, err
			}
			out := make([]map[string]any, 0, len(apps))
			for _, a := range apps {
				out = append(out, summary(a))
			}
			return textResult(out)
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_app", Annotations: read,
		Description: "Everything about one app: settings (processes, PHP, HTTPS, group, advanced commands, custom processes, web mode), " +
			"the automatic commands, what was detected in the project, each process's state and the warnings, " +
			"which may come with a fix (.env values) or a command that fixes them (see fix_warning)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in appID) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodGet, "/apps/"+url.PathEscape(in.ID), nil))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "find_projects", Annotations: read,
		Description: "Laravel projects found in the folders Laranest scans (the home folder by default), and whether each is already registered."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ none) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodGet, "/discover", nil))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "inspect_project", Annotations: read,
		Description: "Detects a project without registering it: Laravel and PHP requirements, packages (Horizon, Reverb, Octane, Livewire, Inertia…), queue driver, scheduled tasks, Vite, and the processes Laranest recommends."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in pathInput) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodPost, "/inspect", map[string]string{"path": in.Path}))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "add_app", Annotations: safe,
		Description: "Registers an existing Laravel project in Laranest (it is never modified). Missing PHP versions are downloaded when it starts."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in addInput) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"path": in.Path, "id": in.ID, "group": in.Group, "php": in.PHP, "start": in.Start}
			if in.HTTPS != nil {
				body["https"] = *in.HTTPS
			}
			if in.Processes != nil {
				// Unset ones take the recommendation, as in the panel.
				d, err := c.do(ctx, http.MethodPost, "/inspect", map[string]string{"path": in.Path})
				if err != nil {
					return result(nil, err)
				}
				var ins struct {
					Detection struct {
						Recommended map[string]bool `json:"recommended"`
					} `json:"detection"`
				}
				_ = json.Unmarshal(d, &ins)
				body["processes"] = merge(ins.Detection.Recommended, in.Processes)
			}
			return result(c.do(ctx, http.MethodPost, "/apps", body))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "update_app", Annotations: safe,
		Description: "Changes an app's settings, advanced ones included: rename, group, HTTPS, PHP version, which processes run, " +
			"custom commands for built-in processes, custom processes, and serving the web through a command on a port (e.g. Laravel Octane: " +
			"web_mode=port with commands.web='php artisan octane:start --port={port}'). The app restarts if needed. Re-detecting never undoes these."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in updateInput) (*mcp.CallToolResult, any, error) {
			body := map[string]any{}
			if in.NewID != "" && in.NewID != in.ID {
				body["id"] = in.NewID
			}
			if in.Group != nil {
				body["group"] = *in.Group
			}
			if in.HTTPS != nil {
				body["https"] = *in.HTTPS
			}
			if in.PHP != nil {
				body["php"] = *in.PHP
			}
			if in.Processes != nil {
				cur, err := c.do(ctx, http.MethodGet, "/apps/"+url.PathEscape(in.ID), nil)
				if err != nil {
					return result(nil, err)
				}
				var v struct {
					App struct {
						Processes map[string]bool `json:"processes"`
					} `json:"app"`
				}
				_ = json.Unmarshal(cur, &v)
				body["processes"] = merge(v.App.Processes, in.Processes)
			}
			if in.Commands != nil {
				body["commands"] = in.Commands
			}
			if in.Custom != nil {
				body["custom"] = in.Custom
			}
			if in.WebMode != nil {
				body["web_mode"] = *in.WebMode
			}
			if in.Dumps != nil {
				body["dumps"] = *in.Dumps
			}
			if in.Xdebug != nil {
				body["xdebug"] = *in.Xdebug
			}
			if len(body) == 0 {
				return errorResult("nothing to change: pass at least one setting")
			}
			return result(c.do(ctx, http.MethodPatch, "/apps/"+url.PathEscape(in.ID), body))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "remove_app", Annotations: destructive,
		Description: "Stops an app's processes and removes it from Laranest. The project's files are not touched."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in appID) (*mcp.CallToolResult, any, error) {
			if _, err := c.do(ctx, http.MethodDelete, "/apps/"+url.PathEscape(in.ID), nil); err != nil {
				return result(nil, err)
			}
			return textResult("Removed " + in.ID + " from Laranest (its files were not touched).")
		})

	mcp.AddTool(s, &mcp.Tool{Name: "start_app", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true},
		Description: "Starts an app's processes (downloading its PHP version first if needed) and serves it at its URL."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in appID) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodPost, "/apps/"+url.PathEscape(in.ID)+"/start", nil))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "stop_app", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), IdempotentHint: true},
		Description: "Stops all of an app's processes; its URL shows a 'stopped' page until it starts again."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in appID) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodPost, "/apps/"+url.PathEscape(in.ID)+"/stop", nil))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "restart_app", Annotations: safe,
		Description: "Restarts a whole app, or one of its processes (e.g. the queue after changing code it runs)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in restartInput) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodPost, "/apps/"+url.PathEscape(in.ID)+"/restart", map[string]string{"process": in.Process}))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "redetect_app", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true},
		Description: "Analyzes the project again (after composer require, .env or schedule changes). The app's settings are kept."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in appID) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodPost, "/apps/"+url.PathEscape(in.ID)+"/detect", nil))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "set_env", Annotations: destructive,
		Description: "Sets values in the project's .env (adding or replacing those keys; comments and the rest are kept). The app's workers restart to pick them up. Confirm with the user first."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in envInput) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodPost, "/apps/"+url.PathEscape(in.ID)+"/env", map[string]any{"values": in.Values}))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "fix_warning", Annotations: destructive,
		Description: "Applies the fix Laranest offers for one of an app's warnings: sets the suggested .env values, or runs the command that fixes it. Use get_app to see the warnings and their codes."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in fixInput) (*mcp.CallToolResult, any, error) {
			data, err := c.do(ctx, http.MethodGet, "/apps/"+url.PathEscape(in.ID), nil)
			if err != nil {
				return result(nil, err)
			}
			var v struct {
				App struct {
					Warnings []struct {
						Code    string            `json:"code"`
						Message string            `json:"message"`
						Hint    string            `json:"hint"`
						Fix     map[string]string `json:"fix"`
						Command string            `json:"command"`
					} `json:"warnings"`
				} `json:"app"`
			}
			if err := json.Unmarshal(data, &v); err != nil {
				return nil, nil, err
			}
			for _, w := range v.App.Warnings {
				if w.Code != in.Code {
					continue
				}
				switch {
				case len(w.Fix) > 0:
					return result(c.do(ctx, http.MethodPost, "/apps/"+url.PathEscape(in.ID)+"/env", map[string]any{"values": w.Fix}))
				case w.Command != "":
					return runCommand(ctx, c, in.ID, w.Command, w.Code, 300)
				}
				return errorResult(fmt.Sprintf("Laranest has no automatic fix for %q (%s). Hint: %s", w.Code, w.Message, w.Hint))
			}
			return errorResult(fmt.Sprintf("the app has no %q warning now (it may be fixed already)", in.Code))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "run_command", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(true)},
		Description: "Runs php, composer, npm, npx or node in the project's folder with its own PHP and Node, like 'laranest <command>' in a terminal there " +
			"(e.g. 'php artisan migrate', 'composer require laravel/horizon', 'npm run build'). Waits for it and returns its exit code and output. " +
			"Commands can't read keyboard input: pass --no-interaction or --force where needed. It stays in the app's command history."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in commandInput) (*mcp.CallToolResult, any, error) {
			return runCommand(ctx, c, in.ID, in.Command, "", in.Wait)
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_command", Annotations: read,
		Description: "The state, exit code and output of a command started with run_command (or from the panel)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in commandRef) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodGet, "/apps/"+url.PathEscape(in.ID)+"/commands/"+url.PathEscape(in.CommandID), nil))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_commands", Annotations: read,
		Description: "The app's command history (newest first): what ran, when, and how it ended."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in appID) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodGet, "/apps/"+url.PathEscape(in.ID)+"/commands", nil))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "stop_command", Annotations: destructive,
		Description: "Stops a running command and everything it started."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in commandRef) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodPost, "/apps/"+url.PathEscape(in.ID)+"/commands/"+url.PathEscape(in.CommandID)+"/stop", nil))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_logs", Annotations: read,
		Description: "The last lines of a process's output (errors, exceptions, requests…). Lines marked [laranest] are Laranest's: started, exited with code N, restarting."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in logsInput) (*mcp.CallToolResult, any, error) {
			n := in.Lines
			if n <= 0 {
				n = 100
			}
			lines, err := c.logs(ctx, in.ID, in.Process, min(n, 500))
			if err != nil {
				return result(nil, err)
			}
			if len(lines) == 0 {
				return textResult("No output yet.")
			}
			return textResult(joinLines(lines))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_laravel_log", Annotations: read,
		Description: "The newest entries of the app's Laravel log (storage/logs), newest first, with the exception and stack trace of each: where errors from requests, jobs and commands end up. Defaults to errors and worse."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in laravelLogInput) (*mcp.CallToolResult, any, error) {
			level, limit := in.Level, in.Limit
			if level == "" {
				level = "error"
			}
			if limit <= 0 {
				limit = 10
			}
			q := url.Values{"level": {level}, "q": {in.Search}, "file": {in.File}, "limit": {strconv.Itoa(min(limit, 100))}}
			data, err := c.do(ctx, http.MethodGet, "/apps/"+url.PathEscape(in.ID)+"/laravel-log?"+q.Encode(), nil)
			if err != nil {
				return result(nil, err)
			}
			var log struct {
				File    string `json:"file"`
				Entries []struct {
					Time, Level, Message, Details string
				} `json:"entries"`
			}
			if err := json.Unmarshal(data, &log); err != nil {
				return nil, nil, err
			}
			if log.File == "" {
				return textResult("The app has no log files in storage/logs yet.")
			}
			if len(log.Entries) == 0 {
				return textResult(fmt.Sprintf("No %s-or-worse entries in %s.", level, log.File))
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%s, newest first:\n", log.File)
			for _, e := range log.Entries {
				fmt.Fprintf(&b, "\n[%s] %s: %s\n", e.Time, strings.ToUpper(e.Level), e.Message)
				if lines := strings.Split(e.Details, "\n"); e.Details != "" {
					if len(lines) > 30 { // the top of the stack trace is what matters
						lines = append(lines[:30], fmt.Sprintf("… %d more lines", len(lines)-30))
					}
					b.WriteString(strings.Join(lines, "\n") + "\n")
				}
			}
			return textResult(b.String())
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_dumps", Annotations: read,
		Description: "The newest dump()/dd() calls of an app, as text with the file and line of each and the request or command that made it. Only apps with dumps capture on (update_app dumps=true) record them."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in dumpsInput) (*mcp.CallToolResult, any, error) {
			data, err := c.do(ctx, http.MethodGet, "/apps/"+url.PathEscape(in.ID)+"/dumps", nil)
			if err != nil {
				return result(nil, err)
			}
			var d struct {
				Dumps []struct {
					Time               time.Time
					Text, File, Origin string
					Line               int
				} `json:"dumps"`
			}
			if err := json.Unmarshal(data, &d); err != nil {
				return nil, nil, err
			}
			if len(d.Dumps) == 0 {
				return textResult("No dumps recorded. Is dumps capture on for this app (get_app shows dumps: true)?")
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 10
			}
			dumps := d.Dumps[max(0, len(d.Dumps)-min(limit, 50)):]
			var b strings.Builder
			for i := len(dumps) - 1; i >= 0; i-- { // newest first
				e := dumps[i]
				fmt.Fprintf(&b, "[%s] %s:%d (%s)\n%s\n\n", e.Time.Format("15:04:05"), e.File, e.Line, e.Origin, e.Text)
			}
			return textResult(b.String())
		})

	mcp.AddTool(s, &mcp.Tool{Name: "php_versions", Annotations: read,
		Description: "Installed PHP versions (with available updates and the apps using each) and the ones that can be downloaded."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ none) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodGet, "/php", nil))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "install_php", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(true)},
		Description: "Downloads a PHP version for Laranest (no administrator rights; it doesn't affect any other PHP on the machine)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in phpInput) (*mcp.CallToolResult, any, error) {
			return result(c.do(ctx, http.MethodPost, "/php/"+url.PathEscape(in.Version), nil))
		})
}

// runCommand starts a command and waits up to wait seconds for it.
func runCommand(ctx context.Context, c *client, app, line, fixes string, wait int) (*mcp.CallToolResult, any, error) {
	data, err := c.do(ctx, http.MethodPost, "/apps/"+url.PathEscape(app)+"/commands", map[string]string{"line": line, "fixes": fixes})
	if err != nil {
		return result(nil, err)
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &started); err != nil {
		return nil, nil, err
	}
	if wait <= 0 {
		wait = 120
	}
	deadline := time.Now().Add(time.Duration(min(wait, 600)) * time.Second)
	for {
		data, err = c.do(ctx, http.MethodGet, "/apps/"+url.PathEscape(app)+"/commands/"+url.PathEscape(started.ID), nil)
		if err != nil {
			return result(nil, err)
		}
		var cmd map[string]any
		if err := json.Unmarshal(data, &cmd); err != nil {
			return nil, nil, err
		}
		if cmd["status"] != "running" || time.Now().After(deadline) {
			if out, ok := cmd["output"].(string); ok && len(out) > 20000 {
				cmd["output"] = "…(earlier output cut)…\n" + out[len(out)-20000:]
			}
			if cmd["status"] == "running" {
				cmd["note"] = "still running: check it later with get_command, or stop it with stop_command"
			}
			if cmd["unresolved"] == true {
				cmd["note"] = "it exited without errors, but the warning it was run for is still there: read its output"
			}
			return textResult(cmd)
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// merge applies the processes given over base (current or recommended).
func merge(base map[string]bool, p *processes) map[string]bool {
	out := map[string]bool{}
	for k, v := range base {
		out[k] = v
	}
	set := func(k string, v *bool) {
		if v != nil {
			out[k] = *v
		}
	}
	set("queue", p.Queue)
	set("horizon", p.Horizon)
	set("scheduler", p.Scheduler)
	set("reverb", p.Reverb)
	set("vite", p.Vite)
	return out
}

// summary is the compact view of an app for list_apps.
func summary(a map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"id", "url", "path", "group", "state", "php_version", "https"} {
		if v, ok := a[k]; ok && v != "" {
			out[k] = v
		}
	}
	if d, ok := a["detection"].(map[string]any); ok {
		out["laravel"] = d["laravel_version"]
	}
	var running []string
	if procs, ok := a["procs"].([]any); ok {
		for _, p := range procs {
			if m, ok := p.(map[string]any); ok {
				running = append(running, fmt.Sprintf("%v:%v", m["name"], m["state"]))
			}
		}
	}
	out["processes"] = running
	warnings := 0
	if ws, ok := a["warnings"].([]any); ok {
		for _, w := range ws {
			if m, ok := w.(map[string]any); ok && m["level"] != "info" {
				warnings++
			}
		}
	}
	out["warnings"] = warnings
	return out
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}
