package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadProject(t *testing.T) {
	dir := t.TempDir()
	if c, sum, err := ReadProject(dir); c != nil || sum != "" || err != nil {
		t.Fatalf("no file: %v %q %v", c, sum, err)
	}
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, ProjectFile), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"php": "8.3", "processes": {"queue": false, "vite": true}, "custom": [{"name": "pulse", "command": "php artisan pulse:check"}]}`)
	c, sum, err := ReadProject(dir)
	if err != nil || c == nil || sum == "" || *c.PHP != "8.3" || len(c.Custom) != 1 {
		t.Fatalf("got %+v %q %v", c, sum, err)
	}
	p := c.ApplyProcesses(Processes{Queue: true, Scheduler: true})
	if p != (Processes{Queue: false, Scheduler: true, Vite: true}) {
		t.Errorf("processes: %+v", p)
	}

	write(`{"php": "8.3", "procesess": {}}`)
	if _, _, err := ReadProject(dir); err == nil || !strings.Contains(err.Error(), "procesess") {
		t.Errorf("a typo should be reported: %v", err)
	}
	write(`{"processes": {"octane": true}}`)
	if _, _, err := ReadProject(dir); err == nil || !strings.Contains(err.Error(), "octane") {
		t.Errorf("an unknown process should be reported: %v", err)
	}
}
