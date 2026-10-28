package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ---- cloudflared (sharing an app) ----

const cloudflaredRelease = "https://api.github.com/repos/cloudflare/cloudflared/releases/latest"

// Cloudflared returns the cloudflared binary that shares apps through a
// Cloudflare quick tunnel: the system's if it is on PATH, otherwise the
// latest release, downloaded once and checked against GitHub's digest.
func (t *Tools) Cloudflared(ctx context.Context) (string, error) {
	if p, err := exec.LookPath("cloudflared"); err == nil && !strings.HasPrefix(p, "/mnt/") {
		return p, nil
	}
	bin := filepath.Join(t.DataDir, "cloudflared", "cloudflared")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	_, err, _ := t.group.Do("cloudflared", func() (any, error) { return nil, installCloudflared(ctx, bin) })
	if err != nil {
		return "", fmt.Errorf("downloading cloudflared: %w", err)
	}
	return bin, nil
}

func cloudflaredAsset() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64", "linux/arm64":
		return "cloudflared-linux-" + runtime.GOARCH, nil
	case "darwin/amd64", "darwin/arm64":
		return "cloudflared-darwin-" + runtime.GOARCH + ".tgz", nil
	case "windows/amd64", "windows/arm64": // x64 runs emulated on ARM64
		return "cloudflared-windows-amd64.exe", nil
	}
	return "", fmt.Errorf("cloudflared is not available for %s/%s", runtime.GOOS, runtime.GOARCH)
}

func installCloudflared(ctx context.Context, bin string) error {
	name, err := cloudflaredAsset()
	if err != nil {
		return err
	}
	meta, err := fetch(ctx, cloudflaredRelease)
	if err != nil {
		return err
	}
	var rel struct {
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"` // "sha256:…"
		} `json:"assets"`
	}
	if err := json.Unmarshal(meta, &rel); err != nil {
		return err
	}
	for _, a := range rel.Assets {
		if a.Name != name {
			continue
		}
		want, ok := strings.CutPrefix(a.Digest, "sha256:")
		if !ok {
			return fmt.Errorf("no checksum published for %s", name)
		}
		data, err := fetch(ctx, a.URL)
		if err != nil {
			return err
		}
		if sum := sha256.Sum256(data); !strings.EqualFold(hex.EncodeToString(sum[:]), want) {
			return fmt.Errorf("checksum mismatch for %s", name)
		}
		if strings.HasSuffix(name, ".tgz") {
			if data, err = fileFromTarGz(data, "cloudflared"); err != nil {
				return err
			}
		}
		return writeAtomic(bin, data, 0o755)
	}
	return fmt.Errorf("%s is not in cloudflared's latest release", name)
}

func fileFromTarGz(data []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New(name + " is not in the archive")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
			return io.ReadAll(tr)
		}
	}
}
