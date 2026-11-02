// Package wsl keeps Laranest reachable on WSL: by default WSL shuts a distro
// down shortly after its last terminal closes (systemd services don't count),
// which takes the panel and every app with it.
package wsl

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// Command runs a Windows program from WSL. Interop needs WSL_INTEROP, which
// terminals have but systemd services don't: then use the newest interop
// socket WSL created (one per session). Only the distro's default user gets
// Windows' folders in PATH (not services, nor `su other`), so System32
// programs are also found there.
func Command(name string, args ...string) *exec.Cmd {
	if _, err := exec.LookPath(name); err != nil {
		if p := filepath.Join(system32, name); fileExists(p) {
			name = p
		}
	}
	cmd := exec.Command(name, args...)
	if _, err := os.Stat("/mnt/c"); err == nil {
		cmd.Dir = "/mnt/c" // a WSL working dir makes cmd.exe complain about UNC paths
	}
	if os.Getenv("WSL_INTEROP") == "" {
		if sock := newestInterop(); sock != "" {
			cmd.Env = append(os.Environ(), "WSL_INTEROP="+sock)
		}
	}
	return cmd
}

const system32 = "/mnt/c/Windows/System32"

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func newestInterop() string {
	// 1_interop points at WSL's init, which lives as long as the distro;
	// per-session sockets may belong to terminals already closed.
	if _, err := os.Stat("/run/WSL/1_interop"); err == nil {
		return "/run/WSL/1_interop"
	}
	socks, _ := filepath.Glob("/run/WSL/*_interop")
	best, bestTime := "", int64(0)
	for _, s := range socks {
		if info, err := os.Stat(s); err == nil && info.ModTime().UnixNano() > bestTime {
			best, bestTime = s, info.ModTime().UnixNano()
		}
	}
	return best
}

// IsWSL reports whether we run inside a WSL distro.
func IsWSL() bool {
	if runtime.GOOS != "linux" {
		// On Windows "/proc/…" resolves against the current drive, which can
		// be a WSL share (\\wsl.localhost\Ubuntu) when run from there.
		return false
	}
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(data)), "microsoft")
}

// Env is what Laranest needs to know about the Windows side. It is captured
// by `laranest install`, which runs from a terminal where Windows interop
// works (systemd services can't launch Windows programs).
type Env struct {
	Distro  string `json:"wsl_distro,omitempty"`      // "Ubuntu"
	Profile string `json:"windows_profile,omitempty"` // /mnt/c/Users/name
}

// DetectEnv asks Windows for the user profile and reads the distro name.
func DetectEnv() (Env, error) {
	env := Env{Distro: os.Getenv("WSL_DISTRO_NAME")}
	out, err := Command("cmd.exe", "/c", "echo %USERPROFILE%").Output()
	if err != nil {
		return env, fmt.Errorf("could not query Windows (cmd.exe): %w", err)
	}
	win := strings.TrimSpace(string(out))
	path, err := exec.Command("wslpath", "-u", win).Output()
	if err != nil {
		return env, fmt.Errorf("wslpath %s: %w", win, err)
	}
	env.Profile = strings.TrimSpace(string(path))
	return env, nil
}

// WindowsPortsInUse returns the TCP ports Windows programs listen on. WSL
// forwards localhost to a distro only when Windows doesn't use the port, so
// Laranest must avoid them (a Windows Laranest takes 9151 when WSL's has
// 9150, which a second WSL user would then pick). Ports WSL's own forwarder
// (wslrelay.exe) also holds are left out: they already reach the distro.
func WindowsPortsInUse() (map[int]bool, error) {
	out, err := Command("netstat.exe", "-ano", "-p", "TCP").Output()
	if err != nil {
		return nil, fmt.Errorf("netstat.exe: %w", err)
	}
	out6, _ := Command("netstat.exe", "-ano", "-p", "TCPv6").Output()
	relays := map[string]bool{}
	if tl, err := Command("tasklist.exe", "/fo", "csv", "/nh", "/fi", "imagename eq wslrelay.exe").Output(); err == nil {
		relays = csvPIDs(string(tl), "wslrelay.exe")
	}
	return listeningPorts(string(out)+string(out6), relays), nil
}

// listeningPorts parses `netstat -ano` output. The state column is
// localized (LISTENING, ABIERTO…), so a listener is recognized by its
// unspecified foreign address instead.
func listeningPorts(netstat string, relayPIDs map[string]bool) map[int]bool {
	ports, relayed := map[int]bool{}, map[int]bool{}
	for _, line := range strings.Split(netstat, "\n") {
		f := strings.Fields(line)
		if len(f) != 5 || !strings.EqualFold(f[0], "TCP") {
			continue
		}
		if f[2] != "0.0.0.0:0" && f[2] != "[::]:0" {
			continue
		}
		i := strings.LastIndex(f[1], ":")
		if i < 0 {
			continue
		}
		host := f[1][:i]
		if host != "127.0.0.1" && host != "0.0.0.0" && host != "[::1]" && host != "[::]" {
			continue // a LAN address doesn't block localhost forwarding
		}
		port, err := strconv.Atoi(f[1][i+1:])
		if err != nil {
			continue
		}
		if relayPIDs[f[4]] {
			relayed[port] = true
		} else {
			ports[port] = true
		}
	}
	for port := range relayed {
		delete(ports, port)
	}
	return ports
}

// csvPIDs reads the PIDs of image from `tasklist /fo csv /nh` output.
func csvPIDs(tasklist, image string) map[string]bool {
	pids := map[string]bool{}
	for _, line := range strings.Split(tasklist, "\n") {
		f := strings.Split(strings.TrimSpace(line), ",")
		if len(f) >= 2 && strings.EqualFold(strings.Trim(f[0], `"`), image) {
			pids[strings.Trim(f[1], `"`)] = true
		}
	}
	return pids
}

// Status is the keep-alive state shown in the panel.
type Status struct {
	IsWSL      bool   `json:"is_wsl"`
	Supported  bool   `json:"supported"` // false if the Windows profile is unknown
	Distro     string `json:"distro"`
	User       string `json:"user"`        // the Linux user running Laranest (wsl.exe -u)
	ConfigFile string `json:"config_file"` // the .wslconfig path, as Windows shows it
	KeepAlive  bool   `json:"keep_alive"`  // instanceIdleTimeout = -1
	Autostart  bool   `json:"autostart"`   // starts the distro at Windows login
}

func (e Env) wslconfig() string { return filepath.Join(e.Profile, ".wslconfig") }

func (e Env) startupScript() string {
	return filepath.Join(e.Profile, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "Laranest.vbs")
}

func (e Env) Status() Status {
	s := Status{IsWSL: IsWSL(), Distro: e.Distro}
	if u, err := user.Current(); err == nil {
		s.User = u.Username
	}
	if !s.IsWSL || e.Profile == "" {
		return s
	}
	s.Supported = true
	s.ConfigFile = WindowsPath(e.wslconfig())
	data, _ := os.ReadFile(e.wslconfig())
	s.KeepAlive = strings.TrimSpace(iniValue(string(data), "general", "instanceIdleTimeout")) == "-1"
	_, err := os.Stat(e.startupScript())
	s.Autostart = err == nil
	return s
}

// SetKeepAlive sets (or removes) instanceIdleTimeout=-1 in [general],
// preserving the rest of .wslconfig and backing it up first. WSL reads it
// on its next start.
func (e Env) SetKeepAlive(on bool) error {
	if e.Profile == "" {
		return fmt.Errorf("the Windows profile is unknown: run `laranest install` from a WSL terminal")
	}
	path := e.wslconfig()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(data) > 0 {
		if err := os.WriteFile(path+".laranest.bak", data, 0o644); err != nil {
			return err
		}
	}
	value := "-1"
	if !on {
		value = "" // remove the key: back to WSL's default
	}
	return os.WriteFile(path, []byte(setINIValue(string(data), "general", "instanceIdleTimeout", value)), 0o644)
}

// SetAutostart adds (or removes) a hidden startup script that boots the
// distro when the user logs into Windows, so the panel is up without
// opening a terminal. It needs keep-alive, or WSL would stop it again.
func (e Env) SetAutostart(on bool) error {
	if e.Profile == "" || e.Distro == "" {
		return fmt.Errorf("the Windows profile or the distro is unknown: run `laranest install` from a WSL terminal")
	}
	path := e.startupScript()
	if !on {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	script := "' Created by Laranest: starts the WSL distro at login so its panel is available.\r\n" +
		"' Remove it from Laranest's settings, or delete this file.\r\n" +
		fmt.Sprintf("CreateObject(\"WScript.Shell\").Run \"wsl.exe -d \"\"%s\"\" --exec /bin/true\", 0, False\r\n", e.Distro)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(script), 0o644)
}

var sectionRe = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)

func keyRe(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)^\s*` + regexp.QuoteMeta(key) + `\s*=\s*(.*?)\s*$`)
}

// iniValue reads key from [section] (case-insensitive, as WSL does).
func iniValue(content, section, key string) string {
	in := false
	kr := keyRe(key)
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			in = strings.EqualFold(strings.TrimSpace(m[1]), section)
			continue
		}
		if in {
			if m := kr.FindStringSubmatch(line); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// setINIValue sets key in [section] ("" removes it), touching only that line
// and keeping comments, order and the file's line endings.
func setINIValue(content, section, key, value string) string {
	eol := "\n"
	if strings.Contains(content, "\r\n") {
		eol = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	kr := keyRe(key)
	entry := key + "=" + value

	start, end := -1, len(lines) // [section] header index and end of its body
	for i, line := range lines {
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			if start >= 0 {
				end = i
				break
			}
			if strings.EqualFold(strings.TrimSpace(m[1]), section) {
				start = i
			}
		}
	}

	if start >= 0 {
		for i := start + 1; i < end; i++ {
			if kr.MatchString(lines[i]) {
				if value == "" {
					lines = append(lines[:i], lines[i+1:]...)
					lines = dropEmptySection(lines, start)
				} else {
					lines[i] = entry
				}
				return strings.Join(lines, eol) + eol
			}
		}
		if value != "" {
			lines = append(lines[:start+1], append([]string{entry}, lines[start+1:]...)...)
		}
	} else if value != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "["+section+"]", entry)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, eol) + eol
}

// dropEmptySection removes the header at start (and the blank line before
// it) when the section has no keys left, so reverting leaves the file as it was.
func dropEmptySection(lines []string, start int) []string {
	for i := start + 1; i < len(lines) && !sectionRe.MatchString(lines[i]); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			return lines
		}
	}
	from := start
	if from > 0 && strings.TrimSpace(lines[from-1]) == "" {
		from--
	}
	return append(lines[:from], lines[start+1:]...)
}

// WindowsPath turns /mnt/c/Users/x/.wslconfig into C:\Users\x\.wslconfig for display.
func WindowsPath(p string) string {
	if rest, ok := strings.CutPrefix(p, "/mnt/"); ok && len(rest) >= 1 {
		return strings.ToUpper(rest[:1]) + ":" + strings.ReplaceAll(rest[1:], "/", `\`)
	}
	return p
}
