package dotenv

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSetPreservesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	orig := "# App\nAPP_NAME=\"My App\"\nAPP_URL=http://localhost\n\nexport DB_HOST=127.0.0.1 # local\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Set(path, map[string]string{"APP_URL": "http://demo.localhost", "DB_HOST": "db", "NEW_KEY": "a b"}); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(path)
	want := "# App\nAPP_NAME=\"My App\"\nAPP_URL=http://demo.localhost\n\nDB_HOST=db\n\nNEW_KEY=\"a b\"\n"
	if string(got) != want {
		t.Errorf("unexpected file:\n%s\nwant:\n%s", got, want)
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("permissions changed to %v", info.Mode().Perm())
	}

	vars, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if vars["APP_NAME"] != "My App" || vars["NEW_KEY"] != "a b" || vars["DB_HOST"] != "db" {
		t.Errorf("round trip failed: %v", vars)
	}
}
