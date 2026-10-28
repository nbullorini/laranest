package cli

import (
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/nbullorini/laranest/internal/config"
	"github.com/nbullorini/laranest/internal/service"
	"github.com/nbullorini/laranest/internal/trust"
	"github.com/nbullorini/laranest/internal/wsl"
)

func trustCmd() *cobra.Command {
	var linux, status bool
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Make your browsers trust Laranest's HTTPS certificates",
		Long: "On Windows and on WSL it adds Laranest's local CA to your Windows user (for Chrome,\n" +
			"Edge and Firefox on Windows; Windows asks you to confirm). On Ubuntu it adds it to the\n" +
			"system store (asks for sudo) and to Chrome/Chromium and Firefox (deb or snap).",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			state, err := config.StateDir()
			if err != nil {
				return err
			}
			root := filepath.Join(state, "caddy", "pki", "authorities", "local", "root.crt")
			env := wsl.Env{Distro: cfg.WSLDistro, Profile: cfg.WindowsProfile}

			onWindows, onMac := runtime.GOOS == "windows", runtime.GOOS == "darwin"
			if !status {
				if wsl.IsWSL() || onWindows {
					fmt.Println("Adding Laranest's CA to your Windows user: accept the Windows dialog that pops up…")
					if err := trust.InstallWindows(root, env); err != nil {
						return err
					}
					fmt.Println("✓ Windows trusts Laranest (Chrome, Edge and Firefox on Windows; restart the browser if it was open).")
				}
				if !onWindows && (!wsl.IsWSL() || linux) {
					system := map[bool]string{true: "macOS (the Keychain asks for your password)", false: "Linux (sudo may ask for your password)"}[onMac]
					fmt.Printf("Adding Laranest's CA to %s…\n", system)
					if err := trust.InstallLinux(root); err != nil {
						return err
					}
					fmt.Println("✓ " + map[bool]string{true: "macOS (Safari, Chrome, curl)", false: "Linux"}[onMac] + " trusts Laranest.")
					if !trust.Check(root, env).NSSTool {
						fmt.Printf("! Firefox needs certutil to trust it too: %s && laranest trust\n", trust.NSSToolInstall())
					}
				}
			}

			st := trust.Check(root, env)
			if !st.Ready {
				return fmt.Errorf("Laranest's CA does not exist yet: start Laranest (%s)", service.Hint)
			}
			fmt.Println("\nStatus:")
			if st.Windows != nil {
				fmt.Printf("  Windows (Windows browsers): %s\n", yesNo(*st.Windows))
			}
			if onMac {
				fmt.Printf("  macOS (Keychain):            %s\n", yesNo(st.System))
			} else if !onWindows {
				fmt.Printf("  Linux (system, curl):        %s\n", yesNo(st.System))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&linux, "linux", false, "on WSL, also trust it inside Linux (curl, browsers running in WSL)")
	cmd.Flags().BoolVar(&status, "status", false, "only show where the CA is trusted")
	return cmd
}

func yesNo(b bool) string {
	if b {
		return "✓ trusted"
	}
	return "✗ not trusted"
}
