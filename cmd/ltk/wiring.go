// Rules wiring shared by BOTH commands.
//
// `evaluate` (the hook) and `check` (the query surface) reach the same verdict
// by the same route — find the rules config, load it, expand the @submodules
// sentinel, build the decider — and differ only in what they do when a step
// FAILS: the hook denies, because both hook hosts read a non-zero exit as an
// allow, while check errors, because it is an explicit command. Keeping the
// shared route in a file of its own is what stops "check said allow" and "the
// hook allowed it" drifting apart; the error policies stay with their own
// commands, where they belong.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/ltk/app"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
	"github.com/ctxloom/ctxloom/internal/ltk/scm"
	"github.com/ctxloom/ctxloom/internal/ltk/shellenv"
)

// configSearch lists the default config locations, in order. The .ltk/ layout
// (config + override state together) is preferred; the flat .ltk.yaml is kept
// for back-compat.
var configSearch = []string{
	defaultConfigPath, // .ltk/config.yaml
	legacyConfig,      // .ltk.yaml
	"llm-tool-killer.yaml",
	".llm-tool-killer.yaml",
	".config/llm-tool-killer.yaml",
}

// newDecider builds the rules app both `evaluate` and `check` decide with.
// The two surfaces differ deliberately in ERROR POLICY — the hook fails
// closed, the query surface fails loud — but they must never differ in how a
// decision is REACHED, or the answer to "would this be blocked?" stops
// predicting what the hook does. Keeping the force-shell override and the
// host-shell inference in one place is what makes that structural rather than
// a convention two call sites have to remember.
func newDecider(cfg *rules.Config, forceShell ir.Shell) *app.App {
	return app.New(cfg, app.Shells{
		Force: forceShell,
		Host:  shellenv.ShellFromPath(os.Getenv("SHELL")),
	})
}

// expandSubmodules resolves the `@submodules` path sentinel against this
// repo's .gitmodules, so a rule can block edits inside every submodule without
// naming them. getwd is os.Getwd in production and a stub in tests.
//
// EVERY step reports its failure, including getwd's. Failing to resolve the
// sentinel is NOT the same as "there are no submodules": an unknown working
// directory, an unreadable .gitmodules, or a submodule path that is not a
// valid glob all leave a `path: ["@submodules"]` rule holding the literal
// sentinel, which matches no real path — so a rule written to protect every
// submodule protects none and every edit sails through as an allow. The
// callers decide what to do about it (evaluate denies, check errors); what
// they must not be handed is a nil error over an expansion that never
// happened.
func expandSubmodules(cfg *rules.Config, getwd func() (string, error)) error {
	wd, err := getwd()
	if err != nil {
		return fmt.Errorf("determine the working directory: %w", err)
	}
	subs, err := scm.SubmodulePaths(afero.NewOsFs(), wd)
	if err != nil {
		return err
	}
	return cfg.ExpandSubmodules(subs)
}

// knownShells renders ir.KnownShells() for a diagnostic message.
func knownShells() string {
	shells := ir.KnownShells()
	names := make([]string, len(shells))
	for i, s := range shells {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}

// loadConfig loads the given path, or searches the default locations in the
// cwd and each ancestor up to the repository root, returning the resolved
// path (empty when falling back to the built-in allow-all config).
//
// The ancestor walk matters because hook hosts can differ in the cwd they
// give hooks: Claude Code runs them at the project root; antigravity, before
// it was removed in 0.7.0, ran them inside <workspace>/.agents instead. A
// cwd-only search would have missed the project's rules under that second
// cwd and silently fallen back to the built-in allow-all config — the wrong
// direction for a guard to fail — so the walk stays general for whichever
// second host arrives next.
func loadConfig(path string) (*rules.Config, string, error) {
	if path != "" {
		c, err := rules.Load(path)
		return c, path, err
	}
	for _, dir := range configSearchDirs() {
		for _, candidate := range configSearch {
			p := filepath.Join(dir, candidate)
			if _, err := os.Stat(p); err == nil {
				c, err := rules.Load(p)
				return c, p, err
			}
		}
	}
	return rules.Empty(), "", nil
}

// configSearchDirs returns the cwd and its ancestors, stopping at the first
// directory containing a .git DIRECTORY (the main repository root holds the
// project's rules; directories above it are someone else's territory) or at
// the filesystem root for non-repo workspaces.
//
// A .git FILE (a gitfile pointer) marks a submodule working tree or a linked
// worktree, and is deliberately NOT a boundary: stopping there would make a
// superproject's rules silently vanish inside its submodules (allow-all — the
// wrong direction for a guard to fail). Continuing past it is safe for both
// layouts because loadConfig takes the NEAREST config first, so the extra
// ancestor dirs are only fallback candidates: a worktree (or submodule)
// carrying its own .ltk still wins, and when it carries none, an ancestor
// config — the superproject/monorepo case — is exactly the one that should
// apply.
func configSearchDirs() []string {
	wd, err := os.Getwd()
	if err != nil {
		return []string{"."}
	}
	var dirs []string
	for dir := wd; ; {
		dirs = append(dirs, dir)
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dirs
}
