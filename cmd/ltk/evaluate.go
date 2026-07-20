package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/ltk/app"
	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
	"github.com/ctxloom/ctxloom/internal/ltk/scm"
	"github.com/ctxloom/ctxloom/internal/ltk/shellenv"
	"github.com/ctxloom/ctxloom/internal/ltk/state"
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

func newEvaluateCmd() *cobra.Command {
	var cfgPath, shellName, engineName string
	c := &cobra.Command{
		Use:   "evaluate",
		Short: "Evaluate a hook payload from stdin and emit an allow/deny decision",
		Long: `Evaluate reads a hook payload (JSON) on stdin and emits an allow/deny decision.

It handles both kinds of payload the editing agent sends:

  • a shell command (tool_input.command) — parsed and matched against command
    rules, in the shell the tool implies (or --shell to force one).
  • a file edit (tool_input.file_path) — matched against file rules (match.path):
    globs, directory subtrees (a trailing slash, e.g. vendor/), and the
    "@submodules" sentinel, which is resolved against this repo's .gitmodules so
    one rule blocks edits inside every submodule.

A denial is written in the engine's format (for Claude Code, a permissionDecision
on stdout, exit 0); an allow writes nothing. Intended to be run by the hook, not
by hand.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return runEvaluate(engineName, cfgPath, ir.Shell(shellName))
		},
	}
	c.Flags().StringVar(&cfgPath, "config", "", "path to rules YAML (default: search cwd)")
	c.Flags().StringVar(&shellName, "shell", "", "force a shell dialect, overriding the engine's tool-derived shell")
	c.Flags().StringVar(&engineName, "engine", "claude-code", "hook engine adapter")
	return c
}

// runEvaluate is the process edge around evaluate: it feeds it stdin, writes
// the resulting streams, and exits non-zero when the engine's protocol demands
// it (a zero exit is a normal return, so deferred cleanup runs).
func runEvaluate(engineName, cfgPath string, forceShell ir.Shell) error {
	out, err := evaluate(engineName, cfgPath, forceShell, os.Stdin)
	if err != nil {
		return err
	}
	if len(out.Stdout) > 0 {
		if _, err := os.Stdout.Write(out.Stdout); err != nil {
			return fmt.Errorf("write decision: %w", err)
		}
	}
	if len(out.Stderr) > 0 {
		os.Stderr.Write(out.Stderr)
	}
	if out.ExitCode != 0 {
		os.Exit(out.ExitCode)
	}
	return nil
}

// evaluate runs the full decision path — decode the hook payload, parse the
// command, match the rules, encode the engine's wire output — with no process
// concerns, so it is testable end to end.
func evaluate(engineName, cfgPath string, forceShell ir.Shell, stdin io.Reader) (engine.Output, error) {
	adapter, err := engine.Get(engineName)
	if err != nil {
		// An unknown --engine in the installed hook (a manual edit or a cross-version
		// rename) would otherwise exit 1, which both hosts treat as a silent allow.
		// There is no adapter for the named engine to deny with, so fall back to the
		// claude-code adapter purely to emit a well-formed fail-closed deny.
		fallback, ferr := engine.Get("claude-code")
		if ferr != nil {
			return engine.Output{}, err // unreachable in practice; nothing to deny with
		}
		return failClosed(fallback, fmt.Sprintf(
			"%s is misconfigured: unknown --engine %q; denying everything it guards until the hook command is fixed",
			progName, engineName))
	}
	// A typo'd --shell in the installed hook command would otherwise surface as
	// ErrUnsupportedShell on every parse, which the default on_parse_error: allow
	// turns into a permanent, silent allow-all. Fail closed instead.
	if forceShell != "" && !forceShell.Valid() {
		return failClosed(adapter, fmt.Sprintf(
			"%s is misconfigured: unknown --shell %q (known: %s); denying everything it guards until the hook command is fixed",
			progName, forceShell, knownShells()))
	}
	cfg, resolved, err := loadConfig(cfgPath)
	if err != nil {
		// A rules file was named (--config) or found by the search, but could not
		// be read or parsed. Erroring out here would fail OPEN — exit 1 disables
		// the guard on both hosts — so a broken config must deny instead. The
		// parse error rides in the reason so the agent relays it to the user.
		return failClosed(adapter, fmt.Sprintf(
			"%s could not load its rules config and is denying everything it guards until the config is fixed: %v",
			progName, err))
	}
	// Resolve the `@submodules` path sentinel against this repo's .gitmodules, so
	// a rule can block edits inside every submodule without naming them.
	if wd, err := os.Getwd(); err == nil {
		cfg.ExpandSubmodules(scm.SubmodulePaths(afero.NewOsFs(), wd))
	}
	// Reading or decoding the hook payload could otherwise return a plain error
	// → exit 1 → fail OPEN on both hosts (the same silent allow-all the --shell
	// and config branches above guard against). An adapter is in hand here, so a
	// malformed/version-skewed payload denies rather than sails through ungated.
	input, err := io.ReadAll(stdin)
	if err != nil {
		return failClosed(adapter, fmt.Sprintf(
			"%s could not read the hook payload and is denying this action: %v", progName, err))
	}
	req, err := adapter.Decode(input)
	if err != nil {
		return failClosed(adapter, fmt.Sprintf(
			"%s could not parse the hook payload and is denying this action: %v", progName, err))
	}

	a := app.New(cfg)
	a.ForceShell = forceShell
	a.HostShell = shellenv.FromEnv(os.Getenv("SHELL"))
	resp := a.Decide(context.Background(), req)

	// A denial may be lifted by "confirm by repeating" only when the rule that
	// fired allows it (inviolate rules report Confirmable=false and never reach
	// here, so repeating them never helps).
	if !resp.Allow && resp.Confirmable && resp.ConfirmWindowSeconds > 0 {
		// The override is keyed on what the agent repeats: the command, or the
		// file path for a file-edit rule.
		key := req.Command
		if req.FilePath != "" {
			key = "edit:" + req.FilePath
		}
		resp = confirmByRepeat(resp, key, statePath(resolved),
			time.Duration(resp.ConfirmDelaySeconds)*time.Second,
			time.Duration(resp.ConfirmWindowSeconds)*time.Second)
	}

	out, err := adapter.Encode(resp)
	if err != nil {
		return engine.Output{}, fmt.Errorf("encode decision: %w", err)
	}
	out.Stderr = append(out.Stderr, ungatedToolWarning(adapter, req)...)
	return out, nil
}

// ungatedToolWarning surfaces a payload whose tool name the adapter cannot
// read. ltk's tool matcher is a hand-maintained list over a VENDOR-OWNED tool
// set: when the vendor ships or renames a shell/file tool, ltk keeps exiting 0
// with no decision and every rule silently stops applying to it (stark-boxer).
//
// It WARNS rather than denying, deliberately. Denying would turn a vendor
// rename into a hard stop on tools ltk was never asked to guard; the failure
// being diagnosed here is silence, so a visible signal is the fix. The exit
// code is untouched for the same reason the rest of this file fails closed
// rather than erroring: both hosts treat a non-zero exit as a silent allow.
func ungatedToolWarning(adapter engine.Adapter, req engine.Request) []byte {
	if !req.ToolUngated {
		return nil
	}
	return []byte(fmt.Sprintf(
		"%s: %s tool %q is not in ltk's gated tool set — NO rules were applied to this call. "+
			"If it can run commands or write files, add it to the engine's gated tools and re-run '%s manage install'.\n",
		progName, adapter.Name(), req.ToolName, progName))
}

// failClosed renders reason as a well-formed deny decision in the engine's wire
// format, exit 0. It exists because both hook hosts fail OPEN when a hook exits
// non-zero — Claude Code treats exit 1 as non-blocking, and agy proceeds on any
// crashing hook (see engine.Antigravity) — so a broken ltk installation must
// never surface as an error exit on the hook path: that would silently disable
// every rule. Explicit user-facing commands (manage, --print) keep their
// fail-loud exit-1 behavior; only `evaluate` fails closed.
func failClosed(adapter engine.Adapter, reason string) (engine.Output, error) {
	out, err := adapter.Encode(engine.Response{Allow: false, Reason: reason})
	if err != nil {
		return engine.Output{}, fmt.Errorf("encode fail-closed decision: %w", err)
	}
	return out, nil
}

// knownShells renders ir.KnownShells for a diagnostic message.
func knownShells() string {
	names := make([]string, len(ir.KnownShells))
	for i, s := range ir.KnownShells {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}

// confirmByRepeat applies the "run it again to permit" override (state.ConfirmByRepeat)
// using the wall clock, and notes on stderr when a repeat was honored. The logic
// lives in internal/state so the acceptance suite can exercise it too.
func confirmByRepeat(resp engine.Response, command, stateFile string, delay, window time.Duration) engine.Response {
	out, overridden := state.ConfirmByRepeat(afero.NewOsFs(), resp, command, stateFile, time.Now(), delay, window)
	if overridden {
		fmt.Fprintln(os.Stderr, progName+": command repeated within the override window — allowing.")
	}
	return out
}

// statePath puts the override state in a .ltk directory anchored to the
// resolved config: next to the config when it already lives in a .ltk
// directory, otherwise in a .ltk/ beside the config file (legacy flat configs,
// custom --config paths). Anchoring to the config — never the cwd — keeps
// confirm-by-repeat working when the host varies the hook cwd (agy runs hooks
// in <workspace>/.agents; the config search walks up, so the resolved path is
// the stable anchor). Runtime state always lives inside a .ltk directory,
// never loose in the project root, so .gitignore's ".ltk/state.json" entry
// covers it.
//
// With no config at all there is nothing to anchor to, and also nothing to
// confirm (no rules ⇒ no confirmable denial), so the cwd-relative fallback is
// effectively unreachable on the decision path.
func statePath(configPath string) string {
	if configPath == "" {
		return filepath.Join(configDir, stateBase)
	}
	dir := filepath.Dir(configPath)
	if filepath.Base(dir) == configDir {
		return filepath.Join(dir, stateBase)
	}
	return filepath.Join(dir, configDir, stateBase)
}

// loadConfig loads the given path, or searches the default locations in the
// cwd and each ancestor up to the repository root, returning the resolved
// path (empty when falling back to the built-in allow-all config).
//
// The ancestor walk matters because hook hosts differ in the cwd they give
// hooks: Claude Code runs them at the project root, Antigravity inside
// <workspace>/.agents. A cwd-only search under agy would miss the project's
// rules and silently fall back to the built-in allow-all config — the wrong
// direction for a guard to fail.
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
	c, err := rules.Parse([]byte("version: 1\nrules: []\n"))
	return c, "", err
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
