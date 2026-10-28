package apps

import (
	"bytes"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Dumps: when an app captures them, dump() and dd() go to Laranest instead of
// the page or the terminal. Its processes get VAR_DUMPER_FORMAT pointing at a
// per-app "dumps" process (Symfony's own DumpServer, with the project's
// vendor/), which renders each dump and posts it to the daemon.

//go:embed dump-server.php
var dumpServerScript []byte

// Dump is one dump() call.
type Dump struct {
	ID     int       `json:"id"`
	Time   time.Time `json:"time"`
	HTML   string    `json:"html"` // Symfony's HtmlDumper output, without the header
	Text   string    `json:"text"` // the same as plain text
	File   string    `json:"file,omitempty"`
	Line   int       `json:"line,omitempty"`
	Name   string    `json:"name,omitempty"`   // the file's short name
	Origin string    `json:"origin,omitempty"` // "GET /users" or the command line
}

// Dumps is what the panel polls: the dumps after a given id.
type Dumps struct {
	Header string `json:"header"` // CSS/JS the HTML dumps need
	Dumps  []Dump `json:"dumps"`  // oldest first
	Last   int    `json:"last"`   // id of the newest dump kept
}

// maxDumps per app are kept in memory; older ones are dropped.
const maxDumps = 200

type dumpLog struct {
	mu     sync.Mutex
	next   int
	header string
	dumps  []Dump
}

type dumpStore struct {
	mu   sync.Mutex
	apps map[string]*dumpLog
}

func (s *dumpStore) log(id string) *dumpLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.apps == nil {
		s.apps = map[string]*dumpLog{}
	}
	if s.apps[id] == nil {
		s.apps[id] = &dumpLog{}
	}
	return s.apps[id]
}

func (s *dumpStore) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.apps, id)
}

// AddDump records a dump posted by the app's dumps process.
func (m *Manager) AddDump(id string, d Dump, header string) error {
	m.mu.Lock()
	a := m.store.apps[id]
	m.mu.Unlock()
	if a == nil {
		return ErrNotFound
	}
	l := m.dumps.log(id)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	d.ID = l.next
	if d.Time.IsZero() {
		d.Time = time.Now()
	}
	if header != "" {
		l.header = header
	}
	l.dumps = append(l.dumps, d)
	if len(l.dumps) > maxDumps {
		l.dumps = l.dumps[len(l.dumps)-maxDumps:]
	}
	return nil
}

// Dumps returns the dumps recorded after the given id.
func (m *Manager) Dumps(id string, after int) (Dumps, error) {
	m.mu.Lock()
	a := m.store.apps[id]
	m.mu.Unlock()
	if a == nil {
		return Dumps{}, ErrNotFound
	}
	l := m.dumps.log(id)
	l.mu.Lock()
	defer l.mu.Unlock()
	out := Dumps{Header: l.header, Dumps: []Dump{}, Last: l.next}
	for _, d := range l.dumps {
		if d.ID > after {
			out.Dumps = append(out.Dumps, d)
		}
	}
	return out, nil
}

// ClearDumps forgets the app's dumps (ids keep growing).
func (m *Manager) ClearDumps(id string) error {
	m.mu.Lock()
	a := m.store.apps[id]
	m.mu.Unlock()
	if a == nil {
		return ErrNotFound
	}
	l := m.dumps.log(id)
	l.mu.Lock()
	l.dumps = nil
	l.mu.Unlock()
	return nil
}

// dumpsCommand is the dumps process of an app: the dump server script,
// written next to Laranest's data, run with the app's PHP.
func (m *Manager) dumpsCommand(a *App, prep prepared) ([]string, error) {
	if _, err := os.Stat(filepath.Join(a.Path, "vendor", "symfony", "var-dumper")); err != nil {
		return nil, errors.New("capturing dumps needs the project's Composer dependencies (symfony/var-dumper): run composer install")
	}
	script := filepath.Join(m.rt.DataDir, "dump-server.php")
	if old, err := os.ReadFile(script); err != nil || !bytes.Equal(old, dumpServerScript) {
		if err := os.WriteFile(script, dumpServerScript, 0o644); err != nil {
			return nil, err
		}
	}
	endpoint := "http://" + m.cfg.PanelAddr + "/api/apps/" + a.ID + "/dumps"
	return prep.rt.Command(script, strconv.Itoa(a.DumpPort), a.Path, endpoint), nil
}

func (m *Manager) dumperEnv(a *App) string {
	return "VAR_DUMPER_FORMAT=tcp://127.0.0.1:" + strconv.Itoa(a.DumpPort)
}
