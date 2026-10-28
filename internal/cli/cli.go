// Package cli defines the `laranest` command tree.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nbullorini/laranest/internal/apps"
	"github.com/nbullorini/laranest/internal/config"
	"github.com/nbullorini/laranest/internal/mcpserver"
	"github.com/nbullorini/laranest/internal/selfupdate"
	"github.com/nbullorini/laranest/internal/server"
	"github.com/nbullorini/laranest/internal/service"
	"github.com/nbullorini/laranest/internal/version"
	"github.com/nbullorini/laranest/internal/wsl"
	"github.com/nbullorini/laranest/web"
)

func Execute() error {
	root := &cobra.Command{
		Use:           "laranest",
		Short:         "Run your local Laravel apps with zero config",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(serveCmd(), installCmd(), uninstallCmd(), statusCmd(), versionCmd(), updateCmd(), mcpCmd())
	if runtime.GOOS != "windows" { // Windows lets any user listen on 80/443
		root.AddCommand(setupPortsCmd())
	}
	root.AddCommand(phpCmds()...)
	root.AddCommand(trustCmd(), cgiPoolCmd(), scheduleLoopCmd())
	return root.Execute()
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the Laranest daemon in the foreground",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			// Claim the panel port first: a second daemon (e.g. started twice
			// at sign-in) must exit before touching the apps.
			ln, err := net.Listen("tcp", cfg.PanelAddr)
			if err != nil {
				return fmt.Errorf("the panel address %s is not available (is Laranest already running?): %w", cfg.PanelAddr, err)
			}
			mgr, err := apps.NewManager(cfg)
			if err != nil {
				ln.Close()
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			defer service.MarkRunning()()

			mgr.Boot()
			defer mgr.Shutdown()
			return server.New(cfg, web.UI(), mgr).Serve(ctx, ln)
		},
	}
}

func installCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install and start Laranest as a background service for your user",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() == 0 {
				return errors.New("Laranest runs as your normal user, not root: run `laranest install` without sudo from your own account")
			}
			bin, err := os.Executable()
			if err != nil {
				return err
			}
			if bin, err = filepath.EvalSymlinks(bin); err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if wsl.IsWSL() {
				if env, err := wsl.DetectEnv(); err != nil {
					fmt.Fprintln(os.Stderr, "warning:", err, "(keeping WSL awake will not be available)")
				} else {
					cfg.WSLDistro, cfg.WindowsProfile = env.Distro, env.Profile
				}
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			// Capture the user's PATH so the service finds npm/node (nvm), composer, etc.
			if err := service.Install(bin, os.Getenv("PATH")); err != nil {
				return err
			}
			fmt.Printf("Laranest is running → http://%s\n", strings.Replace(cfg.PanelAddr, "127.0.0.1", "localhost", 1))
			if runtime.GOOS == "windows" {
				fmt.Println("It starts automatically when you sign in to Windows. Open a new terminal to use the `laranest` command.")
			}
			return nil
		},
	}
}

func uninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop every app and remove the background service (keeps your config)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := service.Uninstall(); err != nil {
				return err
			}
			fmt.Println("Laranest service removed.")
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the daemon is running",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			fmt.Printf("service: %s\n", service.Status())

			client := http.Client{Timeout: 2 * time.Second}
			resp, err := client.Get("http://" + cfg.PanelAddr + "/api/health")
			if err != nil {
				fmt.Printf("panel:   unreachable (%v)\n         %s\n", err, service.Hint)
				return nil
			}
			defer resp.Body.Close()
			var health struct{ Version string }
			_ = json.NewDecoder(resp.Body).Decode(&health)
			fmt.Printf("panel:   http://%s (%s)\n", cfg.PanelAddr, health.Version)
			return nil
		},
	}
}

func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update Laranest to the latest release (keeps your apps and settings)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			st := (&selfupdate.Updater{}).Check(ctx, true)
			if st.Error != "" {
				return errors.New(st.Error)
			}
			if !st.Available {
				fmt.Printf("Laranest is up to date (%s).\n", st.Current)
				return nil
			}
			fmt.Printf("Updating Laranest (%s → latest release)…\n", st.Current)
			if err := selfupdate.Update(ctx, false); err != nil {
				return err
			}
			if runtime.GOOS != "windows" { // there `install` already reported
				fmt.Println("Updated and restarted. Check it with: laranest status")
			}
			return nil
		},
	}
}

func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server (stdio) so AI assistants can manage your apps",
		Long: "Speaks the Model Context Protocol on stdin/stdout. Point your AI client at it, e.g.\n" +
			"  claude mcp add -s user laranest -- laranest mcp\n" +
			"It talks to the running Laranest, so assistants can do what the panel does: add and\n" +
			"configure apps (advanced settings too), start and stop them, read logs and run commands.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return mcpserver.Run(ctx, cfg.PanelAddr)
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Laranest version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.Version)
		},
	}
}

const portsSysctl = "/etc/sysctl.d/99-laranest.conf"

func setupPortsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup-ports",
		Short: "Allow Laranest to use port 80 without root (asks for your sudo password once)",
		Long: "Writes " + portsSysctl + " with net.ipv4.ip_unprivileged_port_start=80 so Laranest\n" +
			"can serve http://<app>.localhost on port 80 without running as root. Unlike setcap,\n" +
			"it survives binary upgrades. Run it as your user: it calls sudo itself.",
		RunE: func(cmd *cobra.Command, args []string) error {
			script := fmt.Sprintf("echo 'net.ipv4.ip_unprivileged_port_start=80' > %s && sysctl -q -p %s", portsSysctl, portsSysctl)
			var c *exec.Cmd
			if os.Geteuid() == 0 {
				c = exec.Command("sh", "-c", script) // already root (sudo ~/.local/bin/laranest …)
			} else {
				fmt.Println("This needs administrator rights. It will run with sudo:")
				fmt.Println("  " + script)
				// sudo remembers the password for a few minutes: only announce a
				// prompt when there will actually be one.
				if exec.Command("sudo", "-n", "true").Run() == nil {
					fmt.Println("(sudo already has an active session: it will not ask for your password)")
				} else {
					fmt.Println("sudo will ask for your Linux password:")
				}
				c = exec.Command("sudo", "sh", "-c", script)
			}
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				return err
			}
			fmt.Printf("✓ Done: created %s; Laranest can now use port 80 without root.\n", portsSysctl)

			// The sysctl doesn't help if something else already owns port 80.
			if ln, err := net.Listen("tcp", "127.0.0.1:80"); err != nil {
				if strings.Contains(err.Error(), "address already in use") {
					fmt.Println("! Port 80 is used by another program (usually apache2 or nginx). To free it:")
					fmt.Println("    sudo systemctl disable --now apache2   # or nginx")
				} else {
					fmt.Println("! Port 80 still cannot be used:", err)
				}
			} else {
				ln.Close()
			}
			fmt.Println("Then pick port 80 in the panel settings (or restart: systemctl --user restart laranest).")
			return nil
		},
	}
}
