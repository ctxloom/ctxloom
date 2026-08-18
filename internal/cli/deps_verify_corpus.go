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

// corpusViolationView is one offending bundle in a form every encoding can
// carry. CorpusViolation holds an `error`, which marshals to an empty object
// in JSON and drops the only actionable part of the finding, so the parse
// failure is flattened to its message here.
type corpusViolationView struct {
	Bundle string `json:"bundle" yaml:"bundle" toml:"bundle"`
	Remote string `json:"remote" yaml:"remote" toml:"remote"`
	URL    string `json:"url" yaml:"url" toml:"url"`
	Path   string `json:"path" yaml:"path" toml:"path"`
	Error  string `json:"error" yaml:"error" toml:"error"`
}

// corpusGapView is one unread piece of the corpus, flattened for the same
// reason as corpusViolationView. Path is empty when the whole remote was
// unreadable.
type corpusGapView struct {
	Subject string `json:"subject" yaml:"subject" toml:"subject"`
	Remote  string `json:"remote" yaml:"remote" toml:"remote"`
	URL     string `json:"url" yaml:"url" toml:"url"`
	Path    string `json:"path" yaml:"path" toml:"path"`
	Error   string `json:"error" yaml:"error" toml:"error"`
}

// corpusVerifyResult is the gate's result in the shape a caller parses.
//
// A gate exists to be read by CI, and findings that can only be scraped out of
// prose get a fragile regex written against them. Verdict and ExitCode are both
// present deliberately: the string is what a human reads in a log, the integer
// is the contract a pipeline branches on, and neither should have to be derived
// from the other.
type corpusVerifyResult struct {
	Verdict           string                `json:"verdict" yaml:"verdict" toml:"verdict"`
	ExitCode          int                   `json:"exit_code" yaml:"exit_code" toml:"exit_code"`
	RemotesConfigured int                   `json:"remotes_configured" yaml:"remotes_configured" toml:"remotes_configured"`
	RemotesRead       int                   `json:"remotes_read" yaml:"remotes_read" toml:"remotes_read"`
	Parsed            int                   `json:"parsed" yaml:"parsed" toml:"parsed"`
	Summary           string                `json:"summary" yaml:"summary" toml:"summary"`
	Violations        []corpusViolationView `json:"violations" yaml:"violations" toml:"violations"`
	Gaps              []corpusGapView       `json:"gaps" yaml:"gaps" toml:"gaps"`
}

// corpusResultOf converts a report into the emitted result, and is the SINGLE
// place the verdict name, the exit code and the summary sentence are decided.
// The text renderer reads them from here too, so `--format text` and
// `--format json` cannot come to disagree about whether the corpus passed.
func corpusResultOf(report operations.CorpusReport) corpusVerifyResult {
	// Non-nil empty slices, not nil: an encoding that renders nil as a null
	// forces every consumer to special-case "clean" before it can iterate.
	violations := make([]corpusViolationView, 0, len(report.Violations))
	for _, v := range report.Violations {
		violations = append(violations, corpusViolationView{
			Bundle: v.Bundle.String(),
			Remote: v.Bundle.Remote,
			URL:    v.Bundle.URL,
			Path:   v.Bundle.Path,
			Error:  v.Err.Error(),
		})
	}
	gaps := make([]corpusGapView, 0, len(report.Gaps))
	for _, g := range report.Gaps {
		gaps = append(gaps, corpusGapView{
			Subject: g.String(),
			Remote:  g.Remote,
			URL:     g.URL,
			Path:    g.Path,
			Error:   g.Err.Error(),
		})
	}

	result := corpusVerifyResult{
		ExitCode:          int(report.Verdict()),
		RemotesConfigured: report.RemotesConfigured,
		RemotesRead:       report.RemotesRead,
		Parsed:            report.Parsed,
		Violations:        violations,
		Gaps:              gaps,
	}

	switch report.Verdict() {
	case operations.CorpusViolated:
		result.Verdict = "violated"
		result.Summary = fmt.Sprintf("FAIL: %d published bundle(s) will not parse under this build's bundle schema.", len(report.Violations))
	case operations.CorpusUndetermined:
		// Never collapse this into the clean arm. A corpus nothing was read
		// from produces exactly the same silence as one in which everything
		// passed, and reporting success for it is how a gate comes to guard
		// nothing at all.
		result.Verdict = "undetermined"
		if report.Parsed == 0 && len(report.Gaps) == 0 {
			result.Summary = "UNDETERMINED: no published bundles were found to parse, so nothing was checked."
		} else {
			// Phrased without "see above": this sentence now ships inside
			// json/yaml/toml too, where there is no "above" — the gaps list
			// is a sibling field.
			result.Summary = fmt.Sprintf("UNDETERMINED: %d part(s) of the corpus could not be read, so it was not verified.", len(report.Gaps))
		}
	default:
		result.Verdict = "clean"
		result.Summary = "OK: the published corpus parses under this build's bundle schema."
	}
	return result
}

func runDepsVerifyCorpus(cmd *cobra.Command, _ []string) error {
	cfg := loadConfigOrFallback(GetConfig, os.Stderr)
	report, err := operations.CheckConfiguredCorpus(cmd.Context(), cfg)
	if err != nil {
		return err
	}

	result := corpusResultOf(report)
	if err := emit(cmd, result, func() error {
		reportCorpus(cmd.OutOrStdout(), report)
		return nil
	}); err != nil {
		return err
	}

	if result.ExitCode == 0 {
		return nil
	}
	// The findings are already rendered in full above, in whichever encoding
	// was asked for; ExitError carries the code without re-reporting an error
	// string that would say less than the result does.
	return &ExitError{Code: result.ExitCode}
}

// reportCorpus writes the human rendering of the corpus verdict and every
// finding behind it, and returns the process exit code. It is emit()'s text
// closure, and it reads its verdict, summary and exit code from
// corpusResultOf — the same values the structured encodings carry — so no
// format can report a pass the others call a failure.
//
// Each offending bundle is named WITH its parse error. A gate that reports only
// a count tells its reader nothing they can act on, so the next person to meet
// it turns it off rather than chase three anonymous failures.
func reportCorpus(out io.Writer, report operations.CorpusReport) int {
	result := corpusResultOf(report)

	for _, v := range result.Violations {
		fmt.Fprintf(out, "VIOLATION %s\n  %s\n  %s\n", v.Bundle, v.URL, v.Error)
	}
	for _, g := range result.Gaps {
		fmt.Fprintf(out, "UNCHECKED %s\n  %s\n  %s\n", g.Subject, g.URL, g.Error)
	}

	fmt.Fprintf(out, "\nRead %d of %d configured remotes; parsed %d bundle(s).\n",
		result.RemotesRead, result.RemotesConfigured, result.Parsed)
	fmt.Fprintln(out, result.Summary)

	// Text-only remediation. It is advice, not a finding: a machine consumer
	// branches on verdict and the violations list, and does not want prose
	// wired into its payload.
	if result.ExitCode == int(operations.CorpusViolated) {
		fmt.Fprintln(out, "Fix the content in the publishing repository, or relax the schema, before landing the tightening.")
	}
	return result.ExitCode
}

func init() {
	depsCmd.AddCommand(depsVerifyCorpusCmd)
}
