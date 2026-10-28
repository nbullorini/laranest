package apps

import (
	"os"
	"strings"
	"testing"
)

const sampleLog = `[2026-10-28 14:00:01] local.INFO: User logged in {"id":7} 
[2026-10-28 14:00:02] local.ERROR: SQLSTATE[HY000] [2002] Connection refused {"exception":"[object] (PDOException(code: 2002): SQLSTATE[HY000] [2002] Connection refused at /app/vendor/laravel/framework/src/Illuminate/Database/Connectors/Connector.php:65)
[stacktrace]
#0 /app/vendor/laravel/framework/src/Illuminate/Database/Connectors/Connector.php(65): PDO->__construct()
#1 {main}
"} 
[2026-10-28T14:00:03.123456+00:00] production.WARNING: Slow query [] []
`

func TestParseLaravelLog(t *testing.T) {
	entries := parseLaravelLog([]byte(sampleLog))
	if len(entries) != 3 {
		t.Fatalf("got %d entries: %+v", len(entries), entries)
	}
	if e := entries[0]; e.Level != "info" || e.Env != "local" || e.Message != "User logged in" || e.Details != `{"id":7}` {
		t.Errorf("entry 0: %+v", e)
	}
	e := entries[1]
	if e.Level != "error" || e.Message != "SQLSTATE[HY000] [2002] Connection refused" {
		t.Errorf("entry 1: %+v", e)
	}
	if !strings.Contains(e.Details, "#0 /app/vendor") || !strings.HasPrefix(e.Details, `{"exception"`) {
		t.Errorf("entry 1 details: %q", e.Details)
	}
	if e := entries[2]; e.Level != "warning" || e.Env != "production" || e.Message != "Slow query" || e.Time != "2026-10-28T14:00:03.123456+00:00" {
		t.Errorf("entry 2: %+v", e)
	}
}

func TestReadTailSkipsToAnEntry(t *testing.T) {
	path := t.TempDir() + "/laravel.log"
	if err := os.WriteFile(path, []byte(sampleLog), 0o644); err != nil {
		t.Fatal(err)
	}
	data, truncated, err := readTail(path, 90) // the last entry plus a bit of the previous one
	if err != nil || !truncated {
		t.Fatal(err, truncated)
	}
	if entries := parseLaravelLog(data); len(entries) != 1 || entries[0].Level != "warning" {
		t.Errorf("got %+v", entries)
	}
}
