package php

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"
)

// Installer downloads static PHP builds into <dataDir>/php/<X.Y>/ without root.
type Installer struct {
	DataDir string
	Catalog *Catalog

	group      singleflight.Group
	mu         sync.Mutex
	installing map[string]bool
}

// Installing lists the versions being downloaded right now.
func (in *Installer) Installing() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := []string{}
	for v := range in.installing {
		out = append(out, v)
	}
	return out
}

// Install downloads minor ("8.3") if it isn't already there. Concurrent calls
// for the same version share one download.
func (in *Installer) Install(ctx context.Context, minor string) (Runtime, error) {
	_, err, _ := in.group.Do(minor, func() (any, error) {
		in.setInstalling(minor, true)
		defer in.setInstalling(minor, false)
		return nil, in.install(ctx, minor)
	})
	if err != nil {
		return Runtime{}, err
	}
	for _, r := range managed(in.DataDir) {
		if r.Version == minor {
			return r, nil
		}
	}
	return Runtime{}, fmt.Errorf("PHP %s was downloaded but cannot be found", minor)
}

// Remove deletes a downloaded version (system versions are apt's business).
func (in *Installer) Remove(minor string) error {
	dir := filepath.Join(in.DataDir, "php", minor)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("PHP %s was not installed by Laranest", minor)
	}
	return os.RemoveAll(dir)
}

func (in *Installer) install(ctx context.Context, minor string) error {
	final := filepath.Join(in.DataDir, "php", minor)
	if _, err := os.Stat(filepath.Join(final, webName)); err == nil {
		return nil
	}
	rel, err := in.Catalog.Find(ctx, minor)
	if err != nil {
		return err
	}
	tmp, err := in.stage(ctx, rel, "")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.Rename(tmp, final); err != nil {
		// Another process (a parallel `laranest composer`) installed it first.
		if _, statErr := os.Stat(filepath.Join(final, webName)); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

// Update replaces a downloaded version with the catalog's newest build of
// that minor, keeping the user's php.ini. Running processes keep the old
// binary (renamed files stay alive) until they are restarted.
func (in *Installer) Update(ctx context.Context, minor string) (Runtime, error) {
	_, err, _ := in.group.Do("update:"+minor, func() (any, error) {
		in.setInstalling(minor, true)
		defer in.setInstalling(minor, false)

		final := filepath.Join(in.DataDir, "php", minor)
		ini, err := os.ReadFile(filepath.Join(final, "php.ini"))
		if err != nil {
			return nil, fmt.Errorf("PHP %s was not installed by Laranest", minor)
		}
		rel, err := in.Catalog.Find(ctx, minor)
		if err != nil {
			return nil, err
		}
		tmp, err := in.stage(ctx, rel, string(ini))
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)

		old := final + ".old"
		_ = os.RemoveAll(old)
		if err := os.Rename(final, old); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, final); err != nil {
			_ = os.Rename(old, final) // roll back
			return nil, err
		}
		return nil, os.RemoveAll(old)
	})
	if err != nil {
		return Runtime{}, err
	}
	for _, r := range managed(in.DataDir) {
		if r.Version == minor {
			return r, nil
		}
	}
	return Runtime{}, fmt.Errorf("PHP %s cannot be found after updating", minor)
}

// stage downloads rel into a temp dir next to the final location (so the
// caller can rename it into place atomically) with the given php.ini ("" =
// the default one).
func (in *Installer) stage(ctx context.Context, rel Release, ini string) (string, error) {
	root := filepath.Join(in.DataDir, "php")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(root, "."+rel.Minor+"-*")
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		os.RemoveAll(tmp)
		return "", err
	}

	if runtime.GOOS == "windows" {
		err = stageWindows(ctx, rel, tmp)
	} else if rel.URL != "" {
		err = downloadAndExtract(ctx, rel.URL, rel.SHA256, rel.Headers, tmp)
	} else {
		err = downloadAndExtract(ctx, rel.CLIURL, "", nil, tmp)
		if err == nil {
			err = downloadAndExtract(ctx, rel.FPMURL, "", nil, tmp)
		}
	}
	if err != nil {
		return fail(fmt.Errorf("downloading PHP %s: %w", rel.Version, err))
	}
	if ini == "" {
		ini = defaultINI(tmp, filepath.Join(root, rel.Minor))
	}
	for _, bin := range []string{cliName, webName} {
		if !isExecutable(filepath.Join(tmp, bin)) {
			return fail(fmt.Errorf("the PHP %s package does not contain %s", rel.Version, bin))
		}
	}
	// BUILD identifies the exact archive: a rebuild with more extensions keeps
	// the PHP version, so the version alone can't tell it's newer.
	files := map[string]string{"php.ini": ini, "VERSION": rel.Version + "\n", "ORIGIN": rel.Source + "\n", "BUILD": rel.SHA256 + "\n"}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(content), 0o644); err != nil {
			return fail(err)
		}
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return fail(err)
	}
	return tmp, nil
}

// downloadAndExtract extracts only the php / php-fpm binaries from a tar.gz,
// ignoring any other entry (and therefore any path traversal).
func downloadAndExtract(ctx context.Context, url, wantSHA string, headers map[string]string, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{}).Do(req) // no timeout: archives are ~30 MB
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
		name := filepath.Base(h.Name)
		if h.Typeflag != tar.TypeReg || (name != "php" && name != "php-fpm") {
			continue
		}
		f, err := os.OpenFile(filepath.Join(dest, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, tr)
		f.Close()
		if err != nil {
			return err
		}
	}
	// Drain so the hash covers the whole file, then verify.
	if _, err := io.Copy(io.Discard, io.TeeReader(resp.Body, hash)); err != nil {
		return err
	}
	if wantSHA != "" {
		if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, wantSHA) {
			return fmt.Errorf("checksum mismatch (expected %s, got %s)", wantSHA, got)
		}
	}
	return nil
}

// baseINI is Laranest's development php.ini (the builds ship none).
const baseINI = `; Generated by Laranest. Edit freely: it is not overwritten.
memory_limit = 512M
upload_max_filesize = 64M
post_max_size = 64M
max_execution_time = 60
display_errors = On
error_reporting = E_ALL
date.timezone = UTC
variables_order = "EGPCS"
opcache.enable = 1
opcache.enable_cli = 0
opcache.validate_timestamps = 1
opcache.revalidate_freq = 0
`

// defaultINI is the php.ini for a new runtime staged in staged that will
// live in final.
func defaultINI(staged, final string) string {
	if runtime.GOOS == "windows" {
		return windowsINI(staged, final)
	}
	ini := baseINI
	// Static OpenSSL doesn't know the distro's CA bundle location.
	for _, ca := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"} {
		if _, err := os.Stat(ca); err == nil {
			ini += "openssl.cafile = " + ca + "\ncurl.cainfo = " + ca + "\n"
			break
		}
	}
	return ini
}

func (in *Installer) setInstalling(minor string, on bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.installing == nil {
		in.installing = map[string]bool{}
	}
	if on {
		in.installing[minor] = true
	} else {
		delete(in.installing, minor)
	}
}
