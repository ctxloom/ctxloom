// Package cliemit is the cross-binary --format output filter for the ctxloom
// family (ctxloom, taskloom, ltk). A command builds its result once and hands
// both a structured value and a bespoke text closure to Emit; the global
// --format flag then decides which the user gets, so --format is a
// presentation choice and never a branch in business logic. Every frontend
// (CLI, the VS Code companion, scripts) reads the same backend results.
//
// It lives here rather than being re-declared per binary so the emit()/resolve
// pair is defined once (pkg/clifmt does the actual rendering; this layer is the
// cobra glue).
package cliemit

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// Emit renders data in the format selected by the inherited --format flag:
// json/yaml/toml/markdown all delegate to clifmt.Render (which marshals or
// reflects the value directly — no per-command renderer needed), while text is
// the one format with an escape hatch. When text != nil that bespoke human
// closure runs; a nil text falls back to clifmt's own reflective text render,
// so a call site can route through Emit with zero rendering code.
func Emit(cmd *cobra.Command, data any, text func() error) error {
	format, err := Resolve(cmd)
	if err != nil {
		return err
	}
	if format == clifmt.FormatText && text != nil {
		return text()
	}
	return clifmt.Render(cmd.OutOrStdout(), data, format)
}

// EmitError is Emit's failure half: it renders err to w in the format the
// invocation selected, so a caller that asked for json/yaml/toml gets a
// parseable {"error": "..."} envelope instead of a bare human line, and
// text/markdown keep the "Error: <msg>" line a terminal expects.
//
// It takes an explicit writer because a failure belongs on stderr, not on
// cmd.OutOrStdout(). cmd is the command that OWNS --format — for a top-level
// Execute tail that is the root, whose persistent flag carries the parsed
// value (see Resolve's ordering note). A --format that will not even parse
// cannot be a reason to lose the original error, so an unresolvable format
// falls back to text rather than propagating.
//
// The rendering error is returned for callers that can act on it; a process
// tail writing to os.Stderr has no further fallback and discards it.
func EmitError(w io.Writer, cmd *cobra.Command, err error) error {
	format, ferr := Resolve(cmd)
	if ferr != nil {
		format = clifmt.FormatText
	}
	return clifmt.RenderError(w, err, format)
}

// EmitVersion is the shared body behind every family binary's `version`
// command (ctxloom, ltk, taskloom): build the cross-binary {name,version}
// payload and route it through emitFn, with text printing the bare version
// string. emitFn is injected rather than hard-coded to Emit because ctxloom's
// own version command routes through a package-local wrapper that also
// marks a test-only "format was honored" flag before delegating to Emit;
// ltk and taskloom pass Emit itself.
func EmitVersion(cmd *cobra.Command, emitFn func(cmd *cobra.Command, data any, text func() error) error, name, version string) error {
	return emitFn(cmd, cliversion.Info{Name: name, Version: version}, func() error {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), version)
		return err
	})
}

// Resolve reads the inherited global --format value and parses it via clifmt.
// A set --json flag (the backward-compatible shorthand a few commands still
// carry) is honored as --format json so existing scripts keep working. An unset
// --format (e.g. a unit test that never registered it) reads as "" and is
// treated as text; any other unrecognized value is an error wrapping
// clifmt.ErrUnsupportedFormat.
//
// ORDERING (connascence of execution): Resolve reads cmd.Flags(), and cobra
// merges a parent's PERSISTENT flags into a child's flag set during
// ParseFlags, inside Execute. Call Resolve from RunE/PersistentPreRunE, or
// from Execute's own error tail against the root that OWNS the flag. Called
// earlier against a subcommand it sees no --format and answers text.
//
// Two causes of "cannot read --format" are deliberately NOT the same. An
// ABSENT flag is the affordance above: it lets a command be driven without a
// root, and turning it into an error breaks that contract at 30-plus call
// sites. A flag registered with the WRONG TYPE is a wiring bug — the value
// the user typed cannot be read at all — and is reported.
func Resolve(cmd *cobra.Command) (clifmt.Format, error) {
	if f := cmd.Flags().Lookup("json"); f != nil && f.Changed {
		return clifmt.FormatJSON, nil
	}
	if cmd.Flags().Lookup("format") == nil {
		return clifmt.FormatText, nil
	}
	raw, err := cmd.Flags().GetString("format")
	if err != nil {
		return "", fmt.Errorf("cliemit: --format on %q is not a string flag: %w", cmd.CommandPath(), err)
	}
	if raw == "" {
		return clifmt.FormatText, nil
	}
	return clifmt.ParseFormat(raw)
}
