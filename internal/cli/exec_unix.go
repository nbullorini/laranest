//go:build !windows

package cli

import "syscall"

// replaceProcess replaces this process with argv, so signals, exit codes and
// TTYs (tinker, artisan prompts) behave exactly like running it directly.
func replaceProcess(argv, env []string) error {
	return syscall.Exec(argv[0], argv, env)
}
