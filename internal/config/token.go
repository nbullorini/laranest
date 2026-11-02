package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// TokenFile is the API token's path. Only its owner can read it (0600, or
// %LOCALAPPDATA%'s per-user ACL on Windows): the panel listens on loopback,
// which every user of the machine can reach, and the token is what tells
// the owner's requests apart.
func TokenFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "token"), nil
}

// Token returns the API token, creating it on first use.
func Token() (string, error) {
	path, err := TokenFile()
	if err != nil {
		return "", err
	}
	if t, err := readToken(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return t, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return readToken(path) // created meanwhile (daemon and CLI starting together)
	}
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(token + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return token, nil
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(data))
	if t == "" {
		return "", fmt.Errorf("%s is empty: delete it and restart Laranest", path)
	}
	return t, nil
}

// Authorize adds the token to a request for the daemon's API.
func Authorize(req *http.Request) error {
	t, err := Token()
	if err != nil {
		return fmt.Errorf("reading Laranest's API token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t)
	return nil
}

// PanelURL is the panel's address with the token, for opening it in a
// browser (the page keeps the token and removes it from the address bar).
func PanelURL(cfg Config) (string, error) {
	t, err := Token()
	if err != nil {
		return "", err
	}
	return "http://" + strings.Replace(cfg.PanelAddr, "127.0.0.1", "localhost", 1) + "/?token=" + t, nil
}
