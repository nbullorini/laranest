package detect

import (
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func sqliteDB(t *testing.T, path string, tables ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tbl := range tables {
		if _, err := db.Exec("CREATE TABLE " + tbl + " (id INTEGER)"); err != nil {
			t.Fatal(err)
		}
	}
	if len(tables) == 0 { // force the file to exist
		db.Exec("PRAGMA user_version = 1")
	}
}

func TestSQLiteMigrationsAndTables(t *testing.T) {
	env := "APP_KEY=base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nDB_CONNECTION=sqlite\nQUEUE_CONNECTION=database\nSESSION_DRIVER=database\nCACHE_STORE=database\n"
	root := laravelProject(t, env, "")
	dbPath := filepath.Join(root, "database", "database.sqlite")
	os.Remove(dbPath)
	d, _ := Inspect(root, nil)

	sqliteDB(t, dbPath)
	if got := codes(CheckServices(d, loadEnv(root), Context{})); !slices.Contains(got, "db_unmigrated") {
		t.Errorf("empty db: want db_unmigrated, got %v", got)
	}
	os.Remove(dbPath)
	sqliteDB(t, dbPath, "migrations", "jobs")
	ws := CheckServices(d, loadEnv(root), Context{})
	if got := codes(ws); !slices.Contains(got, "db_tables") {
		t.Fatalf("want db_tables, got %v", got)
	}
	for _, w := range ws {
		if w.Code == "db_tables" && w.Message != `Missing tables needed by the "database" drivers in .env: sessions, cache` {
			t.Errorf("message: %s", w.Message)
		}
	}
	os.Remove(dbPath)
	sqliteDB(t, dbPath, "migrations", "jobs", "sessions", "cache")
	if got := codes(CheckServices(d, loadEnv(root), Context{})); slices.ContainsFunc(got, func(c string) bool { return c[:3] == "db_" }) {
		t.Errorf("migrated db: unexpected %v", got)
	}
}

func TestDockerHostnames(t *testing.T) {
	root := laravelProject(t, "APP_KEY=base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nDB_CONNECTION=mysql\nDB_HOST=mysql-sail-service\nDB_DATABASE=app\n", "")
	d, _ := Inspect(root, nil)
	ws := CheckServices(d, loadEnv(root), Context{})
	found := false
	for _, w := range ws {
		found = found || (w.Code == "db_down" && strings.Contains(w.Message, "only exists inside Docker"))
	}
	if !found {
		t.Errorf("want Docker hostname hint, got %+v", ws)
	}
}

func TestRedisClientExtension(t *testing.T) {
	root := laravelProject(t, "APP_KEY=base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nDB_CONNECTION=sqlite\nCACHE_STORE=redis\nREDIS_PORT=1\n", "")
	d, _ := Inspect(root, nil)
	ws := CheckServices(d, loadEnv(root), Context{Modules: map[string]bool{"pdo": true}, PHPVersion: "8.4"})
	got := codes(ws)
	if !slices.Contains(got, "redis_client") || !slices.Contains(got, "redis_down") {
		t.Errorf("want redis_client and redis_down, got %v", got)
	}
}

func TestSMTPGreeting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("HTTP/1.1 400 Bad Request\r\n")) // not an SMTP server
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ws := checkSMTP(map[string]string{"MAIL_HOST": "127.0.0.1", "MAIL_PORT": port})
	if len(ws) != 1 || ws[0].Code != "mail_protocol" {
		t.Errorf("want mail_protocol, got %+v", ws)
	}
}

func TestStorageLink(t *testing.T) {
	root := laravelProject(t, "APP_KEY=base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n", "")
	os.MkdirAll(filepath.Join(root, "storage", "app", "public"), 0o755)
	if ws := checkStorageLink(&Detection{Path: root}); len(ws) != 1 {
		t.Fatalf("want storage_link, got %+v", ws)
	}
	if err := os.Symlink(filepath.Join(root, "storage", "app", "public"), filepath.Join(root, "public", "storage")); err != nil {
		t.Skip("symlinks need privileges here:", err) // Windows without developer mode
	}
	if ws := checkStorageLink(&Detection{Path: root}); len(ws) != 0 {
		t.Errorf("linked: got %+v", ws)
	}
}

func TestAppKeyValidation(t *testing.T) {
	cases := map[string]bool{ // key → valid
		"base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=": true, // 32 bytes
		"SomeRandomString":                 false, // BookStack's .env.example
		"base64:not-base64!!":              false,
		"12345678901234567890123456789012": true, // raw 32 bytes
	}
	for key, valid := range cases {
		if got := invalidAppKey(key, "") == ""; got != valid {
			t.Errorf("invalidAppKey(%q) valid=%v, want %v", key, got, valid)
		}
	}
	if invalidAppKey("1234567890123456", "AES-128-CBC") != "" {
		t.Error("16 raw bytes are valid for aes-128")
	}
}

func TestLastLoggedError(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "storage", "logs"), 0o755)
	os.WriteFile(filepath.Join(root, "storage", "logs", "laravel.log"), []byte(
		"[2026-10-27 05:09:55] local.ERROR: first {\"exception\":\"x\"}\n#0 trace\n"+
			"[2026-10-27 05:09:56] local.ERROR: Unsupported cipher or incorrect key length. {\"exception\":\"y\"}\n"+
			"[2026-10-27 05:09:57] local.INFO: not an error\n"), 0o644)
	if got := lastLoggedError(root); got != "Unsupported cipher or incorrect key length." {
		t.Errorf("got %q", got)
	}
}

func TestNewAppKeyIsValid(t *testing.T) {
	if msg := invalidAppKey(newAppKey(""), ""); msg != "" {
		t.Error(msg)
	}
	if msg := invalidAppKey(newAppKey("AES-128-GCM"), "AES-128-GCM"); msg != "" {
		t.Error(msg)
	}
}
