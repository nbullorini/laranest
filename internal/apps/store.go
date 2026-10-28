// Package apps manages the registered Laravel apps and their lifecycle.
package apps

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nbullorini/laranest/internal/detect"
)

// App is a registered project, as persisted in apps.json.
type App struct {
	ID         string           `json:"id"` // slug, also the subdomain
	Name       string           `json:"name"`
	Group      string           `json:"group"` // free-form, for grouping in the panel; "" = none
	Path       string           `json:"path"`
	PHP        string           `json:"php"`   // "" = pick automatically
	HTTPS      bool             `json:"https"` // served over TLS with Laranest's local CA
	Processes  detect.Processes `json:"processes"`
	Autostart  bool             `json:"autostart"` // was running when last touched; restarted with the daemon
	ReverbPort int              `json:"reverb_port"`
	// Advanced settings (see custom.go): command overrides of built-in
	// processes, extra processes, and serving the web through a port.
	Commands  map[string]string `json:"commands,omitempty"`
	Custom    []CustomProcess   `json:"custom,omitempty"`
	WebMode   string            `json:"web_mode,omitempty"`
	WebPort   int               `json:"web_port,omitempty"`
	Detection *detect.Detection `json:"detection"`
	CreatedAt time.Time         `json:"created_at"`
}

type store struct {
	path string
	apps map[string]*App
}

func loadStore(path string) (*store, error) {
	s := &store{path: path, apps: map[string]*App{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*App
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for _, a := range list {
		s.apps[a.ID] = a
	}
	return s, nil
}

func (s *store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.list(), "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *store) list() []*App {
	out := make([]*App, 0, len(s.apps))
	for _, a := range s.apps {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *store) byPath(path string) *App {
	for _, a := range s.apps {
		if a.Path == path {
			return a
		}
	}
	return nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify turns a folder name into a valid DNS label.
func Slugify(name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 50 {
		s = strings.TrimRight(s[:50], "-")
	}
	if s == "" {
		s = "app"
	}
	return s
}

var validSlug = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func reservedSlug(id string) bool {
	return id == "laranest" || strings.HasPrefix(id, "reverb-")
}
