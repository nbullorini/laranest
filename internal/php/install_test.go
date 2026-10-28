//go:build !windows

package php

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

// fakeArchive builds a tar.gz with executable php/php-fpm scripts plus an
// entry trying to escape the destination, which must be ignored.
func fakeArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{
		"php":          "#!/bin/sh\necho php\n",
		"php-fpm":      "#!/bin/sh\necho fpm\n",
		"../escape.sh": "#!/bin/sh\n",
	} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestInstallFromManifest(t *testing.T) {
	archive := fakeArchive(t)
	sum := sha256.Sum256(archive)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json", "/manifest-macos.json": // macOS reads its own
			fmt.Fprintf(w, `{"php":[
				{"version":"8.3.9","arch":%q,"url":"%s/a.tgz","sha256":%q},
				{"version":"8.3.10","arch":%q,"url":"%s/a.tgz","sha256":%q},
				{"version":"8.3.10","arch":"other","url":"x","sha256":"x"}]}`,
				arch(), srv.URL, hex.EncodeToString(sum[:]), arch(), srv.URL, hex.EncodeToString(sum[:]))
		case "/a.tgz":
			w.Write(archive)
		}
	}))
	defer srv.Close()

	data := t.TempDir()
	in := &Installer{DataDir: data, Catalog: &Catalog{ManifestURL: srv.URL + "/manifest.json", GenericURL: srv.URL + "/none/"}}
	rt, err := in.Install(context.Background(), "8.3")
	if err != nil {
		t.Fatal(err)
	}
	if rt.Version != "8.3" || rt.Full != "8.3.10" || rt.Source != SourceLaranest || rt.INI == "" {
		t.Errorf("unexpected runtime %+v", rt)
	}
	if _, err := os.Stat(filepath.Join(data, "php", "escape.sh")); err == nil {
		t.Error("archive entry escaped the install dir")
	}
	if ini, _ := os.ReadFile(rt.INI); !strings.Contains(string(ini), "memory_limit") {
		t.Error("php.ini not written")
	}
	if got := Discover(data, false); len(got) == 0 || got[len(got)-1].Source == "" {
		t.Errorf("installed runtime not discovered: %+v", got)
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	archive := fakeArchive(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/manifest") { // manifest-macos.json on macOS
			fmt.Fprintf(w, `{"php":[{"version":"8.4.1","arch":%q,"url":"%s/a.tgz","sha256":"deadbeef"}]}`, arch(), srv.URL)
			return
		}
		w.Write(archive)
	}))
	defer srv.Close()

	data := t.TempDir()
	in := &Installer{DataDir: data, Catalog: &Catalog{ManifestURL: srv.URL + "/manifest.json", GenericURL: srv.URL + "/none/"}}
	if _, err := in.Install(context.Background(), "8.4"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum error, got %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(data, "php")); len(entries) != 0 {
		t.Errorf("failed install left files behind: %v", entries)
	}
}

func TestStaticPHPListing(t *testing.T) {
	a := arch()
	html := fmt.Sprintf(`<a href="php-8.3.1-cli-%[2]s-%[1]s.tar.gz">x</a> <a href="php-8.3.1-fpm-%[2]s-%[1]s.tar.gz">x</a>
		<a href="php-8.3.2-cli-%[2]s-%[1]s.tar.gz">x</a>
		<a href="php-8.4.0-cli-%[2]s-%[1]s.tar.gz">x</a> <a href="php-8.4.0-fpm-%[2]s-%[1]s.tar.gz">x</a>`, a, platform())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, html) }))
	defer srv.Close()

	rels, err := parseStaticPHP(srv.URL+"/", []byte(html))
	if err != nil {
		t.Fatal(err)
	}
	got := latestPerMinor(rels)
	// 8.3.2 has no fpm archive, so 8.3.1 wins; newest minor first.
	if len(got) != 2 || got[0].Version != "8.4.0" || got[1].Version != "8.3.1" {
		t.Fatalf("got %+v", got)
	}
}

func TestUpdateKeepsINIAndReplacesBuild(t *testing.T) {
	archive := fakeArchive(t)
	sum := sha256.Sum256(archive)
	version := "8.3.9"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/manifest") { // manifest-macos.json on macOS
			fmt.Fprintf(w, `{"php":[{"version":%q,"arch":%q,"url":"%s/a.tgz","sha256":%q}]}`,
				version, arch(), srv.URL, hex.EncodeToString(sum[:]))
			return
		}
		w.Write(archive)
	}))
	defer srv.Close()

	data := t.TempDir()
	cat := &Catalog{ManifestURL: srv.URL + "/manifest.json", GenericURL: srv.URL + "/none/"}
	in := &Installer{DataDir: data, Catalog: cat}
	rt, err := in.Install(context.Background(), "8.3")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(rt.INI, []byte("memory_limit = 2G\n"), 0o644)

	version = "8.3.12" // a new patch is published
	if _, err := cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	rt, err = in.Update(context.Background(), "8.3")
	if err != nil {
		t.Fatal(err)
	}
	if rt.Full != "8.3.12" || rt.Origin != SourceLaranest || rt.Build == "" {
		t.Errorf("not updated: %+v", rt)
	}
	if ini, _ := os.ReadFile(rt.INI); string(ini) != "memory_limit = 2G\n" {
		t.Errorf("php.ini not preserved: %q", ini)
	}
	entries, _ := os.ReadDir(filepath.Join(data, "php"))
	if len(entries) != 1 {
		t.Errorf("leftovers after update: %v", entries)
	}
}

func TestManagedIgnoresStagingDirs(t *testing.T) {
	data := t.TempDir()
	for _, dir := range []string{"8.4", ".8.4-123", "8.4.old"} {
		p := filepath.Join(data, "php", dir)
		os.MkdirAll(p, 0o755)
		for _, bin := range []string{"php", "php-fpm"} {
			os.WriteFile(filepath.Join(p, bin), []byte("#!/bin/sh\n"), 0o755)
		}
	}
	if got := managed(data); len(got) != 1 || got[0].Version != "8.4" {
		t.Errorf("got %+v", got)
	}
}

func TestMergeCatalogsPrefersOursPerVersion(t *testing.T) {
	ours := []Release{{Version: "8.4.27", Minor: "8.4", Source: SourceLaranest}}
	generic := []Release{
		{Version: "8.4.23", Minor: "8.4", Source: "static-php"},
		{Version: "8.2.32", Minor: "8.2", Source: "static-php"},
	}
	got := mergeCatalogs(ours, generic)
	if len(got) != 2 || got[0].Version != "8.4.27" || got[1].Version != "8.2.32" {
		t.Fatalf("got %+v", got)
	}
}
