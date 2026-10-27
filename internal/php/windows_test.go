package php

import (
	"archive/zip"
	"bytes"
	"context"
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
