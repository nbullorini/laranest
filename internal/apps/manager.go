package apps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nbullorini/laranest/internal/config"
	"github.com/nbullorini/laranest/internal/detect"
	"github.com/nbullorini/laranest/internal/dotenv"
	"github.com/nbullorini/laranest/internal/php"
	"github.com/nbullorini/laranest/internal/proxy"
	"github.com/nbullorini/laranest/internal/tools"
	"github.com/nbullorini/laranest/internal/trust"
	"github.com/nbullorini/laranest/internal/units"
	"github.com/nbullorini/laranest/internal/wsl"
)

var (
	ErrNotFound = errors.New("app not found")
	ErrExists   = errors.New("an app with that name or path already exists")
)

// processOrder is the display/start order; web (FPM) always runs.
var processOrder = []string{"web", "queue", "horizon", "scheduler", "reverb", "vite", "dumps"}

// Manager owns the registered apps, their processes and the proxy config.
type Manager struct {
	mu       sync.Mutex
	cfg      config.Config
	stateDir string
	store    *store
	rt       *Runtimes
	proxy    *proxy.Proxy
	units    *units.Manager
	// cgiPorts is the first php-cgi worker port of each running app (Windows).
	cgiPorts map[string]int
	cmds     *commandStore
	dumps    dumpStore

	svcMu sync.Mutex
	svc   map[string]svcEntry // app id → cached service checks
}

// svcEntry caches CheckServices: it opens real connections, too costly to
// repeat on every 3-second panel refresh.
type svcEntry struct {
	key string // invalidates on .env or PHP changes
	at  time.Time
	ws  []detect.Warning
}

const svcTTL = 60 * time.Second

func NewManager(cfg config.Config) (*Manager, error) {
	cfgDir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}
	stateDir, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	st, err := loadStore(filepath.Join(cfgDir, "apps.json"))
	if err != nil {
		return nil, fmt.Errorf("reading apps.json: %w", err)
	}
	um, err := units.New(context.Background(), stateDir)
	if err != nil {
		return nil, err
	}
	rts, err := NewRuntimes(cfg)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		cfg:      cfg,
		stateDir: stateDir,
		store:    st,
		rt:       rts,
		units:    um,
		cgiPorts: map[string]int{},
		cmds:     &commandStore{dir: filepath.Join(stateDir, "commands")},
	}
	m.proxy = proxy.New(m.proxyListen(), m.proxyListenTLS(), "laranest."+cfg.Domain, cfg.PanelAddr, m.PanelURL(), filepath.Join(stateDir, "caddy"))
	return m, nil
}

// Boot starts the proxy and every app that should be running. Apps whose units
// survived a daemon restart are left untouched.
func (m *Manager) Boot() {
	// Warm the catalog so views can tell which versions are downloadable.
	go func() { _, _ = m.rt.Catalog.Releases(context.Background()) }()
	if runtime.GOOS == "windows" {
		// PHP on Windows needs a CA file for any outgoing HTTPS.
		go func() {
			if _, err := m.rt.Tools.PublicCAs(context.Background()); err != nil {
				slog.Warn("public CAs", "err", err)
				return
			}
			m.mu.Lock()
			m.writeCABundleLocked()
			m.mu.Unlock()
		}()
	}

	m.mu.Lock()
	m.applyProxyLocked()
	apps := m.store.list()
	m.mu.Unlock()
	for _, a := range apps {
		if a.Autostart {
			if err := m.Start(a.ID); err != nil {
				slog.Warn("autostart failed", "app", a.ID, "err", err)
			}
		}
	}
}

// Shutdown stops the proxy. App units keep running so a daemon restart or
// upgrade doesn't interrupt queues or websockets (on Linux; on Windows the
// apps stop with the daemon and Boot starts them again).
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.proxy.Stop()
	m.units.Close()
}

// ---- queries ----

// View is an app plus its live state, as returned by the API.
type View struct {
	*App
	URL        string           `json:"url"`
	ReverbURL  string           `json:"reverb_url,omitempty"`
	State      string           `json:"state"` // running | partial | stopped
	PHPVersion string           `json:"php_version"`
	PHPSource  string           `json:"php_source"` // system | laranest | "" (to download)
	Procs      []units.Status   `json:"procs"`
	Warnings   []detect.Warning `json:"warnings"`
	// DefaultCommands is what each built-in process runs unless overridden.
	DefaultCommands map[string]string `json:"default_commands"`
}

func (m *Manager) List() []View {
	m.mu.Lock()
	apps := m.store.list()
	m.mu.Unlock()

	views := make([]View, len(apps))
	var wg sync.WaitGroup
	for i, a := range apps {
		wg.Add(1)
		go func() { defer wg.Done(); views[i] = m.view(a) }()
	}
	wg.Wait()
	return views
}

func (m *Manager) Get(id string) (View, error) {
	m.mu.Lock()
	a := m.store.apps[id]
	m.mu.Unlock()
	if a == nil {
		return View{}, ErrNotFound
	}
	return m.view(a), nil
}

func (m *Manager) view(a *App) View {
	m.mu.Lock()
	v := View{App: a, URL: m.appURL(a), State: "stopped", Procs: []units.Status{}, DefaultCommands: defaultCommands(a)}
	if a.Processes.Reverb {
		scheme := "ws://"
		if a.HTTPS {
			scheme = "wss://"
		}
		v.ReverbURL = scheme + m.reverbHost(a.ID) + m.portSuffix(a)
	}
	rt, download, phpErr := m.rt.Plan(a.PHP, requirement(a))
	v.PHPVersion, v.PHPSource = rt.Version, rt.Source
	if download != "" {
		v.PHPVersion = download
	}

	running, alive := 0, 0
	for _, name := range enabledProcesses(a) {
		st := m.units.Status(context.Background(), unitName(a.ID, name), name)
		switch st.State {
		case units.StateRunning:
			running++
			alive++
		case units.StateStopped, units.StateFailed:
		default:
			alive++
		}
		v.Procs = append(v.Procs, st)
	}
	if a.Processes.Vite {
		v.Warnings = append(v.Warnings, viteWarnings(a, v.Procs)...)
	}
	if alive > 0 {
		v.State = "partial"
		if running == len(v.Procs) {
			v.State = "running"
		}
	}
	ctx := detect.Context{
		URL:          v.URL,
		Processes:    a.Processes,
		ReverbHost:   m.reverbHost(a.ID),
		ReverbPort:   m.publicPort(a),
		ReverbScheme: map[bool]string{true: "https", false: "http"}[a.HTTPS],
		PHPDownload:  download,
		Running:      v.State != "stopped",
		PHPVersion:   v.PHPVersion,
		PHPSource:    rt.Source,
	}
	m.mu.Unlock()

	if rt.CLI != "" {
		ctx.Modules = rt.Modules()
	}
	if phpErr != nil {
		ctx.PHPError = phpErr.Error()
	}
	det := a.Detection
	if det == nil {
		det = &detect.Detection{Path: a.Path}
	}
	v.Warnings = append(append(detect.Check(det, ctx), v.Warnings...), m.serviceWarnings(a, det, ctx)...)
	return v
}

func (m *Manager) serviceWarnings(a *App, det *detect.Detection, ctx detect.Context) []detect.Warning {
	envPath := filepath.Join(a.Path, ".env")
	info, err := os.Stat(envPath)
	if err != nil || !det.IsLaravel {
		return nil
	}
	key := fmt.Sprintf("%d|%d|%s|%s|%d|%t|%s", info.ModTime().UnixNano(), info.Size(), ctx.PHPVersion, ctx.PHPSource, len(ctx.Modules), ctx.Running, ctx.URL)
	m.svcMu.Lock()
	e, ok := m.svc[a.ID]
	m.svcMu.Unlock()
	if ok && e.key == key && time.Since(e.at) < svcTTL {
		return e.ws
	}
	env, err := dotenv.Parse(envPath)
	if err != nil {
		return nil
	}
	ws := detect.CheckServices(det, env, ctx)
	m.svcMu.Lock()
	if m.svc == nil {
		m.svc = map[string]svcEntry{}
	}
	m.svc[a.ID] = svcEntry{key: key, at: time.Now(), ws: ws}
	m.svcMu.Unlock()
	return ws
}

// forgetServices drops cached service checks (re-detect, .env edits).
func (m *Manager) forgetServices(id string) {
	m.svcMu.Lock()
	delete(m.svc, id)
	m.svcMu.Unlock()
}

// Inspect detects a project without registering it.
func (m *Manager) Inspect(path string) (*detect.Detection, error) {
	var cmd []string
	installed := m.rt.Installed()
	if rt, err := php.Pick(installed, "", php.Requirement{}); err == nil {
		cmd = rt.Command()
	}
	d, err := detect.Inspect(path, cmd)
	if err != nil {
		return d, err
	}
	// Re-run artisan probes with the PHP version this project would actually use.
	req := php.Requirement{Constraints: append([]string{d.PHPConstraint}, d.LockPHP...), Max: d.PHPMax}
	if rt, err := php.Pick(installed, "", req); err == nil && !slices.Equal(rt.Command(), cmd) {
		return detect.Inspect(path, rt.Command())
	}
	return d, nil
}

// LogUnit returns the systemd unit whose journal holds a process's output.
func (m *Manager) LogUnit(id, process string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store.apps[id] == nil {
		return "", ErrNotFound
	}
	if !slices.Contains(processNames(m.store.apps[id]), process) {
		return "", fmt.Errorf("unknown process: %q", process)
	}
	return unitName(id, process), nil
}

// ---- mutations ----

type AddRequest struct {
	Path      string            `json:"path"`
	ID        string            `json:"id"`
	Group     string            `json:"group"`
	HTTPS     *bool             `json:"https"` // nil = on (the default for new apps)
	PHP       string            `json:"php"`
	Processes *detect.Processes `json:"processes"` // nil = use recommendations
	Start     bool              `json:"start"`
}

func (m *Manager) Add(req AddRequest) (View, error) {
	d, err := m.Inspect(req.Path)
	if err != nil {
		return View{}, err
	}

	m.mu.Lock()
	id := req.ID
	if id == "" {
		id = Slugify(d.Name)
	}
	if err := m.validateID(id, ""); err != nil {
		m.mu.Unlock()
		return View{}, err
	}
	if m.store.byPath(d.Path) != nil {
		m.mu.Unlock()
		return View{}, ErrExists
	}
	procs := d.Recommended
	if req.Processes != nil {
		procs = *req.Processes
	}
	procs = supported(procs)
	group, err := cleanGroup(req.Group)
	if err != nil {
		m.mu.Unlock()
		return View{}, err
	}
	a := &App{
		ID: id, Name: d.Name, Path: d.Path, PHP: req.PHP, Group: group, HTTPS: req.HTTPS == nil || *req.HTTPS,
		Processes: procs, Detection: d, CreatedAt: time.Now(),
	}
	m.store.apps[id] = a
	m.ensureReverbPortLocked(a)
	if err := m.store.save(); err != nil {
		delete(m.store.apps, id)
		m.mu.Unlock()
		return View{}, err
	}
	m.applyProxyLocked()
	m.mu.Unlock()

	if req.Start {
		err = m.Start(id)
	}
	return m.view(a), err
}

type UpdateRequest struct {
	ID        *string           `json:"id"`
	Group     *string           `json:"group"`
	HTTPS     *bool             `json:"https"`
	PHP       *string           `json:"php"`
	Processes *detect.Processes `json:"processes"`
	// Advanced (nil = unchanged): see custom.go.
	Commands *map[string]string `json:"commands"`
	Custom   *[]CustomProcess   `json:"custom"`
	WebMode  *string            `json:"web_mode"`
	Dumps    *bool              `json:"dumps"`
}

// Update changes settings; a running app is restarted when a setting that
// affects its processes changes (not for cosmetic ones like the group).
func (m *Manager) Update(id string, req UpdateRequest) (View, error) {
	m.mu.Lock()
	a := m.store.apps[id]
	if a == nil {
		m.mu.Unlock()
		return View{}, ErrNotFound
	}
	// Validate everything before changing (or stopping) anything: a rejected
	// rename must leave the app exactly as it was, still running.
	var group string
	if req.Group != nil {
		var err error
		if group, err = cleanGroup(*req.Group); err != nil {
			m.mu.Unlock()
			return View{}, err
		}
	}
	if req.ID != nil && *req.ID != id {
		if err := m.validateID(*req.ID, id); err != nil {
			m.mu.Unlock()
			return View{}, err
		}
	}
	if req.Commands != nil || req.Custom != nil || req.WebMode != nil {
		commands, custom, webMode := a.Commands, a.Custom, a.WebMode
		if req.Commands != nil {
			commands = *req.Commands
		}
		if req.Custom != nil {
			custom = *req.Custom
		}
		if req.WebMode != nil {
			webMode = *req.WebMode
		}
		if err := validateAdvanced(commands, custom, webMode); err != nil {
			m.mu.Unlock()
			return View{}, err
		}
	}
	if req.PHP != nil && *req.PHP != "" {
		// Must be installed or downloadable, or the restart would leave the app down.
		if _, _, err := m.rt.Plan(*req.PHP, php.Requirement{}); err != nil {
			m.mu.Unlock()
			return View{}, err
		}
	}

	// Group and HTTPS only change the panel/proxy: no restart needed.
	if req.Group != nil {
		a.Group = group
	}
	if req.HTTPS != nil {
		a.HTTPS = *req.HTTPS
	}
	if req.ID == nil && req.PHP == nil && req.Processes == nil && req.Commands == nil && req.Custom == nil && req.WebMode == nil &&
		(req.Dumps == nil || *req.Dumps == a.Dumps) {
		err := m.store.save()
		if req.HTTPS != nil {
			m.applyProxyLocked()
		}
		m.mu.Unlock()
		return m.view(a), err
	}
	wasRunning := m.isRunning(a)
	if wasRunning {
		m.stopLocked(a)
	}

	if req.ID != nil && *req.ID != id {
		delete(m.store.apps, id)
		_ = os.RemoveAll(m.appStateDir(id))
		m.cmds.rename(id, *req.ID)
		m.dumps.forget(id)
		a.ID = *req.ID
		m.store.apps[a.ID] = a
	}
	if req.PHP != nil {
		a.PHP = *req.PHP
	}
	if req.Processes != nil {
		a.Processes = supported(*req.Processes)
		m.ensureReverbPortLocked(a)
	}
	if req.Commands != nil {
		a.Commands = map[string]string{}
		for name, line := range *req.Commands {
			if strings.TrimSpace(line) != "" { // empty = back to automatic
				a.Commands[name] = strings.TrimSpace(line)
			}
		}
	}
	if req.Custom != nil {
		a.Custom = *req.Custom
	}
	if req.WebMode != nil {
		a.WebMode = *req.WebMode
		if a.WebMode != WebModePort {
			a.WebPort = 0
		}
	}
	if req.Dumps != nil {
		a.Dumps = *req.Dumps
		if !a.Dumps {
			a.DumpPort = 0
		}
	}
	err := m.store.save()
	m.applyProxyLocked()
	m.mu.Unlock()
	if err == nil && wasRunning {
		err = m.Start(a.ID)
	}
	return m.view(a), err
}

func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.store.apps[id]
	if a == nil {
		return ErrNotFound
	}
	m.stopLocked(a)
	delete(m.store.apps, id)
	_ = os.RemoveAll(m.appStateDir(id))
	m.cmds.forget(id)
	m.dumps.forget(id)
	m.applyProxyLocked()
	return m.store.save()
}

// Start launches the app's processes, downloading its PHP (or Node) first if needed.
func (m *Manager) Start(id string) error {
	m.mu.Lock()
	a := m.store.apps[id]
	m.mu.Unlock()
	if a == nil {
		return ErrNotFound
	}
	m.mu.Lock()
	running := m.isRunning(a)
	m.mu.Unlock()
	if running {
		// Already up (e.g. units that survived a daemon restart): nothing to
		// prepare, so don't resolve or download a runtime it won't use.
		return nil
	}
	prep, err := m.prepare(a) // may download: done without holding m.mu
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	err = m.startLocked(a, prep)
	m.applyProxyLocked()
	return err
}

func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.store.apps[id]
	if a == nil {
		return ErrNotFound
	}
	m.stopLocked(a)
	a.Autostart = false
	m.applyProxyLocked()
	return m.store.save()
}

// Restart restarts one process, or the whole app when process is "".
func (m *Manager) Restart(id, process string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.store.apps[id]
	if a == nil {
		return ErrNotFound
	}
	if process == "" {
		m.mu.Unlock()
		prep, err := m.prepare(a)
		m.mu.Lock()
		if err != nil {
			return err
		}
		m.stopLocked(a)
		err = m.startLocked(a, prep)
		m.applyProxyLocked()
		return err
	}
	if !slices.Contains(enabledProcesses(a), process) {
		return fmt.Errorf("process %q is not enabled", process)
	}
	return m.units.Restart(context.Background(), unitName(id, process))
}

// Redetect re-inspects the project (after composer require, .env edits, etc.).
func (m *Manager) Redetect(id string) (View, error) {
	m.mu.Lock()
	a := m.store.apps[id]
	m.mu.Unlock()
	if a == nil {
		return View{}, ErrNotFound
	}
	d, err := m.Inspect(a.Path)
	if err != nil && d == nil {
		return View{}, err
	}
	m.mu.Lock()
	a.Detection = d
	err = m.store.save()
	m.mu.Unlock()
	m.forgetServices(id)
	return m.view(a), err
}

// SetEnv writes values into the app's .env. Callers must have shown the user the diff.
func (m *Manager) SetEnv(id string, values map[string]string) (View, error) {
	m.mu.Lock()
	a := m.store.apps[id]
	m.mu.Unlock()
	if a == nil {
		return View{}, ErrNotFound
	}
	for k := range values {
		if !validEnvKey(k) {
			return View{}, fmt.Errorf("invalid key: %q", k)
		}
	}
	if err := dotenv.Set(filepath.Join(a.Path, ".env"), values); err != nil {
		return View{}, err
	}
	// Workers keep the old config in memory; FPM re-reads .env per request.
	m.mu.Lock()
	if m.isRunning(a) {
		for _, name := range enabledProcesses(a) {
			if name != "web" && name != "vite" {
				_ = m.units.Restart(context.Background(), unitName(id, name))
			}
		}
	}
	m.mu.Unlock()
	return m.view(a), nil
}

// ---- system / settings ----

type System struct {
	Version    string        `json:"version"`
	OS         string        `json:"os"`         // linux | windows
	Executable string        `json:"executable"` // this laranest binary (for MCP client configs)
	DataDir    string        `json:"data_dir"`   // where PHP & co. are downloaded
	PanelURL   string        `json:"panel_url"`
	Domain     string        `json:"domain"`
	HTTPPort   int           `json:"http_port"`
	ProxyError string        `json:"proxy_error,omitempty"`
	ProxyHint  string        `json:"proxy_hint,omitempty"`
	Runtimes   []php.Runtime `json:"runtimes"`
	CLIOnly    []string      `json:"cli_only"` // PHP versions without FPM
	ScanDirs   []string      `json:"scan_dirs"`
	Installing []string      `json:"installing"`
	HTTPSPort  int           `json:"https_port"`
	TLSError   string        `json:"tls_error,omitempty"`
}

func (m *Manager) System() System {
	m.mu.Lock()
	defer m.mu.Unlock()
	installed := m.rt.Installed()
	s := System{
		OS: runtime.GOOS, DataDir: m.rt.DataDir, Executable: executable(),
		PanelURL: m.PanelURL(), Domain: m.cfg.Domain, HTTPPort: m.cfg.HTTPPort,
		Runtimes: installed, ScanDirs: m.scanDirsLocked(), CLIOnly: []string{},
		Installing: m.rt.Installer.Installing(),
		HTTPSPort:  m.cfg.HTTPSPort,
	}
	if err := m.proxy.TLSErr(); err != nil {
		s.TLSError = err.Error()
	}
	have := map[string]bool{}
	for _, r := range installed {
		have[r.Version] = true
	}
	for _, v := range php.CLIVersions() {
		if !m.cfg.UseSystemPHP {
			break
		}
		if !have[v] {
			s.CLIOnly = append(s.CLIOnly, v)
		}
	}
	if err := m.proxy.Err(); err != nil {
		s.ProxyError = err.Error()
		s.ProxyHint = proxyHint(err, m.cfg.HTTPPort)
	}
	return s
}

func proxyHint(err error, port int) string {
	msg := err.Error()
	if runtime.GOOS == "windows" {
		return windowsPortHint(port)
	}
	switch {
	case strings.Contains(msg, "address already in use"):
		return fmt.Sprintf("Another service is using port %d (apache2/nginx?). Stop it with `sudo systemctl disable --now apache2` or pick another port.", port)
	case strings.Contains(msg, "permission denied"):
		return "Linux does not allow ports below 1024 without privileges. Run `laranest setup-ports` in a terminal " +
			"(without sudo in front: it asks for your password itself, only once) or pick a port ≥ 1024."
	}
	return ""
}

// executable is the installed laranest binary, resolved through symlinks.
func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return "laranest"
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real
	}
	return exe
}

// windowsPortHint explains why port can't be used. Windows' error messages
// are localized, so it probes the port and looks at the error code.
func windowsPortHint(port int) string {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err == nil {
		ln.Close()
		return ""
	}
	switch {
	case errors.Is(err, syscall.Errno(10048)): // WSAEADDRINUSE
		return fmt.Sprintf("Another program is using port %d (a Laranest inside WSL, IIS, Apache, XAMPP…). Stop it or pick another port.", port)
	case errors.Is(err, syscall.Errno(10013)): // WSAEACCES: reserved by Hyper-V/WinNAT
		return fmt.Sprintf("Windows reserves port %d (see `netsh int ipv4 show excludedportrange tcp`): pick another port.", port)
	}
	return ""
}

// SetPorts changes the proxy ports (0 = unchanged), persists and rebinds.
func (m *Manager) SetPorts(httpPort, httpsPort int) error {
	for _, p := range []int{httpPort, httpsPort} {
		if p < 0 || p > 65535 {
			return errors.New("invalid port")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	newHTTP, newHTTPS := m.cfg.HTTPPort, m.cfg.HTTPSPort
	if httpPort != 0 {
		newHTTP = httpPort
	}
	if httpsPort != 0 {
		newHTTPS = httpsPort
	}
	if newHTTP == newHTTPS {
		return errors.New("HTTP and HTTPS cannot use the same port")
	}
	if httpPort != 0 {
		m.cfg.HTTPPort = httpPort
	}
	if httpsPort != 0 {
		m.cfg.HTTPSPort = httpsPort
	}
	if err := config.Save(m.cfg); err != nil {
		return err
	}
	m.proxy.SetListen(m.proxyListen(), m.proxyListenTLS())
	return m.applyProxyLocked()
}

// Discover finds Laravel projects in every scan dir, in parallel.
func (m *Manager) Discover() []string {
	m.mu.Lock()
	dirs := m.scanDirsLocked()
	m.mu.Unlock()

	results := make([][]string, len(dirs))
	var wg sync.WaitGroup
	for i, dir := range dirs {
		wg.Add(1)
		go func() { defer wg.Done(); results[i] = detect.Discover(dir, 4) }()
	}
	wg.Wait()

	var out []string
	for _, r := range results {
		out = append(out, r...)
	}
	slices.Sort(out)
	return slices.Compact(out) // overlapping scan dirs yield duplicates
}

// SetScanDirs validates and persists the directories used by Discover.
func (m *Manager) SetScanDirs(dirs []string) error {
	clean := []string{}
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if d == "~" || strings.HasPrefix(d, "~/") {
			home, _ := os.UserHomeDir()
			d = home + d[1:]
		}
		if !filepath.IsAbs(d) {
			return fmt.Errorf("%q is not an absolute path", d)
		}
		d = filepath.Clean(d)
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			return fmt.Errorf("%q does not exist or is not a directory", d)
		}
		if !slices.Contains(clean, d) {
			clean = append(clean, d)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.ScanDirs = clean
	return config.Save(m.cfg)
}

func (m *Manager) scanDirsLocked() []string {
	if len(m.cfg.ScanDirs) > 0 {
		return m.cfg.ScanDirs
	}
	home, _ := os.UserHomeDir()
	return []string{home}
}

func (m *Manager) PanelURL() string {
	return "http://" + strings.Replace(m.cfg.PanelAddr, "127.0.0.1", "localhost", 1)
}

// ---- internals (m.mu held) ----

// prepared is what starting an app needs that may involve downloads; it is
// resolved before taking m.mu so a PHP download doesn't block the API.
type prepared struct {
	rt       php.Runtime
	nodeBin  string // dir with node/npm, when Vite or a custom command uses them
	composer string // composer.phar, when a custom command uses it
}

func (m *Manager) prepare(a *App) (prepared, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	rt, err := m.rt.Resolve(ctx, a.PHP, requirement(a))
	if err != nil {
		return prepared{}, err
	}
	p := prepared{rt: rt}
	if a.Processes.Vite || usesTool(a, "npm", "npx", "node") {
		if p.nodeBin, err = m.rt.Tools.NodeBinDir(ctx); err != nil {
			return p, err
		}
	}
	if usesTool(a, "composer") {
		if p.composer, err = m.rt.Tools.ComposerPhar(ctx); err != nil {
			return p, err
		}
	}
	return p, nil
}

func (m *Manager) startLocked(a *App, prep prepared) error {
	if !m.isRunning(a) {
		// A stale socket left by a crash would make FPM fail to bind.
		_ = os.Remove(m.socketPath(a.ID))
		// Reverb's port is kept across starts; if another program took it
		// meanwhile, move to a free one (browsers reach Reverb through the
		// proxy, so .env doesn't change).
		if a.Processes.Reverb && a.ReverbPort != 0 && portInUse(a.ReverbPort) {
			old := a.ReverbPort
			a.ReverbPort = 0
			m.ensureReverbPortLocked(a)
			slog.Info("reverb port taken by another program: moved", "app", a.ID, "from", old, "to", a.ReverbPort)
		}
		if a.WebMode == WebModePort && (a.WebPort == 0 || portInUse(a.WebPort)) {
			a.WebPort = m.freePortLocked()
		}
		if a.Dumps && (a.DumpPort == 0 || portInUse(a.DumpPort)) {
			a.DumpPort = m.freePortLocked()
		}
		if runtime.GOOS == "windows" && a.WebMode != WebModePort {
			port, err := m.freeCGIPortsLocked(a.ID)
			if err != nil {
				return err
			}
			m.cgiPorts[a.ID] = port
		}
	}
	specs, err := m.specs(a, prep)
	if len(specs) == 0 {
		return err
	}
	errs := []error{err} // processes whose command can't run; the others start
	for _, s := range specs {
		if err := m.units.Start(context.Background(), s); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
		}
	}
	a.Autostart = true
	errs = append(errs, m.store.save())
	return errors.Join(errs...)
}

// stopLocked stops every possible process of the app, including ones that
// were disabled while running.
func (m *Manager) stopLocked(a *App) {
	// On Windows Vite is killed without a chance to clean up: remove the
	// public/hot it leaves behind, or the app keeps loading assets from a dev
	// server that no longer exists.
	viteRan := runtime.GOOS == "windows" &&
		m.units.Status(context.Background(), unitName(a.ID, "vite"), "vite").State != units.StateStopped
	var wg sync.WaitGroup
	for _, name := range processNames(a) {
		wg.Add(1)
		go func() { defer wg.Done(); _ = m.units.Stop(context.Background(), unitName(a.ID, name)) }()
	}
	wg.Wait()
	delete(m.cgiPorts, a.ID)
	if viteRan {
		_ = os.Remove(filepath.Join(a.Path, "public", "hot"))
	}
}

// cgiWorkers is how many php-cgi processes serve each app on Windows (each
// handles one request at a time: enough for Livewire, Vite and a few tabs).
const cgiWorkers = 4

// freeCGIPortsLocked finds cgiWorkers consecutive free ports for an app's
// php-cgi workers (Windows reserves some ranges, so they are probed).
func (m *Manager) freeCGIPortsLocked(id string) (int, error) {
	used := map[int]bool{}
	for other, p := range m.cgiPorts {
		if other != id {
			for i := range cgiWorkers {
				used[p+i] = true
			}
		}
	}
	for _, a := range m.store.apps {
		used[a.ReverbPort] = true
		used[a.WebPort] = true
		used[a.DumpPort] = true
	}
next:
	for base := 19100; base < 20000; base += cgiWorkers {
		for i := range cgiWorkers {
			if used[base+i] {
				continue next
			}
			ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(base+i))
			if err != nil {
				continue next
			}
			ln.Close()
		}
		return base, nil
	}
	return 0, errors.New("no free ports for the PHP workers (19100-19999)")
}

// fastCGILocked returns the app's PHP upstreams for the proxy.
func (m *Manager) fastCGILocked(a *App) []string {
	if runtime.GOOS != "windows" {
		return []string{"unix/" + m.socketPath(a.ID)}
	}
	base, ok := m.cgiPorts[a.ID]
	if !ok {
		return nil
	}
	addrs := make([]string, cgiWorkers)
	for i := range addrs {
		addrs[i] = "127.0.0.1:" + strconv.Itoa(base+i)
	}
	return addrs
}

// isRunning reports whether the app's web process is up (or coming up).
func (m *Manager) isRunning(a *App) bool {
	st := m.units.Status(context.Background(), unitName(a.ID, "web"), "web").State
	return st != units.StateStopped && st != units.StateFailed
}

func (m *Manager) specs(a *App, prep prepared) ([]units.Spec, error) {
	rt := prep.rt
	var web []string
	if a.WebMode == WebModePort {
		// Filled in below, once PATH is known.
	} else if runtime.GOOS == "windows" {
		self, err := os.Executable()
		if err != nil {
			return nil, err
		}
		web = append([]string{self, "cgi-pool", "--addr", "127.0.0.1:" + strconv.Itoa(m.cgiPorts[a.ID]),
			"--workers", strconv.Itoa(cgiWorkers), "--"}, rt.CGICommand()...)
	} else {
		conf, err := php.WriteFPMConfig(m.appStateDir(a.ID), m.socketPath(a.ID), rt.Version)
		if err != nil {
			return nil, err
		}
		web = rt.FPMCommand(conf)
	}
	// Transient units get systemd's environment, not ours: pass PATH so the
	// project's scripts find npm/node (nvm), composer, etc.
	// Processes that shell out to `php` (Vite plugins, artisan subcommands)
	// must get the app's PHP, not whatever the system has: shims first.
	path := os.Getenv("PATH")
	if shims, err := m.rt.Tools.PHPShims(); err == nil {
		path = shims + string(os.PathListSeparator) + path
	}
	if prep.nodeBin != "" {
		path = prep.nodeBin + string(os.PathListSeparator) + path
	}
	env := []string{"PATH=" + path}
	// Units don't inherit our environment: keep the shims (and artisan
	// subprocesses) on this instance's config/data when they're overridden.
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "GH_TOKEN", "LARANEST_GITHUB_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	if _, err := os.Stat(m.proxy.RootCert()); err == nil {
		env = append(env, "NODE_EXTRA_CA_CERTS="+m.proxy.RootCert()) // Vite & co. trust local HTTPS
	}
	env = append(env, rt.Env()...)
	if a.Dumps {
		env = append(env, m.dumperEnv(a))
	}
	spec := func(name string, cmd ...string) units.Spec {
		return units.Spec{
			Unit:        unitName(a.ID, name),
			Name:        name,
			Description: fmt.Sprintf("Laranest %s (%s)", a.ID, name),
			Cmd:         cmd,
			Dir:         a.Path,
			Env:         env,
		}
	}
	artisan := func(name string, args ...string) units.Spec {
		return spec(name, rt.Command(append([]string{"artisan"}, args...)...)...)
	}
	if a.WebMode == WebModePort {
		var err error
		if web, err = m.resolveCommand(a, a.Commands["web"], prep, path); err != nil {
			return nil, fmt.Errorf("web: %w", err)
		}
	}
	specs := []units.Spec{spec("web", web...)}
	p := a.Processes
	if p.Horizon {
		specs = append(specs, artisan("horizon", "horizon"))
	} else if p.Queue {
		specs = append(specs, artisan("queue", "queue:work", "--tries=3"))
	}
	if p.Scheduler {
		if laravelMajor(a) >= 8 || laravelMajor(a) == 0 {
			specs = append(specs, artisan("scheduler", "schedule:work"))
		} else if runtime.GOOS == "windows" {
			// schedule:work arrived in Laravel 8: run schedule:run every minute.
			self, _ := os.Executable()
			specs = append(specs, spec("scheduler", append([]string{self, "schedule-loop", "--"}, rt.Command("artisan", "schedule:run")...)...))
		} else {
			// schedule:work arrived in Laravel 8; emulate it with schedule:run every minute.
			run := shellJoin(rt.Command("artisan", "schedule:run"))
			specs = append(specs, spec("scheduler", "/bin/sh", "-c",
				"while :; do "+run+"; sleep $((60 - $(date +%S | sed 's/^0//'))); done"))
		}
	}
	if p.Reverb {
		specs = append(specs, artisan("reverb", "reverb:start", "--host=127.0.0.1", "--port="+strconv.Itoa(a.ReverbPort)))
	}
	if p.Vite {
		specs = append(specs, spec("vite", tools.NodeCommand(prep.nodeBin, "npm", "run", "dev")...))
	}
	var dumpsErr error
	if a.Dumps {
		// The dump server must not send its own dumps to itself.
		if cmd, err := m.dumpsCommand(a, prep); err != nil {
			dumpsErr = fmt.Errorf("dumps: %w", err)
		} else {
			s := spec("dumps", cmd...)
			s.Env = slices.DeleteFunc(slices.Clone(env), func(e string) bool { return strings.HasPrefix(e, "VAR_DUMPER_FORMAT=") })
			specs = append(specs, s)
		}
	}
	// Overrides replace the automatic command of a built-in process. A
	// command that can't run (a program that isn't installed) only keeps its
	// own process down: the rest of the app starts, and the error is reported.
	errs := []error{dumpsErr}
	resolved := specs[:0]
	for _, s := range specs {
		if line, ok := a.Commands[s.Name]; ok && s.Name != "web" {
			cmd, err := m.resolveCommand(a, line, prep, path)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
				continue
			}
			s.Cmd = cmd
		}
		resolved = append(resolved, s)
	}
	specs = resolved
	for _, c := range a.Custom {
		if c.Disabled {
			continue
		}
		cmd, err := m.resolveCommand(a, c.Command, prep, path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.Name, err))
			continue
		}
		specs = append(specs, spec(c.Name, cmd...))
	}
	return specs, errors.Join(errs...)
}

// laravelMajor returns the installed framework's major version (0 if unknown).
func laravelMajor(a *App) int {
	if a.Detection == nil {
		return 0
	}
	major, _ := strconv.Atoi(strings.SplitN(strings.TrimLeft(a.Detection.LaravelVersion, "^~v"), ".", 2)[0])
	return major
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

func (m *Manager) applyProxyLocked() error {
	var sites []proxy.Site
	for _, a := range m.store.list() {
		s := proxy.Site{Host: m.appHost(a.ID), Root: filepath.Join(a.Path, "public"), HTTPS: a.HTTPS}
		if a.Dumps && a.DumpPort != 0 {
			s.Env = map[string]string{"VAR_DUMPER_FORMAT": "tcp://127.0.0.1:" + strconv.Itoa(a.DumpPort)}
		}
		if m.isRunning(a) && a.WebMode == WebModePort {
			s.Upstream = "127.0.0.1:" + strconv.Itoa(a.WebPort)
		} else if m.isRunning(a) {
			s.FastCGI = m.fastCGILocked(a)
		}
		if m.isRunning(a) {
			if a.Processes.Reverb {
				s.ReverbHost = m.reverbHost(a.ID)
				s.ReverbAddr = "127.0.0.1:" + strconv.Itoa(a.ReverbPort)
			}
		}
		sites = append(sites, s)
	}
	err := m.proxy.Apply(sites)
	if err != nil {
		slog.Error("proxy", "err", err)
	}
	m.writeCABundleLocked()
	return err
}

// writeCABundleLocked keeps the PHP/Node CA bundle in sync with Laranest's
// CA (which exists once the proxy loaded). On Windows it waits for the public
// CA list: a bundle with only Laranest's CA would break every other HTTPS call.
func (m *Manager) writeCABundleLocked() {
	public := ""
	if runtime.GOOS == "windows" {
		if public = m.rt.Tools.PublicCAsPath(); public == "" {
			return
		}
	}
	if _, err := trust.Bundle(m.proxy.RootCert(), m.rt.CABundle(), public); err != nil {
		slog.Warn("ca bundle", "err", err)
	}
}

func (m *Manager) ensureReverbPortLocked(a *App) {
	if !a.Processes.Reverb || a.ReverbPort != 0 {
		return
	}
	a.ReverbPort = m.freePortLocked()
}

// freePortLocked picks a free port in 18080–18999 that no app has kept for
// Reverb, its web command or its dumps.
func (m *Manager) freePortLocked() int {
	used := map[int]bool{}
	for _, o := range m.store.apps {
		used[o.ReverbPort] = true
		used[o.WebPort] = true
		used[o.DumpPort] = true
	}
	for port := 18080; port < 19000; port++ {
		if used[port] {
			continue
		}
		if ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			ln.Close()
			return port
		}
	}
	return 0
}

func (m *Manager) validateID(id, current string) error {
	if !validSlug.MatchString(id) || len(id) > 50 {
		return fmt.Errorf("%q is not a valid name (lowercase letters, digits and dashes)", id)
	}
	if reservedSlug(id) {
		return fmt.Errorf("%q is reserved", id)
	}
	if id != current && m.store.apps[id] != nil {
		return ErrExists
	}
	return nil
}

func unitName(id, process string) string { return "laranest-" + id + "-" + process + ".service" }

func (m *Manager) appStateDir(id string) string { return filepath.Join(m.stateDir, "apps", id) }

// socketPath lives in the runtime dir because unix socket paths are capped at
// 108 bytes, which deep state dirs (or long slugs) can exceed.
func (m *Manager) socketPath(id string) string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = m.stateDir
	}
	return filepath.Join(base, "laranest", id+".sock")
}

func (m *Manager) appHost(id string) string    { return id + "." + m.cfg.Domain }
func (m *Manager) reverbHost(id string) string { return "reverb-" + id + "." + m.cfg.Domain }
func (m *Manager) proxyListen() string         { return "127.0.0.1:" + strconv.Itoa(m.cfg.HTTPPort) }
func (m *Manager) proxyListenTLS() string      { return "127.0.0.1:" + strconv.Itoa(m.cfg.HTTPSPort) }

func (m *Manager) appURL(a *App) string {
	scheme := "http://"
	if a.HTTPS {
		scheme = "https://"
	}
	return scheme + m.appHost(a.ID) + m.portSuffix(a)
}

// publicPort is the port browsers use for the app (HTTPS or HTTP).
func (m *Manager) publicPort(a *App) int {
	if a.HTTPS {
		return m.cfg.HTTPSPort
	}
	return m.cfg.HTTPPort
}

func (m *Manager) portSuffix(a *App) string {
	port := m.publicPort(a)
	if (a.HTTPS && port == 443) || (!a.HTTPS && port == 80) {
		return ""
	}
	return ":" + strconv.Itoa(port)
}

// viteWarnings explains a Vite dev server that keeps exiting. Laranest
// doesn't pick Vite's port (Vite takes 5173 or the next free one), unless
// the project pins it with strictPort: then another program on it stops it.
func viteWarnings(a *App, procs []units.Status) []detect.Warning {
	for _, p := range procs {
		if p.Name != "vite" || (p.State != units.StateBackoff && p.State != units.StateFailed) {
			continue
		}
		if port, strict := detect.VitePort(a.Path); strict && port > 0 && portInUse(port) {
			return []detect.Warning{{Level: detect.LevelError, Code: "vite_port",
				Message: fmt.Sprintf("Vite can't start: port %d is in use by another program, and vite.config sets strictPort", port),
				Hint:    "Stop that program, or change server.port (or remove strictPort) in vite.config"}}
		}
		return []detect.Warning{{Level: detect.LevelWarning, Code: "vite_exits",
			Message: "The Vite dev server keeps exiting", Hint: "See its logs (Logs → Vite)"}}
	}
	return nil
}

// portInUse reports whether another program listens on a loopback port, on
// IPv4 or IPv6 (Node's "localhost" may be either).
func portInUse(port int) bool {
	for _, addr := range []string{"127.0.0.1", "::1"} {
		ln, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
		if err == nil {
			ln.Close()
			continue
		}
		// WSAEADDRINUSE on Windows; other errors (no IPv6 here) don't count.
		if errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.Errno(10048)) {
			return true
		}
	}
	return false
}

// supported swaps what can't run on this platform: Horizon needs pcntl,
// which PHP doesn't have on Windows, so its queues get queue:work there.
func supported(p detect.Processes) detect.Processes {
	if runtime.GOOS == "windows" && p.Horizon {
		p.Horizon, p.Queue = false, true
	}
	return p
}

// enabledProcesses lists what runs for the app: the built-in processes, then
// the enabled custom ones.
func enabledProcesses(a *App) []string {
	names := enabledBuiltins(a)
	for _, c := range a.Custom {
		if !c.Disabled {
			names = append(names, c.Name)
		}
	}
	return names
}

func enabledBuiltins(a *App) []string {
	on := map[string]bool{
		"web":       true,
		"queue":     a.Processes.Queue && !a.Processes.Horizon,
		"horizon":   a.Processes.Horizon,
		"scheduler": a.Processes.Scheduler,
		"reverb":    a.Processes.Reverb,
		"vite":      a.Processes.Vite,
		"dumps":     a.Dumps,
	}
	var out []string
	for _, name := range processOrder {
		if on[name] {
			out = append(out, name)
		}
	}
	return out
}

func cleanGroup(g string) (string, error) {
	g = strings.Join(strings.Fields(g), " ")
	if len([]rune(g)) > 40 {
		return "", errors.New("the group name is too long (max 40)")
	}
	return g, nil
}

func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if !(r == '_' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// ---- PHP runtimes ----

// PHPVersion is an installed runtime plus what the catalog offers for it.
type PHPVersion struct {
	php.Runtime
	Full string `json:"full"`
	// Latest is the newest downloadable build of this minor; Update is set
	// when it's newer, or a better build (ours vs. static-php's generic set).
	Latest string   `json:"latest,omitempty"`
	Update bool     `json:"update"`
	UsedBy []string `json:"used_by"`
}

type PHPInfo struct {
	Installed  []PHPVersion  `json:"installed"`
	Available  []php.Release `json:"available"` // downloadable minors not installed
	Installing []string      `json:"installing"`
	Error      string        `json:"error,omitempty"` // catalog unreachable
}

// PHP lists every version: installed (with update status and the apps using
// it) and downloadable. refresh re-fetches the catalog ("check for updates").
func (m *Manager) PHP(ctx context.Context, refresh bool) PHPInfo {
	info := PHPInfo{Installed: []PHPVersion{}, Available: []php.Release{}, Installing: m.rt.Installer.Installing()}
	var rels []php.Release
	var err error
	if refresh {
		rels, err = m.rt.Catalog.Refresh(ctx)
	} else {
		rels, err = m.rt.Catalog.Releases(ctx)
	}
	if err != nil {
		info.Error = err.Error()
	}
	latest := map[string]php.Release{}
	for _, r := range rels {
		latest[r.Minor] = r
	}

	usedBy := m.phpUsage()
	for _, rt := range m.rt.Installed() {
		v := PHPVersion{Runtime: rt, Full: rt.FullVersion(), UsedBy: usedBy[rt.Source+"/"+rt.Version]}
		if v.UsedBy == nil {
			v.UsedBy = []string{}
		}
		if rel, ok := latest[rt.Version]; ok && rt.Source == php.SourceLaranest {
			v.Latest = rel.Version
			v.Update = php.UpdateAvailable(rt, v.Full, rel)
		}
		info.Installed = append(info.Installed, v)
		delete(latest, rt.Version)
	}
	for _, r := range rels {
		if _, ok := latest[r.Minor]; ok {
			info.Available = append(info.Available, r)
		}
	}
	return info
}

// phpUsage maps "source/version" to the apps that run on it.
func (m *Manager) phpUsage() map[string][]string {
	m.mu.Lock()
	apps := m.store.list()
	m.mu.Unlock()
	out := map[string][]string{}
	for _, a := range apps {
		if rt, _, err := m.rt.Plan(a.PHP, requirement(a)); err == nil {
			key := rt.Source + "/" + rt.Version
			out[key] = append(out[key], a.ID)
		}
	}
	return out
}

func (m *Manager) InstallPHP(ctx context.Context, minor string) error {
	_, err := m.rt.Installer.Install(ctx, minor)
	return err
}

// UpdatePHP installs the newest build of minor and restarts the running apps
// using it, returning their IDs.
func (m *Manager) UpdatePHP(ctx context.Context, minor string) ([]string, error) {
	if runtime.GOOS == "windows" {
		return m.updatePHPStopped(ctx, minor)
	}
	if _, err := m.rt.Installer.Update(ctx, minor); err != nil {
		return nil, err
	}
	restarted := []string{} // non-nil: the update itself succeeded
	var errs []error
	for _, id := range m.phpUsage()[php.SourceLaranest+"/"+minor] {
		m.mu.Lock()
		a := m.store.apps[id]
		running := a != nil && m.isRunning(a)
		m.mu.Unlock()
		if !running {
			continue
		}
		if err := m.Restart(id, ""); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			continue
		}
		restarted = append(restarted, id)
	}
	return restarted, errors.Join(errs...)
}

// updatePHPStopped updates on Windows, where files of a running program
// can't be replaced: the apps using minor are stopped first and started again.
func (m *Manager) updatePHPStopped(ctx context.Context, minor string) ([]string, error) {
	var running []*App
	for _, id := range m.phpUsage()[php.SourceLaranest+"/"+minor] {
		m.mu.Lock()
		if a := m.store.apps[id]; a != nil && m.isRunning(a) {
			m.stopLocked(a)
			running = append(running, a)
		}
		m.mu.Unlock()
	}
	_, updateErr := m.rt.Installer.Update(ctx, minor)
	restarted := []string{}
	errs := []error{updateErr}
	for _, a := range running {
		if err := m.Start(a.ID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.ID, err))
			continue
		}
		restarted = append(restarted, a.ID)
	}
	if updateErr != nil {
		return nil, errors.Join(errs...)
	}
	return restarted, errors.Join(errs...)
}

// RemovePHP deletes a downloaded version unless a running app uses it.
func (m *Manager) RemovePHP(minor string) error {
	for _, id := range m.phpUsage()[php.SourceLaranest+"/"+minor] {
		m.mu.Lock()
		a := m.store.apps[id]
		running := a != nil && m.isRunning(a)
		m.mu.Unlock()
		if running {
			return fmt.Errorf("app %s is using PHP %s: stop it first", id, minor)
		}
	}
	return m.rt.Installer.Remove(minor)
}

// ---- WSL ----

func (m *Manager) wslEnv() wsl.Env {
	m.mu.Lock()
	defer m.mu.Unlock()
	return wsl.Env{Distro: m.cfg.WSLDistro, Profile: m.cfg.WindowsProfile}
}

func (m *Manager) WSL() wsl.Status { return m.wslEnv().Status() }

// SetWSL changes the keep-alive / autostart settings (nil = unchanged).
func (m *Manager) SetWSL(keepAlive, autostart *bool) (wsl.Status, error) {
	env := m.wslEnv()
	if keepAlive != nil {
		if err := env.SetKeepAlive(*keepAlive); err != nil {
			return env.Status(), err
		}
		if !*keepAlive {
			off := false
			autostart = &off // pointless without keep-alive: WSL would stop it again
		}
	}
	if autostart != nil {
		if err := env.SetAutostart(*autostart); err != nil {
			return env.Status(), err
		}
	}
	return env.Status(), nil
}

// ---- local HTTPS trust ----

func (m *Manager) Trust() trust.Status { return trust.Check(m.proxy.RootCert(), m.wslEnv()) }

// TrustWindows installs the CA in the Windows user store (WSL). Windows shows
// a confirmation dialog on the desktop that the user has to accept.
func (m *Manager) TrustWindows() (trust.Status, error) {
	err := trust.InstallWindows(m.proxy.RootCert(), m.wslEnv())
	return m.Trust(), err
}

func (m *Manager) RootCert() string { return m.proxy.RootCert() }
