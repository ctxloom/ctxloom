package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// Output formats accepted by the global --format flag. These two remain
// distinct string constants (not clifmt.Format aliases) because a handful of
// streaming commands — session watch, plan watch, run's structured REPL/chat
// event stream — parse --format themselves via their own text/json-only
// switch (see unknownFormatError below) rather than going through emit();
// they render one event at a time and structurally can't hand clifmt a
// single result value. Widening those to the full five formats is out of
// scope here.
const (
	formatText = "text"
	formatJSON = "json"
)

// unknownFormatError is the error the streaming commands above return for
// any --format value outside their own text/json pair. emit() has its own,
// wider error (clifmt.ParseFormat, via cliemit.Resolve) covering all five
// formats.
func unknownFormatError(format string) error {
	return fmt.Errorf("unknown format %q (supported: %s, %s)", format, formatText, formatJSON)
}

// emit renders a command's result in the format selected by the global
// --format flag. It delegates to the cross-binary cliemit filter (shared with
// cmd/taskloom and cmd/ltk) so the emit()/resolve pair is defined once:
// json/yaml/toml/markdown go through clifmt.Render, text runs the bespoke human
// closure (or, when nil, clifmt's reflective text render).
//
// Commands build their result once and hand both forms here, so --format is a
// presentation choice and never a branch in business logic. This keeps every
// frontend (CLI, the VSCode companion, scripts) reading the same backend
// results.
//
// Also marks formatWasHonored (U104-F01): calling emit() at all is this
// invocation's proof that its command actually read the resolved --format
// value, regardless of which branch it took.
func emit(cmd *cobra.Command, data any, text func() error) error {
	formatWasHonored = true
	return cliemit.Emit(cmd, data, text)
}

// outputFormatOf reads the raw inherited --format flag value, unparsed. An
// unset flag (e.g. a unit test that never registered it) reads as "".
//
// Also marks formatWasHonored (U104-F01), same reasoning as emit(): every
// current caller (session watch, plan watch, run's structured REPL/chat
// stream) is one of the streaming commands the const block above documents
// as reading --format itself instead of going through emit() — this is
// their half of the same proof-of-honoring signal.
func outputFormatOf(cmd *cobra.Command) string {
	formatWasHonored = true
	format, _ := cmd.Flags().GetString("format")
	return format
}

// formatWasHonored is the runtime half of U104-F01 ("--format is registered
// globally but honoured opt-in, with nothing binding the two — so it is
// accepted and silently ignored on dozens of commands"). --format is a
// PERSISTENT flag (registered once below, on rootCmd) inherited by every
// descendant, so it is ACCEPTED by every command whether or not that
// command's RunE ever reads it. This tracks whether the invoked command
// actually did — via emit() or the streaming commands' outputFormatOf escape
// valve, the only two paths through this package that read --format — and
// checkFormatWasHonored (rootCmd's PersistentPostRunE, wired in root.go)
// turns "accepted and silently discarded" into a loud, actionable error
// instead of a false "success". This is the runtime enforcement counterpart
// to format_coverage_test.go's static formatDebtAllowlist ledger, which
// documents the same gap per-command but enforces nothing at runtime.
//
// Package-level, reset once per invocation (resetFormatGuard, called from
// root.go's PersistentPreRun) rather than threaded through cmd.Context(): a
// single `ctxloom` process handles exactly one command invocation end to
// end, so there is never a second concurrent invocation to conflate this
// with — and cobra calls PersistentPreRun/PersistentPostRunE at most once
// per Execute() (never for --help/--version/completion, which return before
// any Run hook fires), so the reset-then-check pairing is exact.
var formatWasHonored bool

// resetFormatGuard clears formatWasHonored; called once per invocation from
// root.go's PersistentPreRun, before anything can call emit()/outputFormatOf.
func resetFormatGuard() { formatWasHonored = false }

// checkFormatWasHonored is root.go's PersistentPostRunE body. Cobra invokes
// PersistentPostRunE only after RunE returns a nil error (an already-failing
// command has its own, louder error to report — this never overrides that),
// and never at all for --help/--version/completion (cobra returns before any
// Run hook in those cases), so this fires exactly in the "exited 0 having
// silently ignored --format" window U104-F01 describes. format == FormatText
// (the default, or an explicit `--format text`) is always fine: there is
// nothing to honor.
func checkFormatWasHonored(cmd *cobra.Command) error {
	format, ferr := cliemit.Resolve(cmd)
	if ferr != nil || format == clifmt.FormatText || formatWasHonored {
		return nil
	}
	return fmt.Errorf("%s: --format %s was accepted but this command does not support it yet (nothing was rendered through it) — rerun without --format, or see format_coverage_test.go's formatDebtAllowlist for the tracked fix", cmd.CommandPath(), format)
}

func init() {
	rootCmd.PersistentFlags().String("format", formatText,
		"Output format: json, yaml, toml, text, or markdown")
}
