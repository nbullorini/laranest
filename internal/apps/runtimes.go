package apps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nbullorini/laranest/internal/config"
	"github.com/nbullorini/laranest/internal/detect"
	"github.com/nbullorini/laranest/internal/php"
	"github.com/nbullorini/laranest/internal/tools"
)

// Runtimes picks, and if needed downloads, the PHP (and Composer/Node) an
// app or a directory should use. Shared by the daemon and the CLI.
type Runtimes struct {
	DataDir    string
	DefaultPHP string
	UseSystem  bool
	Catalog    *php.Catalog
	Installer  *php.Installer
	Tools      *tools.Tools
}

func NewRuntimes(cfg config.Config) (*Runtimes, error) {
	dataDir, err := config.DataDir()
	if err != nil {
		return nil, err
	}
	cat := &php.Catalog{ManifestURL: cfg.PHPManifestURL}
	return &Runtimes{
		DataDir:    dataDir,
		DefaultPHP: cfg.DefaultPHP,
		UseSystem:  cfg.UseSystemPHP,
		Catalog:    cat,
		Installer:  &php.Installer{DataDir: dataDir, Catalog: cat},
		Tools:      &tools.Tools{DataDir: dataDir},
	}, nil
}

// CABundle is the system CAs plus Laranest's local CA (see trust.Bundle).
func (rs *Runtimes) CABundle() string { return filepath.Join(rs.DataDir, "ca", "bundle.pem") }

// Installed lists the runtimes, configured to trust Laranest's local HTTPS
// when its CA bundle exists (apps calling https://*.localhost, Reverb, etc.).
func (rs *Runtimes) Installed() []php.Runtime {
	rts := php.Discover(rs.DataDir, rs.UseSystem)
	if _, err := os.Stat(rs.CABundle()); err == nil {
		for i := range rts {
			rts[i].Settings = []string{"openssl.cafile=" + rs.CABundle(), "curl.cainfo=" + rs.CABundle()}
		}
	}
	return rts
}

// Plan resolves without side effects: the installed runtime to use, or the
// version that would be downloaded (from the already-fetched catalog).
func (rs *Runtimes) Plan(requested string, req php.Requirement) (rt php.Runtime, download string, err error) {
	rt, err = php.Pick(rs.Installed(), requested, req)
	if err == nil {
		return rt, "", nil
	}
	if minor := rs.installable(requested, req, rs.Catalog.Cached()); minor != "" {
		return php.Runtime{}, minor, nil
	}
	if cached := rs.Catalog.Cached(); cached != nil {
		return php.Runtime{}, "", unavailable(requested, req, cached)
	}
	return php.Runtime{}, "", err
}

// Resolve returns the runtime to use, downloading it if necessary.
func (rs *Runtimes) Resolve(ctx context.Context, requested string, req php.Requirement) (php.Runtime, error) {
	if rt, err := php.Pick(rs.Installed(), requested, req); err == nil {
		return rt, nil
	}
	rels, err := rs.Catalog.Releases(ctx)
	if err != nil {
		return php.Runtime{}, err
	}
	minor := rs.installable(requested, req, rels)
	if minor == "" {
		return php.Runtime{}, unavailable(requested, req, rels)
	}
	if _, err := rs.Installer.Install(ctx, minor); err != nil {
		return php.Runtime{}, err
	}
	return php.Pick(rs.Installed(), minor, php.Requirement{}) // with Settings applied
}

// unavailable explains why no PHP Laranest can provide fits the project.
func unavailable(requested string, req php.Requirement, rels []php.Release) error {
	if requested != "" {
		return fmt.Errorf("PHP %s is neither installed nor available for download", requested)
	}
	if len(rels) == 0 {
		return fmt.Errorf("there are no PHP versions available for download")
	}
	oldest, newest := rels[len(rels)-1].Minor, rels[0].Minor
	return fmt.Errorf("this project needs PHP %s and Laranest offers %s to %s: no compatible version", req, oldest, newest)
}

func (rs *Runtimes) installable(requested string, req php.Requirement, rels []php.Release) string {
	for _, r := range rels { // newest first
		if requested != "" {
			if r.Minor == requested {
				return r.Minor
			}
			continue
		}
		if req.Allows(r.Minor) {
			return r.Minor
		}
	}
	return ""
}

// ResolveDir picks the runtime for a working directory: the registered app
// containing it, else the nearest composer.json's PHP constraint, else the
// default version.
func (rs *Runtimes) ResolveDir(ctx context.Context, dir string) (php.Runtime, error) {
	requested, req := rs.DefaultPHP, php.Requirement{}
	if a := appForDir(dir); a != nil {
		requested, req = a.PHP, requirement(a)
	} else if root, ok := composerRoot(dir); ok {
		requested, req = "", requirementForDir(root)
	}
	if requested == "" && len(req.Constraints) == 0 && req.Max == "" {
		// Outside any project: newest installed, or download the newest.
		if rts := rs.Installed(); len(rts) > 0 {
			return rts[0], nil
		}
	}
	return rs.Resolve(ctx, requested, req)
}

// requirement is the PHP an app accepts. It's read from composer.json/lock
// on disk rather than from the stored detection, which goes stale after a
// `composer update` (or predates the fields).
func requirement(a *App) php.Requirement { return requirementForDir(a.Path) }

var (
	reqMu    sync.Mutex
	reqCache = map[string]reqEntry{}
)

type reqEntry struct {
	key string
	req php.Requirement
}

// requirementForDir caches by the mtimes of composer.json and composer.lock.
func requirementForDir(root string) php.Requirement {
	key := ""
	for _, f := range []string{"composer.json", "composer.lock"} {
		if info, err := os.Stat(filepath.Join(root, f)); err == nil {
			key += fmt.Sprint(info.ModTime().UnixNano(), info.Size(), ";")
		}
	}
	reqMu.Lock()
	e, ok := reqCache[root]
	reqMu.Unlock()
	if ok && e.key == key {
		return e.req
	}
	c, lock, max, _ := detect.PHPRequirementForDir(root)
	req := php.Requirement{Constraints: append([]string{c}, lock...), Max: max}
	reqMu.Lock()
	reqCache[root] = reqEntry{key, req}
	reqMu.Unlock()
	return req
}

// appForDir finds the registered app whose path contains dir.
func appForDir(dir string) *App {
	cfgDir, err := config.ConfigDir()
	if err != nil {
		return nil
	}
	st, err := loadStore(filepath.Join(cfgDir, "apps.json"))
	if err != nil {
		return nil
	}
	var best *App
	for _, a := range st.apps {
		if (dir == a.Path || strings.HasPrefix(dir, a.Path+string(os.PathSeparator))) &&
			(best == nil || len(a.Path) > len(best.Path)) {
			best = a
		}
	}
	return best
}

// composerRoot walks up from dir to the nearest directory with a composer.json.
func composerRoot(dir string) (string, bool) {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "composer.json")); err == nil {
			return d, true
		}
		if parent := filepath.Dir(d); parent == d {
			return "", false
		}
	}
}
