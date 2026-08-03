package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/watch"
)

// planWatchDebounce coalesces the burst of filesystem events a single plan
// write produces into one logical change.
const planWatchDebounce = 100 * time.Millisecond

// planChangeEvent is one line of the `ctxloom plan watch` JSONL stream.
type planChangeEvent struct {
	Event string `json:"event"` // always "changed"
	Kind  string `json:"kind"`  // "plans"
}

var planCmd = groupNode(&cobra.Command{
	Use:   "plan",
	Short: "Work with session plan documents (~/.ctxloom/sessions/<harp>/*.plan.md)",
	// GUI-facing (only the plan watch stream lives here); hidden from CLI help.
	Hidden: true,
})

var planWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Stream plan change events as JSONL (one line per change)",
	Long: `Watch the session plan files under ~/.ctxloom/sessions and emit an event
whenever a *.plan.md is added, changed, or removed.

Long-lived: runs until interrupted (Ctrl-C / SIGTERM). With --format json (the
contract structured frontends consume) each change is one NDJSON line,
{"event":"changed","kind":"plans"}, and a GUI re-queries the plan list on each.
Text mode prints a short line per change. The watch plumbing lives here in the
backend so a frontend never has to touch ~/.ctxloom itself.`,
	Args: cobra.NoArgs,
	RunE: runPlanWatch,
}

func runPlanWatch(cmd *cobra.Command, args []string) error {
	root, err := paths.HomeSessionsDir()
	if err != nil {
		return err
	}

	// Recursive: plans live in per-harp subdirectories, and new session dirs
	// appear over a session's life. Filter to *.plan.md so unrelated session
	// files (transcripts, essence) don't churn the stream. watch.New no
	// longer creates the root itself — this IS the reviewable
	// call site that genuinely needs it: a fresh project with no session yet
	// has no sessions dir at all, and this command should still be able to
	// watch for the first one appearing.
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	// The extension comes from paths.PlanFileExt, the constant that defines
	// it, not a hardcoded ".plan.md" literal.
	w, err := watch.New(root, true, func(p string) bool {
		return strings.HasSuffix(p, paths.PlanFileExt)
	})
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()

	format := outputFormatOf(cmd)
	if format != "" && format != formatJSON && format != formatText {
		return unknownFormatError(format)
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), shutdownSignals...)
	defer stop()

	enc := json.NewEncoder(cmd.OutOrStdout())
	emit := func() error {
		if format == formatJSON {
			return enc.Encode(planChangeEvent{Event: "changed", Kind: "plans"})
		}
		_, perr := fmt.Fprintln(cmd.OutOrStdout(), "plans changed")
		return perr
	}

	return watch.Stream(ctx, w, planWatchDebounce, emit)
}

func init() {
	planCmd.AddCommand(planWatchCmd)
	rootCmd.AddCommand(planCmd)
}
