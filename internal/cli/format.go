package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// Output formats accepted by the global --format flag. These two remain
// distinct string constants (not clifmt.Format aliases) because a handful of
// streaming commands — session transcript watch, plan watch, run's structured REPL/chat
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
// Also marks formatWasHonored: calling emit() at all is this
// invocation's proof that its command actually read the resolved --format
// value, regardless of which branch it took.
func emit(cmd *cobra.Command, data any, text func() error) error {
	formatWasHonored = true
	return cliemit.Emit(cmd, data, text)
}

// outputFormatOf reads the raw inherited --format flag value, unparsed. An
// unset flag (e.g. a unit test that never registered it) reads as "".
//
// Also marks formatWasHonored, same reasoning as emit(): every
// current caller (session transcript watch, plan watch, run's structured REPL/chat
// stream) is one of the streaming commands the const block above documents
// as reading --format itself instead of going through emit() — this is
// their half of the same proof-of-honoring signal.
func outputFormatOf(cmd *cobra.Command) string {
	formatWasHonored = true
	format, _ := cmd.Flags().GetString("format")
	return format
}

// reviewWantsListing decides which half of `ctxloom review` an invocation
// gets: the machine-readable pending table (true) or the interactive
// countersigning walk (false). interactive is the caller's TTY answer, kept a
// parameter so the decision is testable without one.
//
// --format is part of the decision, not just of the rendering. An invocation
// that asked for json/yaml/toml/markdown asked for a value it can parse, and
// the walk renders nothing through emit — so without this it would prompt a
// human through an approval session and only afterwards fail the
// format-was-honored guard, having already written countersignatures. A
// --format that will not parse lands here too: an invocation whose output
// contract cannot be interpreted is not one to start an approval session on,
// and emit reports the parse failure properly.
//
// This deliberately does NOT mark formatWasHonored: the proof of honoring is
// emit() actually rendering, further down the listing path.
func reviewWantsListing(cmd *cobra.Command, listFlag, interactive bool) bool {
	if listFlag || !interactive {
		return true
	}
	format, err := cliemit.Resolve(cmd)
	return err != nil || format != clifmt.FormatText
}

// formatWasHonored is the runtime guard against --format being registered
// globally but honoured opt-in, with nothing binding the two — so it would
// otherwise be accepted and silently ignored on dozens of commands. --format
// is a PERSISTENT flag (registered once below, on rootCmd) inherited by
// every descendant, so it is ACCEPTED by every command whether or not that
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
// silently ignored --format" window described above. format == FormatText
// (the default, or an explicit `--format text`) is always fine: there is
// nothing to honor.
//
// A namespace never reaches the debt error below, and needs no exemption from
// it: groupNode's own RunE refuses a machine-readable --format outright, and
// cobra skips PersistentPostRunE entirely once a RunE has returned an error.
// The refusal lives there rather than here so the caller gets the error alone
// instead of a screenful of help followed by one — see groupNodeFormatRefusal.
func checkFormatWasHonored(cmd *cobra.Command) error {
	format, ferr := cliemit.Resolve(cmd)
	if ferr != nil || format == clifmt.FormatText || formatWasHonored {
		return nil
	}
	// Only a format the caller ASKED for can be dishonored. Off a terminal
	// Resolve defaults to JSON, so without this every command carrying format
	// debt would fail for every piped or scripted caller — after doing its
	// work, which for `run` means burning the LLM call and then reporting
	// failure.
	if !cliemit.Explicit(cmd) {
		return nil
	}
	return unsupportedFormatError(cmd, string(format))
}

// errFormatUnsupportedFragment is the stable part of both refusals' text. Both
// the early refusal and the post-run backstop report the same condition, so
// they share one message rather than drifting into two descriptions of one
// thing — and tests assert this constant instead of a copied string literal.
const errFormatUnsupportedFragment = "does not support it yet"

// unsupportedFormatError is the single message for "you asked for a machine
// format this command cannot produce". It names the remedy, because the
// refusal is the entire user interface for this failure.
func unsupportedFormatError(cmd *cobra.Command, format string) error {
	return fmt.Errorf("%s: --format %s was accepted but this command %s (nothing was rendered through it) — rerun without --format, or see formatDebtAllowlist in internal/cli/format_debt.go for the tracked fix",
		cmd.CommandPath(), format, errFormatUnsupportedFragment)
}

// refuseUnsupportedFormat is the PRE-run half of the guard, and the one that
// makes the failure safe: it refuses before RunE can do any work.
//
// checkFormatWasHonored (post-run) cannot do this. It keys off a flag emit()
// sets, which is only knowable once RunE has already run — so by the time it
// can tell, a mutating command has already mutated. This half consults the
// static ledger instead, which knows in advance.
//
// Both halves stay. The ledger covers KNOWN debt; the post-run check still
// catches a command that carries new, untracked debt, so newly-broken commands
// keep failing loudly instead of silently discarding --format.
func refuseUnsupportedFormat(cmd *cobra.Command) error {
	format, err := cliemit.Resolve(cmd)
	if err != nil || format == clifmt.FormatText {
		return nil
	}
	// Only a format the caller ASKED for can be refused. Off a terminal
	// Resolve defaults to JSON, so refusing on a derived format would break
	// every piped and scripted invocation of every debt-carrying command.
	if !cliemit.Explicit(cmd) {
		return nil
	}
	// CommandPath() is "ctxloom foo bar"; the ledger is keyed "foo bar".
	path := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
	if _, tracked := formatDebtAllowlist[path]; !tracked {
		return nil
	}
	return unsupportedFormatError(cmd, string(format))
}

func init() {
	rootCmd.PersistentFlags().String("format", formatText,
		"Output format: json, yaml, toml, text, or markdown")
}
