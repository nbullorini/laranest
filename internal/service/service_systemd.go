//go:build !windows

package service

import "github.com/nbullorini/laranest/internal/systemd"

// Install installs binary as the service (with envPath as its PATH) and starts it.
func Install(binary, envPath string) error { return systemd.Install(binary, envPath) }

// Uninstall stops every app process and removes the service.
func Uninstall() error { return systemd.Uninstall() }

// Status is "active", "inactive"… (systemd's ActiveState).
func Status() string { return systemd.Status() }

// Hint tells how to start the service when the panel is unreachable.
const Hint = "systemctl --user start laranest"

// MarkRunning is a no-op: systemd tracks the daemon.
func MarkRunning() func() { return func() {} }
