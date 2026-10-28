package php

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWindowsPHP(t *testing.T) {
	body := []byte(`{
	  "8.4": {"version": "8.4.27-1",
	    "nts-vs17-x64": {"zip": {"path": "php-8.4.27-1-nts-Win32-vs17-x64.zip", "sha256": "abc"}},
	    "ts-vs17-x64": {"zip": {"path": "php-8.4.27-1-Win32-vs17-x64.zip", "sha256": "def"}},
	    "nts-vs17-x86": {"zip": {"path": "php-8.4.27-1-nts-Win32-vs17-x86.zip", "sha256": "ghi"}}},
	  "7.4": {"version": "7.4.33",
	    "nts-vc15-x64": {"zip": {"path": "php-7.4.33-nts-Win32-vc15-x64.zip", "sha256": "jkl"}}}
	}`)
	rels, err := parseWindowsPHP("https://example.test/releases/releases.json", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 {
		t.Fatalf("want the two x64 NTS builds, got %+v", rels)
	}
	r := rels[0]
	if r.Minor != "8.4" || r.Version != "8.4.27" || r.SHA256 != "abc" || r.Source != OriginPHPNet ||
		r.URL != "https://example.test/releases/php-8.4.27-1-nts-Win32-vs17-x64.zip" {
		t.Errorf("unexpected release %+v", r)
	}
	if rels[1].Minor != "7.4" {
		t.Errorf("want newest first, got %+v", rels)
	}
}

func TestDownloadZipRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"php.exe", "ext/php_intl.dll", "../escape.txt"} {
		w, _ := zw.Create(name)
		w.Write([]byte("x"))
	}
	zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(buf.Bytes()) }))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "php")
	os.MkdirAll(dest, 0o755)
	err := downloadZip(context.Background(), srv.URL, "", nil, dest)
	if err == nil || !strings.Contains(err.Error(), "invalid entry") {
		t.Fatalf("want an invalid entry error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escape.txt")); err == nil {
		t.Error("an entry escaped the destination")
	}
}

func TestWindowsINIEnablesShippedExtensions(t *testing.T) {
	staged := t.TempDir()
	os.MkdirAll(filepath.Join(staged, "ext"), 0o755)
	for _, dll := range []string{"php_intl.dll", "php_redis.dll", "php_opcache.dll"} {
		os.WriteFile(filepath.Join(staged, "ext", dll), nil, 0o644)
	}
	ini := windowsINI(staged, `C:\Laranest\php\8.4`)
	for _, want := range []string{"extension=intl\n", "extension=redis\n", "zend_extension=opcache\n",
		`extension_dir = "` + filepath.Join(`C:\Laranest\php\8.4`, "ext") + `"`, "cgi.force_redirect = 0"} {
		if !strings.Contains(ini, want) {
			t.Errorf("php.ini lacks %q:\n%s", want, ini)
		}
	}
	if strings.Contains(ini, "extension=ldap") {
		t.Error("enabled an extension the build does not ship")
	}
}

func TestInstallVCRuntime(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"vcruntime140.dll", "msvcp140.dll", "readme.txt"} {
		w, _ := zw.Create(name)
		w.Write([]byte(name))
	}
	zw.Close()
	sum := sha256.Sum256(buf.Bytes())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/php/vcruntime-x64.zip":
			w.Write(buf.Bytes())
		case "/php/vcruntime-x64.zip.sha256":
			fmt.Fprintf(w, "%s  vcruntime-x64.zip\n", hex.EncodeToString(sum[:]))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if got := vcruntimeURL(srv.URL + "/php/manifest.json"); got != srv.URL+"/php/vcruntime-x64.zip" {
		t.Fatalf("vcruntimeURL = %s", got)
	}
	dir := t.TempDir()
	if err := installVCRuntime(context.Background(), srv.URL+"/php/vcruntime-x64.zip", dir); err != nil {
		t.Fatal(err)
	}
	for _, dll := range []string{"vcruntime140.dll", "msvcp140.dll"} {
		if _, err := os.Stat(filepath.Join(dir, dll)); err != nil {
			t.Errorf("%s was not put next to php.exe", dll)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "readme.txt")); err == nil {
		t.Error("copied a file that is not a DLL")
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, ".vcruntime-*")); len(entries) != 0 {
		t.Error("left its temporary folder behind")
	}

	// A tampered download is rejected.
	buf.WriteString("tampered")
	if err := installVCRuntime(context.Background(), srv.URL+"/php/vcruntime-x64.zip", t.TempDir()); err == nil {
		t.Error("accepted a download that doesn't match its checksum")
	}
}

// static-php-cli lists builds for every OS: only this one's are offered.
func TestParseStaticPHPPicksThisPlatform(t *testing.T) {
	var html strings.Builder
	for _, p := range []string{"linux", "macos"} {
		for _, sapi := range []string{"cli", "fpm"} {
			fmt.Fprintf(&html, `<a href="php-8.4.8-%s-%s-%s.tar.gz">`, sapi, p, arch())
		}
	}
	rels, err := parseStaticPHP("https://x/", []byte(html.String()))
	if err != nil || len(rels) != 1 {
		t.Fatalf("want one release, got %+v %v", rels, err)
	}
	if !strings.Contains(rels[0].CLIURL, "-"+platform()+"-") || !strings.Contains(rels[0].FPMURL, "-"+platform()+"-") {
		t.Errorf("got another platform's build: %+v", rels[0])
	}
}
