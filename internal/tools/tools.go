// Package tools downloads Composer and Node.js into Laranest's data dir, so
// projects work on machines without them (and without root).
package tools

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sync/singleflight"
)

type Tools struct {
	DataDir string
	group   singleflight.Group
}

// ---- Composer ----

const composerBase = "https://getcomposer.org/download/latest-stable/"

// ComposerPhar returns composer.phar, downloading it (checksum-verified) on first use.
func (t *Tools) ComposerPhar(ctx context.Context) (string, error) {
	path := filepath.Join(t.DataDir, "bin", "composer.phar")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	_, err, _ := t.group.Do("composer", func() (any, error) {
		sum, err := fetch(ctx, composerBase+"composer.phar.sha256")
		if err != nil {
			return nil, err
		}
		phar, err := fetch(ctx, composerBase+"composer.phar")
		if err != nil {
			return nil, err
		}
		want := strings.Fields(string(sum))
		got := sha256.Sum256(phar)
		if len(want) == 0 || !strings.EqualFold(want[0], hex.EncodeToString(got[:])) {
			return nil, fmt.Errorf("composer.phar checksum mismatch")
		}
		return nil, writeAtomic(path, phar, 0o755)
	})
	if err != nil {
		return "", fmt.Errorf("downloading Composer: %w", err)
	}
	return path, nil
}

// ---- php / composer shims ----

// PHPShims returns a directory with `php` and `composer` links to the
// laranest binary, which picks each project's PHP. Put it first on PATH for
// tools that shell out to `php artisan` (laravel-vite-plugin, Wayfinder,
// Ziggy…) so they don't run whatever PHP the system happens to have.
// On Windows they are php.exe / composer.exe hard links (symlinks need
// administrator rights or developer mode).
func (t *Tools) PHPShims() (string, error) {
	if runtime.GOOS == "windows" {
		return t.windowsShims()
	}
	dir := filepath.Join(t.DataDir, "shims")
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, name := range []string{"php", "composer"} {
		link := filepath.Join(dir, name)
		if target, err := os.Readlink(link); err == nil && target == bin {
			continue
		}
		_ = os.Remove(link)
		if err := os.Symlink(bin, link); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// ---- Node.js ----

// NodeBinDir returns a directory containing node/npm/npx: the system's if
// npm is on PATH, otherwise the latest LTS downloaded from nodejs.org.
func (t *Tools) NodeBinDir(ctx context.Context) (string, error) {
	// On WSL, PATH also lists Windows' npm (/mnt/c/...), which can't build a
	// Linux project: only a Linux one counts.
	if npm, err := exec.LookPath("npm"); err == nil && !strings.HasPrefix(npm, "/mnt/") {
		// On Windows NodeCommand needs node.exe and npm's JavaScript next to
		// npm.cmd (the official installer and nvm-windows; not Volta's shims).
		if _, err := os.Stat(npmCLI(filepath.Dir(npm))); err == nil || runtime.GOOS != "windows" {
			return filepath.Dir(npm), nil
		}
	}
	if dir := t.installedNode(); dir != "" {
		return dir, nil
	}
	_, err, _ := t.group.Do("node", func() (any, error) { return nil, t.installNode(ctx) })
	if err != nil {
		return "", fmt.Errorf("downloading Node.js: %w", err)
	}
	if dir := t.installedNode(); dir != "" {
		return dir, nil
	}
	return "", fmt.Errorf("Node.js was downloaded but cannot be found")
}

func (t *Tools) installedNode() string {
	bin := filepath.Join(t.DataDir, "node", "current", "bin")
	if runtime.GOOS == "windows" {
		bin = filepath.Join(t.DataDir, "node", "current") // the zip has no bin/
	}
	if _, err := os.Stat(npmCLI(bin)); err == nil {
		return bin
	}
	if _, err := os.Stat(filepath.Join(bin, "npm")); err == nil {
		return bin
	}
	return ""
}

// NodeCommand builds argv for node, npm or npx from the Node in binDir. On
// Windows npm and npx are .cmd scripts, which can't be started directly
// without a shell: run their JavaScript entry point with node instead.
func NodeCommand(binDir, name string, args ...string) []string {
	if runtime.GOOS != "windows" {
		return append([]string{filepath.Join(binDir, name)}, args...)
	}
	node := filepath.Join(binDir, "node.exe")
	switch name {
	case "npm", "npx":
		cli := filepath.Join(binDir, "node_modules", "npm", "bin", name+"-cli.js")
		return append([]string{node, cli}, args...)
	}
	return append([]string{node}, args...)
}

func npmCLI(binDir string) string {
	return filepath.Join(binDir, "node_modules", "npm", "bin", "npm-cli.js")
}

func (t *Tools) installNode(ctx context.Context) error {
	index, err := fetch(ctx, "https://nodejs.org/dist/index.json")
	if err != nil {
		return err
	}
	var releases []struct {
		Version string `json:"version"`
		LTS     any    `json:"lts"` // false or the codename
	}
	if err := json.Unmarshal(index, &releases); err != nil {
		return err
	}
	version := ""
	for _, r := range releases { // newest first
		if lts, ok := r.LTS.(string); ok && lts != "" {
			version = r.Version
			break
		}
	}
	if version == "" {
		return fmt.Errorf("no Node.js LTS version found")
	}

	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	osName := map[string]string{"linux": "linux", "darwin": "darwin"}[runtime.GOOS]
	name := fmt.Sprintf("node-%s-%s-%s", version, osName, arch)
	archive := name + ".tar.gz"
	if runtime.GOOS == "windows" {
		name = fmt.Sprintf("node-%s-win-%s", version, arch)
		archive = name + ".zip"
	}
	base := "https://nodejs.org/dist/" + version + "/"
	sums, err := fetch(ctx, base+"SHASUMS256.txt")
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == archive {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum for %s", name)
	}

	root := filepath.Join(t.DataDir, "node")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(root, ".download-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if runtime.GOOS == "windows" {
		// No symlinks without privileges: the extracted dir becomes "current".
		if err := extractZip(ctx, base+archive, want, tmp); err != nil {
			return err
		}
		current := filepath.Join(root, "current")
		_ = os.RemoveAll(current)
		return os.Rename(filepath.Join(tmp, name), current)
	}
	if err := extractTarGz(ctx, base+archive, want, tmp); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(tmp, name), filepath.Join(root, version)); err != nil && !os.IsExist(err) {
		return err
	}
	current := filepath.Join(root, "current")
	_ = os.Remove(current)
	return os.Symlink(version, current)
}

// extractTarGz extracts a full archive (Node needs its lib/ tree), rejecting
// entries that escape dest.
func extractTarGz(ctx context.Context, url, wantSHA, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	hash := sha256.New()
	gz, err := gzip.NewReader(io.TeeReader(resp.Body, hash))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, h.Name)
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("invalid entry in the archive: %s", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) {
				return fmt.Errorf("absolute symlink in the archive: %s", h.Name)
			}
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			err = os.Symlink(h.Linkname, target)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			var f *os.File
			f, err = os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o755)
			if err == nil {
				_, err = io.Copy(f, tr)
				f.Close()
			}
		}
		if err != nil {
			return err
		}
	}
	if _, err := io.Copy(io.Discard, io.TeeReader(resp.Body, hash)); err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("checksum mismatch for %s", url)
	}
	return nil
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
