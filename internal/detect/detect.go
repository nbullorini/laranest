// Package detect inspects a directory to figure out what a Laravel app needs.
package detect

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Detection is a snapshot of what a project is and what it needs. It is taken
// when the app is added (and on demand), since some probes are slow.
type Detection struct {
	Path           string `json:"path"`
	IsLaravel      bool   `json:"is_laravel"`
	Name           string `json:"name"`
	LaravelVersion string `json:"laravel_version"`
	PHPConstraint  string `json:"php_constraint"`
	// LockPHP are the distinct PHP constraints of the locked packages, and
	// PHPMax the newest PHP the installed Laravel supports ("" = no limit).
	LockPHP   []string        `json:"lock_php,omitempty"`
	PHPMax    string          `json:"php_max,omitempty"`
	Packages  map[string]bool `json:"packages"` // horizon, reverb, octane, pulse, livewire, inertia
	UsesVite  bool            `json:"uses_vite"`
	HasEnv    bool            `json:"has_env"`
	HasVendor bool            `json:"has_vendor"`
	// ScheduledTasks is the number of tasks reported by `schedule:list`, or -1 if unknown.
	ScheduledTasks int       `json:"scheduled_tasks"`
	Recommended    Processes `json:"recommended"`
	DetectedAt     time.Time `json:"detected_at"`
}

// Processes are the optional long-running processes of an app (web is always on).
type Processes struct {
	Queue     bool `json:"queue"`
	Horizon   bool `json:"horizon"`
	Scheduler bool `json:"scheduler"`
	Reverb    bool `json:"reverb"`
	Vite      bool `json:"vite"`
}

var trackedPackages = map[string]string{
	"laravel/horizon":           "horizon",
	"laravel/reverb":            "reverb",
	"laravel/octane":            "octane",
	"laravel/pulse":             "pulse",
	"livewire/livewire":         "livewire",
	"inertiajs/inertia-laravel": "inertia",
}

var ErrNotLaravel = errors.New("this does not look like a Laravel project (no artisan or laravel/framework)")

// Inspect detects the project at path. phpCmd (the PHP CLI argv prefix) is
// used to run artisan probes; pass nil to skip them.
func Inspect(path string, phpCmd []string) (*Detection, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return nil, errors.New("the directory does not exist")
	}

	d := &Detection{
		Path:           path,
		Name:           filepath.Base(path),
		Packages:       map[string]bool{},
		ScheduledTasks: -1,
		DetectedAt:     time.Now(),
	}

	composer, _ := readComposer(filepath.Join(path, "composer.json"))
	_, artisanErr := os.Stat(filepath.Join(path, "artisan"))
	if artisanErr != nil || composer == nil || composer.Require["laravel/framework"] == "" {
		return d, ErrNotLaravel
	}
	d.IsLaravel = true
	d.PHPConstraint = composer.Require["php"]
	d.LaravelVersion = lockedVersion(path, "laravel/framework")
	if d.LaravelVersion == "" {
		d.LaravelVersion = composer.Require["laravel/framework"]
	}
	d.LockPHP = lockPHPConstraints(path)
	d.PHPMax = MaxPHPForLaravel(d.LaravelVersion)
	for pkg, key := range trackedPackages {
		if composer.Require[pkg] != "" || composer.RequireDev[pkg] != "" {
			d.Packages[key] = true
		}
	}

	d.HasEnv = exists(filepath.Join(path, ".env"))
	d.HasVendor = exists(filepath.Join(path, "vendor", "autoload.php"))
	d.UsesVite = exists(filepath.Join(path, "vite.config.js")) || exists(filepath.Join(path, "vite.config.ts"))

	if len(phpCmd) > 0 && d.HasVendor && d.HasEnv {
		d.ScheduledTasks = countScheduledTasks(path, phpCmd)
	}

	d.Recommended = recommend(d, loadEnv(path))
	return d, nil
}

func recommend(d *Detection, env map[string]string) Processes {
	queue := env["QUEUE_CONNECTION"]
	broadcast := firstNonEmpty(env["BROADCAST_CONNECTION"], env["BROADCAST_DRIVER"])
	var p Processes
	// Horizon needs pcntl, which PHP doesn't have on Windows: plain workers there.
	if d.Packages["horizon"] && queue == "redis" && runtime.GOOS != "windows" {
		p.Horizon = true
	} else if queue != "" && queue != "sync" && queue != "null" {
		p.Queue = true
	}
	p.Scheduler = d.ScheduledTasks > 0
	p.Reverb = d.Packages["reverb"] && broadcast == "reverb"
	return p
}

type composerJSON struct {
	Require    map[string]string `json:"require"`
	RequireDev map[string]string `json:"require-dev"`
}

func readComposer(path string) (*composerJSON, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c composerJSON
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// lockedVersion returns the installed version of pkg from composer.lock.
func lockedVersion(root, pkg string) string {
	data, err := os.ReadFile(filepath.Join(root, "composer.lock"))
	if err != nil {
		return ""
	}
	var lock struct {
		Packages []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"packages"`
	}
	if json.Unmarshal(data, &lock) != nil {
		return ""
	}
	for _, p := range lock.Packages {
		if p.Name == pkg {
			return strings.TrimPrefix(p.Version, "v")
		}
	}
	return ""
}

var cronLineRe = regexp.MustCompile(`^\s*(\S+\s+){4}\S+\s+`)

func countScheduledTasks(root string, phpCmd []string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := append(append([]string{}, phpCmd[1:]...), "artisan", "schedule:list", "--no-ansi")
	cmd := exec.CommandContext(ctx, phpCmd[0], args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return -1
	}
	if strings.Contains(string(out), "No scheduled tasks") {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if cronLineRe.MatchString(line) && strings.Contains(line, "Next Due") {
			n++
		}
	}
	return n
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// RequiredExtensions maps each PHP extension the project needs ("intl",
// "pdo_pgsql"…) to the packages requiring it, from composer.json and the
// "ext-*" requirements of every locked package (dev ones included: this is
// a development machine).
func RequiredExtensions(root string) map[string][]string {
	out := map[string][]string{}
	add := func(owner string, reqs map[string]string) {
		for name := range reqs {
			if ext, ok := strings.CutPrefix(strings.ToLower(name), "ext-"); ok {
				ext = strings.TrimPrefix(ext, "zend-")
				out[ext] = append(out[ext], owner)
			}
		}
	}
	if c, err := readComposer(filepath.Join(root, "composer.json")); err == nil {
		add("composer.json", c.Require)
		add("composer.json", c.RequireDev)
	}
	data, err := os.ReadFile(filepath.Join(root, "composer.lock"))
	if err != nil {
		return out
	}
	var lock struct {
		Packages    []lockedPackage `json:"packages"`
		PackagesDev []lockedPackage `json:"packages-dev"`
	}
	if json.Unmarshal(data, &lock) == nil {
		for _, p := range append(lock.Packages, lock.PackagesDev...) {
			add(p.Name, p.Require)
		}
	}
	return out
}

type lockedPackage struct {
	Name    string            `json:"name"`
	Require map[string]string `json:"require"`
}

// laravelMaxPHP is the newest PHP each Laravel major supports (laravel.com/docs/releases).
var laravelMaxPHP = map[int]string{5: "7.4", 6: "8.0", 7: "8.0", 8: "8.1", 9: "8.2", 10: "8.3", 11: "8.4"}

// MaxPHPForLaravel maps "8.83.1" or "^10.0" to the newest PHP that Laravel
// supports ("" when unknown or unbounded).
func MaxPHPForLaravel(version string) string {
	major, err := strconv.Atoi(strings.SplitN(strings.TrimLeft(version, "^~vV>=< "), ".", 2)[0])
	if err != nil {
		return ""
	}
	return laravelMaxPHP[major]
}

// lockPHPConstraints collects the "php" requirement of every locked package:
// Composer refuses to install when the running PHP breaks any of them.
func lockPHPConstraints(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, "composer.lock"))
	if err != nil {
		return nil
	}
	var lock struct {
		Packages    []lockedPackage `json:"packages"`
		PackagesDev []lockedPackage `json:"packages-dev"`
	}
	if json.Unmarshal(data, &lock) != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range lock.Packages { // dev packages don't run in the app
		if c := strings.TrimSpace(p.Require["php"]); c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// PHPRequirementForDir builds the requirement of a (maybe unregistered)
// project directory, for `laranest php` outside registered apps.
func PHPRequirementForDir(root string) (constraint string, lock []string, max string, ok bool) {
	c, err := readComposer(filepath.Join(root, "composer.json"))
	if err != nil {
		return "", nil, "", false
	}
	laravel := lockedVersion(root, "laravel/framework")
	if laravel == "" {
		laravel = c.Require["laravel/framework"]
	}
	return c.Require["php"], lockPHPConstraints(root), MaxPHPForLaravel(laravel), true
}

var (
	viteStrictPort = regexp.MustCompile(`strictPort\s*:\s*true`)
	vitePortRe     = regexp.MustCompile(`\bport\s*:\s*(\d{2,5})`)
)

// VitePort reads the dev server port pinned in vite.config (0 if none) and
// whether strictPort forbids Vite from moving to another one.
func VitePort(path string) (port int, strict bool) {
	for _, name := range []string{"vite.config.js", "vite.config.ts", "vite.config.mjs", "vite.config.mts"} {
		data, err := os.ReadFile(filepath.Join(path, name))
		if err != nil {
			continue
		}
		if m := vitePortRe.FindSubmatch(data); m != nil {
			port, _ = strconv.Atoi(string(m[1]))
		}
		return port, viteStrictPort.Match(data)
	}
	return 0, false
}
