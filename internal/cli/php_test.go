package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteRC(t *testing.T) {
	rc := filepath.Join(t.TempDir(), ".bashrc")
	os.WriteFile(rc, []byte("alias ll='ls -l'\nexport PATH=\"$HOME/.config/herd-lite/bin:$PATH\"\n"), 0o644)
	block := rcBegin + "\nexport PATH=\"/shims:$PATH\"\n" + rcEnd + "\n"

	// Enabling twice must not duplicate the block, and it must come last.
	for range 2 {
		if err := rewriteRC(rc, block); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(rc)
	if strings.Count(string(got), rcBegin) != 1 || !strings.HasSuffix(string(got), block) {
		t.Fatalf("unexpected rc:\n%s", got)
	}

	if err := rewriteRC(rc, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(rc)
	if strings.Contains(string(got), "laranest") || !strings.Contains(string(got), "alias ll") {
		t.Fatalf("disable should only remove our block:\n%s", got)
	}
}
