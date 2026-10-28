package php

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Xdebug for step debugging, loaded only into the apps that turn it on:
//   - Laranest's Linux builds: xdebug.so, built with each PHP and published
//     next to it (the manifest's xdebug_url), downloaded on first use;
//   - Windows: php_xdebug.dll from PECL, like phpredis;
//   - system PHP: the distribution's package, if it is installed;
//   - static-php's generic builds (macOS for now) can't load extensions.

// XdebugSettings make Xdebug wait for a trigger (a browser extension's
// cookie, or XDEBUG_TRIGGER) and connect to the IDE on the usual port.
var XdebugSettings = []string{"xdebug.mode=debug", "xdebug.start_with_request=trigger", "xdebug.client_host=localhost", "xdebug.client_port=9003"}

// XdebugArgs returns the PHP options that load Xdebug into rt, downloading it
// first if needed.
func (in *Installer) XdebugArgs(ctx context.Context, rt Runtime) ([]string, error) {
	var args []string
	switch {
	case rt.Source == SourceSystem:
		if !rt.Modules()["xdebug"] {
			return nil, fmt.Errorf("Xdebug is not installed for the system's PHP %s: install your distribution's package (e.g. php%s-xdebug)", rt.Version, rt.Version)
		}
	case rt.Origin == "static-php":
		return nil, fmt.Errorf("this PHP %s build (static-php's generic one) can't load Xdebug", rt.Version)
	default:
		path, err := in.xdebugFile(ctx, rt)
		if err != nil {
			return nil, err
		}
		args = append(args, "-d", "zend_extension="+path)
	}
	for _, s := range XdebugSettings {
		args = append(args, "-d", s)
	}
	// Check it loads before an app's web process depends on it.
	argv := append(rt.Command(args...), "-r", "echo extension_loaded('xdebug') ? 'ok' : 'missing';")
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(out)), "ok") {
		return nil, fmt.Errorf("Xdebug does not load into PHP %s: %s", rt.Version, strings.TrimSpace(string(out)))
	}
	return args, nil
}

func (in *Installer) xdebugFile(ctx context.Context, rt Runtime) (string, error) {
	dir := filepath.Dir(rt.CLI)
	if runtime.GOOS == "windows" {
		ext := filepath.Join(dir, "ext")
		dll := filepath.Join(ext, "php_xdebug.dll")
		if _, err := os.Stat(dll); err == nil {
			return dll, nil
		}
		rel, err := in.Catalog.Find(ctx, rt.Version)
		if err != nil {
			return "", err
		}
		if err := installPECL(ctx, "xdebug", rt.Version, rel.URL, ext); err != nil {
			return "", fmt.Errorf("downloading Xdebug: %w", err)
		}
		return dll, nil
	}
	so := filepath.Join(dir, "xdebug.so")
	if _, err := os.Stat(so); err == nil {
		return so, nil
	}
	_, err, _ := in.group.Do("xdebug-"+rt.Version, func() (any, error) {
		rel, err := in.Catalog.Find(ctx, rt.Version)
		if err != nil {
			return nil, err
		}
		if rel.XdebugURL == "" {
			return nil, fmt.Errorf("Xdebug for PHP %s is not published yet: try again later, or update PHP %s", rt.Version, rt.Version)
		}
		data, err := get(ctx, rel.XdebugURL, rel.Headers)
		if err != nil {
			return nil, fmt.Errorf("downloading Xdebug: %w", err)
		}
		if sum := sha256.Sum256(data); !strings.EqualFold(hex.EncodeToString(sum[:]), rel.XdebugSHA256) {
			return nil, errors.New("downloading Xdebug: checksum mismatch")
		}
		tmp := so + ".tmp"
		if err := os.WriteFile(tmp, data, 0o755); err != nil {
			return nil, err
		}
		return nil, os.Rename(tmp, so)
	})
	return so, err
}
