package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// session edit — the session noun's one mutation verb. Renaming a session is
// a field assignment, not a verb of its own, so it rides `--name` here
// alongside whatever fields the noun grows later.
//
// THE BARE FORM REFUSES, which departs from every other `edit` in the tree.
// The spine's contract is that a bare `edit <ref>` opens $EDITOR on the
// referenced document, and a session has none to open. Its index entry is
// machine-written — session id, timestamps, transcript path, engine version —
// and its essence is DERIVED: `session distill` regenerates that file
// wholesale, so an editor round-trip there would offer edits the next
// distillation silently discards. That is this project's characteristic
// failure shape (an exit-0 success message over work that did not survive),
// so the bare form says outright that there is no document and names the
// assignment it does take.

// sessionEditName backs --name. Read through Flags().Changed rather than
// emptiness: `--name ""` is a caller asking for an invalid harp and must
// reach the validator, not look identical to not passing the flag at all.
var sessionEditName string

// sessionEditResult is `session edit`'s payload. It names the session under
// BOTH spellings: a report that says only what a session is called now leaves
// an operator holding the new name with no way back to the old one.
type sessionEditResult struct {
	Harp         string   `json:"harp"`
	PreviousHarp string   `json:"previous_harp,omitempty"`
	Fields       []string `json:"fields"`
}

var sessionEditCmd = &cobra.Command{
	Use:   "edit <harp-name>",
	Short: "Assign a recorded session's fields (today: --name, which renames the harp)",
	Long: `Assigns fields on a session's index entry. Only the flags you pass are
applied; everything else keeps its current value.

  --name <new-harp>   rename the harp. The backend transcript is unaffected —
                      the entry keeps its bound session id, its transcript
                      path and its essence; only the name it answers to moves.

Unlike every other 'edit' in ctxloom, the bare form does NOT open an editor.
A session is a record of something that happened: its index entry is written
by ctxloom itself, and its essence is derived — 'ctxloom session distill'
rewrites that file whole. There is no authored document here for an editor to
round-trip, so the bare form refuses rather than accept edits a later
distillation would discard.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionEdit,
}

func init() {
	sessionEditCmd.Flags().StringVar(&sessionEditName, "name", "",
		"Rename the harp to this name. The backend transcript is unaffected.")
}

func runSessionEdit(cmd *cobra.Command, args []string) error {
	harp := args[0]
	if !cmd.Flags().Changed("name") {
		return fmt.Errorf("ctxloom session edit %s: nothing to assign, and a session has no editable document to open — "+
			"its index entry is machine-written and its essence is derived (`ctxloom session distill` rewrites it whole). "+
			"Name a field: --name <new-harp>", harp)
	}

	entry, err := operations.GetSession(harp)
	if err != nil {
		return err
	}
	if entry == nil {
		return fmt.Errorf("harp not found: %q", harp)
	}
	if err := operations.RenameSession(harp, sessionEditName); err != nil {
		return err
	}

	res := sessionEditResult{Harp: sessionEditName, PreviousHarp: harp, Fields: []string{"name"}}
	return emit(cmd, res, func() error {
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("renamed %s → %s\n", harp, sessionEditName)
		return w.Err()
	})
}
