package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

var lintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Report triage tag-standard violations across every task (advisory, never a write gate)",
	Long: `Fold every task in the project — regardless of status — and report
violations of the triage tag standard, sourced from this project's own
tag_schema config (` + "`.taskloom/config.yaml`" + `'s ` + "`tag_schema`" + ` key, or the shipped
default) so the check always matches what THIS project actually declares:

  - for every target the schema declares an enum for (e.g. "triage:kind",
    "triage:exposed" in the shipped default): a value that isn't one of the
    declared enum values
  - for every target the schema declares a numeric range for (e.g.
    "triage:effort" in the shipped default): a value that doesn't parse as a
    number, or parses but falls outside the declared range
  - a value-qualified placeholder in a declared priority_fn/decay_fn (e.g.
    "{{triage:kind=foo}}") whose value isn't one of that target's own
    declared enum values
  - an arity=scalar target carrying more than one distinct value on the same
    task (shouldn't happen once a task is only ever tagged through taskloom —
    the write-seam already collapses those — but foreign or hand-edited log
    data might still carry it)

Lint never blocks a write; it is READ-TIME. But it is a BACKSTOP, not the only
check: taskloom's write seam already refuses an out-of-enum or out-of-range
value the moment you tag, naming the declared set, and already collapses an
arity=scalar target. So a violation reaching lint means the data did not come in
through taskloom — a hand-edited log, an import, or a schema that changed after
the tag was written. Run it in CI to catch exactly that drift; it exits non-zero
when any violation is found.`,
	Example: `  taskloom lint
  taskloom lint --json`,
	Args: cobra.NoArgs,
	RunE: runLint,
}

func runLint(cmd *cobra.Command, args []string) error {
	format, err := cliemit.Resolve(cmd)
	if err != nil {
		return err
	}
	tc, err := taskContextSingle()
	if err != nil {
		return err
	}
	return runLintCmd(cmd.OutOrStdout(), tc, format)
}

// runLintCmd is lintCmd's RunE body, factored out so it can be driven in
// tests without cobra machinery — mirroring runListCmd's own convention.
func runLintCmd(out io.Writer, tc operations.TaskContext, format clifmt.Format) error {
	result, err := operations.LintTasks(tc)
	if err != nil {
		return err
	}
	violations := result.Violations
	if format != clifmt.FormatText {
		if err := clifmt.Render(out, result, format); err != nil {
			return err
		}
	} else {
		w := iox.NewErrWriter(out)
		switch {
		case result.CheckedTargets == 0:
			w.Println("0 targets checked — this project's tag_schema declares no enum/range facets for lint to check")
		case len(violations) == 0:
			w.Println("no triage-standard violations found")
		default:
			for _, v := range violations {
				w.Printf("%s: %s\n", v.HarpID, v.Reason)
			}
		}
		if err := w.Err(); err != nil {
			return err
		}
	}
	if result.CheckedTargets == 0 {
		return fmt.Errorf("taskloom lint checked 0 targets — this project's tag_schema declares no enum/range facets, so the CI gate is not actually checking anything")
	}
	if len(violations) > 0 {
		return fmt.Errorf("%d triage-standard violation(s) found", len(violations))
	}
	return nil
}

func init() {
	rootCmd.AddCommand(lintCmd)
}
