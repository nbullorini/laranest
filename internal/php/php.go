// Package php manages the PHP runtimes Laranest downloads (and, only when
// explicitly enabled, the system's apt ones) and runs per-app FPM pools
// (php-cgi workers on Windows, which has no FPM).
package php

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
)

const (
	SourceSystem   = "system"   // installed with apt (/usr/sbin/php-fpmX.Y)
	SourceLaranest = "laranest" // static build downloaded into the data dir

	// OriginPHPNet marks the official windows.php.net builds Laranest
	// downloads on Windows.
	OriginPHPNet = "php.net"
)

// Binary names inside a runtime dir: Windows has php-cgi instead of FPM.
var cliName, webName = func() (string, string) {
	if runtime.GOOS == "windows" {
		return "php.exe", "php-cgi.exe"
	}
	return "php", "php-fpm"
}()

// Runtime is one installed PHP version with both an FPM and a CLI binary.
type Runtime struct {
	Version string `json:"version"` // "8.4"
	Full    string `json:"full"`    // "8.4.23" (when known)
	Source  string `json:"source"`
	FPM     string `json:"fpm"` // php-cgi.exe on Windows
	CLI     string `json:"cli"`
	// INI is passed with -c; static builds have no default php.ini.
	INI string `json:"ini,omitempty"`
	// Origin is where a Laranest-managed build came from ("laranest" or "static-php").
	Origin string `json:"origin,omitempty"`
	// Build is the sha256 of the installed archive (Laranest builds).
	Build string `json:"build,omitempty"`
	// Settings are extra -d ini overrides (e.g. the CA bundle that trusts
	// Laranest's local HTTPS).
	Settings []string `json:"-"`
}

func (r Runtime) iniArgs() []string {
	var args []string
	if r.INI != "" {
		args = append(args, "-c", r.INI)
	}
	for _, s := range r.Settings {
		args = append(args, "-d", s)
	}
	return args
}

// Command builds argv for running the CLI with args.
func (r Runtime) Command(args ...string) []string {
	cmd := append([]string{r.CLI}, r.iniArgs()...)
	return append(cmd, args...)
}

// Env is environment the runtime needs: on Windows, OpenSSL only finds its
// config (needed by openssl_pkey_new and friends) through OPENSSL_CONF.
func (r Runtime) Env() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	conf := filepath.Join(filepath.Dir(r.CLI), "extras", "ssl", "openssl.cnf")
	if _, err := os.Stat(conf); err != nil {
		return nil
	}
	return []string{"OPENSSL_CONF=" + conf}
}

// CGICommand builds argv for a php-cgi FastCGI worker (Windows), without the
// bind address, which the worker pool adds.
func (r Runtime) CGICommand() []string {
	return append([]string{r.FPM}, r.iniArgs()...)
}

// FPMCommand builds argv for running FPM in the foreground with conf.
func (r Runtime) FPMCommand(conf string) []string {
	cmd := append([]string{r.FPM}, r.iniArgs()...)
	return append(cmd, "-y", conf, "-F")
}

var (
	fpmRe   = regexp.MustCompile(`^php-fpm(\d+\.\d+)$`)
	minorRe = regexp.MustCompile(`^\d+\.\d+$`)
)

// Discover lists installed runtimes, newest first. By default only
// Laranest's own builds count, so every machine behaves the same whatever
// apt installed; withSystem (an advanced opt-in) adds the system's, which
// win over a download of the same version.
func Discover(dataDir string, withSystem bool) []Runtime {
	byVersion := map[string]Runtime{}
	for _, r := range managed(dataDir) {
		byVersion[r.Version] = r
	}
	if withSystem {
		for _, r := range system() {
			byVersion[r.Version] = r
		}
	}
	out := make([]Runtime, 0, len(byVersion))
	for _, r := range byVersion {
		out = append(out, r)
	}
	sortNewestFirst(out)
	return out
}

func system() []Runtime {
	if runtime.GOOS == "windows" {
		return nil
	}
	entries, _ := os.ReadDir("/usr/sbin")
	var out []Runtime
	for _, e := range entries {
		m := fpmRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		cli := "/usr/bin/php" + m[1]
		if _, err := os.Stat(cli); err != nil {
			continue
		}
		out = append(out, Runtime{Version: m[1], Source: SourceSystem, FPM: filepath.Join("/usr/sbin", e.Name()), CLI: cli})
	}
	return out
}

// managed lists runtimes under <dataDir>/php/<X.Y>/ (see Installer).
func managed(dataDir string) []Runtime {
	dirs, _ := filepath.Glob(filepath.Join(dataDir, "php", "*.*"))
	var out []Runtime
	for _, dir := range dirs {
		if !minorRe.MatchString(filepath.Base(dir)) {
			continue // staging dirs (".8.4-123") and leftovers ("8.4.old")
		}
		r := Runtime{
			Version: filepath.Base(dir),
			Source:  SourceLaranest,
			FPM:     filepath.Join(dir, webName),
			CLI:     filepath.Join(dir, cliName),
			INI:     filepath.Join(dir, "php.ini"),
		}
		if full, err := os.ReadFile(filepath.Join(dir, "VERSION")); err == nil {
			r.Full = strings.TrimSpace(string(full))
		}
		if origin, err := os.ReadFile(filepath.Join(dir, "ORIGIN")); err == nil {
			r.Origin = strings.TrimSpace(string(origin))
		}
		if build, err := os.ReadFile(filepath.Join(dir, "BUILD")); err == nil {
			r.Build = strings.TrimSpace(string(build))
		}
		if isExecutable(r.FPM) && isExecutable(r.CLI) {
			out = append(out, r)
		}
	}
	return out
}

func sortNewestFirst(rs []Runtime) {
	sort.Slice(rs, func(i, j int) bool { return versionLess(rs[j].Version, rs[i].Version) })
}

func versionLess(a, b string) bool {
	va, errA := semver.NewVersion(a)
	vb, errB := semver.NewVersion(b)
	if errA != nil || errB != nil {
		return a < b
	}
	return va.LessThan(vb)
}

// CLIVersions lists CLI-only system versions (no FPM), to hint which php-fpm package is missing.
func CLIVersions() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	matches, _ := filepath.Glob("/usr/bin/php[0-9]*.[0-9]*")
	var out []string
	for _, m := range matches {
		out = append(out, strings.TrimPrefix(filepath.Base(m), "php"))
	}
	return out
}

// Satisfies reports whether version (e.g. "8.3") meets a composer constraint (e.g. "^8.2").
func Satisfies(version, constraint string) bool {
	if strings.TrimSpace(constraint) == "" {
		return true
	}
	c, err := semver.NewConstraint(normalizeConstraint(constraint))
	if err != nil {
		return true // unparseable: don't block the user
	}
	// Check the newest patch of the minor, since composer resolves against it.
	v, err := semver.NewVersion(version + ".99")
	if err != nil {
		return false
	}
	return c.Check(v)
}

// normalizeConstraint converts composer syntax ("^8.1|^8.2", ">=8.1 <9",
// "~8.0") to Masterminds/semver's.
func normalizeConstraint(c string) string {
	c = strings.ReplaceAll(c, "||", "|")
	parts := strings.Split(c, "|")
	for i, p := range parts {
		fields := strings.Fields(strings.ReplaceAll(p, ",", " "))
		for j, f := range fields {
			fields[j] = composerTilde(f)
		}
		parts[i] = strings.Join(fields, ", ")
	}
	return strings.Join(parts, " || ")
}

// composerTilde fixes a semantic difference: in Composer "~8.0" (or "~8")
// means >=8.0 <9.0, while semver libraries read it as >=8.0 <8.1. With three
// parts ("~8.0.1" → <8.1) both agree.
func composerTilde(f string) string {
	v, ok := strings.CutPrefix(f, "~")
	if !ok || strings.Count(v, ".") >= 2 {
		return f
	}
	return "^" + v
}

// Requirement is what a project accepts: every constraint must hold (the
// root composer.json plus each locked package, as Composer checks) and the
// version can't be newer than Max (the newest PHP its Laravel supports).
type Requirement struct {
	Constraints []string
	Max         string
}

// Allows reports whether a PHP minor ("8.3") meets the requirement.
func (r Requirement) Allows(version string) bool {
	for _, c := range r.Constraints {
		if !Satisfies(version, c) {
			return false
		}
	}
	return r.Max == "" || !versionLess(r.Max, version)
}

func (r Requirement) String() string {
	var parts []string
	if len(r.Constraints) > 0 && r.Constraints[0] != "" {
		parts = append(parts, r.Constraints[0])
	}
	if r.Max != "" {
		parts = append(parts, "≤ "+r.Max)
	}
	if len(parts) == 0 {
		return "any"
	}
	return strings.Join(parts, ", ")
}

// Pick returns the requested runtime, or the newest one the requirement allows.
func Pick(runtimes []Runtime, requested string, req Requirement) (Runtime, error) {
	for _, r := range runtimes {
		if requested != "" && r.Version == requested {
			return r, nil
		}
	}
	if requested != "" {
		return Runtime{}, fmt.Errorf("PHP %s is not installed", requested)
	}
	for _, r := range runtimes {
		if req.Allows(r.Version) {
			return r, nil
		}
	}
	if len(runtimes) == 0 {
		return Runtime{}, fmt.Errorf("no PHP is installed")
	}
	return Runtime{}, fmt.Errorf("no installed PHP version fits this project (PHP %s)", req)
}

var (
	modulesMu    sync.Mutex
	modulesCache = map[string]map[string]bool{}
	fullCache    = map[string]string{}
)

// FullVersion returns the exact version ("8.4.23"), asking the binary when
// it isn't recorded (system PHP). Cached per binary and modification time,
// so an apt upgrade is noticed.
func (r Runtime) FullVersion() string {
	if r.Full != "" {
		return r.Full
	}
	info, err := os.Stat(r.CLI)
	if err != nil {
		return r.Version
	}
	key := r.CLI + "|" + info.ModTime().String()
	modulesMu.Lock()
	defer modulesMu.Unlock()
	if v, ok := fullCache[key]; ok {
		return v
	}
	argv := r.Command("-r", "echo PHP_VERSION;")
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	if err != nil {
		return r.Version
	}
	fullCache[key] = strings.TrimSpace(string(out))
	return fullCache[key]
}

// Modules returns the extensions compiled into or loaded by the CLI (lower-case,
// as composer names them: "pdo_sqlite", "opcache"…). Cached per binary.
func (r Runtime) Modules() map[string]bool {
	key := r.CLI + "|" + r.INI + "|" + strings.Join(r.Settings, "|")
	modulesMu.Lock()
	defer modulesMu.Unlock()
	if m, ok := modulesCache[key]; ok {
		return m
	}
	argv := r.Command("-m")
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	if err != nil {
		return nil
	}
	m := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.ToLower(strings.TrimSpace(line))
		if name == "" || strings.HasPrefix(name, "[") {
			continue
		}
		m[strings.TrimPrefix(name, "zend ")] = true // "Zend OPcache" → "opcache"
	}
	modulesCache[key] = m
	return m
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if runtime.GOOS == "windows" {
		return err == nil && info.Mode().IsRegular() // no exec bit: the .exe name says it
	}
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

// UpdateAvailable reports whether rel is a better build of the minor than
// the installed rt (whose exact version is full).
func UpdateAvailable(rt Runtime, full string, rel Release) bool {
	if rel.Source == SourceLaranest && rt.Origin != SourceLaranest {
		return true // our full build replaces static-php's generic one
	}
	if rel.SHA256 != "" && rel.Version == full && rel.Source == rt.Origin && rt.Build != rel.SHA256 {
		return true // same version, rebuilt (e.g. with more extensions)
	}
	a, errA := semver.NewVersion(rel.Version)
	b, errB := semver.NewVersion(full)
	return errA == nil && errB == nil && a.GreaterThan(b)
}
