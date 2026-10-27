package detect

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "storage": true, "public": true,
	"snap": true, "go": true, "Library": true,
	// Windows profile folders that never hold projects (and are huge).
	"AppData": true, "Application Data": true, "Program Files": true, "Program Files (x86)": true,
	"Windows": true, "$Recycle.Bin": true, "scoop": true,
}

// Discover walks root (up to maxDepth levels) looking for Laravel projects.
// It is bounded in time so a huge home directory can't hang the panel.
func Discover(root string, maxDepth int) []string {
	deadline := time.Now().Add(3 * time.Second)
	var found []string
	base := strings.Count(filepath.Clean(root), string(os.PathSeparator))

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if time.Now().After(deadline) || len(found) >= 200 {
			return filepath.SkipAll
		}
		name := d.Name()
		if path != root && (strings.HasPrefix(name, ".") || skipDirs[name]) {
			return filepath.SkipDir
		}
		if exists(filepath.Join(path, "artisan")) && exists(filepath.Join(path, "composer.json")) {
			found = append(found, path)
			return filepath.SkipDir // don't look for apps nested inside apps
		}
		if strings.Count(path, string(os.PathSeparator))-base >= maxDepth {
			return filepath.SkipDir
		}
		return nil
	})
	sort.Strings(found)
	return found
}
