package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
)

var depsVerifyCorpusCmd = &cobra.Command{
	Use:   "verify-corpus",
	Short: "Parse every bundle published by the configured remotes under the current schema",
	Long: `Parse the published corpus of every configured remote and fail if any bundle
would not load.

This is the gate that stops a schema tightening from shipping ahead of the
content it breaks. ctxloom's bundle parser is strict — a key the schema does
not model is refused rather than dropped — so tightening the schema can turn
already-published bundles into parse failures that surface only as a warning in
every project that has them installed. Run this before landing such a change:
it applies THIS BUILD's rules to the bundles the remotes actually serve.

WHERE THE CORPUS COMES FROM. Every remote in .ctxloom/remotes.yaml, read from
its local clone under .ctxloom/cache/repos. Nothing is hardcoded, so adding a
remote extends the gate automatically.

OFFLINE. No fetch is performed, so a warm cache needs no network. A remote with
no local clone is reported as unchecked, not as passing — run 'ctxloom deps
pull' or 'ctxloom remote create' to populate it.

EXIT CODES. 0 when every bundle in a non-empty corpus parsed. 1 when at least
one bundle would not parse; each is named with its parse error. 2 when the
corpus could not be checked — an unreadable remote, or nothing found to parse.
2 is not a pass: "clean" and "I could not look" are different answers.`,
	Args: cobra.NoArgs,
	RunE: runDepsVerifyCorpus,
}

func runDepsVerifyCorpus(cmd *cobra.Command, _ []string) error {
	cfg := loadConfigOrFallback(GetConfig, os.Stderr)
	report, err := operations.CheckConfiguredCorpus(cmd.Context(), cfg)
	if err != nil {
		return err
	}
	code := reportCorpus(cmd.OutOrStdout(), report)
	if code == 0 {
		return nil
	}
	// The findings are already printed in full above; ExitError carries the
	// code without re-reporting an error string that would say less than the
	// report does.
	return &ExitError{Code: code}
}

// reportCorpus writes the corpus verdict and every finding behind it, and
// returns the process exit code.
//
// Each offending bundle is named WITH its parse error. A gate that reports only
// a count tells its reader nothing they can act on, so the next person to meet
// it turns it off rather than chase three anonymous failures.
func reportCorpus(out io.Writer, report operations.CorpusReport) int {
	for _, v := range report.Violations {
		fmt.Fprintf(out, "VIOLATION %s\n  %s\n  %v\n", v.Bundle, v.Bundle.URL, v.Err)
	}
	for _, g := range report.Gaps {
		fmt.Fprintf(out, "UNCHECKED %s\n  %s\n  %v\n", g, g.URL, g.Err)
	}

	fmt.Fprintf(out, "\nRead %d of %d configured remotes; parsed %d bundle(s).\n",
		report.RemotesRead, report.RemotesConfigured, report.Parsed)

	switch report.Verdict() {
	case operations.CorpusViolated:
		fmt.Fprintf(out, "FAIL: %d published bundle(s) will not parse under this build's bundle schema.\n", len(report.Violations))
		fmt.Fprintln(out, "Fix the content in the publishing repository, or relax the schema, before landing the tightening.")
		return int(operations.CorpusViolated)
	case operations.CorpusUndetermined:
		// Never collapse this into the clean arm. A corpus nothing was read
		// from produces exactly the same silence as one in which everything
		// passed, and reporting success for it is how a gate comes to guard
		// nothing at all.
		if report.Parsed == 0 && len(report.Gaps) == 0 {
			fmt.Fprintln(out, "UNDETERMINED: no published bundles were found to parse, so nothing was checked.")
		} else {
			fmt.Fprintln(out, "UNDETERMINED: part of the corpus could not be read (see UNCHECKED above), so it was not verified.")
		}
		return int(operations.CorpusUndetermined)
	default:
		fmt.Fprintln(out, "OK: the published corpus parses under this build's bundle schema.")
		return int(operations.CorpusClean)
	}
}

func init() {
	depsCmd.AddCommand(depsVerifyCorpusCmd)
}
