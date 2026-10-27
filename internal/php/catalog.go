package php

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nbullorini/laranest/internal/ghrelease"
)

// DefaultManifestURL lists Laranest's own static PHP builds (see
// .github/workflows/php.yml), built with the Herd/Forge set of extensions.
const DefaultManifestURL = "https://github.com/nbullorini/laranest/releases/download/php/manifest.json"

// staticPHPCommon is the fallback when our builds are unreachable: static-php-cli's
// prebuilt "common" set (has sqlite/pgsql/mysql/redis/gd, lacks intl/opcache/sodium).
const staticPHPCommon = "https://dl.static-php.dev/static-php-cli/common/"

// Release is one downloadable PHP version.
type Release struct {
	Version string `json:"version"` // "8.4.23"
	Minor   string `json:"minor"`   // "8.4"
	Source  string `json:"source"`  // "laranest" or "static-php"
	// Either a single archive with php + php-fpm (our builds)…
	URL    string `json:"url,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	// …or one archive per SAPI (static-php-cli's layout).
	CLIURL string `json:"cli_url,omitempty"`
	FPMURL string `json:"fpm_url,omitempty"`
	// Headers authenticate the download (private GitHub release).
	Headers map[string]string `json:"-"`
}

// Catalog fetches and caches the list of installable versions.
type Catalog struct {
	ManifestURL string
	GenericURL  string // static-php-cli listing; empty = staticPHPCommon
	WindowsURL  string // windows.php.net's releases.json; empty = WindowsReleasesURL

	mu      sync.Mutex
	cached  []Release
	fetched time.Time
}

const catalogTTL = time.Hour

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Releases returns the newest release of each PHP minor, newest first.
func (c *Catalog) Releases(ctx context.Context) ([]Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached != nil && time.Since(c.fetched) < catalogTTL {
		return c.cached, nil
	}
	if runtime.GOOS == "windows" {
		rels, err := fromWindowsPHP(ctx, c.WindowsURL)
		if err != nil {
			return nil, fmt.Errorf("could not get the list of PHP versions: %w", err)
		}
		c.cached, c.fetched = rels, time.Now()
		return c.cached, nil
	}
	ours, errOurs := c.fromManifest(ctx)
	generic, errGeneric := fromStaticPHP(ctx, c.GenericURL)
	if errOurs != nil && errGeneric != nil {
		return nil, fmt.Errorf("could not get the list of PHP versions: %w", errors.Join(errOurs, errGeneric))
	}
	c.cached, c.fetched = mergeCatalogs(latestPerMinor(ours), latestPerMinor(generic)), time.Now()
	return c.cached, nil
}

// Refresh drops the cache and fetches the catalog again ("check for updates").
func (c *Catalog) Refresh(ctx context.Context) ([]Release, error) {
	c.mu.Lock()
	c.cached = nil
	c.mu.Unlock()
	return c.Releases(ctx)
}

// mergeCatalogs prefers our builds and fills the versions we don't publish
// for this architecture (or at all, e.g. 8.0) with static-php's generic set.
func mergeCatalogs(ours, generic []Release) []Release {
	have := map[string]bool{}
	for _, r := range ours {
		have[r.Minor] = true
	}
	out := append([]Release{}, ours...)
	for _, r := range generic {
		if !have[r.Minor] {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j].Minor, out[i].Minor) })
	return out
}

// Cached returns the last fetched releases without hitting the network (nil if never fetched).
func (c *Catalog) Cached() []Release {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cached
}

// Find returns the newest release of minor ("8.3").
func (c *Catalog) Find(ctx context.Context, minor string) (Release, error) {
	rels, err := c.Releases(ctx)
	if err != nil {
		return Release{}, err
	}
	for _, r := range rels {
		if r.Minor == minor {
			return r, nil
		}
	}
	return Release{}, fmt.Errorf("PHP %s is not available for download", minor)
}

func (c *Catalog) fromManifest(ctx context.Context) ([]Release, error) {
	url := c.ManifestURL
	if url == "" {
		url = DefaultManifestURL
	}
	// The repo may be private (pre-release): then it goes through the API with a token.
	body, release, err := ghrelease.Get(ctx, url)
	if err != nil {
		return nil, err
	}
	assets, headers := release.Assets, release.Headers
	var m struct {
		PHP []struct {
			Version string `json:"version"`
			Arch    string `json:"arch"`
			URL     string `json:"url"`
			SHA256  string `json:"sha256"`
		} `json:"php"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	var out []Release
	for _, p := range m.PHP {
		if p.Arch != arch() {
			continue
		}
		rel := Release{Version: p.Version, Minor: minorOf(p.Version), Source: SourceLaranest, URL: p.URL, SHA256: p.SHA256}
		if assets != nil {
			api, ok := assets[path.Base(p.URL)]
			if !ok {
				continue
			}
			rel.URL, rel.Headers = api, headers
		}
		out = append(out, rel)
	}
	return out, nil
}

var staticPHPFile = regexp.MustCompile(`php-(\d+\.\d+\.\d+)-(cli|fpm)-linux-([a-z0-9_]+)\.tar\.gz`)

// fromStaticPHP scrapes static-php-cli's directory listing, keeping versions
// that have both a cli and an fpm archive for our architecture.
func fromStaticPHP(ctx context.Context, base string) ([]Release, error) {
	if base == "" {
		base = staticPHPCommon
	}
	body, err := get(ctx, base, nil)
	if err != nil {
		return nil, err
	}
	return parseStaticPHP(base, body)
}

func parseStaticPHP(base string, body []byte) ([]Release, error) {
	sapis := map[string]map[string]bool{}
	for _, m := range staticPHPFile.FindAllStringSubmatch(string(body), -1) {
		if m[3] != arch() {
			continue
		}
		if sapis[m[1]] == nil {
			sapis[m[1]] = map[string]bool{}
		}
		sapis[m[1]][m[2]] = true
	}
	var out []Release
	for v, s := range sapis {
		if !s["cli"] || !s["fpm"] {
			continue
		}
		file := func(sapi string) string {
			return fmt.Sprintf("%sphp-%s-%s-linux-%s.tar.gz", base, v, sapi, arch())
		}
		out = append(out, Release{Version: v, Minor: minorOf(v), Source: "static-php", CLIURL: file("cli"), FPMURL: file("fpm")})
	}
	return out, nil
}

func latestPerMinor(rels []Release) []Release {
	best := map[string]Release{}
	for _, r := range rels {
		if cur, ok := best[r.Minor]; !ok || versionLess(cur.Version, r.Version) {
			best[r.Minor] = r
		}
	}
	out := make([]Release, 0, len(best))
	for _, r := range best {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j].Minor, out[i].Minor) })
	return out
}

func get(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

func minorOf(version string) string {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return version
	}
	return parts[0] + "." + parts[1]
}

func arch() string {
	if runtime.GOARCH == "arm64" {
		return "aarch64"
	}
	return "x86_64"
}
