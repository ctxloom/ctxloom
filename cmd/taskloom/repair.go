package main

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
)

var repairCmd = &cobra.Command{
	Use:   "repair",
	Short: "Re-introduce any task displaced by an unresolved harp-id collision",
	Long: `An unresolved harp-id collision — two different tasks independently minted
with the same harp, most likely two branches later union-merged — makes
EVERY read of this project's task log fail loud: list, summary, tag counts,
--sort priority, and lint all refuse to run rather than silently show only
the survivor. The collision error names this command as the remedy.

repair re-introduces each displaced task under a FRESH harp id, preserving
its text, tags, status/trigger, and original creation time. It never
rewrites history: the log still records both the original collision and
the repair as separate events. Idempotent — running it again once nothing
is displaced is a no-op.`,
	Example: `  taskloom repair`,
	Args:    cobra.NoArgs,
	RunE:    runRepair,
}

func runRepair(cmd *cobra.Command, args []string) error {
	tc, err := taskContextSingle()
	if err != nil {
		return err
	}
	return runRepairCmd(cmd.OutOrStdout(), tc)
}

// runRepairCmd is repairCmd's RunE body, factored out so it can be driven in
// tests without cobra machinery — mirroring runLintCmd's own convention.
func runRepairCmd(out io.Writer, tc operations.TaskContext) error {
	if err := operations.RepairStore(tc); err != nil {
		return err
	}
	w := iox.NewErrWriter(out)
	w.Println("repair complete — any displaced task has been re-introduced under a fresh harp id")
	return w.Err()
}

func init() {
	rootCmd.AddCommand(repairCmd)
}
