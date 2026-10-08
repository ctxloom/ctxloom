// Package cobrafmt is clifmt's adapter for github.com/spf13/cobra: one
// --format flag on a command tree, the format an invocation resolves to, a
// command's result emitted in it, and the process tail that reports a
// failure and picks the exit status. A command builds its result once and
// hands it to Emit; --format decides how the user gets it, so the format is
// a presentation choice and never a branch in business logic.
//
// Core clifmt imports no cobra; this package is the only place that does, so
// a program that does not use cobra never links it through clifmt.
package cobrafmt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// formatFlag is the persistent flag AddFlag registers and Resolve reads.
const formatFlag = "format"

// AddFlag registers the persistent --format flag on root: its usage is
// clifmt.FormatUsage, its default is empty (an unset flag follows the
// terminal; see Resolve), and shell completion offers
// clifmt.SupportedFormats.
func AddFlag(root *cobra.Command) {
	root.PersistentFlags().String(formatFlag, "", clifmt.FormatUsage())
	_ = root.RegisterFlagCompletionFunc(formatFlag, func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
		formats := clifmt.SupportedFormats()
		out := make([]cobra.Completion, len(formats))
		for i, f := range formats {
			out[i] = string(f)
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	})
}

// Emit renders data to cmd's output in the format the invocation resolves to
// (see Resolve), through the Printer installed on cmd's tree (WithPrinter),
// with the call's own options. A bespoke human view for one command is
// clifmt.WithWriter(clifmt.FormatText, …); with no options every format is
// clifmt's own.
func Emit(cmd *cobra.Command, data any, opts ...clifmt.Option) error {
	format, err := Resolve(cmd)
	if err != nil {
		return err
	}
	return printerFor(cmd).Render(cmd.OutOrStdout(), data, format, opts...)
}

// EmitError is Emit's failure half: it renders err to w in the format the
// invocation selected, so a caller that asked for json/yaml/toml gets a
// parseable {"error": "..."} envelope instead of a bare human line.
//
// It takes an explicit writer because a failure belongs on stderr, not on
// cmd.OutOrStdout(). cmd is the command that OWNS --format — for a process
// tail that is the root, whose persistent flag carries the parsed value (see
// Resolve's ordering note).
//
// ONLY an EXPLICIT request restructures the error stream. Resolve derives a
// format from stdout not being a terminal, and that derivation is about who
// consumes OUTPUT — it says nothing about the error stream, which is a
// different fd with a different reader. Honouring it here would turn every
// piped or redirected invocation's human error line into a JSON envelope. A
// --format that will not even parse cannot be a reason to lose the original
// error, so an unresolvable format falls back to text.
func EmitError(w io.Writer, cmd *cobra.Command, err error) error {
	format := clifmt.FormatText
	if Explicit(cmd) {
		if resolved, ferr := Resolve(cmd); ferr == nil {
			format = resolved
		}
	}
	return clifmt.RenderError(w, err, format)
}

// VersionInfo is the {name, version} payload every family binary's
// `version` command emits.
type VersionInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// EmitVersion is the shared body of a `version` command: the VersionInfo
// payload, with text printing the bare version string.
func EmitVersion(cmd *cobra.Command, name, version string) error {
	return Emit(cmd, VersionInfo{Name: name, Version: version}, clifmt.WithWriter(clifmt.FormatText, func(w io.Writer) error {
		_, err := fmt.Fprintln(w, version)
		return err
	}))
}

// ApplyDiagnostics switches clidiag's process-wide structured-diagnostics
// channel to match the invocation: JSON Lines warnings when the resolved
// format is json/yaml/toml, the human "<prog>: warning:" line otherwise
// (including when --format will not parse; that error is the command's to
// report). Call it from the root's PersistentPreRun(E), once per process.
func ApplyDiagnostics(cmd *cobra.Command) {
	format, err := Resolve(cmd)
	clidiag.SetStructured(err == nil && format.Structured())
}

// Execute runs root and returns the process exit status; see report for what
// it writes. It never calls os.Exit, so the caller can flush what it must
// before exiting.
// Reporting is Execute's, so it turns cobra's own error print off
// (root.SilenceErrors) rather than let the failure be reported twice.
func Execute(root *cobra.Command, prog string, stderr io.Writer) int {
	root.SilenceErrors = true
	return report(root, prog, stderr, root.Execute())
}

// report is the family's process tail for err, the error root's execution
// ended with, returning the exit status (clifmt.ExitCodeOf):
//   - nil: 0, nothing written;
//   - a clifmt.ExitStatus: its code, nothing written (the command has said
//     all it has to say, or relays a wrapped process's own status);
//   - an explicit structured --format (json/yaml/toml): err as a
//     clifmt.ErrorEnvelope on stderr;
//   - anything else: the human line "<prog>: <msg>", then clifmt.FixLine
//     when the error names a fix.
func report(root *cobra.Command, prog string, stderr io.Writer, err error) int {
	if err == nil {
		return 0
	}
	var silent clifmt.ExitStatus
	if errors.As(err, &silent) {
		return silent.Code
	}
	if format, ferr := Resolve(root); Explicit(root) && ferr == nil && format.Structured() {
		_ = EmitError(stderr, root, err)
	} else {
		fix, _ := clifmt.RemedyOf(err)
		_, _ = fmt.Fprintf(stderr, "%s: %s%s\n", prog, err.Error(), clifmt.FixLine("  ", fix))
	}
	return clifmt.ExitCodeOf(err)
}

// printerKey is the context key WithPrinter stores a tree's Printer under.
type printerKey struct{}

// WithPrinter makes Emit render through p for every command in root's tree.
// The Printer rides on root's context, so install it AFTER anything that
// replaces that context: root.ExecuteContext(ctx) or root.SetContext(ctx)
// called later drops it.
func WithPrinter(root *cobra.Command, p *clifmt.Printer) {
	ctx := root.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	root.SetContext(context.WithValue(ctx, printerKey{}, p))
}

// printerFor is the Printer installed on cmd's tree, or a bare one. It reads
// the root's context, not cmd's: a command emitted outside Execute has no
// context of its own.
func printerFor(cmd *cobra.Command) *clifmt.Printer {
	if ctx := cmd.Root().Context(); ctx != nil {
		if p, ok := ctx.Value(printerKey{}).(*clifmt.Printer); ok {
			return p
		}
	}
	return &clifmt.Printer{}
}

// isInteractiveTerminal reports whether stdout is attached to a terminal —
// i.e. a human is presumably watching it directly rather than a pipe, file
// redirect, or another process consuming it. Only stdout is checked, not
// stdin: format selection is a question about who consumes OUTPUT, and
// `echo x | prog show foo` (stdin piped, stdout at a human's terminal) must
// still render for the human reading their screen.
//
// A var rather than a plain func so tests can present either side of the
// human/machine split without a real terminal (OverrideTerminal): a test
// binary's stdout is never a terminal, so every unmocked test is permanently
// on the machine side.
var isInteractiveTerminal = func() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// OverrideTerminal makes Resolve see stdout as a terminal (interactive) or not,
// until the returned restore runs. It is the TEST seam for every package whose
// behaviour turns on Resolve: without it, a caller outside this package can
// only ever exercise the machine arm. Use it as
// t.Cleanup(cobrafmt.OverrideTerminal(true)) in a test that does not run in
// parallel — the check is process-wide.
//
// It takes no testing.TB so this package, which every binary links, does not
// import "testing".
func OverrideTerminal(interactive bool) (restore func()) {
	orig := isInteractiveTerminal
	isInteractiveTerminal = func() bool { return interactive }
	return func() { isInteractiveTerminal = orig }
}

// Resolve reads the inherited global --format value and resolves it with
// clifmt.ResolveFormat against whether stdout is a terminal. A set --json
// flag (the backward-compatible shorthand a few commands still carry) is
// honored as --format json so existing scripts keep working.
//
// An unset --format (e.g. a command driven without its root, which never
// registered it) reads as text unconditionally, since there is no flag to
// have been left at its default; any other unrecognized explicit value is an
// error wrapping clifmt.ErrUnsupportedFormat.
//
// ORDERING (connascence of execution): Resolve reads cmd.Flags(), and cobra
// merges a parent's PERSISTENT flags into a child's flag set during
// ParseFlags, inside Execute. Call Resolve from RunE/PersistentPreRunE, or
// from Execute's own error tail against the root that OWNS the flag. Called
// earlier against a subcommand it sees no --format and answers text.
//
// Two causes of "cannot read --format" are deliberately NOT the same. An
// ABSENT flag is the affordance above: it lets a command be driven without a
// root. A flag registered with the WRONG TYPE is a wiring bug — the value
// the user typed cannot be read at all — and is reported, even when nothing
// explicitly set it.
func Resolve(cmd *cobra.Command) (clifmt.Format, error) {
	if f := cmd.Flags().Lookup("json"); f != nil && f.Changed {
		return clifmt.FormatJSON, nil
	}
	flag := cmd.Flags().Lookup(formatFlag)
	if flag == nil {
		return clifmt.FormatText, nil
	}
	raw, err := cmd.Flags().GetString(formatFlag)
	if err != nil {
		return "", fmt.Errorf("cobrafmt: --format on %q is not a string flag: %w", cmd.CommandPath(), err)
	}
	return clifmt.ResolveFormat(raw, flag.Changed, isInteractiveTerminal())
}

// Explicit reports whether the caller actually ASKED for a format — `--json`,
// or `--format` typed on the command line — as opposed to one Resolve derived
// from stdout not being a terminal.
//
// The distinction is load-bearing and not cosmetic: a derived format is not a
// request, so no caller may treat it as one. A command that renders nothing
// through Emit is a real defect when someone typed `--format json` and got
// silence, and is nothing at all when the format was merely inferred from a
// pipe.
func Explicit(cmd *cobra.Command) bool {
	if f := cmd.Flags().Lookup("json"); f != nil && f.Changed {
		return true
	}
	f := cmd.Flags().Lookup(formatFlag)
	return f != nil && f.Changed
}
