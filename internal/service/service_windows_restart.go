//go:build windows

package service

// Restart isn't used on Windows: updates run the new binary's install,
// which restarts the daemon (see selfupdate).
func Restart(fromDaemon bool) error { return nil }
