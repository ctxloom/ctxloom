package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
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
	// Analyzed is false when the decision above was reached WITHOUT fully
	// parsing the command (defaults.on_parse_error let it through, or denied
	// it, without ever consulting a single rule) — see engine.Response.
	// Unanalyzed. Before this field existed an unanalyzed allow and a clean
	// allow were byte-identical here (U005-F01): a caller had no way to tell
	// "nothing matched" from "nothing could be checked at all".
	Analyzed bool `json:"analyzed"`
	// ParseError carries the human-facing detail when Analyzed is false.
	ParseError string `json:"parse_error,omitempty"`
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
			return runCheck(cmd.OutOrStdout(), cmd.ErrOrStderr(), command, cfgPath, ir.Shell(shellName), format)
		},
	}
	c.Flags().StringVar(&command, "command", "", "the shell command to check (required)")
	c.Flags().StringVar(&cfgPath, "config", "", "path to rules YAML (default: search cwd)")
	c.Flags().StringVar(&shellName, "shell", "", "force a shell dialect for parsing")
	_ = c.MarkFlagRequired("command")
	return c
}

// runCheck loads the rules, evaluates the command, and writes the verdict to
// w. It reuses evaluate's loadConfig + expandSubmodules + newDecider wiring,
// but skips the engine adapter (no stdin, no wire format) and the
// confirm-by-repeat state. text prints the human verdict via printCheckText;
// json/yaml/toml/markdown serialize checkResult through the shared clifmt
// filter.
//
// diag takes DIAGNOSTICS about how much checking happened, kept off w so the
// structured payload's shape is unchanged for the tools that parse it.
func runCheck(w, diag io.Writer, command, cfgPath string, forceShell ir.Shell, format clifmt.Format) error {
	// Unreachable from the CLI — newCheckCmd's RunE resolves the format
	// through cliemit.Resolve, which already rejects anything clifmt cannot
	// parse. It survives as the seam for direct in-package calls (see
	// check_test.go's "unknown format errors").
	if !format.Valid() {
		return clifmt.UnsupportedFormatError(string(format))
	}
	if forceShell != "" && !forceShell.Valid() {
		return fmt.Errorf("unknown --shell %q (known: %s)", forceShell, knownShells())
	}
	cfg, resolved, err := loadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load rules config: %w", err)
	}
	if resolved == "" {
		// No config was found anywhere from cwd up to the repository root, so
		// the answer below was reached against the built-in zero-rule config.
		// The hook surface already discloses this (see evaluate); without the
		// same disclosure here, "allow" means two different things depending
		// on which surface a tool asked, and the one whose whole job is
		// answering "would this be blocked?" is the one that stayed silent.
		// Diagnostic only — the verdict on w is unchanged, and it is kept off
		// w so the structured payload's shape does not move.
		fmt.Fprintf(diag, "%s: no rules config found (searched cwd and ancestors for %s); nothing is being gated\n",
			progName, strings.Join(configSearch, ", "))
	}
	// `check` is the diagnostic surface, not the guard: it may fail loudly. A
	// working directory that cannot be determined, a .gitmodules that exists
	// but cannot be read, or a submodule path that is not a valid glob would
	// otherwise leave a `path: ["@submodules"]` rule expanded to nothing and
	// report the resulting non-decision as an "allow".
	if err := expandSubmodules(cfg, os.Getwd); err != nil {
		return fmt.Errorf("resolve @submodules: %w", err)
	}

	resp := newDecider(cfg, forceShell).Decide(context.Background(), engine.Request{Command: command})

	result := checkResult{Decision: "allow", Analyzed: !resp.Unanalyzed, ParseError: resp.ParseError}
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
		if _, err := fmt.Fprintln(w, "allow"); err != nil {
			return err
		}
		if !r.Analyzed {
			// Without this, an unanalyzed allow (on_parse_error let it through
			// without ever consulting a rule) prints identically to a clean
			// allow — U005-F01.
			_, err := fmt.Fprintf(w, "note: could not fully analyze this command (%s); allowed per defaults.on_parse_error\n", r.ParseError)
			return err
		}
		return nil
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
