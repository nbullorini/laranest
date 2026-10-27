// Package dotenv reads and edits Laravel .env files without disturbing
// unrelated lines, comments or ordering.
package dotenv

import (
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/joho/godotenv"
)

var lineRe = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*=\s*(.*)$`)

// Parse reads path into a key → value map (quoting, escapes and ${VAR}
// expansion handled by godotenv).
func Parse(path string) (map[string]string, error) {
	return godotenv.Read(path)
}

// Set updates (or appends) the given keys in path, keeping everything else intact.
func Set(path string, values map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	pending := map[string]string{}
	for k, v := range values {
		pending[k] = v
	}

	for i, line := range lines {
		m := lineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if v, ok := pending[m[1]]; ok {
			lines[i] = m[1] + "=" + quote(v)
			delete(pending, m[1])
		}
	}

	keys := make([]string, 0, len(pending))
	for k := range pending {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		lines = append(lines, "")
	}
	for _, k := range keys {
		lines = append(lines, k+"="+quote(pending[k]))
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), info.Mode().Perm())
}

func quote(v string) string {
	if v == "" || strings.ContainsAny(v, " #\"'$") {
		return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
	}
	return v
}
