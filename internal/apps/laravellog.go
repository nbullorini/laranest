package apps

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The Laravel log: what the app writes to storage/logs (the "single" and
// "daily" channels), parsed into entries so the panel and the MCP server can
// show them by level, with their stack traces.

// LogEntry is one record of a Laravel log file.
type LogEntry struct {
	Time    string `json:"time"`    // as written: "2026-10-28 14:03:11"
	Env     string `json:"env"`     // the channel name, the app's environment ("local")
	Level   string `json:"level"`   // debug, info, notice, warning, error, critical, alert, emergency
	Message string `json:"message"` // the first line, without the exception's JSON
	// Details are the rest: the context, the exception and its stack trace.
	Details string `json:"details,omitempty"`
}

// LaravelLog is the tail of one log file.
type LaravelLog struct {
	Files     []LogFile  `json:"files"` // newest first
	File      string     `json:"file"`  // the one read ("" if there are none)
	Entries   []LogEntry `json:"entries"`
	Truncated bool       `json:"truncated"` // older entries were left out
}

type LogFile struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// LogQuery narrows what LaravelLog returns.
type LogQuery struct {
	File   string // a file in storage/logs; "" = the most recent
	Level  string // minimum level; "" = all
	Search string // case-insensitive text in the message or details
	Limit  int    // newest entries to return; 0 = 200
}

// logTail is how much of the end of a file is read: logs grow without
// bound, and the newest entries are the useful ones.
const logTail = 4 << 20

var (
	logHeader = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2}[ T][0-9:.]+(?:[+-]\d{2}:?\d{2}|Z)?)\] ([\w.-]+)\.([A-Z]+): ?(.*)$`)
	levels    = []string{"debug", "info", "notice", "warning", "error", "critical", "alert", "emergency"}
)

func levelRank(l string) int {
	for i, name := range levels {
		if name == l {
			return i
		}
	}
	return 0
}

func (m *Manager) logDir(id string) (string, error) {
	m.mu.Lock()
	a := m.store.apps[id]
	m.mu.Unlock()
	if a == nil {
		return "", ErrNotFound
	}
	return filepath.Join(a.Path, "storage", "logs"), nil
}

// LaravelLog reads the newest entries of one of the app's log files.
func (m *Manager) LaravelLog(id string, q LogQuery) (LaravelLog, error) {
	dir, err := m.logDir(id)
	if err != nil {
		return LaravelLog{}, err
	}
	out := LaravelLog{Files: logFiles(dir), Entries: []LogEntry{}}
	if len(out.Files) == 0 {
		return out, nil
	}
	out.File = out.Files[0].Name
	if q.File != "" {
		if !hasLogFile(out.Files, q.File) {
			return LaravelLog{}, errors.New("no such log file: " + q.File)
		}
		out.File = q.File
	}
	data, truncated, err := readTail(filepath.Join(dir, out.File), logTail)
	if err != nil {
		return LaravelLog{}, err
	}
	entries := parseLaravelLog(data)
	minRank, search := levelRank(strings.ToLower(q.Level)), strings.ToLower(q.Search)
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	// Newest first.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if levelRank(e.Level) < minRank {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(e.Message+"\n"+e.Details), search) {
			continue
		}
		if len(out.Entries) == limit {
			truncated = true
			break
		}
		out.Entries = append(out.Entries, e)
	}
	out.Truncated = truncated
	return out, nil
}

// ClearLaravelLog empties a log file (Laravel keeps writing to it).
func (m *Manager) ClearLaravelLog(id, file string) error {
	dir, err := m.logDir(id)
	if err != nil {
		return err
	}
	if !hasLogFile(logFiles(dir), file) {
		return errors.New("no such log file: " + file)
	}
	return os.Truncate(filepath.Join(dir, file), 0)
}

func logFiles(dir string) []LogFile {
	entries, _ := os.ReadDir(dir)
	var files []LogFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, LogFile{Name: e.Name(), Size: info.Size(), Modified: info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Modified.After(files[j].Modified) })
	return files
}

// hasLogFile also keeps names like "../.env" out: only listed files are read.
func hasLogFile(files []LogFile, name string) bool {
	for _, f := range files {
		if f.Name == name {
			return true
		}
	}
	return false
}

func readTail(path string, limit int64) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	offset := max(0, info.Size()-limit)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, false, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, false, err
	}
	if offset > 0 { // starts mid-entry: skip to the next one
		if i := bytes.Index(data, []byte("\n[")); i >= 0 {
			data = data[i+1:]
		}
	}
	return data, offset > 0, nil
}

// parseLaravelLog splits Monolog's line format into entries: a header line
// "[date] env.LEVEL: message {context} [extra]", then any continuation lines
// (the exception's stack trace).
func parseLaravelLog(data []byte) []LogEntry {
	var entries []LogEntry
	var details []string
	flush := func() {
		if n := len(entries); n > 0 && len(details) > 0 {
			entries[n-1].Details = strings.TrimRight(entries[n-1].Details+"\n"+strings.Join(details, "\n"), "\n")
			entries[n-1].Details = strings.TrimLeft(entries[n-1].Details, "\n")
		}
		details = details[:0]
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if m := logHeader.FindStringSubmatch(line); m != nil {
			flush()
			msg, ctx := splitContext(m[4])
			entries = append(entries, LogEntry{Time: m[1], Env: m[2], Level: strings.ToLower(m[3]), Message: msg, Details: ctx})
			continue
		}
		if len(entries) > 0 {
			details = append(details, line)
		}
	}
	flush()
	return entries
}

// splitContext separates the message from the JSON context Monolog appends
// (` {"exception":"[object] (…"`, ` {"userId":1} []`).
func splitContext(s string) (string, string) {
	s = strings.TrimRight(s, " ")
	for strings.HasSuffix(s, " []") { // empty context and extra
		s = strings.TrimSuffix(s, " []")
	}
	if i := strings.Index(s, ` {"`); i >= 0 {
		return s[:i], s[i+1:]
	}
	if i := strings.Index(s, ` ["`); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}
