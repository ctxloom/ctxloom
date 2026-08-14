package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

func newManageCmd() *cobra.Command {
	m := &cobra.Command{
		Use:   "manage",
		Short: "Install or remove the pre-tool hook in your LLM agent's config",
	}
	m.AddCommand(newInstallCmd(), newUninstallCmd())
	return m
}

// manageFlags are shared by install and uninstall.
type manageFlags struct {
	engineName     string
	settingsPath   string
	bin            string
	configPath     string
	global         bool
	printOnly      bool
	noDefaultRules bool
	force          bool
}

func (f *manageFlags) bind(c *cobra.Command) {
	c.Flags().StringVar(&f.engineName, "engine", "", "force an engine (default: auto-detect the most relevant)")
	c.Flags().StringVar(&f.settingsPath, "settings", "", "explicit settings file path (overrides the engine default)")
	c.Flags().StringVar(&f.bin, "bin", progName, "the ltk invocation the hook should run")
	c.Flags().StringVar(&f.configPath, "config", defaultConfigPath, "rules file the hook should use (\"\" to omit)")
	c.Flags().BoolVar(&f.global, "global", false, "target the user-level config instead of the project")
	c.Flags().BoolVar(&f.printOnly, "print", false, "print the resulting config to stdout instead of writing")
	c.Flags().BoolVar(&f.noDefaultRules, "no-default-rules", false, "scaffold an empty rules file instead of the shipped defaults")
	c.Flags().BoolVar(&f.force, "force", false, "overwrite an existing rules file (a .bak backup is written first)")
}

// resolve validates the flags shared by install and uninstall, then picks the
// engine and its settings path.
func (f *manageFlags) resolve() (engine.Engine, string, error) {
	// Every byte of the installed hook command line is derived from --bin. An
	// empty (or blank) one still yields a syntactically valid command — it just
	// names no program — which the hook host runs, fails to exec, and, both
	// hosts failing OPEN on a hook error, treats as an allow. Installing that
	// reports success over a guard that gates nothing, permanently. Uninstall
	// takes the same refusal: the command it matches on is built the same way,
	// so an empty --bin could only ever match a hook nobody could have installed.
	if strings.TrimSpace(f.bin) == "" {
		return nil, "", errors.New("--bin must name the ltk invocation the hook should run: " +
			"an empty value builds a hook command that can never execute, and both hook hosts treat a failed hook as an allow")
	}
	var (
		eng engine.Engine
		err error
	)
	if f.engineName != "" {
		if eng, err = engine.Get(f.engineName); err != nil {
			return nil, "", err
		}
	} else {
		var ok bool
		if eng, ok = engine.Detect("."); !ok {
			return nil, "", errors.New("no LLM agent config detected here; pass --engine to choose one")
		}
	}
	path := f.settingsPath
	if path == "" {
		if path, err = eng.SettingsPath(".", f.global); err != nil {
			return nil, "", err
		}
	}
	return eng, path, nil
}

// hookRulesPath is the rules path baked into the installed hook command line.
//
// A user-level (--global) install is consulted in EVERY project on the
// machine, and the hook host runs the hook with the project as its working
// directory, so a PROJECT-RELATIVE path in that command resolves somewhere
// different in each project. In any project that does not happen to have that
// file, evaluate's explicit-config branch denies everything it guards (it
// cannot tell a typo'd path from a deleted rules file, and failing open there
// would silently disable the guard) — so one `manage install --global` becomes
// a machine-wide deny-everything. Omitting the path instead is what --global
// actually wants: evaluate then searches the cwd and its ancestors, finding
// each project's own rules and falling back to the built-in allow-all, with a
// diagnostic, where there are none.
//
// An ABSOLUTE path is honoured: that names one machine-wide rules file, which
// is a coherent thing to ask a user-level install for. install and uninstall
// both route through here so they stay exact inverses — uninstall matches on
// the command string install wrote.
func (f *manageFlags) hookRulesPath() string {
	if f.global && f.configPath != "" && !filepath.IsAbs(f.configPath) {
		return ""
	}
	return f.configPath
}

func newInstallCmd() *cobra.Command {
	f := &manageFlags{}
	c := &cobra.Command{
		Use:   "install",
		Short: "Add the pre-tool hook to the most relevant LLM config",
		Args:  cobra.NoArgs,
		RunE:  f.runInstall,
	}
	f.bind(c)
	return c
}

// runInstall is newInstallCmd's RunE. A METHOD on *manageFlags because the
// command literal is built inside the factory and its body reads that call's
// own flag struct.
//
// The read-modify-write against path (readIfExists through writeFile) runs
// under agent.WithFileLock, closing lively-skillet: `ltk manage install`
// writes the SAME engine settings files (~/.claude/settings.json, a
// project's .mcp.json, ...) ctxloom's own SettingsWriter family locks via the
// identical helper (C6, and the D7-remainder sites this fix's other units
// close) — an unlocked ltk was the one companion binary racing that lock
// from outside it. --print is included in the locked span too: a preview
// that raced a concurrent install could show content that was never
// actually on disk at any single instant.
func (f *manageFlags) runInstall(cmd *cobra.Command, _ []string) error {
	eng, path, err := f.resolve()
	if err != nil {
		return err
	}
	rulesPath := f.hookRulesPath()
	if rulesPath != f.configPath {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: --global installs apply in every project, so the hook is installed "+
			"without --config %s; each project's rules are found by evaluate's own search\n", progName, f.configPath)
	}
	command := eng.HookCommand(f.bin, rulesPath)
	// --print is a dry run: show the merged settings without touching
	// the filesystem, so no rules-file scaffold either.
	if f.configPath != "" && !f.printOnly {
		if err := scaffoldConfig(f.configPath, !f.noDefaultRules, f.force); err != nil {
			return err
		}
	}
	return agent.WithFileLock(afero.NewOsFs(), path, func() error {
		existing, err := readIfExists(path)
		if err != nil {
			return err
		}
		merged, note, err := eng.Install(existing, command)
		if err != nil {
			return err
		}
		if note != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), progName+": %s\n", note)
		}
		if f.printOnly {
			_, err := cmd.OutOrStdout().Write(merged)
			return err
		}
		if err := writeFile(path, merged); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), progName+": installed hook for %s\n  settings: %s\n  command:  %s\n", eng.Name(), path, command)
		return nil
	})
}

func newUninstallCmd() *cobra.Command {
	f := &manageFlags{}
	c := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the pre-tool hook from the LLM config",
		Args:  cobra.NoArgs,
		RunE:  f.runUninstall,
	}
	f.bind(c)
	return c
}

// runUninstall is newUninstallCmd's RunE; a method for the same reason
// runInstall is. Same locked-span reasoning as runInstall's doc.
func (f *manageFlags) runUninstall(cmd *cobra.Command, _ []string) error {
	eng, path, err := f.resolve()
	if err != nil {
		return err
	}
	command := eng.HookCommand(f.bin, f.hookRulesPath())
	return agent.WithFileLock(afero.NewOsFs(), path, func() error {
		existing, err := readIfExists(path)
		if err != nil {
			return err
		}
		if existing == nil {
			fmt.Fprintf(cmd.ErrOrStderr(), progName+": nothing to uninstall (%s not found)\n", path)
			return nil
		}
		updated, removed, err := eng.Uninstall(existing, command)
		if err != nil {
			return err
		}
		if f.printOnly {
			_, err := cmd.OutOrStdout().Write(updated)
			return err
		}
		// No matching hook (e.g. installed under different --bin/--config flags
		// than these): don't rewrite the user's settings file or claim a removal
		// that didn't happen. Rewriting would re-serialize the user's whole
		// settings document — key order, indentation and all — for a removal
		// that never took place.
		if !removed {
			fmt.Fprintf(cmd.ErrOrStderr(), progName+": no matching hook found in %s (nothing removed)\n", path)
			return nil
		}
		if err := writeFile(path, updated); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), progName+": removed hook for %s from %s\n", eng.Name(), path)
		return nil
	})
}

// scaffoldConfig writes a starter rules file. An existing file is NOT
// overwritten unless force is set — your edited rules are never silently
// clobbered. Without force, an existing file is kept and a warning explains how
// to overwrite. With force, the old file is backed up to <path>.bak first.
func scaffoldConfig(path string, withDefaults, force bool) error {
	// Decide and CHECK the content before touching anything — including the
	// --force backup — so a refusal leaves the filesystem exactly as it was.
	content := minimalRules
	if withDefaults {
		content = defaultRules
		// rules.Parse tolerates an empty document (io.EOF), so a rule-less
		// file is a VALID allow-all config: the parser will never object on
		// the user's behalf. Writing one would install a guard that gates
		// nothing and report "wrote rules file" over it. sample.ltk.yaml is
		// GENERATED from docs/DEFAULTS.md by tools/extract-defaults, which
		// checks fence count and parseability but not rule count.
		cfg, err := rules.Parse([]byte(content))
		if err != nil {
			return fmt.Errorf("the built-in default rules do not parse (this is a bug in ltk): %w", err)
		}
		if len(cfg.Rules) == 0 {
			return fmt.Errorf("refusing to write %s: the built-in default rule set contains no rules, "+
				"which would install a guard that allows everything (use --no-default-rules for a deliberately empty config)", path)
		}
	}

	if _, err := os.Stat(path); err == nil {
		if !force {
			fmt.Fprintf(os.Stderr, "%s: %s already exists — keeping your rules (use --force to overwrite; the old file is backed up to %s.bak)\n", progName, path, path)
			return nil
		}
		backup := path + ".bak"
		if err := copyFile(path, backup); err != nil {
			return fmt.Errorf("back up %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "%s: backed up existing rules to %s before overwriting\n", progName, backup)
	} else if !errors.Is(err, os.ErrNotExist) {
		// Neither "exists" nor "does not exist" — a path component that is a
		// file, an unreadable parent, a symlink loop. os.Stat's *fs.PathError
		// already names the path; what it cannot say is which of scaffoldConfig's
		// filesystem steps was underway, so say that, as every sibling here does.
		return fmt.Errorf("check for an existing rules file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// content is always non-empty here: it is one of the two go:embed'd
	// templates (defaults.go), and the rule-count check above already
	// refused an empty defaultRules — so iox's default empty-over-existing
	// refusal never applies, and no AllowEmpty escape hatch is needed.
	if err := iox.WriteFileAtomic(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write rules file %s: %w", path, err)
	}
	fmt.Fprintf(os.Stderr, "%s: wrote rules file %s (edit it to taste)\n", progName, path)
	return nil
}

// copyFile copies src to dst, preserving both contents and permissions (used
// for the --force backup).
//
// The mode is carried across because the backup is a copy of the USER's rules
// file: writing it at a hardcoded 0o644 republishes a config the user
// deliberately restricted to 0600 as world-readable. iox.WriteFileAtomic
// applies perm EXACTLY via its own explicit Chmod (ignoring umask, per its
// doc), so — unlike the os.WriteFile this replaces — nothing further is
// needed after the write to get dst's mode right.
//
// iox.AllowEmpty(): this is a byte-for-byte MIRROR of src, and src's own
// existence was just confirmed by the caller's Stat — but src's SIZE is not
// guaranteed non-zero (a user's rules file legitimately can be, per
// minimalRules' own "valid but empty config" contract). A backup that
// silently refused to reproduce an empty source would stop matching the
// thing it is a backup OF.
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600) // conservative default; src is readable, so Stat succeeds
	if fi, err := os.Stat(src); err == nil {
		mode = fi.Mode().Perm()
	}
	return iox.WriteFileAtomic(dst, b, mode, iox.AllowEmpty())
}

func readIfExists(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return b, nil
}

// writeFile writes data to path atomically via shared/iox (a sibling temp file
// fsynced and renamed into place), so an interrupt mid-write can never leave a
// truncated settings file — those files hold the user's unrelated config too.
// The parent dir is created and an existing file's permissions are preserved.
func writeFile(path string, data []byte) error {
	// These files hold the user's unrelated configuration, and the payload
	// comes from engine code. An empty one can only be an upstream bug, and
	// WriteFileAtomic would make the truncation durable while the caller
	// printed "installed hook for ...".
	if len(data) == 0 {
		return fmt.Errorf("refusing to write an empty %s (the engine produced no settings)", path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := iox.WriteFileAtomic(path, data, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
