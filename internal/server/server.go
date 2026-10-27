// Package server hosts the panel UI and its JSON API.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nbullorini/laranest/internal/apps"
	"github.com/nbullorini/laranest/internal/config"
	"github.com/nbullorini/laranest/internal/selfupdate"
)

type Server struct {
	cfg  config.Config
	ui   fs.FS
	apps *apps.Manager
	http *http.Server
	quit chan struct{} // closed by POST /api/shutdown
	once sync.Once

	updater selfupdate.Updater
}

func New(cfg config.Config, ui fs.FS, mgr *apps.Manager) *Server {
	s := &Server{cfg: cfg, ui: ui, apps: mgr, quit: make(chan struct{})}

	mux := http.NewServeMux()
	s.routes(mux)
	mux.Handle("/", spaHandler(ui))

	s.http = &http.Server{
		Addr:              cfg.PanelAddr,
		Handler:           guard(cfg, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// Serve serves on ln until ctx is cancelled or the API is asked to shut
// down, then stops gracefully.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	slog.Info("panel listening", "url", "http://"+ln.Addr().String())

	errCh := make(chan error, 1)
	go func() { errCh <- s.http.Serve(ln) }()

	select {
	case <-s.quit:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.http.Shutdown(shutdownCtx)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.http.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// spaHandler serves the embedded UI, falling back to index.html for client-side routes.
func spaHandler(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" {
			if _, err := fs.Stat(ui, name); err != nil {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		files.ServeHTTP(w, r)
	})
}
