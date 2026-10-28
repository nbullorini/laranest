// Package mcpserver exposes Laranest to AI assistants through the Model
// Context Protocol: `laranest mcp` runs an MCP server on stdio that the
// assistant's client (Claude Code, Claude Desktop, Cursor, VS Code…) starts.
//
// It is a thin client of the daemon's HTTP API, so an assistant can do
// exactly what the panel does, with the same validations: find and add
// projects, read and change their settings (advanced ones included), start
// and stop them, read their logs and run commands in them.
package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nbullorini/laranest/internal/version"
)

const instructions = `Laranest runs local Laravel projects ("apps"), each with its own PHP version,
processes (web, queue/Horizon, scheduler, Reverb, Vite, custom ones) and an HTTPS
URL at https://<id>.localhost. Laranest does not run databases, Redis or mail
servers: it connects to the ones each project's .env points at.

Typical flow: find_projects or a known path → inspect_project → add_app →
get_app (read its warnings) → fix them (fix_warning, set_env, run_command) →
start_app. Commands run with run_command take what you would type after
"laranest" in the project folder: "php artisan migrate", "composer install",
"npm run build" — with the project's own PHP and Node. Only php, composer, npm,
npx and node can be run. Ask the user before destructive actions (remove_app,
stop_app on apps they use, changing .env values).`

// Run serves MCP on stdio, talking to the daemon at panelAddr ("127.0.0.1:9150").
func Run(ctx context.Context, panelAddr string) error {
	c := &client{base: "http://" + panelAddr + "/api", http: &http.Client{Timeout: 10 * time.Minute}}
	if _, err := c.do(ctx, http.MethodGet, "/health", nil); err != nil {
		return fmt.Errorf("Laranest is not running on %s (start it, then try again): %w", panelAddr, err)
	}
	return newServer(c).Run(ctx, &mcp.StdioTransport{})
}

func newServer(c *client) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "laranest", Title: "Laranest", Version: version.Version},
		&mcp.ServerOptions{Instructions: instructions})
	register(s, c)
	return s
}

// client calls the daemon's JSON API.
type client struct {
	base string
	http *http.Client
}

// apiError is an error the API returned (its message is meant for people).
type apiError struct{ msg string }

func (e apiError) Error() string { return e.msg }

func (c *client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(data, &e)
	if resp.StatusCode >= 300 {
		if e.Error == "" {
			e.Error = resp.Status
		}
		return nil, apiError{e.Error}
	}
	if e.Error != "" { // done, with a problem worth reporting (e.g. a process that didn't start)
		return data, apiError{e.Error}
	}
	return data, nil
}

// logs reads the last lines of a process's output from the daemon's live
// stream (Server-Sent Events): the backlog arrives first.
func (c *client) logs(ctx context.Context, app, process string, lines int) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/apps/"+url.PathEscape(app)+"/logs/"+url.PathEscape(process), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return nil, apiError{e.Error}
	}
	var out []string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() { // ends when the timeout cuts the stream
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var l struct {
			Time   time.Time `json:"time"`
			Stream string    `json:"stream"`
			Text   string    `json:"text"`
		}
		if json.Unmarshal([]byte(data), &l) == nil {
			prefix := ""
			if l.Stream == "sys" {
				prefix = "[laranest] "
			}
			out = append(out, l.Time.Format("15:04:05")+" "+prefix+l.Text)
		}
	}
	if len(out) > lines {
		out = out[len(out)-lines:]
	}
	return out, nil
}

// result turns an API answer (or error) into a tool result: JSON for the
// model to read, or an error it can act on.
func result(data json.RawMessage, err error) (*mcp.CallToolResult, any, error) {
	var api apiError
	if err != nil && !errors.As(err, &api) {
		return nil, nil, err // the daemon is unreachable: a protocol-level failure
	}
	text := string(data)
	if err != nil {
		if len(data) > 0 {
			text = "Done, with a problem: " + api.msg + "\n" + text
		} else {
			text = api.msg
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: err != nil && len(data) == 0}, nil, nil
}

// textResult returns plain text (or JSON of v) to the model.
func textResult(v any) (*mcp.CallToolResult, any, error) {
	text, ok := v.(string)
	if !ok {
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return nil, nil, err
		}
		text = string(data)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
}

func errorResult(msg string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: msg}}, IsError: true}, nil, nil
}
