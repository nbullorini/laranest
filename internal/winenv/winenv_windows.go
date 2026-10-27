//go:build windows

package winenv

import (
	"errors"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// AddUserPath appends dir to the user's PATH (for new terminals). Reports
// whether it changed anything.
func AddUserPath(dir string) (bool, error) {
	return editUserPath(func(parts []string) []string {
		for _, p := range parts {
			if samePath(p, dir) {
				return nil
			}
		}
		return append(parts, dir)
	})
}

// RemoveUserPath removes dir from the user's PATH.
func RemoveUserPath(dir string) (bool, error) {
	return editUserPath(func(parts []string) []string {
		out := parts[:0:0]
		for _, p := range parts {
			if !samePath(p, dir) {
				out = append(out, p)
			}
		}
		if len(out) == len(parts) {
			return nil
		}
		return out
	})
}

// HasUserPath reports whether dir is in the user's PATH.
func HasUserPath(dir string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	cur, _, _ := k.GetStringValue("Path")
	for _, p := range strings.Split(cur, ";") {
		if samePath(p, dir) {
			return true
		}
	}
	return false
}

// editUserPath rewrites HKCU\Environment\Path with edit (nil = no change),
// keeping it REG_EXPAND_SZ so entries like %USERPROFILE%\bin keep working.
func editUserPath(edit func([]string) []string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	cur, _, err := k.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, err
	}
	var parts []string
	for _, p := range strings.Split(cur, ";") {
		if strings.TrimSpace(p) != "" {
			parts = append(parts, p)
		}
	}
	next := edit(parts)
	if next == nil {
		return false, nil
	}
	if err := k.SetExpandStringValue("Path", strings.Join(next, ";")); err != nil {
		return false, err
	}
	broadcastEnvChange()
	return true, nil
}

func samePath(a, b string) bool {
	clean := func(p string) string {
		return strings.ToLower(filepath.Clean(strings.TrimRight(strings.TrimSpace(p), `\/`)))
	}
	return clean(a) == clean(b)
}

var sendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

// broadcastEnvChange tells Explorer (and so new terminals) to reload the environment.
func broadcastEnvChange() {
	const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xffff, 0x001A, 0x0002
	env, _ := windows.UTF16PtrFromString("Environment")
	var result uintptr
	sendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)),
		smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&result)))
}

// SetRunAtSignIn registers (command != "") or removes a program started when
// the user signs in to Windows.
func SetRunAtSignIn(name, command string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if command == "" {
		if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	return k.SetStringValue(name, command)
}

// RunAtSignIn returns the registered command for name ("" if none).
func RunAtSignIn(name string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue(name)
	return v
}
