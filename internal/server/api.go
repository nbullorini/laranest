package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nbullorini/laranest/internal/apps"
	"github.com/nbullorini/laranest/internal/selfupdate"
	"github.com/nbullorini/laranest/internal/units"
	"github.com/nbullorini/laranest/internal/version"
)

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/system", s.handleSystem)
	mux.HandleFunc("PATCH /api/system", s.handleUpdateSystem)
	mux.HandleFunc("GET /api/discover", s.handleDiscover)
	mux.HandleFunc("GET /api/fs", s.handleFS)
	mux.HandleFunc("POST /api/shutdown", s.handleShutdown)
	mux.HandleFunc("GET /api/update", s.handleCheckUpdate)
	mux.HandleFunc("POST /api/update", s.handleUpdate)

	mux.HandleFunc("GET /api/trust", s.handleTrust)
	mux.HandleFunc("POST /api/trust/windows", s.handleTrustWindows)
	mux.HandleFunc("GET /api/wsl", s.handleWSL)
	mux.HandleFunc("PATCH /api/wsl", s.handleSetWSL)
	mux.HandleFunc("GET /api/php", s.handlePHP)
	mux.HandleFunc("POST /api/php/{version}", s.handleInstallPHP)
	mux.HandleFunc("POST /api/php/{version}/update", s.handleUpdatePHP)
	mux.HandleFunc("DELETE /api/php/{version}", s.handleRemovePHP)

	mux.HandleFunc("GET /api/apps", s.handleListApps)
	mux.HandleFunc("POST /api/apps", s.handleAddApp)
	mux.HandleFunc("POST /api/inspect", s.handleInspect)
	mux.HandleFunc("GET /api/apps/{id}", s.handleGetApp)
	mux.HandleFunc("PATCH /api/apps/{id}", s.handleUpdateApp)
	mux.HandleFunc("DELETE /api/apps/{id}", s.handleRemoveApp)
	mux.HandleFunc("POST /api/apps/{id}/start", s.action((*apps.Manager).Start))
	mux.HandleFunc("POST /api/apps/{id}/stop", s.action((*apps.Manager).Stop))
	mux.HandleFunc("POST /api/apps/{id}/restart", s.handleRestart)
	mux.HandleFunc("POST /api/apps/{id}/detect", s.handleRedetect)
	mux.HandleFunc("POST /api/apps/{id}/env", s.handleSetEnv)
	mux.HandleFunc("GET /api/apps/{id}/logs/{process}", s.handleLogs)
	mux.HandleFunc("POST /api/apps/{id}/share", s.handleShare)
	mux.HandleFunc("DELETE /api/apps/{id}/share", s.handleUnshare)
	mux.HandleFunc("GET /api/apps/{id}/dumps", s.handleDumps)
	mux.HandleFunc("POST /api/apps/{id}/dumps", s.handleAddDump)
	mux.HandleFunc("DELETE /api/apps/{id}/dumps", s.handleClearDumps)
	mux.HandleFunc("GET /api/apps/{id}/laravel-log", s.handleLaravelLog)
	mux.HandleFunc("DELETE /api/apps/{id}/laravel-log", s.handleClearLaravelLog)
	mux.HandleFunc("GET /api/apps/{id}/commands", s.handleCommands)
	mux.HandleFunc("POST /api/apps/{id}/commands", s.handleRunCommand)
	mux.HandleFunc("GET /api/apps/{id}/commands/{cmd}", s.handleCommand)
	mux.HandleFunc("POST /api/apps/{id}/commands/{cmd}/stop", s.handleStopCommand)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": version.Version})
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	sys := s.apps.System()
	sys.Version = version.Version
	writeJSON(w, http.StatusOK, sys)
}

func (s *Server) handleUpdateSystem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HTTPPort  int      `json:"http_port"`  // 0 = unchanged
		HTTPSPort int      `json:"https_port"` // 0 = unchanged
		ScanDirs  []string `json:"scan_dirs"`  // nil = unchanged
	}
	if !decode(w, r, &req) {
		return
	}
	if req.ScanDirs != nil {
		if err := s.apps.SetScanDirs(req.ScanDirs); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
	}
	if req.HTTPPort != 0 || req.HTTPSPort != 0 {
		// Bind failures are reported through System, so they aren't API errors;
		// invalid values are.
		if err := s.apps.SetPorts(req.HTTPPort, req.HTTPSPort); err != nil && !strings.Contains(err.Error(), "proxy") {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
	}
	s.handleSystem(w, r)
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	registered := map[string]bool{}
	for _, a := range s.apps.List() {
		registered[a.Path] = true
	}
	type item struct {
		Path       string `json:"path"`
		Registered bool   `json:"registered"`
	}
	items := []item{}
	for _, p := range s.apps.Discover() {
		items = append(items, item{Path: p, Registered: registered[p]})
	}
	writeJSON(w, http.StatusOK, items)
}

// handleFS lists subdirectories for path autocompletion.
func (s *Server) handleFS(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	if prefix == "" {
		prefix, _ = os.UserHomeDir()
		prefix += string(filepath.Separator)
	}
	if strings.HasPrefix(prefix, "~") {
		home, _ := os.UserHomeDir()
		prefix = home + prefix[1:]
	}
	dir, partial := filepath.Dir(prefix), filepath.Base(prefix)
	if strings.HasSuffix(prefix, "/") || strings.HasSuffix(prefix, string(filepath.Separator)) {
		dir, partial = filepath.Clean(prefix), ""
	}
	entries, _ := os.ReadDir(dir)
	out := []string{}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(strings.ToLower(e.Name()), strings.ToLower(partial)) {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(partial, ".") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
		if len(out) == 50 {
			break
		}
	}
	sort.Strings(out)
	writeJSON(w, http.StatusOK, out)
}

// handleCheckUpdate says whether a newer Laranest is published (?refresh=1
// skips the hour-long cache).
func (s *Server) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.updater.Check(r.Context(), r.URL.Query().Get("refresh") != ""))
}

// handleUpdate installs the latest Laranest and restarts the daemon: the
// response goes out before it stops (shutdown waits for running requests).
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := selfupdate.Update(ctx, true); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
}

// handleShutdown stops the daemon (`laranest install`/`uninstall` on Windows,
// where there is no service manager to ask).
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
	s.once.Do(func() { close(s.quit) })
}

func (s *Server) handleTrust(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.apps.Trust())
}

func (s *Server) handleTrustWindows(w http.ResponseWriter, r *http.Request) {
	st, err := s.apps.TrustWindows()
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "trust": st})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleWSL(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.apps.WSL())
}

func (s *Server) handleSetWSL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeepAlive *bool `json:"keep_alive"`
		Autostart *bool `json:"autostart"`
	}
	if !decode(w, r, &req) {
		return
	}
	st, err := s.apps.SetWSL(req.KeepAlive, req.Autostart)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handlePHP lists PHP versions; ?refresh=1 re-checks the catalog for updates.
func (s *Server) handlePHP(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.apps.PHP(r.Context(), r.URL.Query().Get("refresh") != ""))
}

func (s *Server) handleUpdatePHP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	restarted, err := s.apps.UpdatePHP(ctx, r.PathValue("version"))
	if err != nil && restarted == nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	resp := map[string]any{"php": s.apps.PHP(r.Context(), false), "restarted": restarted}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleInstallPHP(w http.ResponseWriter, r *http.Request) {
	// Detached from the request: closing the tab must not abort a half-done download.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := s.apps.InstallPHP(ctx, r.PathValue("version")); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	s.handlePHP(w, r)
}

func (s *Server) handleRemovePHP(w http.ResponseWriter, r *http.Request) {
	if err := s.apps.RemovePHP(r.PathValue("version")); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	s.handlePHP(w, r)
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.apps.List())
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	v, err := s.apps.Get(r.PathValue("id"))
	respond(w, v, err)
}

func (s *Server) handleInspect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !decode(w, r, &req) {
		return
	}
	d, err := s.apps.Inspect(expandHome(req.Path))
	if err != nil && d == nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	resp := map[string]any{"detection": d, "suggested_id": apps.Slugify(d.Name)}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAddApp(w http.ResponseWriter, r *http.Request) {
	var req apps.AddRequest
	if !decode(w, r, &req) {
		return
	}
	req.Path = expandHome(req.Path)
	v, err := s.apps.Add(req)
	if err != nil && v.App == nil {
		writeError(w, statusFor(err), err)
		return
	}
	// Added but failed to start: still 201, the UI shows the error.
	resp := map[string]any{"app": v}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleUpdateApp(w http.ResponseWriter, r *http.Request) {
	var req apps.UpdateRequest
	if !decode(w, r, &req) {
		return
	}
	v, err := s.apps.Update(r.PathValue("id"), req)
	respond(w, v, err)
}

func (s *Server) handleRemoveApp(w http.ResponseWriter, r *http.Request) {
	if err := s.apps.Remove(r.PathValue("id")); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) action(fn func(*apps.Manager, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := fn(s.apps, id); err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		s.handleGetApp(w, r)
	}
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Process string `json:"process"`
	}
	if r.ContentLength > 0 && !decode(w, r, &req) {
		return
	}
	if err := s.apps.Restart(r.PathValue("id"), req.Process); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	s.handleGetApp(w, r)
}

func (s *Server) handleRedetect(w http.ResponseWriter, r *http.Request) {
	v, err := s.apps.Redetect(r.PathValue("id"))
	respond(w, v, err)
}

func (s *Server) handleSetEnv(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Values map[string]string `json:"values"`
	}
	if !decode(w, r, &req) {
		return
	}
	v, err := s.apps.SetEnv(r.PathValue("id"), req.Values)
	respond(w, v, err)
}

// handleShare opens a public tunnel to the app (it waits for its address).
func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	v, err := s.apps.Share(r.PathValue("id"))
	respond(w, v, err)
}

func (s *Server) handleUnshare(w http.ResponseWriter, r *http.Request) {
	v, err := s.apps.Unshare(r.PathValue("id"))
	respond(w, v, err)
}

// handleDumps returns the app's dump() calls after ?after=<id> (the panel polls it).
func (s *Server) handleDumps(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	d, err := s.apps.Dumps(r.PathValue("id"), after)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// handleAddDump receives a dump from the app's dumps process.
func (s *Server) handleAddDump(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	var req struct {
		apps.Dump
		Header string `json:"header"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.apps.AddDump(r.PathValue("id"), req.Dump, req.Header); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleClearDumps(w http.ResponseWriter, r *http.Request) {
	if err := s.apps.ClearDumps(r.PathValue("id")); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleLaravelLog returns the newest entries of the app's storage/logs.
func (s *Server) handleLaravelLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	log, err := s.apps.LaravelLog(r.PathValue("id"), apps.LogQuery{
		File: q.Get("file"), Level: q.Get("level"), Search: q.Get("q"), Limit: min(limit, 1000)})
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, log)
}

func (s *Server) handleClearLaravelLog(w http.ResponseWriter, r *http.Request) {
	if err := s.apps.ClearLaravelLog(r.PathValue("id"), r.URL.Query().Get("file")); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCommands lists the commands run from the panel, newest first.
func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	cmds, err := s.apps.Commands(r.PathValue("id"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, cmds)
}

// handleRunCommand starts `laranest <line>` in the app's folder.
func (s *Server) handleRunCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Line  string `json:"line"`  // "php artisan migrate" ("laranest" optional)
		Fixes string `json:"fixes"` // the warning's code, from its Run button
	}
	if !decode(w, r, &req) {
		return
	}
	cmd, err := s.apps.RunCommand(r.PathValue("id"), req.Line, req.Fixes)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, cmd)
}

// handleCommand returns a command with its output so far (the panel polls it).
func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	cmd, err := s.apps.CommandOutput(r.PathValue("id"), r.PathValue("cmd"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, cmd)
}

func (s *Server) handleStopCommand(w http.ResponseWriter, r *http.Request) {
	if err := s.apps.StopCommand(r.PathValue("id"), r.PathValue("cmd")); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
}

// handleLogs streams a process's journal as Server-Sent Events: backlog first, then live lines.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	unit, err := s.apps.LogUnit(r.PathValue("id"), r.PathValue("process"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	lines := make(chan units.Line, 256)
	go func() {
		defer close(lines)
		_ = units.Follow(r.Context(), unit, 500, func(l units.Line) { lines <- l })
	}()

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return
			}
			data, _ := json.Marshal(l)
			fmt.Fprintf(w, "data: %s\n\n", data)
			// Flush once the burst (e.g. the backlog) is drained, not per line.
			if len(lines) == 0 {
				flusher.Flush()
			}
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// ---- helpers ----

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return false
	}
	return true
}

func respond(w http.ResponseWriter, v apps.View, err error) {
	if err != nil && v.App == nil {
		writeError(w, statusFor(err), err)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"app": v, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"app": v})
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, apps.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, apps.ErrExists):
		return http.StatusConflict
	}
	return http.StatusUnprocessableEntity
}

func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return home + p[1:]
	}
	return p
}
