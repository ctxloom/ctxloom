package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/pidalive"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// sessionWorktreeRow is `session worktrees`' rendering projection — never the
// domain type, matching cli.SessionRow's own convention (see session_row.go).
type sessionWorktreeRow struct {
	Harp       string `json:"harp"                label:"Harp"      col:"HARP"`
	Worktree   string `json:"worktree"             label:"Worktree" col:"WORKTREE"`
	Path       string `json:"path"                 label:"Path"     col:"PATH"`
	OwnerPID   int    `json:"owner_pid,omitempty"  label:"Owner PID" col:"OWNER"`
	OwnerState string `json:"owner_state"          label:"Owner"    col:"OWNER STATE"`
	Verdict    string `json:"verdict"              label:"Verdict"  col:"VERDICT"`
	Reason     string `json:"reason,omitempty"     label:"Reason"   col:"REASON"`
}

// sessionWorktreeReport is `session worktrees`' --format json payload.
// Worktrees is normalized nil -> [] so JSON renders [] not null — the same
// reason loadSessionEntries does it.
type sessionWorktreeReport struct {
	Worktrees []sessionWorktreeRow `json:"worktrees"`
	Reaped    int                  `json:"reaped"`
	Spared    int                  `json:"spared"`
	Skipped   int                  `json:"skipped"`
	// Applied is true only when --reap --yes actually ran ReapWorktrees.
	// Every other invocation — bare, or --reap without --yes — is a plan:
	// nothing on disk changed, regardless of what Verdict says would happen.
	Applied bool `json:"applied"`
}

func newSessionWorktreeRow(c isolation.WorktreeCandidate) sessionWorktreeRow {
	return sessionWorktreeRow{
		Harp:       c.Harp,
		Worktree:   filepath.Base(c.Path),
		Path:       c.Path,
		OwnerPID:   c.OwnerPID,
		OwnerState: worktreeOwnerStateText(c),
		Verdict:    string(c.Verdict),
		Reason:     c.Reason,
	}
}

// worktreeOwnerStateText renders a WorktreeCandidate's owner probe as a short
// human word. OwnerPID == 0 means no sibling marker was found at all (never
// probed) rather than "pid 0 was probed and found dead" — pidalive.Probe is
// never even called in that case (see isolation.classifyOneWorktree), so
// OwnerState there is a meaningless zero value that must not be rendered as
// "dead".
func worktreeOwnerStateText(c isolation.WorktreeCandidate) string {
	if c.OwnerPID == 0 {
		return "no marker"
	}
	switch c.OwnerState {
	case pidalive.Dead:
		return "dead"
	case pidalive.Alive:
		return "alive"
	default:
		return "unsure"
	}
}

// session worktrees — the sub-noun for the scratch git checkouts a session
// leaves behind. Like `transcript` and `artifacts` it is a POPULATION with
// its own listing and its own destroyer, so it is a namespace whose bare form
// lists and whose `purge` leaf removes.
//
// The old shape was one command with a --reap switch and a --harp selector.
// Both converge here: the switch becomes the verb, and the selector becomes
// the positional every other leaf under `session` already takes.

var sessionWorktreesPurgeYes bool

var sessionWorktreesCmd = groupNodeDefault(&cobra.Command{
	Use:   "worktrees",
	Short: "The scratch git checkouts a session left behind: list them, remove the safe ones",
	Long: `Every "ctxloom-wt-*" checkout ctxloom itself created under
~/.ctxloom/sessions/<harp>/ephemeral/ — leftovers from a per-agent worktree
whose owning process crashed before it could clean up after itself.

  list    what is there, and what would happen to each one (the bare form)
  purge   remove the ones this run can PROVE are safe

A long-lived worktree ctxloom did not create (anything outside
~/.ctxloom/sessions/) is never listed or touched here — "ctxloom doctor"
reports on those, and only ever suggests the commands to remove them by hand.`,
}, "list")

var sessionWorktreesListCmd = &cobra.Command{
	Use:   "list [<harp-name>]",
	Short: "List ctxloom-owned scratch worktrees and the verdict each one would get",
	Long: `Lists every ctxloom-owned scratch worktree and, for each, the verdict
isolation.ReapOrphanedWorktrees' safety rules would reach: reapable
(orphaned and clean), spared (orphaned but carrying real or unknowable
work), or skipped (owner alive, or its liveness can't be proven).

Read-only. Naming a harp restricts the listing to that one session.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSessionWorktreesList,
}

var sessionWorktreesPurgeCmd = &cobra.Command{
	Use:   "purge <harp-name>",
	Short: "Remove a session's scratch worktrees that this run can prove are safe",
	Long: `Removes the scratch worktrees that are BOTH orphaned (their owning process
is confirmed dead) and clean (no uncommitted work, no gitignored content).
Anything else is left in place and the report says why.

Without --yes this only reports; nothing is removed and no git command that
changes anything runs, on a TTY or not.

It refuses (exit 2) when it was asked to remove and removed nothing, so an
unattended run that found nothing to do cannot be mistaken for one that
cleaned up.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionWorktreesPurge,
}

func init() {
	sessionWorktreesPurgeCmd.Flags().BoolVarP(&sessionWorktreesPurgeYes, "yes", "y", false,
		"Act on exactly the plan this invocation printed. No config key may pre-answer it.")
	sessionWorktreesCmd.AddCommand(sessionWorktreesListCmd, sessionWorktreesPurgeCmd)
	sessionCmd.AddCommand(sessionWorktreesCmd)
}

// runSessionWorktreesList is the read-only half, and the bare sub-noun's
// answer.
func runSessionWorktreesList(cmd *cobra.Command, args []string) error {
	harp := ""
	if len(args) == 1 {
		harp = args[0]
		if err := verifyHarpDirExists(harp); err != nil {
			return err
		}
	}
	rep, err := classifyHarpWorktrees(cmd.Context(), harp, false)
	if err != nil {
		return err
	}
	return emit(cmd, rep, func() error {
		return renderSessionWorktrees(cmd.OutOrStdout(), rep)
	})
}

// runSessionWorktreesPurge is the destroying half. It reports through emit()
// regardless of outcome, so every --format still gets a real result; the
// loud "removed nothing" line and the exit-2 refusal are decided AFTER that
// render.
func runSessionWorktreesPurge(cmd *cobra.Command, args []string) error {
	harp := args[0]
	if err := verifyHarpDirExists(harp); err != nil {
		return err
	}
	rep, err := classifyHarpWorktrees(cmd.Context(), harp, sessionWorktreesPurgeYes)
	if err != nil {
		return err
	}
	if emitErr := emit(cmd, rep, func() error {
		return renderSessionWorktrees(cmd.OutOrStdout(), rep)
	}); emitErr != nil {
		return emitErr
	}

	if !sessionWorktreesPurgeYes {
		return reportPlanOnly(cmd, fmt.Sprintf("ctxloom session worktrees purge %s --yes", harp))
	}
	// An action verb that changed nothing refuses (docs/cli-ux-principles.md
	// §7): --yes ran, and not one candidate actually moved to VerdictReaped.
	// Spared/skipped candidates are not a failure — the report above already
	// said why each one was left alone — but reporting "0" here would make an
	// unattended purge that found nothing to do indistinguishable from one
	// that refused to touch anything it could have.
	if rep.Reaped == 0 {
		return reportRefusal(cmd, fmt.Sprintf(
			"ctxloom: removed no worktree for %s (%d spared, %d skipped) — nothing was provably safe to remove",
			harp, rep.Spared, rep.Skipped))
	}
	return nil
}

// sweepHarpWorktrees is `session purge`'s worktree half. Same body, called
// with the sweep's own apply decision.
func sweepHarpWorktrees(ctx context.Context, harp string, apply bool) (sessionWorktreeReport, error) {
	return classifyHarpWorktrees(ctx, harp, apply)
}

// classifyHarpWorktrees always classifies first, then — ONLY when apply —
// runs isolation.ReapWorktrees over exactly what was just classified, on this
// one invocation's own plan.
func classifyHarpWorktrees(ctx context.Context, harp string, apply bool) (sessionWorktreeReport, error) {
	candidates, err := isolation.ClassifyOrphanedWorktrees(ctx, nil, harp)
	if err != nil {
		return sessionWorktreeReport{}, fmt.Errorf("list scratch worktrees: %w", err)
	}
	if apply {
		candidates = isolation.ReapWorktrees(ctx, nil, candidates)
	}

	rep := sessionWorktreeReport{Worktrees: make([]sessionWorktreeRow, 0, len(candidates)), Applied: apply}
	for _, c := range candidates {
		rep.Worktrees = append(rep.Worktrees, newSessionWorktreeRow(c))
		switch c.Verdict {
		case isolation.VerdictReaped:
			rep.Reaped++
		case isolation.VerdictSpared:
			rep.Spared++
		case isolation.VerdictSkipped:
			rep.Skipped++
		}
	}
	return rep, nil
}

// verifyHarpDirExists returns an ordinary error (exit 1) when --harp names a
// session that has no directory at all under ~/.ctxloom/sessions — the
// design's "ordinary error" case, distinct from a harp that exists but simply
// has no scratch worktrees (an empty, successful listing).
func verifyHarpDirExists(harp string) error {
	dir, err := paths.HarpDir(harp)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		if os.IsNotExist(statErr) {
			return fmt.Errorf("harp not found: %q", harp)
		}
		return fmt.Errorf("stat harp %q: %w", harp, statErr)
	}
	return nil
}

// renderSessionWorktrees is the text/markdown-free (clifmt handles markdown
// itself) human render: a table when there is anything to show, one summary
// line naming what happened (or would happen) after it.
func renderSessionWorktrees(w io.Writer, rep sessionWorktreeReport) error {
	ew := iox.NewErrWriter(w)
	if len(rep.Worktrees) == 0 {
		ew.Println("no ctxloom-owned scratch worktrees")
		return ew.Err()
	}
	if err := clifmt.Render(w, rep.Worktrees, clifmt.FormatText); err != nil {
		return err
	}
	if rep.Applied {
		ew.Printf("removed %d, spared %d, skipped %d\n", rep.Reaped, rep.Spared, rep.Skipped)
	} else {
		ew.Printf("%d worktree(s) reviewed; %d spared, %d skipped (read-only — nothing was removed)\n",
			len(rep.Worktrees), rep.Spared, rep.Skipped)
	}
	return ew.Err()
}
