package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// Hidden commands the daemon uses as app processes where the platform lacks
// something (Windows: no FPM, no sh).

// cgiPoolCmd runs php-cgi FastCGI workers on consecutive ports (Windows has
// no FPM and php-cgi serves one request at a time), restarting any that
// exits. Running inside the process's Job Object, the workers die with it.
func cgiPoolCmd() *cobra.Command {
	var addr string
	var workers int
	cmd := &cobra.Command{
		Use:    "cgi-pool --addr 127.0.0.1:PORT --workers N -- php-cgi [args...]",
		Short:  "Run php-cgi workers on consecutive ports (used by the daemon on Windows)",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, argv []string) error {
			host, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				return err
			}
			port, err := strconv.Atoi(portStr)
			if err != nil || workers < 1 {
				return errors.New("invalid --addr or --workers")
			}
			var wg sync.WaitGroup
			for i := range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					bind := net.JoinHostPort(host, strconv.Itoa(port+i))
					// On Windows processes with the same cache_id share OPcache's
					// memory, which fails at random when ASLR maps it elsewhere
					// ("Opcode handlers are unusable due to ASLR"): one per worker.
					cacheID := "opcache.cache_id=laranest-" + strconv.Itoa(port+i)
					keepRunning(append(append([]string{}, argv...), "-d", cacheID, "-b", bind),
						// 0 = never exit after N requests (php-cgi defaults to 500).
						"PHP_FCGI_MAX_REQUESTS=0")
				}()
			}
			wg.Wait()
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:19100", "first worker's address")
	cmd.Flags().IntVar(&workers, "workers", 4, "number of workers")
	return cmd
}

// keepRunning runs argv forever, restarting it a second after it exits.
func keepRunning(argv []string, env ...string) {
	for {
		c := exec.Command(argv[0], argv[1:]...)
		c.Env = append(os.Environ(), env...)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v; restarting\n", argv[len(argv)-1], err)
		}
		time.Sleep(time.Second)
	}
}

// scheduleLoopCmd runs a command at the start of every minute: `schedule:work`
// for Laravel < 8, which lacks it (on Linux a sh loop does the same).
func scheduleLoopCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "schedule-loop -- php artisan schedule:run",
		Short:  "Run a command every minute (Laravel < 8 scheduler)",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, argv []string) error {
			for {
				time.Sleep(time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)))
				c := exec.CommandContext(context.Background(), argv[0], argv[1:]...)
				c.Stdout, c.Stderr = os.Stdout, os.Stderr
				if err := c.Run(); err != nil {
					fmt.Fprintln(os.Stderr, "schedule:run:", err)
				}
			}
		},
	}
}
