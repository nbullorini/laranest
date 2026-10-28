// Package selfupdate updates Laranest itself from its latest GitHub release.
//
// Releases keep reusing the same version until the project is stable, so a
// new build is told apart by its checksum: the installed binary is compared
// with the release's SHA256SUMS.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nbullorini/laranest/internal/ghrelease"
	"github.com/nbullorini/laranest/internal/version"
)

// latestURL is where the newest release's files are; LARANEST_DOWNLOAD_URL
// overrides it (a mirror, or a local test server), as in the installers.
func latestURL() string {
	if u := os.Getenv("LARANEST_DOWNLOAD_URL"); u != "" {
		return strings.TrimRight(u, "/") + "/"
	}
	return "https://github.com/nbullorini/laranest/releases/latest/download/"
}

// Asset is this platform's binary in a release. Windows on ARM64 runs the
// x64 build (emulated), like the installer does.
func Asset() string {
	if runtime.GOOS == "windows" {
		return "laranest-windows-amd64.exe"
	}
	return "laranest-" + runtime.GOOS + "-" + runtime.GOARCH // linux/darwin, amd64/arm64
}

// Status says whether a newer build is published.
type Status struct {
	Current   string    `json:"current"`
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	Error     string    `json:"error,omitempty"`
}

// Updater caches checks: the panel asks on every load.
type Updater struct {
	mu     sync.Mutex
	status Status
}

const checkTTL = time.Hour

// Check compares the running binary with the latest release (cached for an
// hour unless refresh).
func (u *Updater) Check(ctx context.Context, refresh bool) Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !refresh && !u.status.CheckedAt.IsZero() && time.Since(u.status.CheckedAt) < checkTTL {
		return u.status
	}
	st := Status{Current: version.Version, CheckedAt: time.Now()}
	want, _, err := latestSum(ctx)
	if err == nil {
		var got string
		if got, err = selfSum(); err == nil {
			st.Available = !strings.EqualFold(want, got)
		}
	}
	if err != nil {
		st.Error = err.Error()
	}
	u.status = st
	return st
}

// latestSum returns the published checksum of this platform's binary.
func latestSum(ctx context.Context) (string, ghrelease.Release, error) {
	sums, rel, err := ghrelease.Get(ctx, latestURL()+"SHA256SUMS")
	if err != nil {
		return "", rel, fmt.Errorf("checking for updates: %w", err)
	}
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == Asset() {
			return f[0], rel, nil
		}
	}
	return "", rel, fmt.Errorf("the latest release has no %s", Asset())
}

func selfSum() (string, error) {
	exe, err := executable()
	if err != nil {
		return "", err
	}
	f, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Download fetches and verifies the latest binary into dir, returning its path.
func Download(ctx context.Context, dir string) (string, error) {
	want, rel, err := latestSum(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.URL(latestURL(), Asset()), nil)
	if err != nil {
		return "", err
	}
	for k, v := range rel.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req) // no timeout: ~70 MB
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: %s", Asset(), resp.Status)
	}
	pattern := ".laranest-update-*"
	if runtime.GOOS == "windows" {
		pattern = "laranest-update-*.exe"
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want) {
		err = errors.New("checksum mismatch: the download is corrupt")
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o755)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
