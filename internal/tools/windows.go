package tools

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// windowsShims hard-links laranest.exe as php.exe and composer.exe. A link
// left pointing at an older laranest.exe (after an update) is replaced; one
// that is running can't be, and is kept until the next time.
func (t *Tools) windowsShims() (string, error) {
	dir := filepath.Join(t.DataDir, "shims")
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	binInfo, err := os.Stat(bin)
	if err != nil {
		return "", err
	}
	for _, name := range []string{"php.exe", "composer.exe"} {
		link := filepath.Join(dir, name)
		if info, err := os.Stat(link); err == nil && os.SameFile(info, binInfo) {
			continue
		}
		old := link + ".old"
		_ = os.Remove(old)
		_ = os.Rename(link, old)
		if err := os.Link(bin, link); err != nil {
			// Another volume (no hard links): fall back to a copy.
			data, rerr := os.ReadFile(bin)
			if rerr != nil {
				return "", err
			}
			if err := os.WriteFile(link, data, 0o755); err != nil {
				return "", err
			}
		}
		_ = os.Remove(old)
	}
	return dir, nil
}

// extractZip downloads a zip, verifies it and extracts it into dest,
// rejecting entries that escape it.
func extractZip(ctx context.Context, url, wantSHA, dest string) error {
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
	f, err := os.CreateTemp(dest, ".download-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, hash), resp.Body)
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("checksum mismatch for %s", url)
	}
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return err
	}
	root := filepath.Clean(dest) + string(os.PathSeparator)
	for _, zf := range zr.File {
		target := filepath.Join(dest, filepath.FromSlash(zf.Name))
		if !strings.HasPrefix(target, root) {
			return fmt.Errorf("invalid entry in the archive: %s", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		r, err := zf.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err == nil {
			_, err = io.Copy(w, r)
			if cerr := w.Close(); err == nil {
				err = cerr
			}
		}
		r.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// ---- public CA certificates ----

// PublicCAsPath returns the downloaded public CA file, or "" if there is none yet.
func (t *Tools) PublicCAsPath() string {
	path := filepath.Join(t.DataDir, "ca", "cacert.pem")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

const mozillaCA = "https://curl.se/ca/cacert.pem"

// PublicCAs returns a PEM file with the public CAs (Mozilla's set, as
// published by curl), downloading it on first use and refreshing it monthly.
// PHP on Windows needs it: its OpenSSL doesn't read the Windows store, so
// without it every outgoing HTTPS call (Composer, Http::get…) fails.
func (t *Tools) PublicCAs(ctx context.Context) (string, error) {
	path := filepath.Join(t.DataDir, "ca", "cacert.pem")
	if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) < 30*24*time.Hour {
		return path, nil
	}
	_, err, _ := t.group.Do("cacert", func() (any, error) {
		sum, err := fetch(ctx, mozillaCA+".sha256")
		if err != nil {
			return nil, err
		}
		pem, err := fetch(ctx, mozillaCA)
		if err != nil {
			return nil, err
		}
		want := strings.Fields(string(sum))
		got := sha256.Sum256(pem)
		if len(want) == 0 || !strings.EqualFold(want[0], hex.EncodeToString(got[:])) {
			return nil, fmt.Errorf("cacert.pem checksum mismatch")
		}
		return nil, writeAtomic(path, pem, 0o644)
	})
	if err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return path, nil // stale but usable
		}
		return "", fmt.Errorf("downloading the public CA list: %w", err)
	}
	return path, nil
}
