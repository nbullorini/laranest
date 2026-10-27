package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nbullorini/laranest/internal/apps"
	"github.com/nbullorini/laranest/internal/config"
	"github.com/nbullorini/laranest/internal/php"
	"github.com/nbullorini/laranest/internal/tools"
	"github.com/nbullorini/laranest/internal/winenv"
)

// RunShim handles invocations through the optional `php` / `composer`
// symlinks (see `laranest shims`). It reports whether argv[0] was a shim.
func RunShim() (bool, error) {
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"))
	switch name {
	case "php":
		return true, execPHP(os.Args[1:])
	case "composer":
		return true, execComposer(os.Args[1:])
	}
	return false, nil
}

func phpCmds() []*cobra.Command {
	passthrough := func(use, short string, run func([]string) error) *cobra.Command {
		return &cobra.Command{
			Use:                use + " [args...]",
			Short:              short,
			DisableFlagParsing: true, // everything goes to php/composer verbatim
			RunE:               func(cmd *cobra.Command, args []string) error { return run(args) },
		}
	}
	return []*cobra.Command{
		passthrough("php", "Run PHP with the version of the project in the current directory", execPHP),
		passthrough("composer", "Run Composer with the version of PHP of the current project", execComposer),
		passthrough("npm", "Run npm (system Node if installed, else Laranest's)", func(a []string) error { return execNode("npm", a) }),
		passthrough("npx", "Run npx (system Node if installed, else Laranest's)", func(a []string) error { return execNode("npx", a) }),
		passthrough("node", "Run Node (system Node if installed, else Laranest's)", func(a []string) error { return execNode("node", a) }),
		{
			Use:   "which-php",
			Short: "Show which PHP the current directory uses",
			RunE: func(cmd *cobra.Command, args []string) error {
				rt, err := resolveHere()
				if err != nil {
					return err
				}
				fmt.Printf("%s (PHP %s, %s)\n", rt.CLI, describeVersion(rt), rt.Source)
				return nil
			},
		},
		{
			Use:   "php:list",
			Short: "List installed and downloadable PHP versions",
			RunE:  func(cmd *cobra.Command, args []string) error { return listPHP() },
		},
		{
			Use:   "php:install <version>",
			Short: "Download a PHP version (e.g. 8.3) without root",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				rs, err := runtimes()
				if err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "Downloading PHP %s…\n", args[0])
				rt, err := rs.Installer.Install(context.Background(), args[0])
				if err != nil {
					return err
				}
				fmt.Printf("PHP %s installed in %s\n", describeVersion(rt), filepath.Dir(rt.CLI))
				return nil
			},
		},
		{
			Use:   "php:update [version...]",
			Short: "Update PHP versions downloaded by Laranest to their latest build",
			RunE:  func(cmd *cobra.Command, args []string) error { return updatePHP(args) },
		},
		{
			Use:   "php:remove <version>",
			Short: "Remove a PHP version downloaded by Laranest",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				rs, err := runtimes()
				if err != nil {
					return err
				}
				return rs.Installer.Remove(args[0])
			},
		},
		shimsCmd(),
	}
}

func runtimes() (*apps.Runtimes, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return apps.NewRuntimes(cfg)
}

func resolveHere() (php.Runtime, error) {
	rs, err := runtimes()
	if err != nil {
		return php.Runtime{}, err
	}
	dir, err := os.Getwd()
	if err != nil {
		return php.Runtime{}, err
	}
	if len(rs.Installed()) == 0 {
		fmt.Fprintln(os.Stderr, "Laranest: downloading PHP (first time only)…")
	}
	return rs.ResolveDir(context.Background(), dir)
}

// execPHP replaces this process with the right PHP, so signals, exit codes
// and TTYs (tinker, artisan prompts) behave exactly like running php directly.
func execPHP(args []string) error {
	rt, err := resolveHere()
	if err != nil {
		return err
	}
	argv := rt.Command(args...)
	return replaceProcess(argv, append(envWithShims(), rt.Env()...))
}

// envWithShims puts Laranest's php/composer shims first on PATH, so scripts
// that call plain `php` (Composer scripts like "php artisan ide-helper:…",
// tools that shell out) run the project's PHP too, even where the system has
// none (Windows) or a different one.
func envWithShims() []string {
	rs, err := runtimes()
	if err != nil {
		return os.Environ()
	}
	shims, err := rs.Tools.PHPShims()
	if err != nil {
		return os.Environ()
	}
	return withEnv(os.Environ(), "PATH", shims+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func execComposer(args []string) error {
	rt, err := resolveHere()
	if err != nil {
		return err
	}
	rs, err := runtimes()
	if err != nil {
		return err
	}
	phar, err := rs.Tools.ComposerPhar(context.Background())
	if err != nil {
		return err
	}
	argv := rt.Command(append([]string{phar}, args...)...)
	return replaceProcess(argv, append(envWithShims(), rt.Env()...))
}

// execNode runs node/npm/npx from the same Node that Vite uses, downloading the
// LTS on first use, with it first on PATH (npm scripts call `node`).
func execNode(name string, args []string) error {
	rs, err := runtimes()
	if err != nil {
		return err
	}
	bin, err := rs.Tools.NodeBinDir(context.Background())
	if err != nil {
		return err
	}
	path := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	if shims, err := rs.Tools.PHPShims(); err == nil {
		path = bin + string(os.PathListSeparator) + shims + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	env := withEnv(os.Environ(), "PATH", path)
	return replaceProcess(tools.NodeCommand(bin, name, args...), env)
}

func listPHP() error {
	rs, err := runtimes()
	if err != nil {
		return err
	}
	rels, relErr := rs.Catalog.Releases(context.Background())
	latest := map[string]php.Release{}
	for _, r := range rels {
		latest[r.Minor] = r
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "VERSION\tSOURCE\tSTATUS")
	for _, rt := range rs.Installed() {
		full := rt.FullVersion()
		status := "installed"
		if rel, ok := latest[rt.Version]; ok && rt.Source == php.SourceLaranest && php.UpdateAvailable(rt, full, rel) {
			what := rel.Version
			if rel.Version == full {
				what = "a new build of " + full
			}
			status = fmt.Sprintf("update available: %s → laranest php:update %s", what, rt.Version)
		} else if rt.Source == php.SourceSystem {
			status = "installed (updated with apt)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", full, rt.Source, status)
		delete(latest, rt.Version)
	}
	for _, r := range rels {
		if _, ok := latest[r.Minor]; ok {
			fmt.Fprintf(w, "%s\t%s\tavailable: laranest php:install %s\n", r.Version, r.Source, r.Minor)
		}
	}
	w.Flush()
	if relErr != nil {
		fmt.Fprintln(os.Stderr, "(could not fetch the downloadable versions:", relErr, ")")
	}
	return nil
}

// updatePHP asks the daemon (so it restarts the apps using each version) and
// falls back to updating the files directly when it isn't running.
func updatePHP(versions []string) error {
	rs, err := runtimes()
	if err != nil {
		return err
	}
	rels, err := rs.Catalog.Refresh(context.Background())
	if err != nil {
		return err
	}
	latest := map[string]php.Release{}
	for _, r := range rels {
		latest[r.Minor] = r
	}
	if len(versions) == 0 {
		for _, rt := range rs.Installed() {
			if rel, ok := latest[rt.Version]; ok && rt.Source == php.SourceLaranest && php.UpdateAvailable(rt, rt.FullVersion(), rel) {
				versions = append(versions, rt.Version)
			}
		}
		if len(versions) == 0 {
			fmt.Println("All Laranest PHP versions are up to date.")
			return nil
		}
	}

	cfg, _ := config.Load()
	for _, v := range versions {
		fmt.Fprintf(os.Stderr, "Updating PHP %s to %s…\n", v, latest[v].Version)
		restarted, err := daemonUpdatePHP(cfg, v)
		if errors.Is(err, errDaemonDown) {
			_, err = rs.Installer.Update(context.Background(), v)
			if err == nil {
				fmt.Println("  done (the daemon is not running: restart the apps that use this version)")
			}
		} else if err == nil && len(restarted) > 0 {
			fmt.Println("  done; restarted:", strings.Join(restarted, ", "))
		} else if err == nil {
			fmt.Println("  done")
		}
		if err != nil {
			return fmt.Errorf("PHP %s: %w", v, err)
		}
	}
	return nil
}

var errDaemonDown = errors.New("daemon unavailable")

func daemonUpdatePHP(cfg config.Config, version string) ([]string, error) {
	resp, err := http.Post("http://"+cfg.PanelAddr+"/api/php/"+version+"/update", "application/json", nil)
	if err != nil {
		return nil, errDaemonDown
	}
	defer resp.Body.Close()
	var body struct {
		Restarted []string `json:"restarted"`
		Error     string   `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Error != "" {
		return body.Restarted, errors.New(body.Error)
	}
	return body.Restarted, nil
}

func describeVersion(rt php.Runtime) string {
	if rt.Full != "" {
		return rt.Full
	}
	return rt.Version
}

// ---- shims ----

const (
	rcBegin = "# >>> laranest shims >>>"
	rcEnd   = "# <<< laranest shims <<<"
)

func shimsDir() (string, error) {
	data, err := config.DataDir()
	return filepath.Join(data, "shims"), err
}

func shimsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shims",
		Short: "Make plain `php` and `composer` use each project's PHP version (opt-in)",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "enable",
		Short: "Add php/composer shims to PATH via your shell rc files",
		RunE: func(cmd *cobra.Command, args []string) error {
			rs, err := runtimes()
			if err != nil {
				return err
			}
			dir, err := rs.Tools.PHPShims()
			if err != nil {
				return err
			}
			if runtime.GOOS == "windows" {
				if _, err := winenv.AddUserPath(dir); err != nil {
					return err
				}
				fmt.Println("Added", dir, "to your user PATH. Open a new terminal and try `where php`.")
				fmt.Println("(Programs earlier in PATH win: remove other PHP installs from PATH if `where php` lists them first.)")
				return nil
			}
			block := fmt.Sprintf("%s\nexport PATH=\"%s:$PATH\"\n%s\n", rcBegin, dir, rcEnd)
			for _, rc := range rcFiles() {
				if err := rewriteRC(rc, block); err != nil {
					return err
				}
				fmt.Println("Updated", rc)
			}
			fmt.Println("Done. Open a new terminal (or `source ~/.bashrc`) and try `which php`.")
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "disable",
		Short: "Remove the shims and the PATH lines",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := shimsDir()
			if err != nil {
				return err
			}
			if runtime.GOOS == "windows" {
				if _, err := winenv.RemoveUserPath(dir); err != nil {
					return err
				}
				_ = os.RemoveAll(dir) // fails for a shim that is running; harmless once off PATH
				fmt.Println("Shims disabled. Open a new terminal.")
				return nil
			}
			_ = os.RemoveAll(dir)
			for _, rc := range rcFiles() {
				if err := rewriteRC(rc, ""); err != nil {
					return err
				}
			}
			fmt.Println("Shims disabled. Open a new terminal.")
			return nil
		},
	})
	return cmd
}

// rcFiles are the shell startup files to touch: bash's always, zsh's if present.
func rcFiles() []string {
	home, _ := os.UserHomeDir()
	files := []string{filepath.Join(home, ".bashrc")}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); err == nil {
		files = append(files, filepath.Join(home, ".zshrc"))
	}
	return files
}

// rewriteRC replaces Laranest's marked block in rc with block ("" removes it),
// appending it at the end so it wins over earlier PATH edits (e.g. Herd-lite).
func rewriteRC(rc, block string) error {
	data, err := os.ReadFile(rc)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content := string(data)
	if i := strings.Index(content, rcBegin); i >= 0 {
		if j := strings.Index(content[i:], rcEnd); j >= 0 {
			content = content[:i] + strings.TrimLeft(content[i+j+len(rcEnd):], "\n")
		}
	}
	if block != "" {
		content = strings.TrimRight(content, "\n") + "\n\n" + block
	}
	return os.WriteFile(rc, []byte(content), 0o644)
}

// withEnv sets key in env, replacing it: a duplicate PATH= would lose,
// since the first occurrence is the one programs see. Windows names are
// case-insensitive (its PATH is "Path").
func withEnv(env []string, key, value string) []string {
	out := []string{key + "=" + value}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if name != key && (runtime.GOOS != "windows" || !strings.EqualFold(name, key)) {
			out = append(out, kv)
		}
	}
	return out
}
