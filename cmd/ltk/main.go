// Command ltk is the llm-tool-killer CLI.
//
//	ltk evaluate            # the hook: read a payload on stdin, emit a decision
//	ltk manage install      # add the hook to the most relevant LLM config
//	ltk manage uninstall    # remove it
//
// The hook gates two kinds of agent action: shell commands (Bash/PowerShell
// tools, matched by command rules) and file edits (Edit/Write/MultiEdit/
// NotebookEdit tools, matched by `match.path` rules). See docs/RULES.md.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// Version is set at build time via ldflags (package main), e.g.
//
//	-X main.Version=v1.2.3
//
// It defaults to "dev"; the justfile stamps it from versionator.
var Version = "dev"

// newRootCmd assembles the ltk command tree. It is a factory rather than an
// inline root so the documentation generator can walk exactly the tree the
// binary runs (`just gen-docs`; see docs_gen.go).
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   progName,
		Short: "Gate an LLM agent's shell commands and file edits via a pre-tool hook",
		Long: `ltk is a pre-tool hook for LLM coding agents. It gates two kinds of action:

  • shell commands (Bash/PowerShell tools) — matched by command rules, which
    parse the command and match program/args across shells.
  • file edits (Edit/Write/MultiEdit/NotebookEdit tools) — matched by file
    rules (match.path), which match the target file path against globs.

File (match.path) rules use full globs (*, ?, [..], {a,b}, and ** which spans
directories). A trailing slash means a whole directory subtree (vendor/ blocks
everything under vendor). The special pattern "@submodules" expands to every
path in .gitmodules, so one rule blocks edits inside all git submodules.

On a denial the agent is handed your message and suggested alternative so it can
retry the right way. See docs/RULES.md for the full rule model.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("format", string(clifmt.FormatText),
		"Output format: json, yaml, toml, text, or markdown")
	root.AddCommand(newEvaluateCmd(), newCheckCmd(), newManageCmd(), newVersionCmd(), newLoadoutCmd())
	registerDocsCmd(root)
	return root
}

// reportExecuteError writes a terminal error on w in the format the invocation
// selected. json/yaml/toml go through the shared cliemit filter, so a caller
// reading a machine-readable stream gets a parseable {"error": "..."} envelope
// for the failure rather than a human sentence that ends its parse on the one
// event it most needs to handle.
//
// text and markdown keep the "ltk: <err>" line verbatim. The family's other
// binaries print "Error: <msg>" there, but ltk has always named itself instead,
// and that line is what a terminal and any script grepping stderr sees today;
// aligning the wording is a separate, human-visible change and not this one.
//
// root is the command that OWNS --format, which is why the flag is resolved
// against it and not against whatever subcommand failed (see cliemit.Resolve's
// ordering note). An unresolvable --format reads as not-structured and keeps
// the human line: a format that will not parse must not cost the caller the
// original error.
func reportExecuteError(w io.Writer, root *cobra.Command, err error) {
	// Explicit gates this, not Resolve alone. Resolve DERIVES a format from
	// stdout not being a terminal, and a derived format is not a request — so
	// without this gate every piped or redirected `ltk` lost its "ltk: <err>"
	// line to a structured envelope nobody asked for, which is exactly what the
	// guard below this function's own doc promises cannot happen.
	if format, ferr := cliemit.Resolve(root); cliemit.Explicit(root) && ferr == nil && format.Structured() {
		// EmitError only fails when w does, and w is the only channel that
		// failure could have been reported on.
		_ = cliemit.EmitError(w, root, err)
		return
	}
	fmt.Fprintln(w, progName+":", err)
}

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		reportExecuteError(os.Stderr, root, err)
		os.Exit(1)
	}
}
