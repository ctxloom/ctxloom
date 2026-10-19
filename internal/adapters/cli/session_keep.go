package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
)

// sessionKeepUndo backs --undo: release the mark instead of setting it.
var sessionKeepUndo bool

var sessionKeepCmd = &cobra.Command{
	Use:   "keep <session-name>",
	Short: "Keep a session's transcript at any age (--undo releases it)",
	Long: `Marks a session so its transcript — the session's record — is never
reclaimed, at any age: 'session transcript purge', 'session purge', 'session
remove', 'session sweep' and 'clean --include-persist' all refuse it. Without
the mark a transcript becomes reclaimable once the session ended more than
session_transcript_retention ago (default 30d).

The mark lives in the session's own record and shows in 'ctxloom session
list'. --undo releases it.`,
	Example: `  ctxloom session keep amber-swift-owl
  ctxloom session keep amber-swift-owl --undo`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionKeep,
}

// sessionKeepResult is `session keep`'s payload: the session and where its
// mark now stands.
type sessionKeepResult struct {
	Harp string `json:"harp"`
	Kept bool   `json:"kept"`
}

func runSessionKeep(cmd *cobra.Command, args []string) error {
	harp := args[0]
	keep := !sessionKeepUndo
	if err := operations.SetSessionKeep(harp, keep); err != nil {
		return fmt.Errorf("session keep %s: %w", harp, err)
	}
	res := sessionKeepResult{Harp: harp, Kept: keep}
	return emit(cmd, res, func() error {
		msg := fmt.Sprintf("%s is marked keep: its transcript is kept at any age", harp)
		if !keep {
			msg = fmt.Sprintf("%s is no longer marked keep: its transcript is reclaimable once session_transcript_retention has passed since it ended", harp)
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), msg)
		return err
	})
}

func init() {
	sessionKeepCmd.Flags().BoolVar(&sessionKeepUndo, "undo", false, "release the mark: the transcript becomes reclaimable once its retention has passed")
	sessionCmd.AddCommand(sessionKeepCmd)
}
