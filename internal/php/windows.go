package php

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nbullorini/laranest/internal/ghrelease"
)

// On Windows Laranest uses the official builds from windows.php.net
// (non-thread-safe, x64: what php-cgi and the CLI need), which ship every
// common extension as a DLL, plus phpredis from PECL.

// WindowsReleasesURL lists the newest build of every PHP minor for Windows.
const WindowsReleasesURL = "https://downloads.php.net/~windows/releases/releases.json"

const windowsPECL = "https://downloads.php.net/~windows/pecl/releases/"

// windowsExtensions are enabled when the build ships them (ext/php_<name>.dll),
// in this order (exif needs mbstring on old versions). Close to Laranest's
// Linux builds (php/extensions.txt); bcmath, ctype, tokenizer, xml, session,
// pdo and friends are compiled in.
var windowsExtensions = []string{
	"bz2", "curl", "fileinfo", "ftp", "gd", "gettext", "gmp", "intl", "ldap",
	"mbstring", "exif", "mysqli", "openssl", "pdo_mysql", "pdo_pgsql", "pdo_sqlite",
	"pgsql", "shmop", "soap", "sockets", "sodium", "sqlite3", "tidy", "xsl", "zip",
	"ffi", "redis",
}

var windowsBuild = regexp.MustCompile(`^nts-v[cs](\d+)-x64$`)

// fromWindowsPHP parses windows.php.net's releases.json.
func fromWindowsPHP(ctx context.Context, url string) ([]Release, error) {
	if url == "" {
		url = WindowsReleasesURL
	}
	body, err := get(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	return parseWindowsPHP(url, body)
}

func parseWindowsPHP(url string, body []byte) ([]Release, error) {
	var minors map[string]map[string]json.RawMessage
	if err := json.Unmarshal(body, &minors); err != nil {
		return nil, fmt.Errorf("invalid Windows PHP list: %w", err)
	}
	base := url[:strings.LastIndex(url, "/")+1]
	var out []Release
	for minor, entry := range minors {
		var version string
		_ = json.Unmarshal(entry["version"], &version)
		for key, raw := range entry {
			if !windowsBuild.MatchString(key) {
				continue
			}
			var b struct {
				Zip struct {
					Path   string `json:"path"`
					SHA256 string `json:"sha256"`
				} `json:"zip"`
			}
			if json.Unmarshal(raw, &b) != nil || b.Zip.Path == "" {
				continue
			}
			// "8.4.27-1" is a rebuild of 8.4.27: the version stays 8.4.27
			// and the checksum tells the builds apart.
			full, _, _ := strings.Cut(version, "-")
			out = append(out, Release{
				Version: full, Minor: minor, Source: OriginPHPNet,
				URL: base + b.Zip.Path, SHA256: b.Zip.SHA256,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j].Minor, out[i].Minor) })
	return out, nil
}

// stageWindows extracts a windows.php.net zip into dir and adds phpredis
// and the Visual C++ runtime.
func stageWindows(ctx context.Context, rel Release, dir, manifestURL string) error {
	if err := downloadZip(ctx, rel.URL, rel.SHA256, rel.Headers, dir); err != nil {
		return err
	}
	// phpredis is optional: Laravel defaults to it, but predis works without.
	_ = installPECL(ctx, "redis", rel.Minor, rel.URL, filepath.Join(dir, "ext"))
	if err := installVCRuntime(ctx, vcruntimeURL(manifestURL), dir); err != nil && !systemHasVCRuntime() {
		return fmt.Errorf("PHP for Windows needs the Microsoft Visual C++ Redistributable, and downloading it failed (%w): "+
			"install it from https://aka.ms/vs/17/release/vc_redist.x64.exe and try again", err)
	}
	return nil
}

// vcruntimeURL is the Visual C++ runtime zip next to Laranest's PHP manifest
// (see .github/workflows/php.yml).
func vcruntimeURL(manifestURL string) string {
	if manifestURL == "" {
		manifestURL = DefaultManifestURL
	}
	return manifestURL[:strings.LastIndex(manifestURL, "/")+1] + "vcruntime-x64.zip"
}

// installVCRuntime puts the Visual C++ runtime DLLs next to php.exe: the
// official builds need them and a fresh Windows may not have them. Windows
// loads DLLs from the program's folder first, so it doesn't depend on (or
// touch) what the system has installed.
func installVCRuntime(ctx context.Context, url, dir string) error {
	sums, rel, err := ghrelease.Get(ctx, url+".sha256")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(sums))
	if len(fields) == 0 {
		return fmt.Errorf("empty checksum file")
	}
	tmp, err := os.MkdirTemp(dir, ".vcruntime-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := downloadZip(ctx, rel.URL(url[:strings.LastIndex(url, "/")+1], path.Base(url)), fields[0], rel.Headers, tmp); err != nil {
		return err
	}
	dlls, _ := filepath.Glob(filepath.Join(tmp, "*.dll"))
	if len(dlls) == 0 {
		return fmt.Errorf("no DLLs in %s", url)
	}
	for _, dll := range dlls {
		if err := os.Rename(dll, filepath.Join(dir, filepath.Base(dll))); err != nil {
			return err
		}
	}
	return nil
}

func systemHasVCRuntime() bool {
	_, err := os.Stat(filepath.Join(os.Getenv("SystemRoot"), "System32", "vcruntime140.dll"))
	return err == nil
}

var compilerTag = regexp.MustCompile(`-(v[cs]\d+)-x64\.zip$`)

// installPECL downloads the newest release of a PECL extension built for
// this PHP minor and compiler, and puts its DLL in extDir.
func installPECL(ctx context.Context, name, minor, phpURL, extDir string) error {
	m := compilerTag.FindStringSubmatch(phpURL)
	if m == nil {
		return fmt.Errorf("unknown compiler for %s", phpURL)
	}
	listing, err := get(ctx, windowsPECL+name+"/", nil)
	if err != nil {
		return err
	}
	var versions []string
	for _, v := range regexp.MustCompile(`href="(\d+\.\d+\.\d+)/"`).FindAllStringSubmatch(string(listing), -1) {
		versions = append(versions, v[1])
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[j], versions[i]) })
	for _, v := range versions {
		url := fmt.Sprintf("%s%s/%s/php_%s-%s-%s-nts-%s-x64.zip", windowsPECL, name, v, name, v, minor, m[1])
		tmp, err := os.MkdirTemp(extDir, ".pecl-*")
		if err != nil {
			return err
		}
		err = downloadZip(ctx, url, "", nil, tmp)
		if err == nil {
			err = os.Rename(filepath.Join(tmp, "php_"+name+".dll"), filepath.Join(extDir, "php_"+name+".dll"))
		}
		os.RemoveAll(tmp)
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("no %s build for PHP %s", name, minor)
}

// downloadZip downloads a zip (checking wantSHA when given) and extracts it
// into dest, rejecting entries that escape it.
func downloadZip(ctx context.Context, url, wantSHA string, headers map[string]string, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{}).Do(req)
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
	if wantSHA != "" {
		if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, wantSHA) {
			return fmt.Errorf("checksum mismatch (expected %s, got %s)", wantSHA, got)
		}
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
		if err := extractZipFile(zf, target); err != nil {
			return err
		}
	}
	return nil
}

func extractZipFile(zf *zip.File, target string) error {
	r, err := zf.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

// windowsINI is the default php.ini for a Windows build staged in staged
// that will live in dir: it loads the extensions the build ships, from an
// absolute extension_dir (a relative one would be resolved against each
// project's directory).
func windowsINI(staged, dir string) string {
	ini := baseINI + `
; php-cgi behind Laranest's proxy (no web-server redirect involved).
cgi.force_redirect = 0
cgi.fix_pathinfo = 1
extension_dir = "` + filepath.Join(dir, "ext") + `"
`
	for _, ext := range windowsExtensions {
		if _, err := os.Stat(filepath.Join(staged, "ext", "php_"+ext+".dll")); err == nil {
			ini += "extension=" + ext + "\n"
		}
	}
	if _, err := os.Stat(filepath.Join(staged, "ext", "php_opcache.dll")); err == nil {
		ini += "zend_extension=opcache\n" // PHP 8.5 has it built in
	}
	return ini
}
