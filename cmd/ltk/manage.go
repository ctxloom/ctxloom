package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
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

// resolve picks the engine and its settings path.
func (f *manageFlags) resolve() (engine.Engine, string, error) {
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

func newInstallCmd() *cobra.Command {
	f := &manageFlags{}
	c := &cobra.Command{
		Use:   "install",
		Short: "Add the pre-tool hook to the most relevant LLM config",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			eng, path, err := f.resolve()
			if err != nil {
				return err
			}
			command := eng.HookCommand(f.bin, f.configPath)
			// --print is a dry run: show the merged settings without touching
			// the filesystem, so no rules-file scaffold either.
			if f.configPath != "" && !f.printOnly {
				if err := scaffoldConfig(f.configPath, !f.noDefaultRules, f.force); err != nil {
					return err
				}
			}
			existing, err := readIfExists(path)
			if err != nil {
				return err
			}
			merged, note, err := eng.Install(existing, command)
			if err != nil {
				return err
			}
			if note != "" {
				fmt.Fprintf(os.Stderr, progName+": %s\n", note)
			}
			if f.printOnly {
				_, err := os.Stdout.Write(merged)
				return err
			}
			if err := writeFile(path, merged); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, progName+": installed hook for %s\n  settings: %s\n  command:  %s\n", eng.Name(), path, command)
			return nil
		},
	}
	f.bind(c)
	return c
}

func newUninstallCmd() *cobra.Command {
	f := &manageFlags{}
	c := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the pre-tool hook from the LLM config",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			eng, path, err := f.resolve()
			if err != nil {
				return err
			}
			command := eng.HookCommand(f.bin, f.configPath)
			existing, err := readIfExists(path)
			if err != nil {
				return err
			}
			if existing == nil {
				fmt.Fprintf(os.Stderr, progName+": nothing to uninstall (%s not found)\n", path)
				return nil
			}
			updated, removed, err := eng.Uninstall(existing, command)
			if err != nil {
				return err
			}
			if f.printOnly {
				_, err := os.Stdout.Write(updated)
				return err
			}
			// No matching hook (e.g. installed under different --bin/--config flags
			// than these): don't rewrite the user's settings file or claim a removal
			// that didn't happen.
			if !removed {
				fmt.Fprintf(os.Stderr, progName+": no matching hook found in %s (nothing removed)\n", path)
				return nil
			}
			if err := writeFile(path, updated); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, progName+": removed hook for %s from %s\n", eng.Name(), path)
			return nil
		},
	}
	f.bind(c)
	return c
}

// scaffoldConfig writes a starter rules file. An existing file is NOT
// overwritten unless force is set — your edited rules are never silently
// clobbered. Without force, an existing file is kept and a warning explains how
// to overwrite. With force, the old file is backed up to <path>.bak first.
func scaffoldConfig(path string, withDefaults, force bool) error {
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
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content := minimalRules
	if withDefaults {
		content = defaultRules
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write rules file %s: %w", path, err)
	}
	fmt.Fprintf(os.Stderr, "%s: wrote rules file %s (edit it to taste)\n", progName, path)
	return nil
}

// copyFile copies src to dst, preserving contents (used for the --force backup).
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
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
