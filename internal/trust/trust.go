// Package trust makes browsers trust Laranest's local CA, wherever they run:
//
//   - Windows (natively, or WSL browsing from Windows): the CA goes into the
//     Windows user's "Root" store (certutil.exe -user: no admin; Windows asks
//     for confirmation).
//   - Ubuntu with local browsers: the Linux system store (sudo) plus the NSS
//     databases of Chrome/Chromium and Firefox (deb and snap), via
//     smallstep/truststore — the library Caddy itself uses.
package trust

import (
	"bytes"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/smallstep/truststore"

	"github.com/nbullorini/laranest/internal/wsl"
)

// Status says where the CA is trusted. Nil pointers mean "not applicable".
type Status struct {
	Ready   bool   `json:"ready"` // the CA exists (created by the proxy)
	Subject string `json:"subject,omitempty"`
	// Linux system store (curl, Chrome on Linux via p11-kit, PHP/Node of the system).
	System bool `json:"system"`
	// Windows user store, on Windows or on WSL when the profile is known.
	Windows *bool `json:"windows,omitempty"`
	// NSSTool reports whether certutil (NSS tools) is available for Firefox/Chrome on Linux.
	NSSTool bool `json:"nss_tool"`
}

func load(rootPath string) (*x509.Certificate, error) {
	if _, err := os.Stat(rootPath); err != nil {
		return nil, errors.New("Laranest's CA does not exist yet: start Laranest (laranest install)")
	}
	return truststore.ReadCertificate(rootPath)
}

func Check(rootPath string, env wsl.Env) Status {
	st := Status{NSSTool: haveCertutil()}
	cert, err := load(rootPath)
	if err != nil {
		return st
	}
	st.Ready, st.Subject = true, cert.Subject.CommonName
	if runtime.GOOS == "windows" {
		ok := windowsHas(cert)
		st.Windows, st.System = &ok, ok
		return st
	}
	if pool, err := x509.SystemCertPool(); err == nil {
		_, err := cert.Verify(x509.VerifyOptions{Roots: pool})
		st.System = err == nil
	}
	if wsl.IsWSL() && env.Profile != "" {
		ok := windowsHas(cert)
		st.Windows = &ok
	}
	return st
}

// InstallLinux adds the CA to the system store (asks for sudo) and to every
// NSS database found (Chrome/Chromium, Firefox deb and snap profiles).
func InstallLinux(rootPath string) error {
	cert, err := load(rootPath)
	if err != nil {
		return err
	}
	if err := truststore.Install(cert); err != nil {
		return fmt.Errorf("system store: %w", err)
	}
	if !haveCertutil() {
		return nil // caller explains how to get Firefox/Chrome support
	}
	// truststore's NSS pass ("WithFirefox") covers Chrome/Chromium's
	// ~/.pki/nssdb plus the Firefox profiles in NSSProfile; run it once for
	// deb Firefox (and Chrome) and once more for the snap Firefox profiles.
	home, _ := os.UserHomeDir()
	var errs []error
	for _, profiles := range []string{
		filepath.Join(home, ".mozilla", "firefox", "*"),
		filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox", "*"), // Ubuntu's default Firefox
	} {
		truststore.NSSProfile = profiles
		err := truststore.Install(cert, truststore.WithFirefox(), truststore.WithNoSystem())
		if err != nil && !strings.Contains(err.Error(), "not NSS security databases found") {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// InstallWindows adds the CA to the Windows user's Root store. Windows shows a
// confirmation dialog the user must accept; no administrator rights needed.
func InstallWindows(rootPath string, env wsl.Env) error {
	cert, err := load(rootPath)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("certutil.exe", "-user", "-addstore", "Root", rootPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("certutil: %w\n%s", err, bytes.TrimSpace(out))
		}
		if !windowsHas(cert) {
			return errors.New("Windows did not add the certificate (was the confirmation dialog cancelled?)")
		}
		return nil
	}
	if env.Profile == "" {
		return errors.New("the Windows profile is unknown: run `laranest install` from a WSL terminal")
	}
	// certutil.exe needs a Windows path: keep a copy in the user's profile.
	dst := filepath.Join(env.Profile, "AppData", "Local", "Laranest", "laranest-ca.crt")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(rootPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	out, err := wsl.Command(certutil(), "-user", "-addstore", "Root", wsl.WindowsPath(dst)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("certutil: %w\n%s", err, bytes.TrimSpace(out))
	}
	if !windowsHas(cert) {
		return errors.New("Windows did not add the certificate (was the confirmation dialog cancelled?)")
	}
	return nil
}

func windowsHas(cert *x509.Certificate) bool {
	sum := sha1.Sum(cert.Raw)
	if runtime.GOOS == "windows" {
		return exec.Command("certutil.exe", "-user", "-verifystore", "Root", hex.EncodeToString(sum[:])).Run() == nil
	}
	return wsl.Command(certutil(), "-user", "-verifystore", "Root", hex.EncodeToString(sum[:])).Run() == nil
}

func certutil() string {
	const abs = "/mnt/c/Windows/System32/certutil.exe"
	if _, err := os.Stat(abs); err == nil {
		return abs
	}
	return "certutil.exe"
}

// NSSToolInstall is the command that installs certutil on this distribution.
func NSSToolInstall() string {
	data, _ := os.ReadFile("/etc/os-release")
	return nssToolInstall(string(data))
}

func nssToolInstall(osRelease string) string {
	ids := ""
	for _, line := range strings.Split(osRelease, "\n") {
		if v, ok := strings.CutPrefix(line, "ID="); ok {
			ids += " " + strings.Trim(v, `"`)
		}
		if v, ok := strings.CutPrefix(line, "ID_LIKE="); ok {
			ids += " " + strings.Trim(v, `"`)
		}
	}
	for _, f := range strings.Fields(ids) {
		switch f {
		case "debian", "ubuntu":
			return "sudo apt install libnss3-tools"
		case "fedora", "rhel", "centos":
			return "sudo dnf install nss-tools"
		case "arch":
			return "sudo pacman -S nss"
		case "suse", "opensuse":
			return "sudo zypper install mozilla-nss-tools"
		}
	}
	return "install your distribution's NSS tools (certutil)"
}

func haveCertutil() bool {
	path, err := exec.LookPath("certutil")
	return err == nil && !strings.HasPrefix(path, "/mnt/") // not Windows' certutil.exe
}

// Bundle writes the system CA bundle (or public, a PEM file with the public
// CAs, where the OS has no bundle: Windows) plus Laranest's CA to dst, so the
// PHP and Node that run the apps trust https://*.localhost without touching
// the system store (which needs root). Returns false if there's no CA yet.
func Bundle(rootPath, dst, public string) (bool, error) {
	root, err := os.ReadFile(rootPath)
	if err != nil {
		return false, nil
	}
	var system []byte
	candidates := []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"}
	if public != "" {
		candidates = []string{public}
	}
	for _, f := range candidates {
		if system, err = os.ReadFile(f); err == nil {
			break
		}
	}
	data := append(append(bytes.TrimRight(system, "\n"), '\n'), root...)
	if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, data) {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(dst, data, 0o644)
}
