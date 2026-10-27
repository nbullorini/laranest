//go:build windows

package cli

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
)

// replaceProcess runs argv attached to this console and exits with its exit
// code: Windows can't replace a process. Ctrl+C reaches both; we ignore it
// and let the child decide (artisan, tinker and composer handle it).
func replaceProcess(argv, env []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Notify(make(chan os.Signal, 1), os.Interrupt)
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
