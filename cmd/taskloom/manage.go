package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/taskloom/engine"
)

// manage registers the `taskloom mcp` server with agent backends directly —
// taskloom's standalone path, no ctxloom required. ctxloom users get the same
// registration from the embedded taskloom bundle instead; the two never fight
// because both write the same entry under the same key.

var (
	manageEngine    string
	manageDir       string
	manageProject   bool
	managePrintOnly bool
)

var manageCmd = &cobra.Command{
	Use:   "manage",
	Short: "Register the taskloom MCP server with agent backends",
}

var manageInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Add the taskloom MCP server to backend configs",
	Long: `Register ` + "`taskloom mcp`" + ` as an MCP server. By default every backend
present at the chosen scope is updated (user-level: Claude Code, Codex, and
Kiro configs under your home directory).
Name one with --engine to register just that backend — creating its config
if needed. --project writes the project-scoped config under --dir instead of
the user-level one.`,
	Args: cobra.NoArgs,
	RunE: runManageInstall,
}

func runManageInstall(*cobra.Command, []string) error {
	return manageInstall(manageEngine, manageDir, !manageProject, managePrintOnly, os.Stderr)
}

var manageUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove the taskloom MCP server from backend configs",
	Args:  cobra.NoArgs,
	RunE:  runManageUninstall,
}

func runManageUninstall(*cobra.Command, []string) error {
	return manageUninstall(manageEngine, manageDir, !manageProject, os.Stderr)
}

var manageCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Report where the taskloom MCP server is registered",
	Args:  cobra.NoArgs,
	RunE:  runManageCheck,
}

func runManageCheck(cmd *cobra.Command, _ []string) error {
	return manageCheck(manageDir, cmd.OutOrStdout())
}

// resolveEngines picks the engines to operate on: the one explicitly named
// (config created if needed), or every backend present at the scope.
func resolveEngines(name, dir string, global bool) ([]engine.Engine, error) {
	if name != "" {
		e, err := engine.Get(name)
		if err != nil {
			return nil, err
		}
		return []engine.Engine{e}, nil
	}
	var engines []engine.Engine
	for _, e := range engine.All() {
		if e.Present(dir, global) {
			engines = append(engines, e)
		}
	}
	return engines, nil
}

func manageInstall(name, dir string, global, printOnly bool, errOut io.Writer) error {
	engines, err := resolveEngines(name, dir, global)
	if err != nil {
		return err
	}
	if len(engines) == 0 {
		return errors.New("no agent backends detected; name one with --engine (claude-code, codex, kiro)")
	}
	server := engine.TaskloomServer()
	// The entry about to be written names a bare command. Say so now if
	// nothing answers to it here, rather than leaving the user with a
	// registration that reports success and a server that never starts.
	if err := engine.VerifyCommandResolvable(); err != nil {
		fmt.Fprintf(errOut, "taskloom: warning: %v\n  the registered MCP entry runs %q, which the agent resolves against ITS OWN PATH at startup; install taskloom somewhere on that PATH or the server will not start\n", err, engine.TaskloomCommand)
	}
	for _, e := range engines {
		path, err := e.ConfigPath(dir, global)
		if err != nil {
			return err
		}
		// The read-modify-write against path (readIfExists through
		// writeConfig) runs under agent.WithFileLock, closing lively-skillet:
		// `taskloom manage install` writes the SAME engine config files
		// ctxloom's own SettingsWriter family locks via the identical
		// helper — an unlocked taskloom was the other companion binary
		// racing that lock from outside it. One lock per engine's own path,
		// since each engine in this loop targets a DIFFERENT file.
		err = agent.WithFileLock(afero.NewOsFs(), path, func() error {
			existing, err := readIfExists(path)
			if err != nil {
				return err
			}
			merged, err := e.Install(existing, engine.TaskloomName, server)
			if err != nil {
				return fmt.Errorf("%s: %w", e.Name(), err)
			}
			if printOnly {
				fmt.Fprintf(errOut, "# %s → %s\n%s", e.Name(), path, merged)
				return nil
			}
			if err := writeConfig(path, merged); err != nil {
				return fmt.Errorf("%s: %w", e.Name(), err)
			}
			fmt.Fprintf(errOut, "taskloom: registered MCP server for %s\n  config: %s\n", e.Name(), path)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func manageUninstall(name, dir string, global bool, errOut io.Writer) error {
	engines, err := resolveEngines(name, dir, global)
	if err != nil {
		return err
	}
	// Nothing to remove from is a legitimate empty state (unlike install,
	// which cannot do the thing the user asked for and so errors), but it is
	// not a silent one: an empty stdout/stderr with exit 0 is exactly what a
	// SUCCESSFUL removal looks like.
	if len(engines) == 0 {
		fmt.Fprintln(errOut, "taskloom: nothing to remove (no agent backends detected; name one with --engine)")
		return nil
	}
	for _, e := range engines {
		path, err := e.ConfigPath(dir, global)
		if err != nil {
			return err
		}
		// Same locked-span reasoning as manageInstall's loop above.
		err = agent.WithFileLock(afero.NewOsFs(), path, func() error {
			existing, err := readIfExists(path)
			if err != nil {
				return err
			}
			if existing == nil {
				return nil
			}
			// Only a config that actually carries the entry is rewritten. Without
			// this, "removed MCP server from <engine>" is printed for a backend
			// that never had it — a success message for a no-op — and the user's
			// config is reformatted by a write that changes nothing.
			installed, err := e.Installed(existing, engine.TaskloomName)
			if err != nil {
				return fmt.Errorf("%s: %w", e.Name(), err)
			}
			if !installed {
				fmt.Fprintf(errOut, "taskloom: not registered with %s, nothing to remove\n  config: %s\n", e.Name(), path)
				return nil
			}
			cleaned, err := e.Uninstall(existing, engine.TaskloomName)
			if err != nil {
				return fmt.Errorf("%s: %w", e.Name(), err)
			}
			if err := writeConfig(path, cleaned); err != nil {
				return fmt.Errorf("%s: %w", e.Name(), err)
			}
			fmt.Fprintf(errOut, "taskloom: removed MCP server from %s\n  config: %s\n", e.Name(), path)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func manageCheck(dir string, out io.Writer) error {
	if err := engine.VerifyCommandResolvable(); err != nil {
		fmt.Fprintln(out, "binary: taskloom NOT on PATH")
	} else {
		fmt.Fprintln(out, "binary: taskloom on PATH")
	}
	for _, e := range engine.All() {
		for _, scope := range []struct {
			label  string
			global bool
		}{{"user", true}, {"project", false}} {
			path, err := e.ConfigPath(dir, scope.global)
			if err != nil {
				continue
			}
			raw, err := readIfExists(path)
			// An absent config is a real empty state and stays silent; a
			// config that exists but cannot be READ is a different answer
			// entirely, and dropping it from the table renders it
			// indistinguishable from "this backend has no config here".
			if err != nil {
				fmt.Fprintf(out, "%-12s %-8s unreadable: %v (%s)\n", e.Name(), scope.label, err, path)
				continue
			}
			if raw == nil {
				continue
			}
			ok, err := e.Installed(raw, engine.TaskloomName)
			if err != nil {
				fmt.Fprintf(out, "%-12s %-8s unreadable: %v (%s)\n", e.Name(), scope.label, err, path)
				continue
			}
			state := "not registered"
			if ok {
				state = "registered"
			}
			fmt.Fprintf(out, "%-12s %-8s %s (%s)\n", e.Name(), scope.label, state, path)
		}
	}
	return nil
}

// readIfExists returns the file's bytes, or nil (no error) when it is absent.
func readIfExists(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return raw, err
}

func writeConfig(path string, data []byte) error {
	// The payload comes from engine code outside this package. An empty one
	// can only be a bug up there, and committing it would atomically truncate
	// the user's real backend config — durably, and reported as success.
	if len(data) == 0 {
		return fmt.Errorf("refusing to write an empty config to %s (the backend produced no content)", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Atomic temp-then-rename (shared family convention): manage rewrites the
	// user's real backend config files, so a crash/power loss mid-write must not
	// leave a truncated config. MkdirAll above satisfies the parent-exists
	// precondition of iox.WriteFileAtomic.
	return iox.WriteFileAtomic(path, data, 0o644)
}

func init() {
	for _, c := range []*cobra.Command{manageInstallCmd, manageUninstallCmd} {
		c.Flags().StringVar(&manageEngine, "engine", "", "Backend to target: claude-code, codex, or kiro (default: all present)")
		c.Flags().BoolVar(&manageProject, "project", false, "Write the project-scoped config under --dir instead of the user-level one")
	}
	manageInstallCmd.Flags().BoolVar(&managePrintOnly, "print-only", false, "Print the merged configs to stderr instead of writing them")
	for _, c := range []*cobra.Command{manageInstallCmd, manageUninstallCmd, manageCheckCmd} {
		c.Flags().StringVar(&manageDir, "dir", ".", "Project directory for project-scoped configs")
	}
	manageCmd.AddCommand(manageInstallCmd)
	manageCmd.AddCommand(manageUninstallCmd)
	manageCmd.AddCommand(manageCheckCmd)
	rootCmd.AddCommand(manageCmd)
}
