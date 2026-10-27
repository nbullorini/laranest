// Package ghrelease downloads files from GitHub releases, also while the
// repository is private (pre-release testing): the public download URLs
// then 404, and the files are fetched through the API with a token.
package ghrelease

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 30 * time.Second}

// Release is where a file came from: for a private release, the API URLs of
// every asset and the headers that download them.
type Release struct {
	Assets  map[string]string // file name → URL to download it
	Headers map[string]string
}

// URL returns where to download another file of the same release.
func (r Release) URL(base, name string) string {
	if r.Assets != nil {
		return r.Assets[name]
	}
	return base + name
}

// Get downloads a release file (up to 10 MB), falling back to the API.
func Get(ctx context.Context, url string) ([]byte, Release, error) {
	body, err := get(ctx, url, nil)
	if err == nil {
		return body, Release{}, nil
	}
	if body, rel, perr := fromPrivate(ctx, url); perr == nil {
		return body, rel, nil
	}
	return nil, Release{}, err
}

var releaseURL = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/releases/(?:download/([^/]+)|(latest)/download)/([^/]+)$`)

func fromPrivate(ctx context.Context, url string) ([]byte, Release, error) {
	m := releaseURL.FindStringSubmatch(url)
	token := Token(ctx)
	if m == nil || token == "" {
		return nil, Release{}, fmt.Errorf("could not download %s", url)
	}
	owner, repo, tag, latest, file := m[1], m[2], m[3], m[4], m[5]
	auth := map[string]string{"Authorization": "Bearer " + token, "X-GitHub-Api-Version": "2022-11-28"}
	api := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", owner, repo, tag)
	if latest != "" {
		api = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	}
	body, err := get(ctx, api, auth)
	if err != nil {
		return nil, Release{}, err
	}
	var rel struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, Release{}, err
	}
	out := Release{Assets: map[string]string{}, Headers: map[string]string{"Accept": "application/octet-stream"}}
	for _, a := range rel.Assets {
		out.Assets[a.Name] = a.URL
	}
	if out.Assets[file] == "" {
		return nil, Release{}, fmt.Errorf("the release has no %s", file)
	}
	// Asset API URLs return the file when asked for octet-stream (after a
	// redirect to storage, where Go drops the Authorization header).
	for k, v := range auth {
		out.Headers[k] = v
	}
	data, err := get(ctx, out.Assets[file], out.Headers)
	return data, out, err
}

// Token finds a token for private releases: env vars, or the gh CLI's login.
func Token(ctx context.Context) string {
	for _, k := range []string{"LARANEST_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func get(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}
