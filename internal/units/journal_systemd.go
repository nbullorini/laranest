//go:build !windows

package units

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"time"
)

// Follow streams the last backlog lines of unit's journal and then new ones,
// until ctx is cancelled. It shells out to journalctl because the native
// journal API (sdjournal) requires cgo.
func Follow(ctx context.Context, unit string, backlog int, fn func(Line)) error {
	cmd := exec.CommandContext(ctx, "journalctl", "--user", "--unit", unit,
		"--follow", "--lines", strconv.Itoa(backlog), "--output", "json",
		"--output-fields", "MESSAGE,_PID,_COMM,__REALTIME_TIMESTAMP")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var e map[string]any
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		fn(toLine(e))
	}
	return cmd.Wait()
}

func toLine(e map[string]any) Line {
	l := Line{Stream: "out", Text: message(e["MESSAGE"])}
	if us, err := strconv.ParseInt(str(e["__REALTIME_TIMESTAMP"]), 10, 64); err == nil {
		l.Time = time.UnixMicro(us)
	}
	if str(e["_COMM"]) == "systemd" {
		l.Stream = "sys"
	}
	return l
}

// message decodes MESSAGE, which journalctl emits as an array of bytes when
// it isn't valid UTF-8 (e.g. raw binary or partial escape sequences).
func message(v any) string {
	switch m := v.(type) {
	case string:
		return m
	case []any:
		b := make([]byte, 0, len(m))
		for _, x := range m {
			if f, ok := x.(float64); ok {
				b = append(b, byte(f))
			}
		}
		return string(b)
	}
	return ""
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
