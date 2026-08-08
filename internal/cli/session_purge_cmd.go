package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// session purge — j001300 close-out area 2. Destroys a finished session's
// machine-written bulk (transcript.jsonl, persist/transcripts/…), keeping the
// distilled essence and the index entry by default; --everything also
// destroys the essence. Human-authored files anywhere under the harp
// directory are NEVER destroyed — they are named in the report instead.
//
// Absence of --yes means report-only, always, on a TTY or not: there is no
// per-item interactive confirmation here. `--yes` applies exactly the plan
// this invocation printed.

var (
	sessionPurgeYes         bool
	sessionPurgeEverything  bool
	sessionPurgeUndistilled bool
)

var sessionPurgeCmd = &cobra.Command{
	Use:   "purge <harp-name>",
	Short: "Destroy a finished session's machine-written bulk, keeping its distilled essence and index entry",
	Long: `Classifies everything under a harp's directory (except ephemeral/, which is
never walked) into three classes and reports what it finds:

  machine   transcript.jsonl / persist/transcripts/… — always destroyed
  derived   essence.md — kept by default; destroyed only with --everything
  authored  anything else — never destroyed, always named in the report

Without --yes this only reports; nothing on disk or in the session index
changes, on a TTY or not. --everything also destroys the distilled essence,
and refuses (naming the session) against one that was never distilled unless
--undistilled is also given: for a structured run the transcript is the ONLY
copy of what happened. purge never touches the session's ephemeral/ scratch
worktrees and runs no git commands — that is session worktrees --reap's job.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionPurge,
}

func init() {
	sessionPurgeCmd.Flags().BoolVarP(&sessionPurgeYes, "yes", "y", false, "apply the plan this invocation printed (default: report only)")
	sessionPurgeCmd.Flags().BoolVar(&sessionPurgeEverything, "everything", false, "also destroy the distilled essence")
	sessionPurgeCmd.Flags().BoolVar(&sessionPurgeUndistilled, "undistilled", false, "permit --everything against a session with no essence (requires --everything)")
	sessionCmd.AddCommand(sessionPurgeCmd)
}

func runSessionPurge(cmd *cobra.Command, args []string) error {
	harpName := args[0]
	if sessionPurgeUndistilled && !sessionPurgeEverything {
		return fmt.Errorf("--undistilled has no effect without --everything")
	}

	req := operations.PurgeSessionRequest{
		Harp:        harpName,
		Everything:  sessionPurgeEverything,
		Undistilled: sessionPurgeUndistilled,
		Apply:       sessionPurgeYes,
	}
	res, purgeErr := operations.PurgeSession(harpName, req)

	var refusal string
	switch {
	case errors.Is(purgeErr, operations.ErrPurgeLiveSession):
		refusal = fmt.Sprintf("ctxloom refuses to purge %s: the session is still live (no ended_at yet); nothing was touched", harpName)
	case errors.Is(purgeErr, operations.ErrPurgeUndistilled):
		refusal = fmt.Sprintf("ctxloom refuses: %s was never distilled (undistilled) — its transcript is the only record of what happened; pass --everything --undistilled to destroy it anyway. Nothing was touched", harpName)
	case errors.Is(purgeErr, operations.ErrPurgeNothingToDo):
		refusal = fmt.Sprintf("ctxloom purged nothing for %s: no machine-written bulk matched; nothing changed", harpName)
	case purgeErr != nil:
		return purgeErr
	}

	if err := emit(cmd, res, func() error {
		return renderSessionPurgePlan(cmd.OutOrStdout(), res)
	}); err != nil {
		return err
	}

	if refusal != "" {
		w := iox.NewErrWriter(cmd.ErrOrStderr())
		w.Println(refusal)
		if werr := w.Err(); werr != nil {
			return werr
		}
		return refusedExit()
	}

	if len(res.Withheld) > 0 {
		w := iox.NewErrWriter(cmd.ErrOrStderr())
		w.Printf("ctxloom refuses to destroy %d authored file(s) --everything asked for: %s — authored work is never negotiable; delete them yourself if you truly mean to\n",
			len(res.Withheld), strings.Join(res.Withheld, ", "))
		if werr := w.Err(); werr != nil {
			return werr
		}
		return refusedExit()
	}

	return nil
}

// sessionPurgeRow is `session purge`'s rendering projection, matching
// cli.SessionRow's convention: never the domain type (operations.PurgeItem),
// tagged for clifmt's reflective text table.
type sessionPurgeRow struct {
	Rel    string `json:"rel"    label:"Path"   col:"PATH"`
	Class  string `json:"class"  label:"Class"  col:"CLASS"`
	Action string `json:"action" label:"Action" col:"ACTION"`
	Bytes  int64  `json:"bytes"  label:"Bytes"  col:"BYTES"`
	Reason string `json:"reason,omitempty" label:"Reason" col:"REASON"`
}

// renderSessionPurgePlan is the text-format body for `session purge`: a
// one-line summary (what would be / was destroyed, bytes freed) followed by a
// table naming every file purge looked at — destroy rows first, then every
// kept file with its reason. A kept file that is never named is a file nobody
// will ever file, so this never omits the keep rows.
func renderSessionPurgePlan(w io.Writer, res *operations.PurgeSessionResult) error {
	if res == nil {
		return nil
	}
	ew := iox.NewErrWriter(w)
	verb := "would destroy"
	if res.Applied {
		verb = "destroyed"
	}
	ew.Printf("session %s — %s %d file(s) (%d byte(s) freed)\n", res.Harp, verb, len(res.Destroy), res.BytesFreed)
	if err := ew.Err(); err != nil {
		return err
	}

	rows := make([]sessionPurgeRow, 0, len(res.Destroy)+len(res.Keep))
	for _, it := range res.Destroy {
		rows = append(rows, sessionPurgeRow{Rel: it.Rel, Class: string(it.Class), Action: it.Action, Bytes: it.Bytes, Reason: it.Reason})
	}
	for _, it := range res.Keep {
		rows = append(rows, sessionPurgeRow{Rel: it.Rel, Class: string(it.Class), Action: it.Action, Bytes: it.Bytes, Reason: it.Reason})
	}
	if len(rows) == 0 {
		ew2 := iox.NewErrWriter(w)
		ew2.Println("(nothing found in this harp's directory)")
		return ew2.Err()
	}
	return clifmt.Render(w, rows, clifmt.FormatText)
}
