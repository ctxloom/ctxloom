package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/ltk/app"
	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/scm"
	"github.com/ctxloom/ctxloom/internal/ltk/shellenv"
	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// checkResult is the machine-readable verdict from `ltk check`: a structured
// allow/deny with the rule's message and suggestion as DISCRETE fields. This is
// the query surface for tools and GUIs ("would this be blocked?"), so they never
// have to construct the engine's hook payload or split the combined
// reason+suggest string that the `evaluate` wire format forces them to.
type checkResult struct {
	Decision   string `json:"decision"`             // "allow" or "deny"
	Message    string `json:"message,omitempty"`    // the rule's message, on a deny
	Suggestion string `json:"suggestion,omitempty"` // the rule's suggested alternative, if any
}

func newCheckCmd() *cobra.Command {
	var cfgPath, shellName, command string
	c := &cobra.Command{
		Use:   "check",
		Short: "Check whether a shell command would be allowed (structured output)",
		Long: `Check evaluates a single shell command against the project's rules and reports
the verdict — allow or deny, with the rule's message and suggested alternative as
separate fields.

Unlike ` + "`evaluate`" + ` (the hook path, which reads a hook payload on stdin and writes
the engine's wire format), check takes the command as a flag and emits a plain
{decision, message, suggestion} object — for tools and humans asking "would this
be blocked?". It applies no confirm-by-repeat override: it reports what the rules
say, nothing stateful.

Structured formats (json/yaml/toml/markdown) serialize {decision, message,
suggestion} via the shared clifmt filter; text prints the human verdict.

Unlike the hook, this is an explicit command, so it fails loud (exit 1) on a
broken/unreadable config rather than failing closed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := cliemit.Resolve(cmd)
			if err != nil {
				return err
			}
			return runCheck(cmd.OutOrStdout(), command, cfgPath, ir.Shell(shellName), format)
		},
	}
	c.Flags().StringVar(&command, "command", "", "the shell command to check (required)")
	c.Flags().StringVar(&cfgPath, "config", "", "path to rules YAML (default: search cwd)")
	c.Flags().StringVar(&shellName, "shell", "", "force a shell dialect for parsing")
	_ = c.MarkFlagRequired("command")
	return c
}

// runCheck loads the rules, evaluates the command, and writes the verdict. It
// reuses evaluate's loadConfig + submodule expansion + app wiring, but skips the
// engine adapter (no stdin, no wire format) and the confirm-by-repeat state.
// text prints the human verdict via printCheckText; json/yaml/toml/markdown
// serialize checkResult through the shared clifmt filter.
func runCheck(w io.Writer, command, cfgPath string, forceShell ir.Shell, format clifmt.Format) error {
	// Unreachable from the CLI — newCheckCmd's RunE resolves the format
	// through cliemit.Resolve, which already rejects anything clifmt cannot
	// parse. It survives as the seam for direct in-package calls (see
	// check_test.go's "unknown format errors").
	if !format.Valid() {
		return fmt.Errorf("%w: %q (supported: json, yaml, toml, text, markdown)", clifmt.ErrUnsupportedFormat, format)
	}
	if forceShell != "" && !forceShell.Valid() {
		return fmt.Errorf("unknown --shell %q (known: %s)", forceShell, knownShells())
	}
	cfg, _, err := loadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load rules config: %w", err)
	}
	// `check` is the diagnostic surface, not the guard: it may fail loudly. A
	// .gitmodules that exists but cannot be read, or a submodule path that is
	// not a valid glob, would otherwise leave a `path: ["@submodules"]` rule
	// expanded to nothing and report the resulting non-decision as an "allow".
	if wd, err := os.Getwd(); err == nil {
		subs, err := scm.SubmodulePaths(afero.NewOsFs(), wd)
		if err != nil {
			return fmt.Errorf("resolve @submodules: %w", err)
		}
		if err := cfg.ExpandSubmodules(subs); err != nil {
			return err
		}
	}

	a := app.New(cfg)
	a.ForceShell = forceShell
	a.HostShell = shellenv.ShellFromPath(os.Getenv("SHELL"))
	resp := a.Decide(context.Background(), engine.Request{Command: command})

	result := checkResult{Decision: "allow"}
	if !resp.Allow {
		result.Decision = "deny"
		result.Message = resp.Reason
		result.Suggestion = resp.Suggest
	}

	if format == clifmt.FormatText {
		return printCheckText(w, result)
	}
	return clifmt.Render(w, result, format)
}

func printCheckText(w io.Writer, r checkResult) error {
	if r.Decision == "allow" {
		_, err := fmt.Fprintln(w, "allow")
		return err
	}
	if _, err := fmt.Fprintln(w, "deny"); err != nil {
		return err
	}
	if r.Message != "" {
		if _, err := fmt.Fprintln(w, r.Message); err != nil {
			return err
		}
	}
	if r.Suggestion != "" {
		if _, err := fmt.Fprintf(w, "Use instead: %s\n", r.Suggestion); err != nil {
			return err
		}
	}
	return nil
}
