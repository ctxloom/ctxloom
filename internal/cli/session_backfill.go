package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// sessionBackfillCmd runs the SAME vendor-transcript conversion the
// interactive-pty exit seam uses (internal/cli/run.go's
// convertVendorTranscriptOnExit) over already-indexed sessions instead of a
// just-exited one — the backfill leg of docs/transcript-schema.md §8's
// interactive-pty gap (petty-green): a session run before this wiring
// existed, or one whose exit-seam import failed/was skipped, gets one more
// chance without re-running it.
var sessionBackfillCmd = &cobra.Command{
	Use:   "backfill [<harp-name>]",
	Short: "Import vendor-native transcripts for sessions ctxloom has no canonical memory of",
	Long: `Runs the same vendor-transcript importer the interactive-pty exit seam
uses (docs/transcript-schema.md §8), but over already-indexed sessions:
every indexed harp (or just the one named) whose backend has a resolvable
vendor-native transcript and no canonical transcript.jsonl yet gets
converted. A harp that already has a canonical transcript (imported before,
or captured live via the structured/ACP tee) is left untouched — safe to
re-run at any time, and cheap to re-run since it skips almost everything.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSessionBackfill,
}

func init() {
	sessionCmd.AddCommand(sessionBackfillCmd)
}

// runSessionBackfill is the cobra RunE for `ctxloom session backfill
// [<harp>]`. A named harp backfills just that one entry (erroring if the
// harp is unknown, matching runSessionDistill's convention); no argument
// backfills every indexed session across every project (operations.
// ListAllSessions — the same reconciled, cross-project set `session list
// --all` shows), since a stray old interactive session could live in any
// project directory.
func runSessionBackfill(cmd *cobra.Command, args []string) error {
	var entries []sessions.Entry
	if len(args) == 1 {
		entry, err := operations.GetSession(args[0])
		if err != nil {
			return err
		}
		if entry == nil {
			return fmt.Errorf("harp not found: %q", args[0])
		}
		entries = []sessions.Entry{*entry}
	} else {
		all, err := operations.ListAllSessions()
		if err != nil {
			return err
		}
		entries = all
	}

	result := operations.BackfillVendorTranscripts(cmd.Context(), entries)
	return emit(cmd, result, func() error {
		return renderBackfillResult(cmd.OutOrStdout(), result)
	})
}

// renderBackfillResult is the text-format render for `session backfill`:
// one line per converted/failed harp, then a one-line summary. Deterministic
// (result.FailedHarps sorts the map's keys) so the output is diffable/testable.
func renderBackfillResult(w io.Writer, result operations.BackfillResult) error {
	ew := iox.NewErrWriter(w)
	for _, h := range result.Converted {
		ew.Printf("converted: %s\n", h)
	}
	for _, h := range result.FailedHarps() {
		ew.Printf("failed: %s (%s)\n", h, result.Failed[h])
	}
	ew.Printf("%d converted, %d skipped, %d failed\n", len(result.Converted), result.Skipped, len(result.Failed))
	return ew.Err()
}
